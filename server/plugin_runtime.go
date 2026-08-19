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
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
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

// pluginLoopMaxQueued bounds one plugin's pending work. Deep enough that a
// burst of calls is absorbed, shallow enough that a plugin falling behind is
// noticed while the process still has memory: every queued job holds a copy of
// whatever it was handed.
const pluginLoopMaxQueued = 4096

// pluginRouteBusyQueue is the backlog past which an HTTP route stops queueing
// and answers 503 straight away.
//
// Far below pluginLoopMaxQueued on purpose: that limit protects the server's
// memory and is the right place to start shedding notifications, which nobody
// is waiting on. A caller holding an HTTP connection open is waiting, and a
// backlog this deep already means it will not be served in time. Better to be
// told now.
const pluginRouteBusyQueue = 64

// pluginRouteTimeout bounds a route handler end to end, including any promise
// it returns.
//
// It has to exceed pluginCallTimeout: the watchdog only covers the synchronous
// part, and a handler that awaits rdio.http is legitimately unfinished when
// that part returns. It used to be pluginCallTimeout + 5s, which was shorter
// than rdio.http's own 60s default — so a route doing the single most ordinary
// async thing a route can do was guaranteed to be abandoned before its own
// request could come back.
//
// A plugin that raises timeoutMs beyond pluginHttpTimeout is still cut off
// here. That is deliberate: something in front of this server (a proxy, a
// browser, a retrying uploader) gives up around the two-minute mark, so a
// route that runs longer is answering into a void.
const pluginRouteTimeout = pluginHttpTimeout + pluginCallTimeout

// pluginBusyError says the plugin's loop is too far behind to take more work.
// A distinct type so the HTTP layer can answer 503 with a Retry-After rather
// than a flat 500 — the difference between "try again shortly" and "this
// request is broken".
type pluginBusyError struct {
	plugin string
	detail string
}

func (e *pluginBusyError) Error() string {
	return fmt.Sprintf("plugin %s is too far behind to take the request (%s)", e.plugin, e.detail)
}

// observerTimeout is what an observer at a point may take.
//
// Observers used to get a flat pluginCallTimeout regardless of where they were
// registered, which meant one on call.emit could hold the loop for thirty
// seconds against a documented 250ms ceiling — two orders of magnitude more
// than the filter running beside it. They get the point's own budget now, with
// a floor so a tight per-listener deadline does not make ordinary observer work
// impossible.
func observerTimeout(point string) time.Duration {
	timeout := pointTimeout(point)

	if timeout < time.Second {
		return time.Second
	}

	return timeout
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
	// listeners are the sockets this plugin opened, closed on Stop so a
	// disabled plugin does not leave a port bound.
	listeners []*pluginListener

	fieldExtensions  []*pluginFieldExtension
	searchExtensions []*pluginSearchExtension
	capabilities     []string
	exposedConfig    map[string]any

	config   map[string]any
	configMu sync.RWMutex

	// queued counts jobs waiting on the event loop, so a plugin that cannot
	// keep up is shed rather than allowed to grow an unbounded backlog.
	queued atomic.Int64

	// The last job that held the loop past pluginSlowJobThreshold. Everything
	// a plugin does shares one loop, so when a call times out the useful
	// question is not "did it time out" but "what was in the way" — and the
	// caller that times out is never the culprit, it is the victim.
	slowJobMutex sync.Mutex
	slowJobLabel string
	slowJobTook  time.Duration
	slowJobAt    time.Time

	loopLogThrottle *LogThrottle

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

	dataDir, err := controller.Plugins.DataDir(controller.Config, plugin.Manifest.Id)
	if err != nil {
		return nil, fmt.Errorf("data directory: %v", err)
	}

	return &PluginRuntime{
		controller:      controller,
		plugin:          plugin,
		manifest:        plugin.Manifest,
		db:              NewPluginDb(controller.Database, plugin.Manifest),
		dataDir:         dataDir,
		handlers:        map[string][]goja.Callable{},
		loopLogThrottle: NewLogThrottle(1, time.Minute),
		wsHandlers:      map[string]goja.Callable{},
		routes:          []*pluginRoute{},
		exposedConfig:   map[string]any{},
		config:          config,
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

// armWatchdog interrupts the runtime if the work it guards outlasts its
// deadline. Jobs are serialised on the event loop, so interrupting only ever
// kills the job that is actually running.
//
// The bookkeeping matters more than it looks. time.Stop does not wait for a
// timer that has already begun running, so the naive version — stop, then
// clear — had a window where the clear ran first and the interrupt landed
// afterwards, with nothing left to receive it. The flag stayed set and the
// *next* job on that loop died immediately, reporting a timeout in whatever
// point the previous one had been running. Clearing unconditionally was wrong
// for the same reason in reverse: with two watchdogs armed, the inner one's
// disarm cancelled the outer one's.
//
// So: the timer only interrupts if it wins the race, and the disarm only
// clears an interrupt this watchdog actually raised.
func (rt *PluginRuntime) armWatchdog(vm *goja.Runtime, label string, timeout time.Duration) func() {
	var (
		mutex    sync.Mutex
		fired    bool
		disarmed bool
	)

	timer := time.AfterFunc(timeout, func() {
		mutex.Lock()
		defer mutex.Unlock()

		if disarmed {
			return
		}

		fired = true
		vm.Interrupt(fmt.Sprintf("plugin %s exceeded the %s time limit in %s", rt.manifest.Id, timeout, label))
	})

	return func() {
		timer.Stop()

		mutex.Lock()
		defer mutex.Unlock()

		// Idempotent: the loop disarms when the handler settles, and the
		// waiting caller disarms if it gives up first. Whichever happens
		// second must not clear an interrupt raised by something else.
		if disarmed {
			return
		}

		disarmed = true

		if fired {
			vm.ClearInterrupt()
		}
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

	// Sockets close before the loop does, or a message arriving mid-shutdown
	// would be queued onto a runtime already on its way out.
	rt.closeListeners()

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
		return

	case <-time.After(5 * time.Second):
	}

	// The loop would not stop, which means something is still running on it.
	//
	// Terminate is not the answer on its own: in goja_nodejs it begins by
	// calling Stop, which is the call that just failed to return — so a plugin
	// spinning in a raw setInterval made Stop block, Terminate block behind it,
	// and Controller.Terminate never reach the exit. SIGTERM simply did not
	// work, which on a container host means every stop is a kill and no plugin
	// ever runs its shutdown handler.
	//
	// Interrupting is what lets Stop return: the running script aborts at its
	// next instruction boundary and the loop can drain. Only rdio.schedule
	// timers are tracked and cleared above; the raw setTimeout and setInterval
	// goja_nodejs binds into every runtime are not, and this is what covers
	// them.
	//
	// Repeatedly, because goja consumes the interrupt flag when it delivers it.
	// One interrupt kills the callback that is running; an interval that
	// re-arms every few milliseconds is back a moment later, so a single shot
	// stops one iteration and nothing more.
	interrupting := time.NewTicker(20 * time.Millisecond)
	defer interrupting.Stop()

	deadline := time.After(5 * time.Second)

	for {
		select {
		case <-stopped:
			return

		case <-deadline:
			rt.loop.Terminate()
			return

		case <-interrupting.C:
			if rt.vm != nil {
				rt.vm.Interrupt(fmt.Sprintf("plugin %s is shutting down", rt.manifest.Id))
			}
		}
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

	rt.runOnLoop(event, func(vm *goja.Runtime) {
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
// pluginSlowJobThreshold is how long one job may hold the event loop before it
// is worth naming. Everything a plugin does is serialized behind this loop, so
// a job over the threshold is delaying every other thing that plugin is trying
// to do — a route handler waiting its turn included.
const pluginSlowJobThreshold = time.Second

// label describes the work for the slow-job log: "route:/api/x", "ws:command",
// a dispatch point name. Kept short — it is read in a log line, next to a
// duration.
func (rt *PluginRuntime) runOnLoop(label string, fn func(vm *goja.Runtime)) bool {
	rt.mutex.RLock()
	stopped := rt.stopped
	loop := rt.loop
	rt.mutex.RUnlock()

	if stopped || loop == nil {
		return false
	}

	// The event loop's queue is unbounded, and the busiest producer is Notify
	// on call.emit — one job per listener per call, each holding a cloned copy
	// of the call. A plugin whose observer is slower than calls arrive builds a
	// backlog that grows without limit, each entry pinning its copy, until the
	// process runs out of memory with nothing having said a word.
	//
	// Shedding is the right failure here for the same reason it is on the emit
	// queue: these are observers, they cannot change anything, and the call is
	// already stored. Refusing to queue costs a plugin one notification;
	// queueing without limit costs the server.
	if rt.queued.Load() >= pluginLoopMaxQueued {
		rt.reportLoopSaturated()
		return false
	}

	rt.queued.Add(1)

	return loop.RunOnLoop(func(vm *goja.Runtime) {
		defer rt.queued.Add(-1)

		defer rt.timeLoopJob(label)()

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

// timeLoopJob starts the clock on a job and returns the function that stops it,
// for `defer rt.timeLoopJob(label)()`.
//
// Split out from runOnLoop because timers do not go through runOnLoop at all —
// rdio.schedule hands its callback straight to the event loop. Timing only the
// jobs that pass through runOnLoop would have left periodic work invisible,
// and periodic work is the likeliest thing to be quietly doing something
// expensive: it runs unprompted, so nobody is watching a request while it does.
//
// The raw setTimeout and setInterval that goja_nodejs binds into every runtime
// stay outside this; reaching them means wrapping the bindings themselves.
func (rt *PluginRuntime) timeLoopJob(label string) func() {
	started := time.Now()

	return func() {
		if elapsed := time.Since(started); elapsed >= pluginSlowJobThreshold {
			rt.recordSlowJob(label, elapsed)
		}
	}
}

// recordSlowJob remembers and reports a job that monopolized the loop.
//
// Remembering matters as much as logging: the log is throttled, but a call
// that times out reads the record unthrottled and can say what it was waiting
// behind. Without that, a timeout names the victim and nothing else, which is
// how "the route is slow" gets investigated for a week before anyone looks at
// the timer beside it.
func (rt *PluginRuntime) recordSlowJob(label string, elapsed time.Duration) {
	rt.slowJobMutex.Lock()
	rt.slowJobLabel = label
	rt.slowJobTook = elapsed
	rt.slowJobAt = time.Now()
	rt.slowJobMutex.Unlock()

	if rt.loopLogThrottle != nil && !rt.loopLogThrottle.Allow(rt.manifest.Id+":slow") {
		return
	}

	rt.controller.Logs.LogEvent(LogLevelWarn, fmt.Sprintf(
		"plugin %s held its event loop for %s in %s; everything else that plugin does was waiting. Slow work belongs off the loop — see the async variants of db, calls and http.",
		rt.manifest.Id, elapsed.Round(time.Millisecond), label,
	))
}

// lastSlowJob describes what recently monopolized the loop, for a caller that
// gave up waiting. Empty when nothing recent is on record.
func (rt *PluginRuntime) lastSlowJob() string {
	rt.slowJobMutex.Lock()
	defer rt.slowJobMutex.Unlock()

	if rt.slowJobLabel == "" || time.Since(rt.slowJobAt) > time.Minute {
		return ""
	}

	return fmt.Sprintf("%s held it for %s, %s ago",
		rt.slowJobLabel,
		rt.slowJobTook.Round(time.Millisecond),
		time.Since(rt.slowJobAt).Round(time.Second),
	)
}

// reportLoopSaturated says once a minute that a plugin is falling behind.
// Throttled because the condition is per dropped job and the log is a database
// write — the same trap the veto line fell into.
func (rt *PluginRuntime) reportLoopSaturated() {
	if rt.loopLogThrottle != nil && !rt.loopLogThrottle.Allow(rt.manifest.Id) {
		return
	}

	rt.controller.Logs.LogEvent(LogLevelWarn, fmt.Sprintf(
		"plugin %s has %d jobs queued and is not keeping up; dropping notifications until it catches up. Slow work belongs on its own worker, not in a handler.",
		rt.manifest.Id, pluginLoopMaxQueued,
	))
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

	rt.runOnLoop("ws:"+command, func(vm *goja.Runtime) {
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
// block — bounded by pluginRouteTimeout, or by the caller going away first.
//
// ctx is the request's, so a client that gives up releases this waiter
// immediately instead of holding a goroutine and a connection until the
// deadline. That matters most exactly when it is most likely: a plugin whose
// loop is congested is also the one whose callers are timing out and retrying.
func (rt *PluginRuntime) DispatchRoute(ctx context.Context, route *pluginRoute, request map[string]any) (result any, err error) {
	type outcome struct {
		value any
		err   error
	}

	// Refuse rather than queue when the loop is already deep in arrears.
	// Waiting the full deadline for a turn that is not coming teaches a caller
	// nothing except to retry, and every retry lands another job on the queue
	// that is already the problem. A prompt 503 is the honest answer and the
	// only one a caller can act on.
	if queued := rt.queued.Load(); queued >= pluginRouteBusyQueue {
		detail := fmt.Sprintf("%d jobs queued", queued)
		if busy := rt.lastSlowJob(); busy != "" {
			detail += "; " + busy
		}

		return nil, &pluginBusyError{plugin: rt.manifest.Id, detail: detail}
	}

	ch := make(chan outcome, 1)

	scheduled := rt.runOnLoop("route:"+route.path, func(vm *goja.Runtime) {
		stop := rt.armWatchdog(vm, "route:"+route.path, pluginCallTimeout)
		defer stop()

		// The raw body becomes an ArrayBuffer here rather than at the call
		// site, because only the loop has a runtime to build one with. goja has
		// no special case for []byte, so without this it would arrive as a
		// reflected Go slice: no byteLength, element access through reflection,
		// and JSON.stringify producing an array of integers one per byte.
		if raw, ok := request["bodyBytes"].([]byte); ok {
			request["bodyBytes"] = vm.NewArrayBuffer(raw)
		}

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

	case <-ctx.Done():
		// The caller has gone. Saying so distinguishes a client that hung up
		// from a plugin that could not answer, which otherwise look identical
		// in the log and lead to opposite investigations.
		return nil, fmt.Errorf("plugin %s: caller disconnected before %s answered", rt.manifest.Id, route.path)

	case <-time.After(pluginRouteTimeout):
		// Name the queue depth and whatever was last seen hogging the loop.
		// A route handler is almost never slow by itself — it is behind
		// something — and a message that only names the route sends whoever
		// reads it to look at the one piece of code that is innocent.
		detail := fmt.Sprintf("%d jobs queued", rt.queued.Load())
		if busy := rt.lastSlowJob(); busy != "" {
			detail += "; " + busy
		}

		return nil, fmt.Errorf("plugin %s timed out handling %s after %s (%s)",
			rt.manifest.Id, route.path, pluginRouteTimeout, detail)
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

	rt.runOnLoop(point, func(vm *goja.Runtime) {
		// The point's own budget, not a flat thirty seconds. An observer on
		// call.emit runs once per listener per call against a documented 250ms
		// ceiling, and giving it two orders of magnitude more than the filter
		// beside it is what let a single observer hold the loop long enough to
		// build the backlog above.
		stop := rt.armWatchdog(vm, point, observerTimeout(point))
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
		// A callable that isn't script-backed runs directly. The event loop and
		// the watchdog exist to contain interpreted code; a host-side callable
		// has neither to contain. pluginCallable is an interface precisely so
		// dispatch has no opinion about the engine, and refusing everything
		// that wasn't goja quietly made that false.
		if callable == nil {
			return nil, fmt.Errorf("no callable")
		}
		return callable.call(args...)
	}

	if !rt.enterDispatch() {
		return nil, fmt.Errorf("plugin %s is stopped", rt.manifest.Id)
	}
	defer rt.leaveDispatch()

	type outcome struct {
		value any
		err   error
	}

	ch := make(chan outcome, 1)

	// The disarm is shared with the waiting goroutine below. A handler whose
	// promise never settles never disarms from the loop, and the interrupt
	// would then sit raised until it killed some later, unrelated job.
	var (
		disarmMutex sync.Mutex
		disarm      func()
	)

	stopWatchdog := func() {
		disarmMutex.Lock()
		fn := disarm
		disarmMutex.Unlock()

		if fn != nil {
			fn()
		}
	}

	scheduled := rt.runOnLoop(point, func(vm *goja.Runtime) {
		// Disarmed by hand rather than deferred, because a handler that returns
		// a promise is not finished when this function returns. Deferring meant
		// the interrupt was cleared the instant the synchronous part ended, so
		// any handler that awaited rdio.http, rdio.exec or rdio.audio ran
		// completely unwatched afterwards — the documented per-point timeouts
		// bounded only how long core waited, never how long the plugin ran.
		stop := rt.armWatchdog(vm, point, timeout)

		disarmMutex.Lock()
		disarm = stop
		disarmMutex.Unlock()

		converted := make([]goja.Value, len(args))
		for i, arg := range args {
			converted[i] = rt.toValue(vm, arg)
		}

		value, err := adapter.fn(goja.Undefined(), converted...)
		if err != nil {
			stop()
			ch <- outcome{nil, err}
			return
		}

		if promise, ok := value.Export().(*goja.Promise); ok {
			rt.awaitPromise(vm, promise, func(v any, err error) {
				stop()
				ch <- outcome{v, err}
			})
			return
		}

		stop()
		ch <- outcome{value.Export(), nil}
	})

	if !scheduled {
		return nil, fmt.Errorf("plugin %s is not running", rt.manifest.Id)
	}

	select {
	case out := <-ch:
		return out.value, out.err
	case <-time.After(timeout + time.Second):
		// Give up waiting, and take the watchdog down with us so a pending
		// promise cannot leave an interrupt raised for the next job to hit.
		stopWatchdog()

		return nil, fmt.Errorf("plugin %s timed out at %s", rt.manifest.Id, point)
	}
}

// enterDispatch reports whether a blocking dispatch may proceed.
//
// This used to refuse a dispatch whenever the runtime was already handling one,
// meaning to guard against a handler causing core to dispatch back into the
// event loop it was itself occupying. It refused far more than that: any two
// dispatches overlapping in time were treated as re-entrant, however unrelated.
//
// That was harmless while every point was an observer fired from one goroutine.
// It stopped being harmless once auth and delivery became extension points,
// because those are reached from every connection at once. Two listeners
// connecting in the same instant meant one of them silently did not get the
// plugin's configuration — no error to the client, just a different server than
// the one next to it. Found exactly that way: two clients, one filtered config.
//
// Concurrency was never the danger. runOnLoop already funnels every handler onto
// the single event loop goroutine, so overlapping dispatches queue and run one
// at a time whether or not anything guards them. Genuine re-entrancy — a handler
// synchronously provoking a dispatch into its own runtime — would still stall,
// but CallSync's own deadline bounds that and turns it into a logged failure
// rather than a hang.
func (rt *PluginRuntime) enterDispatch() bool {
	rt.mutex.Lock()
	defer rt.mutex.Unlock()

	return !rt.stopped
}

func (rt *PluginRuntime) leaveDispatch() {}

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
