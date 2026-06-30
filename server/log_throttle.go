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

// logThrottleMaxKeys bounds the per-key map so a flood from many distinct
// source IPs can't grow it without limit. When exceeded, expired windows are
// pruned on the next Allow call.
const logThrottleMaxKeys = 10000

// LogThrottle is a simple fixed-window rate limiter keyed by an arbitrary
// string (typically a remote IP). It exists to keep high-frequency, low-value
// diagnostic log lines — e.g. the "CAL request: ... not found" line that fires
// on every bot hit to a dead share link — from flooding the rdioScannerLogs
// table (every LogEvent is a DB insert). Allow returns true while a key is
// under its per-window budget and false once it's over, so callers can gate
// LogEvent behind it.
type LogThrottle struct {
	mu     sync.Mutex
	hits   map[string]*logThrottleWindow
	limit  uint
	window time.Duration
}

type logThrottleWindow struct {
	count uint
	start time.Time
}

func NewLogThrottle(limit uint, window time.Duration) *LogThrottle {
	return &LogThrottle{
		hits:   make(map[string]*logThrottleWindow),
		limit:  limit,
		window: window,
	}
}

// Allow records a hit for key and reports whether it is within the per-window
// budget. The first `limit` calls in a window return true; further calls in the
// same window return false until the window rolls over.
func (t *LogThrottle) Allow(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	w := t.hits[key]
	if w == nil || now.Sub(w.start) >= t.window {
		if len(t.hits) >= logThrottleMaxKeys {
			t.pruneLocked(now)
		}
		w = &logThrottleWindow{start: now}
		t.hits[key] = w
	}

	w.count++
	return w.count <= t.limit
}

// pruneLocked drops entries whose window has elapsed. Caller must hold t.mu.
func (t *LogThrottle) pruneLocked(now time.Time) {
	for k, w := range t.hits {
		if now.Sub(w.start) >= t.window {
			delete(t.hits, k)
		}
	}
}
