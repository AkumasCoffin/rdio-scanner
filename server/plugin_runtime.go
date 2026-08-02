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
	// dataDir is this plugin's persistent storage, outside the code directory
	// the installer rewrites on update.
	dataDir string

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
	// dispatching guards against a blocking handler causing core to dispatch
	// back into this same runtime, which would wait on an event loop the
	// handler is itself occupying.
	dispatching bool
}

func NewPluginRuntime(controller *Controller, plugin *Plugin) (*PluginRuntime, error) {
	if plugin.Manifest == nil {
		return nil, fmt.Errorf("plugin has no manifest")
	}

	config, err := ReadPluginConfig(controller.Database, plugin.Manifest)
	if err != nil {
		return nil, fmt.Errorf("config: %v", err)
	}

	dataDir, err := controller.Plugins.DataDir(controller.Config, plugin.Manifest.Id)
	if err != nil {
		return nil, fmt.Errorf("data directory: %v", err)
	}

	return &PluginRuntime{
		controller:    controller,
		plugin:        plugin,
		manifest:      plugin.Manifest,
		db:            NewPluginDb(controller.Database, plugin.Manifest),
		dataDir:       dataDir,
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

	// Setup is scheduled on the loop and waited for, so Start doesn't return
	// until main.js has been evaluated and the plugin is genuinely ready. Note
	// this cannot use loop.Run: that starts a loop of its own and panics on one
	// that is already running.
	if err := rt.runOnLoopAndWait(pluginStartupTimeout+5*time.Second, func(vm *goja.Runtime) {
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
	}); err != nil {
		return err
	}

	if startErr != nil {
		return startErr
	}

	rt.Emit(PointStartup, nil)

	return nil
}

// runOnLoopAndWait schedules work on the loop and blocks until it finishes or
// the deadline passes. Used for the few operations that genuinely need to be
// synchronous — startup and shutdown — never on the ingest path.
func (rt *PluginRuntime) runOnLoopAndWait(timeout time.Duration, fn func(vm *goja.Runtime)) error {
	done := make(chan struct{})

	scheduled := rt.loop.RunOnLoop(func(vm *goja.Runtime) {
		defer close(done)
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

	if !scheduled {
		return fmt.Errorf("plugin %s event loop is not running", rt.manifest.Id)
	}

	select {
	case <-done:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("plugin %s timed out during startup", rt.manifest.Id)
	}
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
		rt.dispatchSync(PointShutdown, nil)
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

	rt.runOnLoopAndWait(pluginCallTimeout, func(vm *goja.Runtime) {
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

// gojaCallable adapts a JS function to the engine-agnostic shape the dispatch
// registry works with.
type gojaCallable struct {
	fn goja.Callable
}

func (c *gojaCallable) call(args ...any) (any, error) {
	// Unused: dispatch always goes through PluginRuntime.CallSync, which owns
	// the event loop and the watchdog. Present only to satisfy the interface.
	return nil, fmt.Errorf("plugin callables must be invoked through CallSync")
}

// EmitTo fires an observer asynchronously. Separate from Emit because the
// dispatch registry already knows which handler it wants, and re-scanning the
// plugin's own handler list would run every handler for the point again.
func (rt *PluginRuntime) EmitTo(point string, payload any) {
	rt.mutex.RLock()
	handlers := append([]goja.Callable{}, rt.handlers[point]...)
	stopped := rt.stopped
	rt.mutex.RUnlock()

	if stopped || len(handlers) == 0 {
		return
	}

	rt.runOnLoop(func(vm *goja.Runtime) {
		stop := rt.armWatchdog(vm, point, pluginCallTimeout)
		defer stop()

		value := rt.toValue(vm, payload)

		for _, handler := range handlers {
			if _, err := handler(goja.Undefined(), value); err != nil {
				rt.logCallError(point, err)
			}
		}
	})
}

// CallSync invokes one handler and waits for its result, resolving a returned
// promise before answering. Used by the blocking verbs.
//
// Re-entrancy is refused rather than risked: a handler that causes core to
// dispatch back into the same plugin would be waiting on an event loop it is
// itself occupying. That is a deadlock, and the only safe answer is to decline
// the inner call and let the failure rule treat it as a no-op.
func (rt *PluginRuntime) CallSync(point string, timeout time.Duration, callable pluginCallable, args ...any) (any, error) {
	adapter, ok := callable.(*gojaCallable)
	if !ok {
		return nil, fmt.Errorf("unknown callable")
	}

	if !rt.enterDispatch() {
		return nil, fmt.Errorf("re-entrant dispatch at %s refused", point)
	}
	defer rt.leaveDispatch()

	type outcome struct {
		value any
		err   error
	}

	ch := make(chan outcome, 1)

	scheduled := rt.runOnLoop(func(vm *goja.Runtime) {
		stop := rt.armWatchdog(vm, point, timeout)
		defer stop()

		converted := make([]goja.Value, len(args))
		for i, arg := range args {
			converted[i] = rt.toValue(vm, arg)
		}

		value, err := adapter.fn(goja.Undefined(), converted...)
		if err != nil {
			ch <- outcome{nil, err}
			return
		}

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
	case <-time.After(timeout + time.Second):
		return nil, fmt.Errorf("plugin %s timed out at %s", rt.manifest.Id, point)
	}
}

// enterDispatch reports whether a blocking dispatch may proceed, guarding
// against the runtime being asked to wait on itself.
func (rt *PluginRuntime) enterDispatch() bool {
	rt.mutex.Lock()
	defer rt.mutex.Unlock()

	if rt.stopped || rt.dispatching {
		return false
	}

	rt.dispatching = true
	return true
}

func (rt *PluginRuntime) leaveDispatch() {
	rt.mutex.Lock()
	rt.dispatching = false
	rt.mutex.Unlock()
}

// awaitPromise resolves a promise by attaching Go-backed callbacks with its own
// then(), rather than polling its state on a timer. Polling worked but added up
// to a tick of latency to every promise-returning route and kept the loop busy
// while a request was in flight.
//
// Must be called on the loop goroutine — promise values cannot be touched from
// anywhere else.
func (rt *PluginRuntime) awaitPromise(vm *goja.Runtime, promise *goja.Promise, done func(any, error)) {
	// Already settled: answer without going back through the microtask queue.
	switch promise.State() {
	case goja.PromiseStateFulfilled:
		done(promise.Result().Export(), nil)
		return
	case goja.PromiseStateRejected:
		done(nil, fmt.Errorf("%v", promise.Result()))
		return
	}

	promiseValue := vm.ToValue(promise).ToObject(vm)

	thenValue := promiseValue.Get("then")
	then, ok := goja.AssertFunction(thenValue)
	if !ok {
		done(nil, fmt.Errorf("plugin returned a promise with no usable then()"))
		return
	}

	// Guards against a plugin resolving and rejecting, or settling twice: the
	// HTTP caller is waiting on a single-slot channel.
	settled := false

	onFulfilled := vm.ToValue(func(call goja.FunctionCall) goja.Value {
		if settled {
			return goja.Undefined()
		}
		settled = true
		done(call.Argument(0).Export(), nil)
		return goja.Undefined()
	})

	onRejected := vm.ToValue(func(call goja.FunctionCall) goja.Value {
		if settled {
			return goja.Undefined()
		}
		settled = true
		done(nil, fmt.Errorf("%v", call.Argument(0)))
		return goja.Undefined()
	})

	if _, err := then(promiseValue, onFulfilled, onRejected); err != nil {
		done(nil, err)
	}
}
