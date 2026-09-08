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
	"sync"
	"sync/atomic"
	"testing"
	"time"
)


// flightWaiters reports how many callers are parked on the flight for key, or
// -1 when nothing is in flight. Lets a test wait until every caller has
// genuinely arrived instead of sleeping and hoping — without it the leader can
// finish before the others even start, and they then correctly become leaders
// of their own, which looks like a broken single-flight and is not one.
func (calls *Calls) flightWaiters(key string) int32 {
	calls.metaMutex.Lock()
	defer calls.metaMutex.Unlock()

	if flight, ok := calls.inFlight[key]; ok {
		return atomic.LoadInt32(&flight.waiters)
	}

	return -1
}

// awaitWaiters blocks until want callers are parked on key.
func awaitWaiters(t *testing.T, calls *Calls, key string, want int32) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if calls.flightWaiters(key) >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}

	t.Fatalf("only %d of %d callers reached the flight for %s", calls.flightWaiters(key), want, key)
}

// A cold cache and many callers at once is the ordinary state of affairs after
// a restart: every open tab searches, and none of them find anything cached.
// Each one used to issue its own copy of the same statement, and because the
// window is as long as the query, the slower the question the more duplicates
// of it — which is how a probe that should be one index lookup showed up in
// production three times over at eight seconds each.
func TestSearchMetaOnceCollapsesConcurrentCallers(t *testing.T) {
	calls := &Calls{metaCache: map[string]*callsSearchMeta{}}

	var ran int32
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once

	const callers = 24
	var wg sync.WaitGroup
	results := make([]*callsSearchMeta, callers)

	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			meta, err := calls.searchMetaOnce("range:true", func() (*callsSearchMeta, error) {
				atomic.AddInt32(&ran, 1)
				// Hold the flight open until every caller has arrived, so they
				// are genuinely concurrent rather than merely quick.
				once.Do(func() { close(started) })
				<-release

				return &callsSearchMeta{count: 42, expires: time.Now().Add(time.Minute)}, nil
			})
			if err != nil {
				t.Errorf("caller %d: %v", i, err)
			}
			results[i] = meta
		}(i)
	}

	<-started
	awaitWaiters(t, calls, "range:true", callers-1)
	close(release)
	wg.Wait()

	if got := atomic.LoadInt32(&ran); got != 1 {
		t.Fatalf("the query ran %d times for %d concurrent callers, want 1", got, callers)
	}

	for i, meta := range results {
		if meta == nil || meta.count != 42 {
			t.Fatalf("caller %d got %+v, want the leader's answer", i, meta)
		}
	}
}

// A follower takes the leader's error rather than trying again. Retrying is the
// stampede this exists to prevent, and it would arrive exactly when the
// database is already in trouble.
func TestSearchMetaOnceSharesTheLeadersFailure(t *testing.T) {
	calls := &Calls{metaCache: map[string]*callsSearchMeta{}}

	wanted := errors.New("database is unwell")

	var ran int32
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once

	const callers = 8
	var wg sync.WaitGroup
	errs := make([]error, callers)

	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := calls.searchMetaOnce("count:true", func() (*callsSearchMeta, error) {
				atomic.AddInt32(&ran, 1)
				once.Do(func() { close(started) })
				<-release

				return nil, wanted
			})
			errs[i] = err
		}(i)
	}

	<-started
	awaitWaiters(t, calls, "count:true", callers-1)
	close(release)
	wg.Wait()

	if got := atomic.LoadInt32(&ran); got != 1 {
		t.Fatalf("the query ran %d times, want 1", got)
	}

	for i, err := range errs {
		if !errors.Is(err, wanted) {
			t.Fatalf("caller %d got %v, want the leader's error", i, err)
		}
	}

	// And nothing was cached, so the next caller is free to try again.
	if _, ok := calls.getSearchMeta("count:true"); ok {
		t.Fatal("a failed computation was cached")
	}
}

// The cache still has to work: a second caller after the first has finished
// takes the stored answer without running anything.
func TestSearchMetaOnceServesTheCache(t *testing.T) {
	calls := &Calls{metaCache: map[string]*callsSearchMeta{}}

	var ran int32
	compute := func() (*callsSearchMeta, error) {
		atomic.AddInt32(&ran, 1)
		return &callsSearchMeta{count: 7, expires: time.Now().Add(time.Minute)}, nil
	}

	for i := 0; i < 5; i++ {
		meta, err := calls.searchMetaOnce("range:true", compute)
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if meta.count != 7 {
			t.Fatalf("call %d: count %d, want 7", i, meta.count)
		}
	}

	if got := atomic.LoadInt32(&ran); got != 1 {
		t.Fatalf("the query ran %d times across five sequential calls, want 1", got)
	}
}

// An expired entry is recomputed rather than served forever.
func TestSearchMetaOnceRecomputesAfterExpiry(t *testing.T) {
	calls := &Calls{metaCache: map[string]*callsSearchMeta{}}

	var ran int32
	compute := func() (*callsSearchMeta, error) {
		atomic.AddInt32(&ran, 1)
		// Already stale when it is stored.
		return &callsSearchMeta{count: 1, expires: time.Now().Add(-time.Second)}, nil
	}

	for i := 0; i < 3; i++ {
		if _, err := calls.searchMetaOnce("range:true", compute); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}

	if got := atomic.LoadInt32(&ran); got != 3 {
		t.Fatalf("the query ran %d times, want 3 — an expired entry must not be served", got)
	}
}
