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

// pendingTranscriptTTL is how long a transcript push for an unknown call is
// held in memory waiting for the matching call-upload to land. Five minutes is
// well over any plausible network/Delayer skew while still bounding leak risk
// from calls that never arrive.
const pendingTranscriptTTL = 5 * time.Minute

// pendingTranscriptCap bounds the in-memory map so a misconfigured upstream
// firehose can't OOM us. When full, all expired entries are pruned; if still
// over the cap, the oldest live entry is dropped.
const pendingTranscriptCap = 1000

type pendingTranscriptEntry struct {
	transcript  string
	apikeyIdent string
	storedAt    time.Time
}

// PendingTranscripts holds transcript pushes whose matching call hasn't been
// stored locally yet. Fixes the race where the upstream's transcript HTTP push
// (tiny JSON) overtakes its own call-upload (large multipart) on the wire.
//
// IngestCall consults this cache after WriteCall and applies any held
// transcript atomically with the call insert.
type PendingTranscripts struct {
	mu sync.Mutex
	m  map[string]*pendingTranscriptEntry
}

func NewPendingTranscripts() *PendingTranscripts {
	return &PendingTranscripts{
		m: make(map[string]*pendingTranscriptEntry),
	}
}

func pendingTranscriptKey(system uint, talkgroup uint, dateTime time.Time) string {
	return fmt.Sprintf("%d:%d:%s", system, talkgroup, dateTime.UTC().Format(time.RFC3339))
}

// Store records a transcript that arrived before its matching call. Replaces
// any existing entry for the same key (retries from upstream are idempotent).
// Also opportunistically prunes expired entries and enforces the cap.
func (p *PendingTranscripts) Store(system uint, talkgroup uint, dateTime time.Time, transcript string, apikeyIdent string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.pruneLocked()

	key := pendingTranscriptKey(system, talkgroup, dateTime)
	p.m[key] = &pendingTranscriptEntry{
		transcript:  transcript,
		apikeyIdent: apikeyIdent,
		storedAt:    time.Now(),
	}
}

// Take returns and removes the pending transcript for the given key, if any.
// Returns ("", "", false) when no live entry exists.
func (p *PendingTranscripts) Take(system uint, talkgroup uint, dateTime time.Time) (string, string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	key := pendingTranscriptKey(system, talkgroup, dateTime)
	entry, ok := p.m[key]
	if !ok {
		return "", "", false
	}
	delete(p.m, key)
	if time.Since(entry.storedAt) > pendingTranscriptTTL {
		// Treat expired entries as misses — don't apply stale transcripts.
		return "", "", false
	}
	return entry.transcript, entry.apikeyIdent, true
}

// pruneLocked removes expired entries and, if the map is still over capacity,
// drops the oldest entries until it fits. Caller must hold p.mu.
func (p *PendingTranscripts) pruneLocked() {
	now := time.Now()
	for k, v := range p.m {
		if now.Sub(v.storedAt) > pendingTranscriptTTL {
			delete(p.m, k)
		}
	}

	if len(p.m) < pendingTranscriptCap {
		return
	}

	// Drop oldest entries until we're back under the cap. Two-pass scan
	// because map iteration is randomized and we want a deterministic-ish
	// "drop the oldest" instead of "drop random N."
	for len(p.m) >= pendingTranscriptCap {
		var oldestKey string
		var oldestAt time.Time
		first := true
		for k, v := range p.m {
			if first || v.storedAt.Before(oldestAt) {
				oldestKey = k
				oldestAt = v.storedAt
				first = false
			}
		}
		if oldestKey == "" {
			break
		}
		delete(p.m, oldestKey)
	}
}
