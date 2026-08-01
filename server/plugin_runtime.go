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
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/dop251/goja"
	"github.com/dop251/goja_nodejs/eventloop"
)

// pluginCallTimeout bounds a single plugin callback. A plugin that spins is
// interrupted rather than pinning its event loop forever. Generous on purpose:
// the goal is to turn "hangs until restart" into "logs an error", not to police
// slow-but-working code.
const pluginCallTimeout = 30 * time.Second

// pluginStartupTimeout bounds evaluating main.js. Shorter than a callback
// because startup should only be registering handlers.
const pluginStartupTimeout = 15 * time.Second

// Lifecycle events dispatched to plugins.
const (
	PluginEventStartup       = "startup"
	PluginEventShutdown      = "shutdown"
	PluginEventCallIngested  = "call.ingested"
	PluginEventCallStored    = "call.stored"
	PluginEventCallEmitted   = "call.emitted"
	PluginEventConfigChanged = "config.changed"
	PluginEventTick          = "tick"
)

var pluginEvents = map[string]bool{
	PluginEventStartup:       true,
	PluginEventShutdown:      true,
	PluginEventCallIngested:  true,
	PluginEventCallStored:    true,
	PluginEventCallEmitted:   true,
	PluginEventConfigChanged: true,
	PluginEventTick:          true,
}

// pluginRoute is one HTTP endpoint a plugin has registered.
type pluginRoute struct {
	method   string
	path     string
	absolute bool
	handler  goja.Callable
}

// pluginFieldExtension declaratively adds a field to outgoing call payloads,
// sourced from one of the plugin's own tables.
//
// This is deliberately data, not a callback: the lookup happens in Go on the
// emit path, which runs for every call to every listener. Routing that through
// the interpreter would serialise the whole broadcast behind one JS event loop.
type pluginFieldExtension struct {
	pluginId    string
	Field       string
	Table       string
	KeyColumn   string
	ValueColumn string
}

// pluginSearchExtension makes a plugin-owned text column searchable through the
// normal call search. Same reasoning as pluginFieldExtension — per-row JS on a
// search over hundreds of thousands of calls is not viable.
type pluginSearchExtension struct {
	pluginId    string
	Table       string
	KeyColumn   string
	TextColumn  string
	ResultField string
}

// PluginRuntime is one plugin's JavaScript environment: a goja runtime, its
// event loop, and the registries the host reads when dispatching work into it.
type PluginRuntime struct {
	controller *Controller
	plugin     *Plugin
	manifest   *PluginManifest
	db         *PluginDb

	loop *eventloop.EventLoop
	vm   *goja.Runtime

	mutex      sync.RWMutex
	handlers   map[string][]goja.Callable
	wsHandlers map[string]goja.Callable
	routes     []*pluginRoute
	intervals  []*eventloop.Interval

	fieldExtensions  []*pluginFieldExtension
	searchExtensions []*pluginSearchExtension
	capabilities     []string
	exposedConfig    map[string]any

	config   map[string]any
	configMu sync.RWMutex

	stopped bool
}

func NewPluginRuntime(controller *Controller, plugin *Plugin) (*PluginRuntime, error) {
	if plugin.Manifest == nil {
		return nil, fmt.Errorf("plugin has no manifest")
	}

	config, err := ReadPluginConfig(controller.Database, plugin.Manifest)
	if err != nil {
		return nil, fmt.Errorf("config: %v", err)
	}

	return &PluginRuntime{
		controller:    controller,
		plugin:        plugin,
		manifest:      plugin.Manifest,
		db:            NewPluginDb(controller.Database, plugin.Manifest),
		handlers:      map[string][]goja.Callable{},
		wsHandlers:    map[string]goja.Callable{},
		routes:        []*pluginRoute{},
		exposedConfig: map[string]any{},
		config:        config,
	}, nil
}

// Start creates the runtime, binds the host API, evaluates main.js and fires
// the startup event.
func (rt *PluginRuntime) Start() error {
	source, err := rt.readMain()
	if err != nil {
		return err
	}

	// Console is bound by the host so plugin output lands in the Rdio Scanner
	// log rather than stdout, where a service install would lose it.
	rt.loop = eventloop.NewEventLoop(eventloop.EnableConsole(false))
	rt.loop.Start()

	var startErr error

	// Run (not RunOnLoop) so setup completes before Start returns and the
	// plugin is reported as running.
	rt.loop.Run(func(vm *goja.Runtime) {
		rt.vm = vm

		// Field names in JS are conventionally camelCase; without this, Go
		// struct fields would be exposed with their Go names.
		vm.SetFieldNameMapper(goja.UncapFieldNameMapper())

		if err := rt.bindHostApi(vm); err != nil {
			startErr = err
			return
		}

		stop := rt.armWatchdog(vm, "startup", pluginStartupTimeout)
		defer stop()

		if _, err := vm.RunScript(rt.manifest.Main, source); err != nil {
			startErr = fmt.Errorf("%s: %v", rt.manifest.Main, err)
		}
	})

	if startErr != nil {
		return startErr
	}

	rt.Emit(PluginEventStartup, nil)

	return nil
}

func (rt *PluginRuntime) readMain() (string, error) {
	// The manifest's entry path was validated as non-escaping at parse time;
	// re-check here because this is where it becomes a filesystem read.
	if err := validatePluginRelPath(rt.manifest.Main); err != nil {
		return "", err
	}

	path := filepath.Join(rt.plugin.dir, filepath.FromSlash(rt.manifest.Main))

	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %v", rt.manifest.Main, err)
	}

	return string(b), nil
}

// armWatchdog interrupts the runtime if the current job overruns. Jobs are
// serialised on the event loop, so interrupting only ever kills the job that
// is actually running.
func (rt *PluginRuntime) armWatchdog(vm *goja.Runtime, label string, timeout time.Duration) func() {
	timer := time.AfterFunc(timeout, func() {
		vm.Interrupt(fmt.Sprintf("plugin %s exceeded the %s time limit in %s", rt.manifest.Id, timeout, label))
	})

	return func() {
		timer.Stop()
		vm.ClearInterrupt()
	}
}

// Stop tears the runtime down. Best effort: a plugin that will not stop must
// not block server shutdown, so a stuck loop is terminated outright.
func (rt *PluginRuntime) Stop() {
	rt.mutex.Lock()
	if rt.stopped {
		rt.mutex.Unlock()
		return
	}
	rt.stopped = true
	intervals := append([]*eventloop.Interval{}, rt.intervals...)
	rt.intervals = nil
	rt.mutex.Unlock()

	if rt.loop == nil {
		return
	}

	for _, interval := range intervals {
		rt.loop.ClearInterval(interval)
	}

	// Give shutdown handlers a moment on the loop, then stop it regardless.
	done := make(chan struct{})
	go func() {
		defer close(done)
		rt.dispatchSync(PluginEventShutdown, nil)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}

	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		rt.loop.Stop()
	}()

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		rt.loop.Terminate()
	}
}

// Emit dispatches an event asynchronously. Never blocks the caller — the
// busiest caller is the single-goroutine ingest path, where blocking would
// stall every subsequent call.
func (rt *PluginRuntime) Emit(event string, payload any) {
	rt.mutex.RLock()
	stopped := rt.stopped
	handlers := append([]goja.Callable{}, rt.handlers[event]...)
	rt.mutex.RUnlock()

	if stopped || len(handlers) == 0 {
		return
	}

	rt.runOnLoop(func(vm *goja.Runtime) {
		stop := rt.armWatchdog(vm, event, pluginCallTimeout)
		defer stop()

		value := rt.toValue(vm, payload)

		for _, handler := range handlers {
			if _, err := handler(goja.Undefined(), value); err != nil {
				rt.logCallError(event, err)
			}
		}
	})
}

// dispatchSync is Emit for shutdown, where the caller does want to wait.
func (rt *PluginRuntime) dispatchSync(event string, payload any) {
	rt.mutex.RLock()
	handlers := append([]goja.Callable{}, rt.handlers[event]...)
	rt.mutex.RUnlock()

	if len(handlers) == 0 {
		return
	}

	rt.loop.Run(func(vm *goja.Runtime) {
		stop := rt.armWatchdog(vm, event, pluginCallTimeout)
		defer stop()

		value := rt.toValue(vm, payload)

		for _, handler := range handlers {
			if _, err := handler(goja.Undefined(), value); err != nil {
				rt.logCallError(event, err)
			}
		}
	})
}

// runOnLoop schedules work on the plugin's event loop, recovering from panics
// so a misbehaving plugin cannot take the server down with it.
func (rt *PluginRuntime) runOnLoop(fn func(vm *goja.Runtime)) bool {
	rt.mutex.RLock()
	stopped := rt.stopped
	loop := rt.loop
	rt.mutex.RUnlock()

	if stopped || loop == nil {
		return false
	}

	return loop.RunOnLoop(func(vm *goja.Runtime) {
		defer func() {
			if r := recover(); r != nil {
				rt.controller.Logs.LogEvent(
					LogLevelError,
					fmt.Sprintf("plugin %s panicked: %v", rt.manifest.Id, r),
				)
			}
		}()
		fn(vm)
	})
}

// toValue converts a Go payload into a JS value. Structs go through goja's
// reflection, which combined with the UncapFieldNameMapper gives plugins
// camelCase property names.
func (rt *PluginRuntime) toValue(vm *goja.Runtime, payload any) goja.Value {
	if payload == nil {
		return goja.Undefined()
	}
	return vm.ToValue(payload)
}

func (rt *PluginRuntime) logCallError(label string, err error) {
	rt.controller.Logs.LogEvent(
		LogLevelError,
		fmt.Sprintf("plugin %s error in %s: %v", rt.manifest.Id, label, err),
	)
}

// --- registries read by the host -----------------------------------------

// Routes returns the HTTP endpoints this plugin has registered.
func (rt *PluginRuntime) Routes() []*pluginRoute {
	rt.mutex.RLock()
	defer rt.mutex.RUnlock()
	return append([]*pluginRoute{}, rt.routes...)
}

// FieldExtensions returns the declarative call-payload extensions.
func (rt *PluginRuntime) FieldExtensions() []*pluginFieldExtension {
	rt.mutex.RLock()
	defer rt.mutex.RUnlock()
	return append([]*pluginFieldExtension{}, rt.fieldExtensions...)
}

// SearchExtensions returns the declarative search extensions.
func (rt *PluginRuntime) SearchExtensions() []*pluginSearchExtension {
	rt.mutex.RLock()
	defer rt.mutex.RUnlock()
	return append([]*pluginSearchExtension{}, rt.searchExtensions...)
}

// Capabilities returns feature names this plugin advertises to peer servers.
func (rt *PluginRuntime) Capabilities() []string {
	rt.mutex.RLock()
	defer rt.mutex.RUnlock()
	return append([]string{}, rt.capabilities...)
}

// ExposedConfig returns the keys this plugin wants included in the config
// payload sent to webapp clients.
func (rt *PluginRuntime) ExposedConfig() map[string]any {
	rt.mutex.RLock()
	defer rt.mutex.RUnlock()

	exposed := map[string]any{}
	for k, v := range rt.exposedConfig {
		exposed[k] = v
	}
	return exposed
}

// HasWsHandler reports whether this plugin claims a websocket command.
func (rt *PluginRuntime) HasWsHandler(command string) bool {
	rt.mutex.RLock()
	defer rt.mutex.RUnlock()
	_, ok := rt.wsHandlers[command]
	return ok
}

// DispatchWs hands a websocket message to the plugin that claimed the command.
func (rt *PluginRuntime) DispatchWs(client *Client, command string, payload any) {
	rt.mutex.RLock()
	handler, ok := rt.wsHandlers[command]
	rt.mutex.RUnlock()

	if !ok {
		return
	}

	rt.runOnLoop(func(vm *goja.Runtime) {
		stop := rt.armWatchdog(vm, "ws:"+command, pluginCallTimeout)
		defer stop()

		clientValue := vm.ToValue(newPluginClientHandle(client))

		if _, err := handler(goja.Undefined(), clientValue, rt.toValue(vm, payload)); err != nil {
			rt.logCallError("ws:"+command, err)
		}
	})
}

// DispatchRoute invokes a registered HTTP handler and waits for its result.
// HTTP handlers are request/response by nature, so unlike events this one does
// block — bounded by the same per-call timeout.
func (rt *PluginRuntime) DispatchRoute(route *pluginRoute, request map[string]any) (result any, err error) {
	type outcome struct {
		value any
		err   error
	}

	ch := make(chan outcome, 1)

	scheduled := rt.runOnLoop(func(vm *goja.Runtime) {
		stop := rt.armWatchdog(vm, "route:"+route.path, pluginCallTimeout)
		defer stop()

		value, err := route.handler(goja.Undefined(), vm.ToValue(request))
		if err != nil {
			ch <- outcome{nil, err}
			return
		}

		// A handler may return a promise (it probably used rdio.http). Resolve
		// it before answering the HTTP request rather than serialising a
		// pending promise object.
		if promise, ok := value.Export().(*goja.Promise); ok {
			rt.awaitPromise(vm, promise, func(v any, err error) {
				ch <- outcome{v, err}
			})
			return
		}

		ch <- outcome{value.Export(), nil}
	})

	if !scheduled {
		return nil, fmt.Errorf("plugin %s is not running", rt.manifest.Id)
	}

	select {
	case out := <-ch:
		return out.value, out.err
	case <-time.After(pluginCallTimeout + 5*time.Second):
		return nil, fmt.Errorf("plugin %s timed out handling %s", rt.manifest.Id, route.path)
	}
}

// awaitPromise polls a promise to settlement on the event loop. goja has no
// callback hook for this, and a promise can only be inspected from the loop
// goroutine, so settlement is checked from a scheduled job.
func (rt *PluginRuntime) awaitPromise(vm *goja.Runtime, promise *goja.Promise, done func(any, error)) {
	switch promise.State() {
	case goja.PromiseStateFulfilled:
		done(promise.Result().Export(), nil)
		return
	case goja.PromiseStateRejected:
		done(nil, fmt.Errorf("%v", promise.Result()))
		return
	}

	// Still pending — let the loop make progress, then look again.
	rt.loop.SetTimeout(func(vm *goja.Runtime) {
		rt.awaitPromise(vm, promise, done)
	}, 5*time.Millisecond)
}
