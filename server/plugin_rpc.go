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
// The hazard here is cycles. A calling B calling A is not hypothetical once
// plugins compose, and without a guard it deadlocks: A's event loop is blocked
// inside the call to B, so when B calls back into A there is nothing left to run
// A's handler.
//
// That deadlock is detected directly rather than by threading a call chain
// through the runtimes. A plugin with an outbound call in flight has its loop
// blocked, by construction — CallSync waits on the answer — so the set of
// plugins currently waiting is exactly the set that cannot service an incoming
// call. A call targeting one of them can never be answered, and is refused
// immediately with an error naming it instead of hanging until a timeout that
// tells nobody anything.
//
// The alternative, carrying the chain along with each call, cannot be made
// correct here: a handler that awaits a promise yields the event loop, so two
// unrelated calls interleave and a per-runtime "current chain" would blame
// whichever arrived second.

// pluginRpcMaxDepth bounds how many plugins may be waiting on each other at
// once. Three covers the compositions that make sense — a detector calling an
// enricher calling a store — and keeps a runaway chain short.
const pluginRpcMaxDepth = 3

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

	// waiting counts outbound calls in flight per plugin. A plugin listed here
	// has its event loop blocked and cannot answer an incoming call.
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

	// The target has an outbound call in flight, so its event loop is blocked
	// waiting and cannot run the handler this call needs. Answering would
	// deadlock both plugins until a timeout neither of them can explain.
	if rpc.waiting[to] > 0 {
		waiting := rpc.waitingList()
		rpc.mutex.Unlock()

		return nil, fmt.Errorf(
			"plugin call cycle: %s is itself waiting on another plugin, so it cannot answer %s.%s (waiting: %v)",
			to, to, method, waiting,
		)
	}

	if len(rpc.waiting) >= pluginRpcMaxDepth {
		waiting := rpc.waitingList()
		rpc.mutex.Unlock()

		return nil, fmt.Errorf(
			"plugin call chain too deep calling %s.%s; already waiting: %v",
			to, method, waiting,
		)
	}

	handler := rpc.methods[to][method]
	if handler == nil {
		rpc.mutex.Unlock()
		return nil, fmt.Errorf("plugin %s offers no method %q", to, method)
	}

	// Mark the caller as blocked for the duration. Counted rather than flagged
	// because a plugin handling two unrelated points at once can legitimately
	// have two calls in flight, and the first to finish must not clear the
	// second's mark.
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

// waitingList names who is currently blocked, for an error message. Called with
// the lock held.
func (rpc *PluginRpc) waitingList() []string {
	names := []string{}
	for name := range rpc.waiting {
		names = append(names, name)
	}
	return names
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

		subscription.runtime.EmitTo("plugins:"+topic, map[string]any{
			"topic":   topic,
			"from":    from,
			"payload": payload,
		})

		delivered++
	}

	return delivered
}
