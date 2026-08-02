// Copyright (C) 2019-2022 Chrystian Huot <chrystian.huot@saubeo.solutions>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>

package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/dop251/goja"
)

// The JavaScript side of plugin-to-plugin work, and creating calls.

// bindPlugins exposes rdio.plugins.
func (rt *PluginRuntime) bindPlugins(vm *goja.Runtime, rdio *goja.Object, throw func(string, ...any)) {
	plugins := vm.NewObject()

	// What else is running, so a plugin can adapt to what is present rather
	// than failing when an optional companion is absent.
	plugins.Set("list", func() goja.Value {
		list := []map[string]any{}

		for _, plugin := range rt.controller.Plugins.Enabled() {
			entry := map[string]any{
				"id":      plugin.PluginId,
				"version": plugin.Version,
				"methods": rt.controller.PluginRpc.Methods(plugin.PluginId),
			}

			if plugin.Manifest != nil {
				entry["name"] = plugin.Manifest.Name
			}

			list = append(list, entry)
		}

		return vm.ToValue(list)
	})

	plugins.Set("has", func(pluginId string) goja.Value {
		for _, plugin := range rt.controller.Plugins.Enabled() {
			if plugin.PluginId == pluginId {
				return vm.ToValue(true)
			}
		}
		return vm.ToValue(false)
	})

	// Offer a method for other plugins to call.
	plugins.Set("handle", func(method string, handler goja.Callable) goja.Value {
		if strings.TrimSpace(method) == "" {
			throw("plugins.handle requires a method name")
		}
		if handler == nil {
			throw("plugins.handle(%q) requires a function", method)
		}

		rt.controller.PluginRpc.Handle(rt.manifest.Id, method, &pluginHandler{
			pluginId: rt.manifest.Id,
			runtime:  rt,
			callable: &gojaCallable{fn: handler},
		})

		return goja.Undefined()
	})

	// Call another plugin and wait for its answer.
	//
	// Returns a promise so the caller's event loop keeps running while the other
	// plugin works. Blocking the loop here would mean a plugin could not answer
	// its own incoming calls while waiting on an outgoing one, which is the
	// deadlock the bus already refuses — no reason to build a second way into it.
	plugins.Set("call", func(pluginId string, method string, args goja.Value) goja.Value {
		promise, resolve, reject := vm.NewPromise()

		var payload any
		if args != nil && !goja.IsUndefined(args) && !goja.IsNull(args) {
			payload = args.Export()
		}

		from := rt.manifest.Id

		go func() {
			result, err := rt.controller.PluginRpc.Call(from, pluginId, method, payload)

			rt.settle(func(vm *goja.Runtime) {
				if err != nil {
					reject(vm.NewGoError(err))
					return
				}
				resolve(vm.ToValue(result))
			})
		}()

		return vm.ToValue(promise)
	})

	// Announce something. Never waits, never reports who listened.
	plugins.Set("publish", func(topic string, payload goja.Value) goja.Value {
		if strings.TrimSpace(topic) == "" {
			throw("plugins.publish requires a topic")
		}

		var value any
		if payload != nil && !goja.IsUndefined(payload) && !goja.IsNull(payload) {
			value = payload.Export()
		}

		delivered := rt.controller.PluginRpc.Publish(rt.manifest.Id, topic, value)

		return vm.ToValue(delivered)
	})

	plugins.Set("subscribe", func(topic string, handler goja.Callable) goja.Value {
		if strings.TrimSpace(topic) == "" {
			throw("plugins.subscribe requires a topic")
		}
		if handler == nil {
			throw("plugins.subscribe(%q) requires a function", topic)
		}

		// Delivery arrives through the runtime's own event channel, keyed by the
		// topic, so a published event runs on the subscriber's loop exactly as a
		// point observer does.
		rt.mutex.Lock()
		rt.handlers["plugins:"+topic] = append(rt.handlers["plugins:"+topic], handler)
		rt.mutex.Unlock()

		rt.controller.PluginRpc.Subscribe(topic, &pluginSubscription{
			pluginId: rt.manifest.Id,
			runtime:  rt,
			callable: &gojaCallable{fn: handler},
		})

		return goja.Undefined()
	})

	rdio.Set("plugins", plugins)
}

// createCall turns a plugin's description of a call into one, and hands it to
// the same ingest channel an upload uses.
//
// Going in the front door matters: everything ingest does — blacklists,
// duplicate detection, conversion, the extension points, delayed emit — applies
// to a plugin's call exactly as it does to an uploaded one. A plugin that wrote
// straight to the calls table would produce something that looked like a call
// but was never processed like one.
func (rt *PluginRuntime) createCall(spec map[string]any) (bool, error) {
	call := NewCall()

	audio, err := pluginBytes(spec["audio"])
	if err != nil {
		return false, fmt.Errorf("audio: %v", err)
	}
	if len(audio) == 0 {
		return false, fmt.Errorf("audio is required")
	}
	call.Audio = audio

	system, ok := pluginUint(spec["system"])
	if !ok || system == 0 {
		return false, fmt.Errorf("system is required")
	}
	call.System = system

	talkgroup, ok := pluginUint(spec["talkgroup"])
	if !ok || talkgroup == 0 {
		return false, fmt.Errorf("talkgroup is required")
	}
	call.Talkgroup = talkgroup

	// Defaults to now, because a plugin bridging a live source usually has no
	// timestamp of its own and a zero time would fail validation with an error
	// that does not explain itself.
	call.DateTime = time.Now().UTC()

	if when, ok := spec["dateTime"].(string); ok && when != "" {
		parsed, err := time.Parse(time.RFC3339, when)
		if err != nil {
			return false, fmt.Errorf("dateTime must be RFC3339: %v", err)
		}
		call.DateTime = parsed.UTC()
	}

	if name, ok := spec["audioName"].(string); ok && name != "" {
		call.AudioName = name
	} else {
		call.AudioName = fmt.Sprintf("%s-%d.bin", rt.manifest.Id, call.DateTime.Unix())
	}

	if kind, ok := spec["audioType"].(string); ok && kind != "" {
		call.AudioType = kind
	}

	for _, field := range []string{"frequency", "frequencies", "patches", "source", "sources"} {
		if value, present := spec[field]; present && value != nil {
			switch field {
			case "frequency":
				call.Frequency = value
			case "frequencies":
				call.Frequencies = normalizeJsonNumbers(value)
			case "patches":
				call.Patches = normalizeJsonNumbers(value)
			case "source":
				call.Source = value
			case "sources":
				call.Sources = normalizeJsonNumbers(value)
			}
		}
	}

	if meta := pluginStringMap(spec["meta"]); meta != nil {
		call.meta = meta
	}

	// Attributed to the plugin, so the newcall log line says where it came from
	// rather than looking like an anonymous upload.
	call.apiKeyIdent = "plugin:" + rt.manifest.Id

	if ok, err := call.IsValid(); !ok {
		return false, err
	}

	// Non-blocking. The ingest channel is deep, and a plugin that could block on
	// a full one would stall its own event loop behind the very goroutine it is
	// feeding.
	select {
	case rt.controller.Ingest <- call:
		return true, nil
	default:
		return false, fmt.Errorf("ingest queue is full")
	}
}
