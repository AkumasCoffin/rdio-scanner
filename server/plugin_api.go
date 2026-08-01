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

// bindHostApi installs the `rdio` global and a console shim. Every entry point
// that needs a permission checks it here rather than at call time, so a plugin
// missing a permission fails loudly at the point of use.
func (rt *PluginRuntime) bindHostApi(vm *goja.Runtime) error {
	throw := func(format string, args ...any) {
		panic(vm.NewGoError(fmt.Errorf(format, args...)))
	}

	requirePermission := func(permission string) {
		if !rt.manifest.HasPermission(permission) {
			throw("plugin %s requires the %q permission in %s to do that", rt.manifest.Id, permission, PluginManifestName)
		}
	}

	rdio := vm.NewObject()

	// --- rdio.plugin ------------------------------------------------------

	pluginInfo := vm.NewObject()
	pluginInfo.Set("id", rt.manifest.Id)
	pluginInfo.Set("version", rt.manifest.Version)
	pluginInfo.Set("dataDir", rt.plugin.dir)
	rdio.Set("plugin", pluginInfo)

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
		requirePermission(PluginPermissionConfigExpose)

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

	rdio.Set("on", func(event string, handler goja.Callable) goja.Value {
		if !pluginEvents[event] {
			throw("unknown event %q", event)
		}
		if handler == nil {
			throw("rdio.on(%q) requires a function", event)
		}

		rt.mutex.Lock()
		rt.handlers[event] = append(rt.handlers[event], handler)
		rt.mutex.Unlock()

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
		requirePermission(PluginPermissionCallsRead)

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
		requirePermission(PluginPermissionCallsRead)

		results, err := rt.searchCalls(options)
		if err != nil {
			throw("calls.search: %v", err)
		}

		return vm.ToValue(results)
	})

	calls.Set("update", func(id int64, fields goja.Value) goja.Value {
		requirePermission(PluginPermissionCallsWrite)

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
		requirePermission(PluginPermissionCallsRead)

		id, err := rt.controller.PluginFindCallId(uint(system), uint(talkgroup), dateTime)
		if err != nil {
			throw("calls.findId: %v", err)
		}

		return vm.ToValue(id)
	})

	rdio.Set("calls", calls)

	// --- rdio.systems -----------------------------------------------------

	systems := vm.NewObject()

	systems.Set("list", func() goja.Value {
		// Ungated: this is the same configuration every websocket client
		// already receives. Withholding it would only push plugins into
		// keeping their own stale copy.
		return vm.ToValue(rt.controller.PluginSystemsList())
	})

	rdio.Set("systems", systems)

	// --- rdio.apikeys -----------------------------------------------------

	apikeys := vm.NewObject()

	apikeys.Set("verify", func(key string, system int64, talkgroup int64) goja.Value {
		requirePermission(PluginPermissionApikeysVerify)

		valid, ident := rt.controller.PluginVerifyApikey(key, uint(system), uint(talkgroup))

		return vm.ToValue(map[string]any{"valid": valid, "ident": ident})
	})

	rdio.Set("apikeys", apikeys)

	// --- rdio.admin -------------------------------------------------------

	admin := vm.NewObject()

	admin.Set("verifyToken", func(token string) goja.Value {
		requirePermission(PluginPermissionAdminVerify)
		return vm.ToValue(rt.controller.PluginVerifyAdminToken(token))
	})

	rdio.Set("admin", admin)

	// --- rdio.downstreams -------------------------------------------------

	downstreams := vm.NewObject()

	downstreams.Set("forward", func(spec goja.Value) goja.Value {
		requirePermission(PluginPermissionDownstreams)

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
		requirePermission(PluginPermissionHttp)
		return rt.httpPromise(vm, spec, false)
	})

	httpObj.Set("multipart", func(spec goja.Value) goja.Value {
		requirePermission(PluginPermissionHttp)
		return rt.httpPromise(vm, spec, true)
	})

	rdio.Set("http", httpObj)

	// --- rdio.routes ------------------------------------------------------

	routes := vm.NewObject()

	routes.Set("register", func(method string, path string, handler goja.Callable) goja.Value {
		requirePermission(PluginPermissionRoutes)
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
		requirePermission(PluginPermissionRoutesAbsolute)
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
		requirePermission(PluginPermissionWs)
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
		requirePermission(PluginPermissionWs)

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

	exported := args.Export()

	if list, ok := exported.([]any); ok {
		return list
	}

	return []any{exported}
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
	meta := call.meta
	if meta == nil {
		meta = map[string]string{}
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

		body, err := io.ReadAll(io.LimitReader(response.Body, pluginHttpMaxResponse))
		if err != nil {
			rt.settle(func(vm *goja.Runtime) { reject(vm.NewGoError(err)) })
			return
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
			"body":    string(body),
		}

		rt.settle(func(vm *goja.Runtime) { resolve(vm.ToValue(result)) })
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
func (rt *PluginRuntime) updateCall(id uint, fields map[string]any) error {
	for key := range fields {
		return fmt.Errorf(
			"field %q cannot be updated by a plugin; store it in one of your own tables and publish it with rdio.calls.extendField",
			key,
		)
	}

	return nil
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
	default:
		return nil, fmt.Errorf("value cannot be used as binary data")
	}
}
