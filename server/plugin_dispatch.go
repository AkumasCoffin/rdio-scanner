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
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// The single mechanism by which plugins reach into the server.
//
// Every extension point is a named string. Core calls one of the dispatch
// helpers at the decision site; plugins register against the name. Adding a new
// point is one line at the call site plus one entry in the point list, which is
// what makes covering the whole server affordable.
//
// Reads are lock free. The registry is swapped atomically when a plugin
// registers — which happens at startup and essentially never afterwards —
// so the hot paths pay one atomic load and one map lookup, and nothing else.
// On an install with no plugins every point is a load, a lookup that misses,
// and an immediate return.

type pluginVerb int

const (
	// verbOn observes. Asynchronous, cannot change or veto anything.
	verbOn pluginVerb = iota
	// verbFilter may modify what passes through, or veto it.
	verbFilter
	// verbOverride replaces core's behaviour for the point entirely.
	verbOverride
	// verbProvide supplies something core does not have. First answer wins.
	verbProvide
)

func (v pluginVerb) String() string {
	switch v {
	case verbOn:
		return "on"
	case verbFilter:
		return "filter"
	case verbOverride:
		return "override"
	case verbProvide:
		return "provide"
	}
	return "unknown"
}

// pluginDispatchTimeout bounds a single blocking handler. Deliberately generous:
// the goal is to turn "hangs until restart" into "logged and ignored", not to
// police slow-but-working code. Points on hot paths override it.
const pluginDispatchTimeout = 30 * time.Second

type pluginHandler struct {
	pluginId string
	verb     pluginVerb
	runtime  *PluginRuntime
	callable pluginCallable
}

// pluginCallable is the runtime-facing shape of a handler, kept as an interface
// so this file has no opinion about the scripting engine.
type pluginCallable interface {
	call(args ...any) (any, error)
}

// dispatchRegistry is immutable once published. Registering builds a new one and
// swaps it in, so readers never take a lock.
type dispatchRegistry struct {
	handlers map[string][]*pluginHandler
}

type PluginDispatch struct {
	registry atomic.Pointer[dispatchRegistry]

	// mutex serialises writers only. Readers use the atomic pointer.
	mutex sync.Mutex

	// points is every name that may be registered against: the built-in set
	// plus anything a plugin defined for other plugins to use.
	points map[string]bool

	controller *Controller
}

func NewPluginDispatch(controller *Controller) *PluginDispatch {
	dispatch := &PluginDispatch{
		points:     map[string]bool{},
		controller: controller,
	}

	for _, point := range pluginPoints {
		dispatch.points[point] = true
	}

	dispatch.registry.Store(&dispatchRegistry{handlers: map[string][]*pluginHandler{}})

	return dispatch
}

// KnownPoint reports whether a name may be registered against. Registering for
// an unknown point is refused loudly rather than silently never firing, which
// is how the old event set let a plugin wait forever on something core never
// emitted.
func (dispatch *PluginDispatch) KnownPoint(point string) bool {
	dispatch.mutex.Lock()
	defer dispatch.mutex.Unlock()
	return dispatch.points[point]
}

// DefinePoint lets a plugin publish an extension point of its own, so plugins
// can extend each other without core changes.
func (dispatch *PluginDispatch) DefinePoint(point string) {
	dispatch.mutex.Lock()
	defer dispatch.mutex.Unlock()
	dispatch.points[point] = true
}

// Register adds a handler. Order within a point is registration order, which is
// plugin load order — deterministic, and reported by the admin panel.
func (dispatch *PluginDispatch) Register(handler *pluginHandler, point string) {
	dispatch.mutex.Lock()
	defer dispatch.mutex.Unlock()

	current := dispatch.registry.Load()

	next := &dispatchRegistry{handlers: make(map[string][]*pluginHandler, len(current.handlers)+1)}
	for name, handlers := range current.handlers {
		next.handlers[name] = handlers
	}

	next.handlers[point] = append(append([]*pluginHandler{}, next.handlers[point]...), handler)

	dispatch.registry.Store(next)
}

// Unregister drops every handler belonging to a plugin, used when one is
// disabled or stopped.
func (dispatch *PluginDispatch) Unregister(pluginId string) {
	dispatch.mutex.Lock()
	defer dispatch.mutex.Unlock()

	current := dispatch.registry.Load()
	next := &dispatchRegistry{handlers: make(map[string][]*pluginHandler, len(current.handlers))}

	for name, handlers := range current.handlers {
		kept := []*pluginHandler{}
		for _, handler := range handlers {
			if handler.pluginId != pluginId {
				kept = append(kept, handler)
			}
		}
		if len(kept) > 0 {
			next.handlers[name] = kept
		}
	}

	dispatch.registry.Store(next)
}

// handlersFor is the hot path: one atomic load, one map lookup, no allocation
// and no lock.
func (dispatch *PluginDispatch) handlersFor(point string, verb pluginVerb) []*pluginHandler {
	registry := dispatch.registry.Load()

	handlers := registry.handlers[point]
	if len(handlers) == 0 {
		return nil
	}

	var matched []*pluginHandler
	for _, handler := range handlers {
		if handler.verb == verb {
			matched = append(matched, handler)
		}
	}

	return matched
}

// Active reports whether anything at all is registered for a point, so a caller
// can skip building an expensive argument when nobody is listening.
func (dispatch *PluginDispatch) Active(point string) bool {
	return len(dispatch.registry.Load().handlers[point]) > 0
}

// --- the four verbs -------------------------------------------------------

// clonePluginValue deep-copies the containers in a dispatch payload.
//
// goja wraps a Go map by reference: a JavaScript property write lands in the
// Go map itself. Handing one payload to several plugins therefore shares
// mutable state between event loops that never synchronise with each other —
// and a plugin filter that does the idiomatic `call.meta.x = 1; return call`
// while another plugin's observer reads the same map is a concurrent map read
// and write, which is a fatal error that takes the whole server down rather
// than an error anyone can recover from.
//
// So every recipient gets its own copy. The maps involved are small; the audio
// blob is deliberately not copied, because it is a byte slice rather than a
// container — writing into it cannot crash the runtime, and copying a couple of
// hundred kilobytes per handler per call would be a real cost to defend against
// a plugin corrupting audio it was given in order to inspect.
func clonePluginValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, entry := range v {
			out[key] = clonePluginValue(entry)
		}
		return out

	case []any:
		out := make([]any, len(v))
		for i, entry := range v {
			out[i] = clonePluginValue(entry)
		}
		return out

	case map[string]string:
		out := make(map[string]string, len(v))
		for key, entry := range v {
			out[key] = entry
		}
		return out

	// A call's sources and frequencies are built as []map[string]any on every
	// ingest path, and only ever become []any after a round trip through the
	// database. Without this case they reached plugins as the live slice, whose
	// element maps core is still reading — a concurrent map write, which is a
	// fatal error rather than something the server can recover from. Two
	// plugins observing the same point was enough.
	case []map[string]any:
		out := make([]map[string]any, len(v))
		for i, entry := range v {
			copied := make(map[string]any, len(entry))
			for key, field := range entry {
				copied[key] = clonePluginValue(field)
			}
			out[i] = copied
		}
		return out

	case []string:
		return append([]string{}, v...)

	case []uint:
		return append([]uint{}, v...)
	}

	return value
}

// Notify fires observers. Never blocks: the busiest caller is the single
// goroutine draining ingest, and an observer must not be able to slow it.
func (dispatch *PluginDispatch) Notify(point string, value any) {
	for _, handler := range dispatch.handlersFor(point, verbOn) {
		handler.runtime.EmitTo(point, clonePluginValue(value))
	}
}

// Filter runs the chain. Each handler receives the value as it stands and may
// return the fields it wants changed, or a veto.
//
// Two different things are tracked, and conflating them was the original bug.
// `current` is what handlers see: the full value with every earlier handler's
// changes overlaid, so the second filter in a chain gets a whole call rather
// than the first one's fragment. `changed` is what the caller gets back: only
// the fields a handler actually named. Returning the whole merged value instead
// would make the caller write every field back onto the object, including ones
// that lose precision on the round trip — dateTime is handed out as RFC3339,
// which has no sub-second component.
//
// A handler that times out, throws, or returns something unusable is treated as
// having done nothing. A plugin may degrade the server's behaviour; it must
// never be able to lose a call.
func (dispatch *PluginDispatch) Filter(point string, value any, timeout time.Duration) (any, bool) {
	return dispatch.FilterWithin(nil, point, value, timeout)
}

// FilterWithin is Filter bounded by an allowance shared across everything one
// call does. Points reached once per call pass nil and are governed by their
// own timeout; the per-listener point passes a real budget, because there the
// cost is the timeout multiplied by the audience.
func (dispatch *PluginDispatch) FilterWithin(budget *pluginBudget, point string, value any, timeout time.Duration) (any, bool) {
	handlers := dispatch.handlersFor(point, verbFilter)
	if len(handlers) == 0 {
		return value, true
	}

	current := value

	var changed map[string]any

	for _, handler := range handlers {
		result, err := dispatch.invokeWithin(budget, handler, point, timeout, current)
		if err == errPluginBudgetSpent {
			// Nothing left for this call. Skipping is the same outcome as a
			// handler that failed — the value passes through untouched — and
			// the caller reports the overrun once rather than per handler.
			break
		}
		if err != nil {
			dispatch.logFailure(handler, point, err)
			continue
		}

		if result == nil {
			continue
		}

		m, ok := result.(map[string]any)
		if !ok {
			continue
		}

		if drop, ok := m["drop"].(bool); ok && drop {
			dispatch.controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf(
				"plugin %s vetoed at %s", handler.pluginId, point,
			))
			return current, false
		}

		// Overlay, never replace. A filter returns only the fields it wants
		// changed — that is the documented contract and the only workable one,
		// since the alternative makes every filter responsible for echoing the
		// whole object back correctly.
		//
		// Assigning the partial result straight to `current` broke that in both
		// directions once a second filter existed: the next handler received an
		// object holding nothing but the previous handler's fields, and its own
		// return then dropped that handler's changes on the floor. With one
		// filter per point it looked correct, which is how it shipped.
		if changed == nil {
			changed = map[string]any{}
		}
		for key, field := range m {
			changed[key] = field
		}

		current = overlayPluginFields(current, m)
	}

	// Nothing was changed: hand back exactly what came in, so a point with a
	// filter registered that stays quiet costs the caller nothing.
	if changed == nil {
		return value, true
	}

	return changed, true
}

// overlayPluginFields writes a filter's returned fields over a copy of the
// value it was given, so the chain accumulates rather than overwrites.
//
// A copy because `base` may be the map another handler is still holding a
// reference to, and because the caller writes the result onto a live object.
func overlayPluginFields(base any, fields map[string]any) map[string]any {
	merged := map[string]any{}

	if existing, ok := base.(map[string]any); ok {
		for key, value := range existing {
			merged[key] = value
		}
	}

	for key, value := range fields {
		merged[key] = value
	}

	return merged
}

// Override replaces core's behaviour. Only one plugin can own a point; the
// first registered wins, and the attempt is logged when a second tries.
//
// The failure rule does not apply here: a plugin that takes over behaviour owns
// its failures, because there is no original behaviour left to fall back to.
func (dispatch *PluginDispatch) Override(point string, value any, timeout time.Duration) (any, bool, error) {
	handlers := dispatch.handlersFor(point, verbOverride)
	if len(handlers) == 0 {
		return nil, false, nil
	}

	result, err := dispatch.invoke(handlers[0], point, timeout, value)
	if err != nil {
		return nil, true, err
	}

	return result, true, nil
}

// Provide asks each provider in turn for something core does not have. The
// first non-nil answer wins.
func (dispatch *PluginDispatch) Provide(point string, args any, timeout time.Duration) (any, bool) {
	for _, handler := range dispatch.handlersFor(point, verbProvide) {
		result, err := dispatch.invoke(handler, point, timeout, args)
		if err != nil {
			dispatch.logFailure(handler, point, err)
			continue
		}
		if result != nil {
			return result, true
		}
	}

	return nil, false
}

func (dispatch *PluginDispatch) invoke(handler *pluginHandler, point string, timeout time.Duration, args ...any) (any, error) {
	return dispatch.invokeWithin(nil, handler, point, timeout, args...)
}

// invokeWithin is invoke with a per-call allowance. A nil budget means
// unbounded, which is every point that is reached once per call rather than
// once per listener.
func (dispatch *PluginDispatch) invokeWithin(budget *pluginBudget, handler *pluginHandler, point string, timeout time.Duration, args ...any) (any, error) {
	if timeout <= 0 {
		timeout = pluginDispatchTimeout
	}

	// The allowance is checked before the work, and charged for what the work
	// actually took. A handler that returns promptly barely touches it.
	allowed, ok := budget.take(timeout)
	if !ok {
		return nil, errPluginBudgetSpent
	}
	timeout = allowed

	started := time.Now()
	defer func() { budget.spend(time.Since(started)) }()

	// Each handler gets its own copy, for the same reason observers do: goja
	// hands the Go map itself to JavaScript, so without this two plugins on one
	// point would be writing into the same map from two event loops.
	copied := make([]any, len(args))
	for i, arg := range args {
		copied[i] = clonePluginValue(arg)
	}

	return handler.runtime.CallSync(point, timeout, handler.callable, copied...)
}

// reportBudgetSpent says once, per call, that the allowance ran out — naming
// how much of the work was skipped so the line is actionable rather than
// merely alarming.
//
// Throttled with the emit warning for the same reason: the condition repeats
// per call, and the log is a database write.
func (dispatch *PluginDispatch) reportBudgetSpent(point string, skipped int, total int) {
	if dispatch.controller == nil || dispatch.controller.emitLogThrottle == nil {
		return
	}
	if !dispatch.controller.emitLogThrottle.Allow("budget:" + point) {
		return
	}

	dispatch.controller.Logs.LogEvent(LogLevelWarn, fmt.Sprintf(
		"plugins at %s used their whole time budget for one call; %d of %d were handled without them. A plugin on this point is too slow to run once per listener.",
		point, skipped, total,
	))
}

func (dispatch *PluginDispatch) logFailure(handler *pluginHandler, point string, err error) {
	dispatch.controller.Logs.LogEvent(LogLevelWarn, fmt.Sprintf(
		"plugin %s failed at %s, continuing without it: %v", handler.pluginId, point, err,
	))
}

// errPluginBudgetSpent means a handler was skipped because the call had
// nothing left to spend, not because anything went wrong. It is deliberately
// distinct from a handler failure: the outcome for the value is the same, but
// only one of the two is worth telling an operator about.
var errPluginBudgetSpent = errors.New("the call's plugin time budget is spent")
