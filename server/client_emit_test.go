// Copyright (C) 2019-2026 Chrystian Huot <chrystian.huot@saubeo.solutions>
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

// stalledClient is a client that is subscribed to the live feed but whose
// writer goroutine is gone and whose send buffer is already full — the state a
// half-open socket lands in once its write deadline expires.
func stalledClient(system uint, talkgroup uint) *Client {
	client := &Client{
		Access:   &Access{},
		Livefeed: NewLivefeed(),
		// Deliberately tiny so the buffer is full without queueing 8192
		// messages; enqueue's behaviour doesn't depend on the capacity.
		Send: make(chan *Message, 1),
	}

	client.Livefeed.Matrix[system] = map[uint]bool{talkgroup: true}
	client.Send <- &Message{Command: MessageCommandCall}

	return client
}

// A single unresponsive listener must not stall the shared emit path. Before
// enqueue(), EmitCall did a blocking channel send while holding
// Clients.mutex.RLock(), which wedged the clientEmitQueue dispatcher and then
// the ingest goroutine behind it.
func TestEmitCallDoesNotBlockOnStalledClient(t *testing.T) {
	clients := NewClients()
	stalled := stalledClient(1, 100)
	clients.Add(stalled)

	// A healthy client behind the stalled one must still get the call — the
	// old code never reached it.
	healthy := &Client{Access: &Access{}, Livefeed: NewLivefeed(), Send: make(chan *Message, 8)}
	healthy.Livefeed.Matrix[1] = map[uint]bool{100: true}
	clients.Add(healthy)

	call := &Call{System: 1, Talkgroup: 100}

	done := make(chan struct{})
	go func() {
		clients.EmitCall(call, false)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("EmitCall blocked on a stalled client (deadlock regression)")
	}

	if got := stalled.dropped.Load(); got != 1 {
		t.Errorf("stalled client: expected 1 dropped message, got %d", got)
	}

	select {
	case msg := <-healthy.Send:
		if msg.Command != MessageCommandCall {
			t.Errorf("healthy client: expected %q, got %q", MessageCommandCall, msg.Command)
		}
	default:
		t.Error("healthy client received nothing — a stalled peer blocked the broadcast")
	}
}

// The deadlock's second half: a blocking send held the read lock, so
// Clients.Remove could never take the write lock to reap the stalled client,
// and Go's RWMutex then starved every later RLock (Count, CountListeners) too.
func TestStalledClientCanStillBeRemoved(t *testing.T) {
	clients := NewClients()
	stalled := stalledClient(2, 200)
	clients.Add(stalled)

	for i := 0; i < 50; i++ {
		clients.EmitCall(&Call{System: 2, Talkgroup: 200}, false)
	}

	done := make(chan struct{})
	go func() {
		clients.Remove(stalled)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Remove blocked behind a stalled client's emit (deadlock regression)")
	}

	if n := clients.Count(); n != 0 {
		t.Errorf("expected 0 clients after Remove, got %d", n)
	}
}

// The production deadlock needed concurrency to appear: emits holding the read
// lock while registrations queued for the write lock. Hammer that interleaving
// with a permanently stalled client present throughout, and require the whole
// thing to drain within a deadline.
func TestEmitUnderConcurrentChurnDoesNotDeadlock(t *testing.T) {
	clients := NewClients()
	clients.Add(stalledClient(3, 300))

	call := &Call{System: 3, Talkgroup: 300}

	var wg sync.WaitGroup

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 200; n++ {
				clients.EmitCall(call, false)
				clients.EmitListenersCount()
			}
		}()
	}

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 200; n++ {
				c := &Client{Access: &Access{}, Livefeed: NewLivefeed(), Send: make(chan *Message, 4)}
				c.Livefeed.Matrix[3] = map[uint]bool{300: true}
				clients.Add(c)
				clients.Count()
				clients.Remove(c)
			}
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("emit/register churn deadlocked with a stalled client present")
	}

	if n := clients.Count(); n != 1 {
		t.Errorf("expected only the stalled client to remain, got %d", n)
	}
}

// enqueue must treat a client whose writer goroutine has exited as dead,
// rather than filling a channel nobody drains.
func TestEnqueueRejectsClosedClient(t *testing.T) {
	client := &Client{Send: make(chan *Message, 8)}

	if !client.enqueue(&Message{Command: MessageCommandCall}) {
		t.Fatal("enqueue on a live client with buffer space should succeed")
	}

	client.closed.Store(true)

	if client.enqueue(&Message{Command: MessageCommandCall}) {
		t.Error("enqueue on a closed client should report failure")
	}
}
