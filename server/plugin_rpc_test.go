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
	"strings"
	"testing"
)

// rpcWithMethods registers a method per plugin without needing runtimes. The
// handler is never reached in these tests: every one of them is about a call
// being refused before it gets that far.
func rpcWithMethods(names ...string) *PluginRpc {
	rpc := NewPluginRpc(nil)

	for _, name := range names {
		rpc.methods[name] = map[string]*pluginHandler{
			"ping": {pluginId: name},
		}
	}

	return rpc
}

// TestABusyPluginCanStillBeCalled is the correction to a guard that was built on
// a false premise.
//
// It used to refuse any call into a plugin that had an outbound call of its own
// in flight, reasoning that such a plugin has its event loop blocked. It does
// not: a call runs on its own goroutine and returns a promise, so the caller
// keeps running and can answer perfectly well. The old rule turned two plugins
// happening to talk at the same time into an error.
func TestABusyPluginCanStillBeCalled(t *testing.T) {
	rpc := rpcWithMethods("alpha", "beta")

	// alpha is mid-call of its own.
	rpc.waiting["alpha"] = 1

	_, err := rpc.Call("beta", "alpha", "ping", nil)

	// It reaches the handler, which has no runtime here — so the failure must be
	// about that, never about alpha being busy.
	if err != nil && strings.Contains(err.Error(), "in flight") {
		t.Errorf("a busy plugin was refused a call it could have answered: %v", err)
	}
}

// TestSelfCallIsRefused covers the degenerate cycle, which deadlocks the same
// way and is the easiest one to write by accident.
func TestSelfCallIsRefused(t *testing.T) {
	rpc := rpcWithMethods("alpha")

	if _, err := rpc.Call("alpha", "alpha", "ping", nil); err == nil {
		t.Error("a plugin was allowed to call itself through the bus")
	}
}

// TestRunawayRecursionIsBounded keeps the thing actually worth stopping.
//
// A calling B calling A is answerable at every hop, so it never deadlocks — but
// each level spawns another goroutine, and nothing else would ever stop it. The
// ceiling is per plugin, so one plugin looping cannot refuse calls between two
// others that have nothing to do with it.
func TestRunawayRecursionIsBounded(t *testing.T) {
	rpc := rpcWithMethods("alpha", "beta", "gamma")

	rpc.waiting["alpha"] = pluginRpcMaxInFlight

	_, err := rpc.Call("alpha", "beta", "ping", nil)
	if err == nil || !strings.Contains(err.Error(), "in flight") {
		t.Fatalf("a plugin past its in-flight ceiling should be refused: %v", err)
	}

	// An unrelated pair is unaffected, which the old global cap got wrong.
	_, other := rpc.Call("beta", "gamma", "ping", nil)
	if other != nil && strings.Contains(other.Error(), "in flight") {
		t.Errorf("one plugin looping refused an unrelated conversation: %v", other)
	}
}

// TestMissingMethodIsReportedHonestly — a caller should learn that nobody offers
// the method, not wait for a timeout.
func TestMissingMethodIsReportedHonestly(t *testing.T) {
	rpc := rpcWithMethods("alpha")

	_, err := rpc.Call("beta", "alpha", "nosuch", nil)

	if err == nil {
		t.Fatal("calling a method nobody offers succeeded")
	}

	if !strings.Contains(err.Error(), "nosuch") {
		t.Errorf("the error should name the method: %v", err)
	}
}

// TestWaitingMarkIsClearedOnFailure guards against a refused call leaving its
// caller permanently marked as blocked, which would quietly disable that
// plugin's ability to call anyone ever again.
func TestWaitingMarkIsClearedOnFailure(t *testing.T) {
	rpc := rpcWithMethods("alpha")

	// Fails because the method does not exist, after the caller would have been
	// marked. The mark must not survive.
	rpc.Call("beta", "alpha", "nosuch", nil)

	rpc.mutex.RLock()
	waiting := rpc.waiting["beta"]
	rpc.mutex.RUnlock()

	if waiting != 0 {
		t.Errorf("beta is still marked as waiting (%d) after a failed call", waiting)
	}
}

// TestUnregisterRemovesMethodsAndSubscriptions keeps a stopped plugin from
// answering. A method left behind would route into a dead runtime and fail every
// call rather than reporting that nobody offers it.
func TestUnregisterRemovesMethodsAndSubscriptions(t *testing.T) {
	rpc := rpcWithMethods("alpha", "beta")

	rpc.Subscribe("pages", &pluginSubscription{pluginId: "alpha"})
	rpc.Subscribe("pages", &pluginSubscription{pluginId: "beta"})

	rpc.Unregister("alpha")

	if len(rpc.Methods("alpha")) != 0 {
		t.Errorf("alpha still offers %v", rpc.Methods("alpha"))
	}

	if len(rpc.Methods("beta")) != 1 {
		t.Errorf("beta's methods were removed too: %v", rpc.Methods("beta"))
	}

	rpc.mutex.RLock()
	subscriptions := rpc.topics["pages"]
	rpc.mutex.RUnlock()

	if len(subscriptions) != 1 || subscriptions[0].pluginId != "beta" {
		t.Errorf("subscriptions after unregister: %v", subscriptions)
	}
}

// TestPublishSkipsTheSender — a plugin that both publishes and subscribes to a
// topic is the normal shape for a shared bus, and echoing back would make every
// one of them write the same guard.
func TestPublishSkipsTheSender(t *testing.T) {
	rpc := NewPluginRpc(nil)

	rpc.Subscribe("pages", &pluginSubscription{pluginId: "alpha"})

	if delivered := rpc.Publish("alpha", "pages", nil); delivered != 0 {
		t.Errorf("the sender received its own event (%d delivered)", delivered)
	}
}

// TestPublishToNobodyIsNotAnError — a topic with no listeners is the ordinary
// case for an optional companion plugin that is not installed.
func TestPublishToNobodyIsNotAnError(t *testing.T) {
	rpc := NewPluginRpc(nil)

	if delivered := rpc.Publish("alpha", "nobody-listening", nil); delivered != 0 {
		t.Errorf("delivered %d to an empty topic", delivered)
	}
}
