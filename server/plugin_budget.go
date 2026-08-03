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
	"time"
)

const (
	// pluginEmitCallBudget bounds everything call.emit costs for one call,
	// across every listener.
	//
	// The per-invocation timeout is 250ms and it is correctly enforced, but
	// nothing bounded invocations × listeners: at 500 listeners a filter that
	// merely hits its timeout costs 125 seconds for a single call, on the one
	// goroutine that drains the emit queue. Two seconds is thousands of
	// invocations for a filter behaving normally — the measured cost of a
	// trivial one is under a microsecond — and a hard stop for one that is not.
	pluginEmitCallBudget = 2 * time.Second

	// pluginIngestCallBudget bounds everything the ingest points cost for one
	// call.
	//
	// The ingest points each have their own ceiling, and they are generous on
	// purpose: re-encoding a long call legitimately takes time. But they are
	// per invocation and per point, so the true worst case is the sum of all
	// five multiplied by the number of registered handlers — roughly 72
	// seconds each — on the single goroutine that every upload waits behind.
	pluginIngestCallBudget = 60 * time.Second
)

// pluginBudget is a wall-clock allowance shared by every dispatch belonging to
// one call.
//
// It exists because a per-invocation timeout answers "how long may this
// handler run", which is not the question that matters at scale. The question
// that matters is "how long may this call tie up a shared goroutine", and that
// is the product of the timeout and however many times the point is reached.
//
// Running out is not an error. A handler that never got to run is treated
// exactly like one that failed: the value passes through unchanged, because a
// plugin must never be able to lose a call — least of all by being slow.
type pluginBudget struct {
	mutex     sync.Mutex
	remaining time.Duration
	overruns  int
}

func newPluginBudget(total time.Duration) *pluginBudget {
	return &pluginBudget{remaining: total}
}

// take reserves up to `want` from what is left.
//
// The returned duration is what the caller should use as its timeout, so the
// last handler before exhaustion gets a short leash rather than a full one.
// false means there is nothing left and the handler should be skipped.
func (budget *pluginBudget) take(want time.Duration) (time.Duration, bool) {
	if budget == nil {
		return want, true
	}

	budget.mutex.Lock()
	defer budget.mutex.Unlock()

	if budget.remaining <= 0 {
		budget.overruns++
		return 0, false
	}

	if want > budget.remaining {
		want = budget.remaining
	}

	return want, true
}

// spend records what a handler actually used. Handlers that finish quickly
// barely touch the allowance, which is the point: the budget only ever bites
// the case it exists for.
func (budget *pluginBudget) spend(used time.Duration) {
	if budget == nil {
		return
	}

	budget.mutex.Lock()
	defer budget.mutex.Unlock()

	budget.remaining -= used
}

// skipped reports how many handlers were passed over because the allowance ran
// out, so the caller can say so once rather than per handler.
func (budget *pluginBudget) skipped() int {
	if budget == nil {
		return 0
	}

	budget.mutex.Lock()
	defer budget.mutex.Unlock()

	return budget.overruns
}
