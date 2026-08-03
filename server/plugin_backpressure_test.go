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
	"time"
)

// The contract is that a plugin may degrade the server but must never be able
// to lose a call. It was broken by backpressure rather than by any of the
// dispatch verbs:
//
//	a slow call.emit filter stalls the single emit dispatcher
//	 -> clientEmitQueue fills
//	 -> EmitCallToClients blocks, and it is called from the ingest goroutine
//	 -> the Ingest channel fills
//	 -> the upload handler blocks on its send
//	 -> the recorder times out and throws the audio away
//
// Every link in that chain was a bare channel send with no default.
func newBackpressureController(t *testing.T, depth int) *Controller {
	t.Helper()

	return &Controller{
		Ingest:              make(chan *Call, depth),
		clientEmitQueue:     make(chan *Call, depth),
		downstreamEmitQueue: make(chan *Call, depth),
		Logs:                &Logs{},
		emitLogThrottle:     NewLogThrottle(1, time.Minute),
	}
}

func TestIngestRefusesRatherThanBlocking(t *testing.T) {
	controller := newBackpressureController(t, 2)

	for i := 0; i < 2; i++ {
		if err := controller.QueueIngest(NewCall()); err != nil {
			t.Fatalf("call %d was refused with room left: %v", i, err)
		}
	}

	// Third call: the queue is full. This must return, not park forever.
	done := make(chan error, 1)
	go func() { done <- controller.QueueIngest(NewCall()) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a full queue accepted a call it had no room for")
		}
		// The message has to be something an operator can act on.
		if !strings.Contains(err.Error(), "full") {
			t.Errorf("the refusal does not say what went wrong: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("QueueIngest blocked on a full queue; an upload handler would hang here until the recorder gave up and discarded the audio")
	}

	// Draining one makes room again — the refusal is a back-off, not a fault.
	<-controller.Ingest
	if err := controller.QueueIngest(NewCall()); err != nil {
		t.Fatalf("a call was refused after the queue drained: %v", err)
	}
}

// The emit queues are fed from the ingest goroutine, so blocking on a full one
// stops ingest — which loses every subsequent call rather than one broadcast.
// Dropping is the lesser loss: the call is already stored and searchable.
func TestEmitQueuesDropRatherThanStallIngest(t *testing.T) {
	controller := newBackpressureController(t, 1)

	call := NewCall()
	call.System = 3
	call.Talkgroup = 10329

	for _, emit := range []struct {
		name string
		fn   func(*Call)
	}{
		{"listeners", controller.EmitCallToClients},
		{"downstreams", controller.EmitCallToDownstreams},
	} {
		emit.fn(call)

		done := make(chan struct{})
		go func() {
			emit.fn(call)
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: a full emit queue blocked its caller, which is the ingest goroutine", emit.name)
		}
	}
}

// The warning about a full queue must not become part of the problem: the
// condition is thousands of calls deep, and LogEvent writes a row per call.
func TestQueueFullWarningIsThrottled(t *testing.T) {
	controller := newBackpressureController(t, 1)

	if !controller.emitLogThrottle.Allow("listener") {
		t.Fatal("the first warning was suppressed")
	}
	for i := 0; i < 100; i++ {
		if controller.emitLogThrottle.Allow("listener") {
			t.Fatal("the warning is not throttled; a sustained overflow would write a log row per dropped call")
		}
	}

	// Separate queues are counted separately, so one flooding does not hide the
	// other.
	if !controller.emitLogThrottle.Allow("downstream") {
		t.Fatal("one queue's throttle suppressed another queue's first warning")
	}
}

// A per-invocation timeout answers "how long may this handler run". At the emit
// point that is the wrong question: the same decision is made once per
// listener, on one goroutine, so the real cost is the timeout multiplied by the
// audience. At 500 listeners a filter that merely hits its 250ms ceiling costs
// 125 seconds for a single call.
func TestEmitBudgetBoundsTheWholeFanOut(t *testing.T) {
	dispatch := &PluginDispatch{points: map[string]bool{PointCallEmit: true}}
	dispatch.registry.Store(&dispatchRegistry{handlers: map[string][]*pluginHandler{}})
	dispatch.controller = &Controller{Logs: &Logs{}, emitLogThrottle: NewLogThrottle(1, time.Minute)}

	// A filter that always overruns, which is the case that used to be
	// unbounded.
	dispatch.addTestFilter("slow", PointCallEmit, func(args ...any) (any, error) {
		time.Sleep(20 * time.Millisecond)
		return nil, nil
	})

	budget := newPluginBudget(100 * time.Millisecond)

	call := NewCall()
	call.System = 3
	call.Talkgroup = 10329

	started := time.Now()

	served := 0
	for i := 0; i < 200; i++ {
		if dispatch.ShouldEmit(call, nil, budget) {
			served++
		}
	}

	elapsed := time.Since(started)

	// Without a budget this is 200 x 20ms = 4 seconds. With one it stops at the
	// allowance, plus the handler that was mid-flight when it ran out.
	if elapsed > time.Second {
		t.Fatalf("the fan-out took %v; the budget did not bound it", elapsed)
	}
	if budget.skipped() == 0 {
		t.Fatal("the budget never reported running out, so nothing was actually bounded")
	}

	// Running out must not silence the feed. Every listener still gets the call
	// — a plugin that cannot answer in time does not get to decide for it.
	if served != 200 {
		t.Fatalf("%d of 200 listeners were served; an exhausted budget dropped listeners instead of passing them through", served)
	}
}

// A filter that answers quickly must not be affected. The budget exists for the
// pathological case and has to be invisible the rest of the time.
func TestEmitBudgetDoesNotBiteAFastFilter(t *testing.T) {
	dispatch := &PluginDispatch{points: map[string]bool{PointCallEmit: true}}
	dispatch.registry.Store(&dispatchRegistry{handlers: map[string][]*pluginHandler{}})
	dispatch.controller = &Controller{Logs: &Logs{}, emitLogThrottle: NewLogThrottle(1, time.Minute)}

	vetoed := 0
	dispatch.addTestFilter("fast", PointCallEmit, func(args ...any) (any, error) {
		vetoed++
		return map[string]any{"drop": true}, nil
	})

	budget := newPluginBudget(pluginEmitCallBudget)
	call := NewCall()

	for i := 0; i < 2000; i++ {
		if dispatch.ShouldEmit(call, nil, budget) {
			t.Fatalf("listener %d was served despite the filter vetoing", i)
		}
	}

	if budget.skipped() != 0 {
		t.Fatalf("a fast filter exhausted the budget after %d skips", budget.skipped())
	}
	if vetoed != 2000 {
		t.Fatalf("the filter ran %d times, expected 2000", vetoed)
	}
}

// The allowance is charged for what handlers actually use, and refuses once
// spent.
func TestPluginBudgetAccounting(t *testing.T) {
	budget := newPluginBudget(100 * time.Millisecond)

	// A handler asking for more than remains gets a shorter leash rather than
	// the full timeout.
	allowed, ok := budget.take(500 * time.Millisecond)
	if !ok || allowed != 100*time.Millisecond {
		t.Fatalf("take returned %v, %v", allowed, ok)
	}

	budget.spend(60 * time.Millisecond)

	if allowed, ok = budget.take(500 * time.Millisecond); !ok || allowed != 40*time.Millisecond {
		t.Fatalf("after spending 60ms of 100ms, take returned %v, %v", allowed, ok)
	}

	budget.spend(40 * time.Millisecond)

	if _, ok = budget.take(time.Millisecond); ok {
		t.Fatal("a spent budget still handed out time")
	}
	if budget.skipped() != 1 {
		t.Fatalf("skipped reported %d, expected 1", budget.skipped())
	}

	// A nil budget means unbounded, which is every point reached once per call.
	var none *pluginBudget
	if allowed, ok = none.take(time.Second); !ok || allowed != time.Second {
		t.Fatalf("a nil budget bounded something: %v, %v", allowed, ok)
	}
	none.spend(time.Hour)
	if none.skipped() != 0 {
		t.Fatal("a nil budget reported skips")
	}
}
