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
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateLogMessageLeavesShortMessagesAlone(t *testing.T) {
	for _, message := range []string{
		"",
		"server started",
		strings.Repeat("x", logMessageMaxLen),
	} {
		if got := truncateLogMessage(message); got != message {
			t.Errorf("message of %d chars was altered: got %d chars", len(message), len(got))
		}
	}
}

func TestTruncateLogMessageClipsToColumnWidth(t *testing.T) {
	message := strings.Repeat("x", logMessageMaxLen*3)

	got := truncateLogMessage(message)

	if n := utf8.RuneCountInString(got); n != logMessageMaxLen {
		t.Errorf("expected exactly %d chars, got %d", logMessageMaxLen, n)
	}
	if !strings.HasSuffix(got, "...") {
		t.Error("expected the clipped message to be marked with an ellipsis")
	}
}

// Postgres counts characters, not bytes, for varchar(255) — so a message of
// 255 multi-byte runes fits and must pass through untouched, and a longer one
// must never be cut mid-rune.
func TestTruncateLogMessageIsRuneSafe(t *testing.T) {
	fits := strings.Repeat("é", logMessageMaxLen)
	if got := truncateLogMessage(fits); got != fits {
		t.Errorf("a %d-rune message fits in varchar(%d) and should pass through unchanged",
			logMessageMaxLen, logMessageMaxLen)
	}

	long := strings.Repeat("é", logMessageMaxLen*2)
	got := truncateLogMessage(long)

	if !utf8.ValidString(got) {
		t.Error("truncation split a multi-byte rune")
	}
	if n := utf8.RuneCountInString(got); n != logMessageMaxLen {
		t.Errorf("expected exactly %d runes, got %d", logMessageMaxLen, n)
	}
}

// The line that reports a failed configuration save concatenates a per-section
// error map. On Postgres an over-long value is rejected rather than truncated,
// and LogEvent's error is discarded by essentially every caller — so this, the
// single most useful line for diagnosing a failed save, was the one guaranteed
// to be dropped. Guard the case rather than trusting it stays short.
func TestConfigurationSaveErrorLineFitsAfterTruncation(t *testing.T) {
	aborted := "pq: current transaction is aborted, commands ignored until end of transaction block"

	sectionErrs := map[string]string{
		"groups": `pq: duplicate key value violates unique constraint "rdioScannerGroups_pkey"`,
	}
	for _, s := range []string{"access", "apiKeys", "dirWatch", "downstreams", "options", "systems", "tags"} {
		sectionErrs[s] = aborted
	}

	raw := fmt.Sprintf("configuration save had errors: %v", sectionErrs)

	if utf8.RuneCountInString(raw) <= logMessageMaxLen {
		t.Skip("message no longer exceeds the column width; truncation guard untested")
	}

	if n := utf8.RuneCountInString(truncateLogMessage(raw)); n > logMessageMaxLen {
		t.Errorf("truncated message still exceeds varchar(%d): %d chars", logMessageMaxLen, n)
	}
}
