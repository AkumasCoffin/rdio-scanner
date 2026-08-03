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
	"sync"
	"time"
)

// Plugins talking to each other.
//
// Everything so far connects a plugin to rdio. This connects plugins to one
// another, which is what stops the ecosystem being a set of isolated tools: a
// tone detector should be able to hand a page to a notifier without either of
// them knowing the other exists, and a transcription plugin should be able to
// answer a question from a search plugin without core mediating the schema.
//
// Two shapes, because they answer different questions:
//
//	call      — I need an answer from a specific plugin, and I will wait.
//	publish   — this happened; anyone who cares can react. Nobody waits.
//
// The hazard is runaway recursion rather than deadlock. A call does not block
// the caller's event loop — rdio.plugins.call returns a promise and the work
// happens on its own goroutine — so A calling B calling A is answerable at every
// hop. What it is not is bounded: each level spawns another goroutine. A ceiling
// on how many calls one plugin may have out at once stops that without refusing
// two plugins the right to talk at the same time.

// pluginRpcMaxInFlight caps how many calls one plugin may have out at once.
// Generous enough that real concurrency is never refused, low enough that a
// plugin calling in a loop is stopped early rather than spawning goroutines
// until something else breaks.
const pluginRpcMaxInFlight = 8

// pluginRpcTimeout bounds one hop, not the whole chain.
const pluginRpcTimeout = 30 * time.Second

// PluginRpc routes calls and events between running plugins.
type PluginRpc struct {
	controller *Controller

	mutex sync.RWMutex

	// methods maps pluginId -> method name -> handler.
	methods map[string]map[string]*pluginHandler

	// topics maps a topic to everyone subscribed to it.
	topics map[string][]*pluginSubscription

	// waiting counts outbound calls in flight per plugin, so runaway recursion
	// can be stopped. It does not mean the plugin is blocked — a call runs on
	// its own goroutine and the caller's event loop stays free.
	waiting map[string]int
}

type pluginSubscription struct {
	pluginId string
	runtime  *PluginRuntime
	callable pluginCallable
}

func NewPluginRpc(controller *Controller) *PluginRpc {
	return &PluginRpc{
		controller: controller,
		methods:    map[string]map[string]*pluginHandler{},
		topics:     map[string][]*pluginSubscription{},
		waiting:    map[string]int{},
	}
}

// Handle registers a method a plugin is willing to answer.
//
// Re-registering replaces, so a plugin reloading does not accumulate stale
// handlers pointing at a runtime that has stopped.
func (rpc *PluginRpc) Handle(pluginId string, method string, handler *pluginHandler) {
	rpc.mutex.Lock()
	defer rpc.mutex.Unlock()

	if rpc.methods[pluginId] == nil {
		rpc.methods[pluginId] = map[string]*pluginHandler{}
	}

	rpc.methods[pluginId][method] = handler
}

// Subscribe adds a listener for a topic.
func (rpc *PluginRpc) Subscribe(topic string, subscription *pluginSubscription) {
	rpc.mutex.Lock()
	defer rpc.mutex.Unlock()

	rpc.topics[topic] = append(rpc.topics[topic], subscription)
}

// Unregister drops everything belonging to a plugin, used when one stops. A
// method left behind would route into a dead runtime and fail every call rather
// than reporting honestly that nobody offers it.
func (rpc *PluginRpc) Unregister(pluginId string) {
	rpc.mutex.Lock()
	defer rpc.mutex.Unlock()

	delete(rpc.methods, pluginId)

	for topic, subscriptions := range rpc.topics {
		kept := []*pluginSubscription{}
		for _, subscription := range subscriptions {
			if subscription.pluginId != pluginId {
				kept = append(kept, subscription)
			}
		}

		if len(kept) == 0 {
			delete(rpc.topics, topic)
		} else {
			rpc.topics[topic] = kept
		}
	}
}

// Methods lists what a plugin offers, so a caller can check before calling
// rather than discovering by failure.
func (rpc *PluginRpc) Methods(pluginId string) []string {
	rpc.mutex.RLock()
	defer rpc.mutex.RUnlock()

	names := []string{}
	for name := range rpc.methods[pluginId] {
		names = append(names, name)
	}

	return names
}

// Call invokes a method on another plugin and waits for its answer.
//
// `from` is the calling plugin, or "" when the host initiates.
func (rpc *PluginRpc) Call(from string, to string, method string, args any) (any, error) {
	if from == to {
		return nil, fmt.Errorf("plugin %s cannot call itself through the plugin bus", to)
	}

	rpc.mutex.Lock()

	// Bounds runaway recursion, not deadlock.
	//
	// This guard used to refuse any call into a plugin that had an outbound call
	// in flight, on the reasoning that such a plugin has its event loop blocked
	// and cannot answer. That reasoning was wrong: rdio.plugins.call returns a
	// promise and runs this on its own goroutine, so the caller's loop stays
	// free the whole time and there is no deadlock to prevent. What it actually
	// did was refuse perfectly answerable calls whenever two plugins happened to
	// be talking at once — and the companion cap on the total waiting set let
	// three unrelated conversations refuse a fourth anywhere on the server.
	//
	// What is worth bounding is A calling B calling A calling B: harmless per
	// hop, but it spawns a goroutine per level and would climb without limit.
	// Each level adds another outbound call for the same plugin, so a per-plugin
	// ceiling stops it while leaving unrelated plugins alone.
	if from != "" && rpc.waiting[from] >= pluginRpcMaxInFlight {
		rpc.mutex.Unlock()

		return nil, fmt.Errorf(
			"plugin %s already has %d calls in flight; refusing %s.%s in case this is a loop",
			from, pluginRpcMaxInFlight, to, method,
		)
	}

	handler := rpc.methods[to][method]
	if handler == nil {
		rpc.mutex.Unlock()
		return nil, fmt.Errorf("plugin %s offers no method %q", to, method)
	}

	// Counted, not flagged: a plugin handling two unrelated points at once can
	// legitimately have two calls out, and the first to finish must not clear
	// the second's mark.
	if from != "" {
		rpc.waiting[from]++
	}

	rpc.mutex.Unlock()

	defer func() {
		if from == "" {
			return
		}

		rpc.mutex.Lock()
		if rpc.waiting[from] > 1 {
			rpc.waiting[from]--
		} else {
			delete(rpc.waiting, from)
		}
		rpc.mutex.Unlock()
	}()

	return handler.runtime.CallSync("rpc:"+method, pluginRpcTimeout, handler.callable, args)
}

// Publish delivers an event to every subscriber except the sender.
//
// Never blocks and never reports delivery. A publisher that needed to know who
// received what wants Call; conflating the two is how a broadcast ends up
// coupled to the slowest listener.
func (rpc *PluginRpc) Publish(from string, topic string, payload any) int {
	rpc.mutex.RLock()
	subscriptions := append([]*pluginSubscription{}, rpc.topics[topic]...)
	rpc.mutex.RUnlock()

	delivered := 0

	for _, subscription := range subscriptions {
		// Not back to the sender. A plugin that both publishes and subscribes to
		// a topic is the normal shape for a shared bus, and echoing to itself
		// would make every such plugin write the same guard.
		if subscription.pluginId == from {
			continue
		}

		// One copy per subscriber, for the same reason Notify clones: each
		// subscriber is a separate event loop, goja hands JavaScript the Go map
		// itself, and `msg.payload.handled = true` is the obvious thing for a
		// subscriber to write. Two of them doing it to a shared map is a fatal
		// concurrent write, reachable with entirely ordinary plugin code.
		subscription.runtime.EmitTo("plugins:"+topic, map[string]any{
			"topic":   topic,
			"from":    from,
			"payload": clonePluginValue(payload),
		})

		delivered++
	}

	return delivered
}
