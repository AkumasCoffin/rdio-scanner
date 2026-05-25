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

// fallbackTranscriptTTL is how long we wait for an upstream's transcript push
// to arrive before assuming it never will and running local Whisper as a
// fallback. Most legitimate pushes arrive within a few tens of seconds, so
// two minutes is comfortably past "should have shown up by now" without
// being so long that calls stay untranscribed for noticeably long stretches
// when upstream silently fails.
const fallbackTranscriptTTL = 2 * time.Minute

// FallbackTranscripts schedules a deferred local transcription for calls
// that arrived from an upstream with transcriptPending=1, in case the
// upstream's transcript-forward push never actually arrives (runtime Whisper
// failure on upstream, rate-limit exhaustion, network outage, etc.).
//
// The map keys are local call ids. Schedule starts a timer; Cancel stops it
// (typically called when the upstream's transcript push finally lands). When
// the timer fires the fallback function runs in its own goroutine — the
// usual TranscribeCallAsync path — so the call gets transcribed locally
// after all.
type FallbackTranscripts struct {
	mu sync.Mutex
	m  map[uint]*time.Timer
}

func NewFallbackTranscripts() *FallbackTranscripts {
	return &FallbackTranscripts{
		m: make(map[uint]*time.Timer),
	}
}

// Schedule starts a fallback timer for the given call id. If a timer was
// already scheduled for this id, it's replaced (the previous one is stopped).
func (f *FallbackTranscripts) Schedule(id uint, fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if existing, ok := f.m[id]; ok {
		existing.Stop()
	}

	// Capture id so the cleanup inside the timer-fire callback can remove the
	// right map entry. The fn closure already captures whatever it needs
	// (typically a controller pointer).
	f.m[id] = time.AfterFunc(fallbackTranscriptTTL, func() {
		f.mu.Lock()
		delete(f.m, id)
		f.mu.Unlock()
		fn()
	})
}

// Cancel stops the fallback timer for the given call id, if one is pending.
// Returns true if a timer was actually cancelled, so the caller can decide
// whether to log it (avoids "cancelled but nothing was scheduled" noise).
func (f *FallbackTranscripts) Cancel(id uint) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.m[id]; ok {
		t.Stop()
		delete(f.m, id)
		return true
	}
	return false
}
