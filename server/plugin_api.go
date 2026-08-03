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
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/dop251/goja"
)

const (
	// pluginHttpTimeout is the default deadline for a plugin's outbound
	// request. Plugins can lower it; they cannot raise it past the cap.
	pluginHttpTimeout    = 60 * time.Second
	pluginHttpTimeoutMax = 5 * time.Minute

	// pluginHttpMaxResponse bounds what a plugin can pull into memory from a
	// single response, so a hostile or broken endpoint can't exhaust the heap.
	pluginHttpMaxResponse = 32 << 20 // 32 MiB

	// pluginMinInterval floors rdio.schedule so a plugin can't spin the loop.
	pluginMinInterval = 250 * time.Millisecond
)

// pluginClientHandle is the opaque client reference handed to websocket
// handlers. Plugins get an identifier they can echo back to reply, not the
// *Client itself.
type pluginClientHandle struct {
	client *Client
	Id     string `json:"id"`
}

func newPluginClientHandle(client *Client) *pluginClientHandle {
	return &pluginClientHandle{client: client, Id: fmt.Sprintf("%p", client)}
}

// bindHostApi installs the `rdio` global and a console shim.
//
// There are no permission gates. A plugin does what it does, and the decision
// about whether to trust it happens once, at install, where a human is present
// — rather than being asked repeatedly by a mechanism that could only ever have
// refused things the manifest already declared.
func (rt *PluginRuntime) bindHostApi(vm *goja.Runtime) error {
	throw := func(format string, args ...any) {
		panic(vm.NewGoError(fmt.Errorf(format, args...)))
	}

	rdio := vm.NewObject()

	// --- rdio.plugin ------------------------------------------------------

	pluginInfo := vm.NewObject()
	pluginInfo.Set("id", rt.manifest.Id)
	pluginInfo.Set("version", rt.manifest.Version)
	// Where the plugin's own files live. Read-only in practice: the installer
	// removes and rewrites this on every update.
	pluginInfo.Set("dir", rt.plugin.dir)
	// Where a plugin should keep anything it wants to survive an update.
	pluginInfo.Set("dataDir", rt.dataDir)
	rdio.Set("plugin", pluginInfo)

	// --- rdio.server ------------------------------------------------------

	// What a plugin is running inside. Manifests already declare a
	// minServerVersion, so a plugin can be refused for being too old for the
	// server — but it had no way to ask at runtime, which is what anything doing
	// feature detection, or simply displaying the version, actually needs.
	serverInfo := vm.NewObject()
	serverInfo.Set("version", Version)
	serverInfo.Set("apiVersion", CurrentPluginApiVersion)
	rdio.Set("server", serverInfo)

	// --- rdio.log ---------------------------------------------------------

	logAt := func(level string, message string) {
		// Prefixed so the admin Logs view attributes the line to its plugin.
		rt.controller.Logs.LogEvent(level, fmt.Sprintf("plugin %s: %s", rt.manifest.Id, message))
	}

	rdio.Set("log", func(call goja.FunctionCall) goja.Value {
		level := LogLevelInfo
		message := ""

		switch len(call.Arguments) {
		case 0:
			return goja.Undefined()
		case 1:
			message = call.Argument(0).String()
		default:
			switch strings.ToLower(call.Argument(0).String()) {
			case "warn", "warning":
				level = LogLevelWarn
			case "error":
				level = LogLevelError
			default:
				level = LogLevelInfo
			}
			message = call.Argument(1).String()
		}

		logAt(level, message)

		return goja.Undefined()
	})

	// console is a convenience shim over the same path — plugin authors reach
	// for it reflexively, and without this it would be undefined.
	console := vm.NewObject()
	consoleAt := func(level string) func(goja.FunctionCall) goja.Value {
		return func(call goja.FunctionCall) goja.Value {
			parts := make([]string, len(call.Arguments))
			for i, arg := range call.Arguments {
				parts[i] = arg.String()
			}
			logAt(level, strings.Join(parts, " "))
			return goja.Undefined()
		}
	}
	console.Set("log", consoleAt(LogLevelInfo))
	console.Set("info", consoleAt(LogLevelInfo))
	console.Set("warn", consoleAt(LogLevelWarn))
	console.Set("error", consoleAt(LogLevelError))
	console.Set("debug", consoleAt(LogLevelInfo))
	vm.Set("console", console)

	// --- rdio.config ------------------------------------------------------

	config := vm.NewObject()

	config.Set("get", func(key string) goja.Value {
		rt.configMu.RLock()
		defer rt.configMu.RUnlock()
		value, ok := rt.config[key]
		if !ok {
			return goja.Undefined()
		}
		return vm.ToValue(value)
	})

	config.Set("getAll", func() goja.Value {
		rt.configMu.RLock()
		defer rt.configMu.RUnlock()
		copied := map[string]any{}
		for k, v := range rt.config {
			copied[k] = v
		}
		return vm.ToValue(copied)
	})

	config.Set("set", func(key string, value goja.Value) goja.Value {
		exported := value.Export()

		rt.configMu.Lock()
		rt.config[key] = exported
		rt.configMu.Unlock()

		if err := WritePluginConfigValue(rt.controller.Database, rt.manifest, key, exported); err != nil {
			throw("config.set failed: %v", err)
		}

		return goja.Undefined()
	})

	config.Set("expose", func(key string, value goja.Value) goja.Value {

		rt.mutex.Lock()
		rt.exposedConfig[key] = value.Export()
		rt.mutex.Unlock()

		// Push the change out immediately so clients don't have to reconnect
		// to notice a plugin toggling a feature flag.
		rt.controller.EmitConfig()

		return goja.Undefined()
	})

	rdio.Set("config", config)

	// --- rdio.on / rdio.schedule -----------------------------------------

	// The four verbs. Every way a plugin reaches into the server is one of
	// these, registered against a named point.
	register := func(verb pluginVerb) func(string, goja.Callable) goja.Value {
		return func(point string, handler goja.Callable) goja.Value {
			if handler == nil {
				throw("rdio.%s(%q) requires a function", verb, point)
			}

			// Refused loudly rather than accepted and never fired. A handler
			// registered against a point that does not exist would otherwise
			// wait forever with no indication anything was wrong.
			if !rt.controller.PluginDispatch.KnownPoint(point) {
				throw("rdio.%s: unknown extension point %q", verb, point)
			}

			// The point existing is not enough — it has to invoke this verb.
			// Most points only ever call one or two, so accepting any of the
			// four meant rdio.filter('call.convert') and rdio.on('call.audio')
			// registered cleanly and then never ran, which is the exact thing
			// the point check was added to prevent.
			if !pointAcceptsVerb(point, verb) {
				throw("rdio.%s: %q does not use %s; it uses %s",
					verb, point, verb, strings.Join(pointVerbNames(point), ", "))
			}

			if verb == verbOn {
				rt.mutex.Lock()
				rt.handlers[point] = append(rt.handlers[point], handler)
				rt.mutex.Unlock()
			}

			rt.controller.PluginDispatch.Register(&pluginHandler{
				pluginId: rt.manifest.Id,
				verb:     verb,
				runtime:  rt,
				callable: &gojaCallable{fn: handler},
			}, point)

			return goja.Undefined()
		}
	}

	rdio.Set("on", register(verbOn))
	rdio.Set("filter", register(verbFilter))
	rdio.Set("override", register(verbOverride))
	rdio.Set("provide", register(verbProvide))

	// Lets a plugin publish an extension point of its own, so plugins can
	// extend each other without waiting on a core release.
	rdio.Set("definePoint", func(point string) goja.Value {
		if strings.TrimSpace(point) == "" {
			throw("definePoint requires a name")
		}
		rt.controller.PluginDispatch.DefinePoint(point)
		return goja.Undefined()
	})

	rdio.Set("schedule", func(intervalMs int64, handler goja.Callable) goja.Value {
		if handler == nil {
			throw("rdio.schedule requires a function")
		}

		interval := time.Duration(intervalMs) * time.Millisecond
		if interval < pluginMinInterval {
			interval = pluginMinInterval
		}

		timer := rt.loop.SetInterval(func(vm *goja.Runtime) {
			stop := rt.armWatchdog(vm, "schedule", pluginCallTimeout)
			defer stop()

			if _, err := handler(goja.Undefined()); err != nil {
				rt.logCallError("schedule", err)
			}
		}, interval)

		rt.mutex.Lock()
		rt.intervals = append(rt.intervals, timer)
		rt.mutex.Unlock()

		return goja.Undefined()
	})

	// --- rdio.db ----------------------------------------------------------

	db := vm.NewObject()

	db.Set("query", func(query string, args goja.Value) goja.Value {
		rows, err := rt.db.Query(query, exportPluginArgs(args))
		if err != nil {
			throw("db.query: %v", err)
		}
		return vm.ToValue(rows)
	})

	db.Set("exec", func(query string, args goja.Value) goja.Value {
		affected, err := rt.db.Exec(query, exportPluginArgs(args))
		if err != nil {
			throw("db.exec: %v", err)
		}
		return vm.ToValue(affected)
	})

	// Async variants. The synchronous ones above run on the plugin's event
	// loop, so a slow query stalls everything else that plugin is doing —
	// fine for the small keyed reads and writes most plugins do, not fine for
	// anything scanning a large table. These run the query on a goroutine and
	// settle a promise back on the loop.
	db.Set("queryAsync", func(query string, args goja.Value) goja.Value {
		exported := exportPluginArgs(args)
		return rt.promiseFrom(vm, func() (any, error) {
			return rt.db.Query(query, exported)
		})
	})

	db.Set("execAsync", func(query string, args goja.Value) goja.Value {
		exported := exportPluginArgs(args)
		return rt.promiseFrom(vm, func() (any, error) {
			return rt.db.Exec(query, exported)
		})
	})

	rdio.Set("db", db)

	// --- rdio.calls -------------------------------------------------------

	calls := vm.NewObject()

	calls.Set("get", func(id int64, options goja.Value) goja.Value {

		call, err := rt.controller.Calls.GetCall(uint(id), rt.controller.Database)
		if err != nil {
			throw("calls.get: %v", err)
		}
		if call == nil {
			return goja.Null()
		}

		withAudio := false
		if options != nil && !goja.IsUndefined(options) && !goja.IsNull(options) {
			if o, ok := options.Export().(map[string]any); ok {
				if v, ok := o["audio"].(bool); ok {
					withAudio = v
				}
			}
		}

		return vm.ToValue(pluginCallValue(call, withAudio))
	})

	calls.Set("search", func(options goja.Value) goja.Value {

		results, err := rt.searchCalls(options)
		if err != nil {
			throw("calls.search: %v", err)
		}

		return vm.ToValue(results)
	})

	calls.Set("update", func(id int64, fields goja.Value) goja.Value {

		// Deliberately narrow. A plugin's own data belongs in its own tables;
		// this exists so a plugin can correct core metadata it is authoritative
		// about, not so it can rewrite the call record wholesale.
		m, ok := fields.Export().(map[string]any)
		if !ok {
			throw("calls.update requires an object")
		}

		if err := rt.updateCall(uint(id), m); err != nil {
			throw("calls.update: %v", err)
		}

		return goja.Undefined()
	})

	calls.Set("extendField", func(spec goja.Value) goja.Value {
		m, ok := spec.Export().(map[string]any)
		if !ok {
			throw("calls.extendField requires an object")
		}

		extension := &pluginFieldExtension{
			pluginId:    rt.manifest.Id,
			Field:       stringFromMap(m, "field"),
			Table:       stringFromMap(m, "table"),
			KeyColumn:   stringFromMap(m, "keyColumn"),
			ValueColumn: stringFromMap(m, "valueColumn"),
		}

		if err := rt.validateExtension(extension.Field, extension.Table, extension.KeyColumn, extension.ValueColumn); err != nil {
			throw("calls.extendField: %v", err)
		}

		rt.mutex.Lock()
		rt.fieldExtensions = append(rt.fieldExtensions, extension)
		rt.mutex.Unlock()

		return goja.Undefined()
	})

	calls.Set("findId", func(system int64, talkgroup int64, dateTime string) goja.Value {

		id, err := rt.controller.PluginFindCallId(uint(system), uint(talkgroup), dateTime)
		if err != nil {
			throw("calls.findId: %v", err)
		}

		return vm.ToValue(id)
	})

	// A call from a plugin goes in the same door an upload does, so a new ingest
	// source — a scanner protocol rdio does not speak, a bridge from another
	// system — is an ordinary plugin rather than a change to core.
	calls.Set("create", func(spec goja.Value) goja.Value {
		m, ok := spec.Export().(map[string]any)
		if !ok {
			throw("calls.create requires an object")
		}

		// createCall reports whether the call was accepted onto the ingest
		// queue, not an id — an id cannot exist yet, because ingest has not run.
		// The variable used to be named `id` and the value handed straight back,
		// so `const id = rdio.calls.create(...)` produced `true`, and
		// `rdio.calls.get(id)` then coerced that to call number 1.
		//
		// Nothing is returned now. A plugin that needs the id waits for the call
		// to exist and asks for it by what it knows.
		if _, err := rt.createCall(m); err != nil {
			throw("calls.create: %v", err)
		}

		return goja.Undefined()
	})

	rdio.Set("calls", calls)

	// --- rdio.fs / rdio.exec / rdio.crypto --------------------------------

	rt.bindFs(vm, rdio, throw)
	rt.bindExec(vm, rdio, throw)
	rt.bindCrypto(vm, rdio, throw)
	rt.bindAudio(vm, rdio, throw)
	rt.bindPlugins(vm, rdio, throw)

	// --- rdio.models ------------------------------------------------------

	models := vm.NewObject()

	for i := range pluginModels {
		name := pluginModels[i].name

		entity := vm.NewObject()

		entity.Set("list", func() goja.Value {
			entries, err := rt.controller.modelList(name)
			if err != nil {
				throw("models.%s.list: %v", name, err)
			}
			return vm.ToValue(entries)
		})

		entity.Set("get", func(key goja.Value) goja.Value {
			entry, err := rt.controller.modelGet(name, key.Export())
			if err != nil {
				throw("models.%s.get: %v", name, err)
			}
			if entry == nil {
				return goja.Null()
			}
			return vm.ToValue(entry)
		})

		entity.Set("set", func(value goja.Value) goja.Value {
			entry, ok := value.Export().(map[string]any)
			if !ok {
				throw("models.%s.set requires an object", name)
			}
			if err := rt.controller.modelSet(name, entry); err != nil {
				throw("models.%s.set: %v", name, err)
			}
			// Configuration changed, so listeners need the new view.
			rt.controller.EmitConfig()
			return goja.Undefined()
		})

		entity.Set("remove", func(key goja.Value) goja.Value {
			if err := rt.controller.modelRemove(name, key.Export()); err != nil {
				throw("models.%s.remove: %v", name, err)
			}
			rt.controller.EmitConfig()
			return goja.Undefined()
		})

		models.Set(name, entity)
	}

	rdio.Set("models", models)

	// --- rdio.systems -----------------------------------------------------

	systems := vm.NewObject()

	systems.Set("list", func() goja.Value {
		// Ungated: this is the same configuration every websocket client
		// already receives. Withholding it would only push plugins into
		// keeping their own stale copy.
		return vm.ToValue(rt.controller.PluginSystemsList())
	})

	// The three lookups a call handler actually performs. call.stored carries
	// system and talkgroup as bare integers, so without these every plugin
	// wanting to name them wrote the same scan over list().
	systems.Set("get", func(systemId int64) goja.Value {
		system := rt.controller.PluginSystem(uint(systemId))
		if system == nil {
			return goja.Null()
		}
		return vm.ToValue(system)
	})

	systems.Set("talkgroup", func(systemId int64, talkgroupId int64) goja.Value {
		talkgroup := rt.controller.PluginTalkgroup(uint(systemId), uint(talkgroupId))
		if talkgroup == nil {
			return goja.Null()
		}
		return vm.ToValue(talkgroup)
	})

	systems.Set("unit", func(systemId int64, unitId int64) goja.Value {
		unit := rt.controller.PluginUnit(uint(systemId), uint(unitId))
		if unit == nil {
			return goja.Null()
		}
		return vm.ToValue(unit)
	})

	rdio.Set("systems", systems)

	// --- rdio.apikeys -----------------------------------------------------

	apikeys := vm.NewObject()

	apikeys.Set("verify", func(key string, system int64, talkgroup int64) goja.Value {

		valid, ident := rt.controller.PluginVerifyApikey(key, uint(system), uint(talkgroup))

		return vm.ToValue(map[string]any{"valid": valid, "ident": ident})
	})

	rdio.Set("apikeys", apikeys)

	// --- rdio.admin -------------------------------------------------------

	admin := vm.NewObject()

	admin.Set("verifyToken", func(token string) goja.Value {
		return vm.ToValue(rt.controller.PluginVerifyAdminToken(token))
	})

	rdio.Set("admin", admin)

	// --- rdio.downstreams -------------------------------------------------

	downstreams := vm.NewObject()

	downstreams.Set("forward", func(spec goja.Value) goja.Value {

		options, ok := spec.Export().(map[string]any)
		if !ok {
			throw("downstreams.forward requires an object")
		}

		routePath := stringFromMap(options, "path")
		if strings.TrimSpace(routePath) == "" {
			throw("downstreams.forward requires a path")
		}

		system, _ := numberFromMap(options, "system")
		talkgroup, _ := numberFromMap(options, "talkgroup")
		feature := stringFromMap(options, "requireFeature")

		body, _ := options["body"].(map[string]any)
		if body == nil {
			body = map[string]any{}
		}

		return rt.promiseFrom(vm, func() (any, error) {
			return rt.controller.ForwardToDownstreams(
				routePath, uint(system), uint(talkgroup), body, feature,
			), nil
		})
	})

	rdio.Set("downstreams", downstreams)

	// --- rdio.search ------------------------------------------------------

	search := vm.NewObject()

	search.Set("extend", func(spec goja.Value) goja.Value {
		m, ok := spec.Export().(map[string]any)
		if !ok {
			throw("search.extend requires an object")
		}

		extension := &pluginSearchExtension{
			pluginId:    rt.manifest.Id,
			Table:       stringFromMap(m, "table"),
			KeyColumn:   stringFromMap(m, "keyColumn"),
			TextColumn:  stringFromMap(m, "textColumn"),
			ResultField: stringFromMap(m, "resultField"),
		}

		if err := rt.validateExtension(extension.TextColumn, extension.Table, extension.KeyColumn, extension.TextColumn); err != nil {
			throw("search.extend: %v", err)
		}

		rt.mutex.Lock()
		rt.searchExtensions = append(rt.searchExtensions, extension)
		rt.mutex.Unlock()

		return goja.Undefined()
	})

	rdio.Set("search", search)

	// --- rdio.http --------------------------------------------------------

	httpObj := vm.NewObject()

	httpObj.Set("request", func(spec goja.Value) goja.Value {
		return rt.httpPromise(vm, spec, false)
	})

	httpObj.Set("multipart", func(spec goja.Value) goja.Value {
		return rt.httpPromise(vm, spec, true)
	})

	rdio.Set("http", httpObj)

	// --- rdio.routes ------------------------------------------------------

	routes := vm.NewObject()

	routes.Set("register", func(method string, path string, handler goja.Callable) goja.Value {
		if handler == nil {
			throw("routes.register requires a function")
		}

		rt.mutex.Lock()
		rt.routes = append(rt.routes, &pluginRoute{
			method:  strings.ToUpper(strings.TrimSpace(method)),
			path:    strings.Trim(strings.TrimSpace(path), "/"),
			handler: handler,
		})
		rt.mutex.Unlock()

		return goja.Undefined()
	})

	routes.Set("registerAbsolute", func(path string, handler goja.Callable) goja.Value {
		if handler == nil {
			throw("routes.registerAbsolute requires a function")
		}

		normalized := "/" + strings.TrimLeft(strings.TrimSpace(path), "/")

		// The admin surface is the one thing a plugin must not be able to
		// shadow — taking over /api/admin/login would be a privilege
		// escalation, not an integration.
		if strings.HasPrefix(normalized, "/api/admin/plugins") || normalized == "/api/admin/login" {
			throw("routes.registerAbsolute: %q is reserved", normalized)
		}

		rt.mutex.Lock()
		rt.routes = append(rt.routes, &pluginRoute{
			method:   "",
			path:     normalized,
			absolute: true,
			handler:  handler,
		})
		rt.mutex.Unlock()

		return goja.Undefined()
	})

	rdio.Set("routes", routes)

	// --- rdio.ws ----------------------------------------------------------

	ws := vm.NewObject()

	ws.Set("on", func(command string, handler goja.Callable) goja.Value {
		if handler == nil {
			throw("ws.on requires a function")
		}

		command = strings.ToUpper(strings.TrimSpace(command))
		if reservedWsCommands[command] {
			throw("ws.on: command %q is reserved by the server", command)
		}

		rt.mutex.Lock()
		rt.wsHandlers[command] = handler
		rt.mutex.Unlock()

		return goja.Undefined()
	})

	ws.Set("emit", func(filter goja.Value, command string, payload goja.Value) goja.Value {

		command = strings.ToUpper(strings.TrimSpace(command))
		if reservedWsCommands[command] {
			throw("ws.emit: command %q is reserved by the server", command)
		}

		var exported any
		if payload != nil && !goja.IsUndefined(payload) {
			exported = payload.Export()
		}

		rt.emitWs(filter, command, exported)

		return goja.Undefined()
	})

	rdio.Set("ws", ws)

	// --- rdio.capabilities ------------------------------------------------

	capabilities := vm.NewObject()

	capabilities.Set("advertise", func(name string) goja.Value {
		name = strings.TrimSpace(name)
		if name == "" {
			return goja.Undefined()
		}

		rt.mutex.Lock()
		rt.capabilities = append(rt.capabilities, name)
		rt.mutex.Unlock()

		return goja.Undefined()
	})

	rdio.Set("capabilities", capabilities)

	return vm.Set("rdio", rdio)
}

// reservedWsCommands are the protocol commands the server currently handles
// itself. A plugin taking one over would break every existing client.
//
// This tracks what core actually implements, not what the protocol has ever
// contained: when a command's handling moves out of core into a plugin, it
// comes off this list so the plugin can claim it.
var reservedWsCommands = map[string]bool{
	MessageCommandCall:           true,
	MessageCommandConfig:         true,
	MessageCommandExpired:        true,
	MessageCommandIOS:            true,
	MessageCommandListCall:       true,
	MessagecommandListenersCount: true,
	MessageCommandLivefeedMap:    true,
	MessageCommandMax:            true,
	MessageCommandPin:            true,
	MessageCommandPushId:         true,
	MessageCommandServer:         true,
	MessageCommandVersion:        true,
}

// validateExtension checks a declarative extension names a table this plugin
// actually owns. Without this a plugin could point an extension at another
// plugin's data and have the host happily join it in.
func (rt *PluginRuntime) validateExtension(field, table, keyColumn, valueColumn string) error {
	if strings.TrimSpace(field) == "" {
		return fmt.Errorf("field is required")
	}
	if strings.TrimSpace(keyColumn) == "" || strings.TrimSpace(valueColumn) == "" {
		return fmt.Errorf("keyColumn and valueColumn are required")
	}
	if !rt.db.tables[table] {
		return fmt.Errorf("table %q is not declared by this plugin", table)
	}
	if !pluginColumnNameRegexp.MatchString(keyColumn) || !pluginColumnNameRegexp.MatchString(valueColumn) {
		return fmt.Errorf("invalid column name")
	}
	return nil
}

// emitWs delivers a plugin message to matching clients, respecting each
// client's access scope so a plugin can't leak restricted data.
func (rt *PluginRuntime) emitWs(filter goja.Value, command string, payload any) {
	message := &Message{Command: command, Payload: payload}

	var (
		targetClient *Client
		system       uint
		talkgroup    uint
		scoped       bool
	)

	if filter != nil && !goja.IsUndefined(filter) && !goja.IsNull(filter) {
		if m, ok := filter.Export().(map[string]any); ok {
			if handle, ok := m["client"].(*pluginClientHandle); ok && handle != nil {
				targetClient = handle.client
			}
			s, hasSystem := numberFromMap(m, "system")
			t, hasTalkgroup := numberFromMap(m, "talkgroup")
			if hasSystem && hasTalkgroup {
				system = uint(s)
				talkgroup = uint(t)
				scoped = true
			}
		}
	}

	if targetClient != nil {
		targetClient.enqueue(message)
		return
	}

	rt.controller.Clients.EmitPluginMessage(message, scoped, system, talkgroup, rt.controller.Accesses.IsRestricted())
}

// --- helpers --------------------------------------------------------------

func exportPluginArgs(args goja.Value) []any {
	if args == nil || goja.IsUndefined(args) || goja.IsNull(args) {
		return nil
	}

	list, ok := args.Export().([]any)
	if !ok {
		return nil
	}

	out := make([]any, len(list))
	for i, value := range list {
		// database/sql has no idea what a goja.ArrayBuffer is, so a blob
		// argument failed at the driver — which meant every binary-producing
		// API in the plugin surface (crypto, fs, audio) had no way to write
		// what it produced, and the blob column type the manifest advertises
		// could not be used at all.
		if buffer, isBuffer := value.(goja.ArrayBuffer); isBuffer {
			out[i] = buffer.Bytes()
			continue
		}
		out[i] = value
	}

	return out
}

func stringFromMap(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func numberFromMap(m map[string]any, key string) (float64, bool) {
	switch v := m[key].(type) {
	case float64:
		return v, true
	case int64:
		return float64(v), true
	case int:
		return float64(v), true
	}
	return 0, false
}

// pluginCallValue is the call shape handed to plugins. Audio is opt-in because
// a call blob is typically 50-200 KB and copying it into the runtime for every
// hook would be pure waste for the majority of plugins that never touch it.
func pluginCallValue(call *Call, withAudio bool) map[string]any {
	// Copied, not shared. This is the live map on the call, and a downstream
	// forward iterates it on another goroutine while a plugin could be writing
	// to it from JavaScript — which is a fatal concurrent map access rather
	// than something the server can survive.
	meta := map[string]string{}
	for key, value := range call.meta {
		meta[key] = value
	}

	value := map[string]any{
		"id":   call.Id,
		"meta": meta,
		// RFC3339 UTC, the same form the wire protocol and every server-to-server
		// endpoint use. Handing over a Go time here would stringify differently
		// in JavaScript, so a plugin correlating a call with an inbound push
		// would silently never match.
		"dateTime":    call.DateTime.UTC().Format(time.RFC3339),
		"system":      call.System,
		"talkgroup":   call.Talkgroup,
		"frequency":   call.Frequency,
		"frequencies": call.Frequencies,
		"patches":     call.Patches,
		"source":      call.Source,
		"sources":     call.Sources,
		"audioName":   call.AudioName,
		"audioType":   call.AudioType,
		"audioSize":   len(call.Audio),
	}

	if withAudio {
		value["audio"] = call.Audio
	}

	return value
}

// httpPromise performs an outbound request off the event loop and settles a
// promise back on it. Plugins get async I/O without the host ever blocking a
// runtime on the network.
func (rt *PluginRuntime) httpPromise(vm *goja.Runtime, spec goja.Value, isMultipart bool) goja.Value {
	promise, resolve, reject := vm.NewPromise()

	options, ok := spec.Export().(map[string]any)
	if !ok {
		reject(vm.NewGoError(fmt.Errorf("http requires an options object")))
		return vm.ToValue(promise)
	}

	request, err := rt.buildHttpRequest(options, isMultipart)
	if err != nil {
		reject(vm.NewGoError(err))
		return vm.ToValue(promise)
	}

	// Whether the response is data or text. Text stays the default so nothing
	// that already calls http changes.
	binary, _ := options["binary"].(bool)

	timeout := pluginHttpTimeout
	if ms, ok := numberFromMap(options, "timeoutMs"); ok && ms > 0 {
		timeout = time.Duration(ms) * time.Millisecond
		if timeout > pluginHttpTimeoutMax {
			timeout = pluginHttpTimeoutMax
		}
	}

	go func() {
		client := &http.Client{Timeout: timeout}

		response, err := client.Do(request)
		if err != nil {
			rt.settle(func(vm *goja.Runtime) { reject(vm.NewGoError(err)) })
			return
		}
		defer response.Body.Close()

		// One byte past the limit, so a response that exactly fills it can be
		// told apart from one that was cut short.
		body, err := io.ReadAll(io.LimitReader(response.Body, pluginHttpMaxResponse+1))
		if err != nil {
			rt.settle(func(vm *goja.Runtime) { reject(vm.NewGoError(err)) })
			return
		}

		truncated := false
		if len(body) > pluginHttpMaxResponse {
			body = body[:pluginHttpMaxResponse]
			truncated = true
		}

		headers := map[string]any{}
		for k, v := range response.Header {
			if len(v) > 0 {
				headers[k] = v[0]
			}
		}

		result := map[string]any{
			"status":  response.StatusCode,
			"headers": headers,
			// Reported rather than swallowed: half a file still parses as a
			// file, so silence here is indistinguishable from success.
			"truncated": truncated,
		}

		rt.settle(func(vm *goja.Runtime) {
			// A JavaScript string is UTF-8, so returning arbitrary bytes
			// through one replaces every invalid sequence — around half of all
			// byte values do not survive. Fetching audio, an image or a
			// protobuf over HTTP was therefore silently corrupted, which is
			// exactly the defect rdio.exec was fixed for. Same remedy: ask for
			// bytes and get an ArrayBuffer.
			if binary {
				result["body"] = vm.NewArrayBuffer(body)
			} else {
				result["body"] = string(body)
			}

			resolve(vm.ToValue(result))
		})
	}()

	return vm.ToValue(promise)
}

// settle runs a promise resolution on the event loop. Resolving from the
// goroutine that did the I/O would touch the runtime from two threads.
func (rt *PluginRuntime) settle(fn func(vm *goja.Runtime)) {
	rt.runOnLoop(fn)
}

// promiseFrom runs work on a goroutine and settles a promise with its result.
// The generic shape behind every async host call.
func (rt *PluginRuntime) promiseFrom(vm *goja.Runtime, work func() (any, error)) goja.Value {
	promise, resolve, reject := vm.NewPromise()

	go func() {
		value, err := work()
		rt.settle(func(vm *goja.Runtime) {
			if err != nil {
				reject(vm.NewGoError(err))
				return
			}
			resolve(vm.ToValue(value))
		})
	}()

	return vm.ToValue(promise)
}

// searchCalls runs a call search on the plugin's behalf. Unscoped: a plugin
// runs server-side with calls-read already granted, so there is no per-listener
// access code to apply here.
func (rt *PluginRuntime) searchCalls(options goja.Value) (*CallsSearchResults, error) {
	searchOptions := &CallsSearchOptions{
		searchPatchedTalkgroups: rt.controller.Options.SearchPatchedTalkgroups,
	}

	if options != nil && !goja.IsUndefined(options) && !goja.IsNull(options) {
		if m, ok := options.Export().(map[string]any); ok {
			searchOptions.fromMap(m)
		}
	}

	// Calls.Search reads scoping maps off the client. A client with no access
	// restriction gives the plugin the unrestricted view it is entitled to.
	client := &Client{
		Controller: rt.controller,
		Access:     &Access{Systems: "*"},
	}
	client.SystemsMap = rt.controller.Systems.GetScopedSystems(
		client, rt.controller.Groups, rt.controller.Tags, rt.controller.Options.SortTalkgroups,
	)
	client.GroupsMap = rt.controller.Groups.GetGroupsMap(&client.SystemsMap)
	client.TagsMap = rt.controller.Tags.GetTagsMap(&client.SystemsMap)

	return rt.controller.Calls.Search(searchOptions, client)
}

// updateCall writes core call metadata a plugin is authoritative about.
//
// Nothing is currently writable. The whitelist is deliberately empty rather
// than absent: every field a plugin might want to attach to a call belongs in
// that plugin's own tables and reaches clients through calls.extendField, and
// letting a plugin rewrite core columns would make a call record's provenance
// impossible to reason about. This exists as the seam for the day a genuinely
// core-owned field needs plugin correction.
// updateCall writes back the parts of a stored call a plugin is allowed to
// change.
//
// Only the audio, and the two fields that describe it. Everything a plugin
// might want to *add* to a call belongs in the plugin's own table, published
// with calls.extendField — that keeps the plugin's schema its own business and
// keeps core's row meaning what it says.
//
// Audio is the exception because there is nothing else it could be. A plugin
// that cleans up or re-encodes a call has to be able to store the result, and
// until now it could not: call.store happens before the row exists and
// call.audio is a read. The method was documented as though it worked and its
// allow-list was empty, so every call to it threw.
func (rt *PluginRuntime) updateCall(id uint, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}

	audio, hasAudio := fields["audio"]
	name, hasName := fields["audioName"]
	audioType, hasType := fields["audioType"]

	for key := range fields {
		switch key {
		case "audio", "audioName", "audioType":
		default:
			return fmt.Errorf(
				"field %q cannot be updated by a plugin; store it in one of your own tables and publish it with rdio.calls.extendField",
				key,
			)
		}
	}

	call, err := rt.controller.Calls.GetCall(id, rt.controller.Database)
	if err != nil {
		return err
	}
	if call == nil {
		return fmt.Errorf("no call with id %d", id)
	}

	if hasAudio {
		body, err := pluginBytes(audio)
		if err != nil {
			return fmt.Errorf("audio: %v", err)
		}
		// Empty is refused rather than stored. A plugin handing back nothing
		// has almost certainly hit an error path it did not handle, and
		// honouring it would silently replace a call with silence — the one
		// outcome that cannot be undone.
		if len(body) == 0 {
			return fmt.Errorf("audio is empty; refusing to replace a stored call with nothing")
		}
		call.Audio = body
	}

	if hasName {
		if text, ok := name.(string); ok {
			call.AudioName = text
		}
	}

	if hasType {
		if text, ok := audioType.(string); ok {
			call.AudioType = text
		}
	}

	return rt.controller.Calls.UpdateAudio(call, rt.controller.Database)
}

func (rt *PluginRuntime) buildHttpRequest(options map[string]any, isMultipart bool) (*http.Request, error) {
	rawUrl := stringFromMap(options, "url")
	if strings.TrimSpace(rawUrl) == "" {
		return nil, fmt.Errorf("url is required")
	}

	method := strings.ToUpper(stringFromMap(options, "method"))

	var (
		body        io.Reader
		contentType string
	)

	if isMultipart {
		buffer := &bytes.Buffer{}
		writer := multipart.NewWriter(buffer)

		if fields, ok := options["fields"].(map[string]any); ok {
			for key, value := range fields {
				if err := writer.WriteField(key, fmt.Sprintf("%v", value)); err != nil {
					return nil, err
				}
			}
		}

		if files, ok := options["files"].([]any); ok {
			for _, entry := range files {
				file, ok := entry.(map[string]any)
				if !ok {
					continue
				}

				field := stringFromMap(file, "field")
				if field == "" {
					field = "file"
				}
				filename := stringFromMap(file, "filename")
				if filename == "" {
					filename = "file"
				}

				data, err := pluginBytes(file["data"])
				if err != nil {
					return nil, fmt.Errorf("file %q: %v", field, err)
				}

				part, err := writer.CreateFormFile(field, filename)
				if err != nil {
					return nil, err
				}
				if _, err := part.Write(data); err != nil {
					return nil, err
				}
			}
		}

		if err := writer.Close(); err != nil {
			return nil, err
		}

		body = buffer
		contentType = writer.FormDataContentType()

		if method == "" {
			method = http.MethodPost
		}
	} else {
		if raw, ok := options["body"]; ok && raw != nil {
			data, err := pluginBytes(raw)
			if err != nil {
				return nil, err
			}
			body = bytes.NewReader(data)
		}

		if method == "" {
			if body != nil {
				method = http.MethodPost
			} else {
				method = http.MethodGet
			}
		}
	}

	request, err := http.NewRequest(method, rawUrl, body)
	if err != nil {
		return nil, err
	}

	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}

	if headers, ok := options["headers"].(map[string]any); ok {
		for key, value := range headers {
			request.Header.Set(key, fmt.Sprintf("%v", value))
		}
	}

	if request.Header.Get("User-Agent") == "" {
		request.Header.Set("User-Agent", "rdio-scanner-plugin/"+rt.manifest.Id)
	}

	return request, nil
}

// pluginBytes coerces the several shapes a JS value can arrive in when it is
// meant to be binary: a string, a Go []byte (what rdio.calls.get hands back),
// or a plain array of numbers.
func pluginBytes(v any) ([]byte, error) {
	switch t := v.(type) {
	case nil:
		return nil, nil
	case string:
		return []byte(t), nil
	case []byte:
		return t, nil
	case goja.ArrayBuffer:
		return t.Bytes(), nil
	case []any:
		out := make([]byte, len(t))
		for i, entry := range t {
			switch n := entry.(type) {
			case int64:
				out[i] = byte(n)
			case float64:
				out[i] = byte(n)
			default:
				return nil, fmt.Errorf("array contains a non-numeric value")
			}
		}
		return out, nil

	// Typed arrays, as their underlying bytes.
	//
	// goja exports each as its Go slice equivalent, and only Uint8Array
	// happened to land on []byte — so the one array type an audio plugin
	// actually holds, Int16Array, was refused with "value cannot be used as
	// binary data". The documented flow said to build one from decoded samples
	// and hand it back to encode, and doing exactly that threw.
	//
	// Little-endian throughout, matching what the JavaScript buffer holds and
	// what the audio pipeline reads and writes.
	case []int8:
		out := make([]byte, len(t))
		for i, n := range t {
			out[i] = byte(n)
		}
		return out, nil

	case []int16:
		out := make([]byte, len(t)*2)
		for i, n := range t {
			binary.LittleEndian.PutUint16(out[i*2:], uint16(n))
		}
		return out, nil

	case []uint16:
		out := make([]byte, len(t)*2)
		for i, n := range t {
			binary.LittleEndian.PutUint16(out[i*2:], n)
		}
		return out, nil

	case []int32:
		out := make([]byte, len(t)*4)
		for i, n := range t {
			binary.LittleEndian.PutUint32(out[i*4:], uint32(n))
		}
		return out, nil

	case []uint32:
		out := make([]byte, len(t)*4)
		for i, n := range t {
			binary.LittleEndian.PutUint32(out[i*4:], n)
		}
		return out, nil

	// Float arrays are refused rather than reinterpreted. Their bytes are
	// perfectly well defined, but every consumer of this function that cares
	// about sample data reads 16-bit PCM — so accepting one would not fail, it
	// would produce noise, which is far worse than an error naming the problem.
	case []float32:
		return nil, fmt.Errorf("a Float32Array is not sample data here; audio is 16-bit, so convert to an Int16Array first")

	case []float64:
		return nil, fmt.Errorf("a Float64Array is not sample data here; audio is 16-bit, so convert to an Int16Array first")

	default:
		return nil, fmt.Errorf("value cannot be used as binary data")
	}
}
