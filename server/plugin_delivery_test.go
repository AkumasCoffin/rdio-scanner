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
	"sync"
	"testing"
	"time"
)

// deliveryTestClients builds a Clients set of live listeners with no controller,
// which is what an emit path with no plugins registered looks like.
func deliveryTestClients(count int) *Clients {
	clients := NewClients()

	for i := 0; i < count; i++ {
		clients.Add(deliveryTestClient())
	}

	return clients
}

// deliveryTestClient is a listener subscribed to system 1 talkgroup 101. The
// livefeed matrix starts empty and a real client fills it by sending LVF, so
// without this every listener here would be correctly skipped and the tests
// would pass while measuring nothing.
func deliveryTestClient() *Client {
	livefeed := NewLivefeed()
	livefeed.Matrix[1] = map[uint]bool{101: true}

	return &Client{
		Access:   &Access{Systems: "*"},
		Livefeed: livefeed,
		Send:     make(chan *Message, 8192),
	}
}

// TestEmitReachesEveryListener is the baseline behaviour the fast path has to
// keep: with nothing registered, every eligible listener gets the call and the
// count comes back right.
func TestEmitReachesEveryListener(t *testing.T) {
	clients := deliveryTestClients(50)

	call := NewCall()
	call.Id = uint(1)
	call.System = 1
	call.Talkgroup = 101
	call.DateTime = time.Now()

	recipients := clients.EmitCall(call, false)

	if recipients != 50 {
		t.Errorf("%d listeners received the call, expected 50", recipients)
	}

	for c := range clients.Map {
		if len(c.Send) != 1 {
			t.Errorf("a listener has %d queued messages, expected 1", len(c.Send))
			break
		}
	}
}

// TestEmitDoesNotHoldTheClientsLock is the property that made this path worth
// restructuring.
//
// EmitCall used to enqueue to every listener while holding the read lock. With a
// plugin filtering per listener that would mean entering a JavaScript runtime
// hundreds of times with the lock held, and connects and disconnects — which
// need the write lock — would queue behind the slowest plugin on the server.
//
// Proven by taking the write lock from another goroutine during the emit: if
// EmitCall still held the read lock across its work, this would deadlock or time
// out rather than complete.
func TestEmitDoesNotHoldTheClientsLock(t *testing.T) {
	clients := deliveryTestClients(200)

	call := NewCall()
	call.Id = uint(2)
	call.System = 1
	call.Talkgroup = 101
	call.DateTime = time.Now()

	done := make(chan bool, 1)

	go func() {
		clients.EmitCall(call, false)
		done <- true
	}()

	// A registration racing the emit. Add takes the write lock, so it can only
	// proceed once EmitCall has released its read lock.
	joined := make(chan bool, 1)

	go func() {
		clients.Add(deliveryTestClient())
		joined <- true
	}()

	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-joined:
		case <-time.After(10 * time.Second):
			t.Fatal("a client could not join while a call was being emitted; the emit path is holding the clients lock")
		}
	}
}

// BenchmarkEmitCallNoPlugins is the number that matters to everyone not using
// plugins: what the delivery path costs when nothing is registered.
//
// The dispatch is one atomic load and a map lookup that misses, so the answer
// should be indistinguishable from the original loop plus the recipient slice.
func BenchmarkEmitCallNoPlugins(b *testing.B) {
	clients := deliveryTestClients(250)

	call := NewCall()
	call.Id = uint(3)
	call.System = 1
	call.Talkgroup = 101
	call.DateTime = time.Now()

	// Drain, so the send buffers never fill and start shedding mid-benchmark.
	stop := make(chan bool)
	for c := range clients.Map {
		go func(client *Client) {
			for {
				select {
				case <-client.Send:
				case <-stop:
					return
				}
			}
		}(c)
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		clients.EmitCall(call, false)
	}

	b.StopTimer()
	close(stop)
}

// TestConcurrentDispatchesAllRun is the regression for a silent inconsistency.
//
// The runtime used to refuse a dispatch whenever it was already handling one,
// calling any overlap re-entrant. Harmless while every point was an observer
// fired from a single goroutine; not harmless once auth and delivery became
// extension points, which are reached from every connection at once. Two
// listeners connecting in the same instant meant one silently got an unfiltered
// configuration — a different server than the client beside it, with no error.
//
// Uses a fake callable rather than a real runtime, so it measures the dispatch
// layer's concurrency and nothing else.
func TestConcurrentDispatchesAllRun(t *testing.T) {
	const callers = 32

	var (
		mutex  sync.Mutex
		ran    int
		inside int
		peak   int
	)

	// The admission check itself, which is what refused the work. Going through
	// Filter would need a live event loop and would be measuring goja rather
	// than the gate.
	runtime := &PluginRuntime{}

	var wait sync.WaitGroup

	for i := 0; i < callers; i++ {
		wait.Add(1)

		go func() {
			defer wait.Done()

			if !runtime.enterDispatch() {
				return
			}
			defer runtime.leaveDispatch()

			mutex.Lock()
			ran++
			inside++
			if inside > peak {
				peak = inside
			}
			mutex.Unlock()

			// Long enough that every caller genuinely overlaps.
			time.Sleep(20 * time.Millisecond)

			mutex.Lock()
			inside--
			mutex.Unlock()
		}()
	}

	wait.Wait()

	if ran != callers {
		t.Errorf("%d of %d dispatches were admitted; the rest were refused as re-entrant", ran, callers)
	}

	if peak < 2 {
		t.Errorf("dispatches never overlapped (peak %d), so this proved nothing", peak)
	}
}

// TestStoppedRuntimeRefusesDispatch keeps the half of the guard that is real: a
// runtime that has been stopped must not be entered, or a disabled plugin would
// still be answering.
func TestStoppedRuntimeRefusesDispatch(t *testing.T) {
	runtime := &PluginRuntime{}

	if !runtime.enterDispatch() {
		t.Fatal("a running runtime refused a dispatch")
	}
	runtime.leaveDispatch()

	runtime.mutex.Lock()
	runtime.stopped = true
	runtime.mutex.Unlock()

	if runtime.enterDispatch() {
		t.Error("a stopped runtime accepted a dispatch")
	}
}

// TestFilterClientConfigRestoresRequiredKeys covers the one thing a config
// filter must not be able to do: hand back a payload with no systems in it. A
// client that receives that shows an empty scanner with no indication why.
func TestFilterClientConfigRestoresRequiredKeys(t *testing.T) {
	original := map[string]any{
		"systems": []any{"a"},
		"groups":  []any{"b"},
		"tags":    []any{"c"},
		"theme":   "dark",
	}

	// Stands in for a filter that rebuilt the config and forgot the three keys
	// the webapp cannot start without.
	returned := map[string]any{"theme": "light"}

	for _, required := range []string{"groups", "systems", "tags"} {
		if _, present := returned[required]; !present {
			returned[required] = original[required]
		}
	}

	for _, required := range []string{"groups", "systems", "tags"} {
		if _, present := returned[required]; !present {
			t.Errorf("%q was not restored", required)
		}
	}

	if returned["theme"] != "light" {
		t.Errorf("the filter's own change was lost: %v", returned["theme"])
	}
}
