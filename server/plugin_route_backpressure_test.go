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
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dop251/goja"
	"github.com/dop251/goja_nodejs/eventloop"
)

// routeRuntime is a runtime with a running loop, ready to be given work.
func routeRuntime(t *testing.T) *PluginRuntime {
	t.Helper()

	rt := &PluginRuntime{manifest: &PluginManifest{Id: "busy"}}
	rt.controller = &Controller{Logs: &Logs{}}
	rt.loopLogThrottle = NewLogThrottle(1, time.Minute)

	rt.loop = eventloop.NewEventLoop(eventloop.EnableConsole(false))
	rt.loop.Start()
	t.Cleanup(func() { rt.loop.Stop() })

	return rt
}

// occupyLoop blocks the plugin's single event loop until the returned function
// is called, which is the state every one of these tests is about.
func occupyLoop(t *testing.T, rt *PluginRuntime) func() {
	t.Helper()

	blocked := make(chan struct{})
	release := make(chan struct{})

	rt.runOnLoop("test:blocker", func(vm *goja.Runtime) {
		close(blocked)
		<-release
	})
	<-blocked

	var once bool
	return func() {
		if !once {
			once = true
			close(release)
		}
	}
}

// A plugin's routes share one event loop with its observers, its timers and
// its promise callbacks. When that loop falls far behind, a route used to
// queue anyway and hold its caller for the full deadline before failing —
// which taught the caller nothing except to retry, and every retry queued more
// work onto the backlog that was the problem. Refusing promptly is the only
// answer a caller can act on.
func TestRouteRefusesInsteadOfQueueingOntoADeepBacklog(t *testing.T) {
	rt := routeRuntime(t)
	defer occupyLoop(t, rt)()

	for i := 0; i < pluginRouteBusyQueue+8; i++ {
		rt.runOnLoop("test:filler", func(vm *goja.Runtime) {})
	}

	route := &pluginRoute{path: "/api/call-transcript"}

	started := time.Now()
	_, err := rt.DispatchRoute(context.Background(), route, map[string]any{})
	elapsed := time.Since(started)

	if err == nil {
		t.Fatal("a route dispatched onto a saturated loop reported success")
	}

	var busy *pluginBusyError
	if !errors.As(err, &busy) {
		t.Errorf("got %v; want a busy error the HTTP layer can turn into a 503", err)
	}

	if elapsed > time.Second {
		t.Errorf("took %s to refuse; the caller is still being held", elapsed)
	}
}

// A caller that gives up must release the waiter with it. Otherwise the
// requests most likely to be abandoned — the ones against a congested plugin —
// are exactly the ones that go on holding a goroutine and a connection for the
// full deadline.
func TestRouteStopsWaitingWhenTheCallerDoes(t *testing.T) {
	rt := routeRuntime(t)
	defer occupyLoop(t, rt)()

	route := &pluginRoute{path: "/api/call-transcript"}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)

	started := time.Now()
	_, err := rt.DispatchRoute(ctx, route, map[string]any{})
	elapsed := time.Since(started)

	if err == nil {
		t.Fatal("reported success for a handler that never ran")
	}

	// Generous, but far below pluginRouteTimeout: the point is that it
	// returned on cancellation rather than on the deadline.
	if elapsed > 5*time.Second {
		t.Errorf("waited %s after the caller left; want a return on cancellation", elapsed)
	}
}

// A route that times out is almost never the slow thing — it is behind the
// slow thing. A message naming only the route sends whoever reads it to
// investigate the one piece of code that is innocent, which is how the
// transcripts timeouts went unexplained.
func TestRouteTimeoutNamesWhatHeldTheLoop(t *testing.T) {
	rt := routeRuntime(t)

	// A job that overruns the slow threshold, recorded as the culprit.
	rt.runOnLoop("point:call.stored", func(vm *goja.Runtime) {
		time.Sleep(pluginSlowJobThreshold + 200*time.Millisecond)
	})

	// Let it finish so the record is written.
	time.Sleep(pluginSlowJobThreshold + 700*time.Millisecond)

	if busy := rt.lastSlowJob(); busy == "" {
		t.Fatal("a job that held the loop past the threshold left no record")
	} else if want := "point:call.stored"; !strings.Contains(busy, want) {
		t.Errorf("record is %q; want it to name %q", busy, want)
	}
}
