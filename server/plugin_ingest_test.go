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
	"testing"
	"time"
)

func ingestTestCall() *Call {
	call := NewCall()
	call.Id = uint(77)
	call.Audio = []byte{1, 2, 3, 4}
	call.AudioName = "original.m4a"
	call.AudioType = "audio/mp4"
	call.DateTime = time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	call.System = 1
	call.Talkgroup = 101
	return call
}

// TestPartialFilterResultLeavesTheRestAlone is the property the whole write-back
// rests on. A plugin returning one field is asking for one change; if anything
// else moved, every filter would have to echo the entire call back correctly and
// the first one to forget a field would corrupt it.
func TestPartialFilterResultLeavesTheRestAlone(t *testing.T) {
	call := ingestTestCall()

	applyPluginCallValue(call, map[string]any{"talkgroup": int64(999)})

	if call.Talkgroup != 999 {
		t.Errorf("talkgroup was not applied: got %d", call.Talkgroup)
	}
	if call.System != 1 {
		t.Errorf("system changed to %d when it was not in the result", call.System)
	}
	if call.AudioName != "original.m4a" {
		t.Errorf("audioName changed to %v when it was not in the result", call.AudioName)
	}
	if len(call.Audio) != 4 {
		t.Errorf("audio changed to %d bytes when it was not in the result", len(call.Audio))
	}
}

// TestFilterCannotRewriteTheId guards the one field that must never be writable.
// A plugin setting an id would produce a call that overwrites another one.
func TestFilterCannotRewriteTheId(t *testing.T) {
	call := ingestTestCall()

	applyPluginCallValue(call, map[string]any{"id": int64(1), "audioName": "new.m4a"})

	if call.Id != uint(77) {
		t.Errorf("id was rewritten to %v", call.Id)
	}
	if call.AudioName != "new.m4a" {
		t.Errorf("the writable field in the same map was not applied: %v", call.AudioName)
	}
}

// TestEmptyAudioDoesNotWipeTheCall covers the failure a plugin is most likely to
// hit: an error path that returns nothing where audio was expected. Honouring it
// would store a silent call, which is worse than storing the original.
func TestEmptyAudioDoesNotWipeTheCall(t *testing.T) {
	for name, value := range map[string]any{
		"nil":         nil,
		"empty bytes": []byte{},
		"empty array": []any{},
		"wrong type":  42,
	} {
		call := ingestTestCall()

		applyPluginCallValue(call, map[string]any{"audio": value})

		if len(call.Audio) != 4 {
			t.Errorf("%s replacement wiped the audio: %d bytes left", name, len(call.Audio))
		}
	}
}

// TestDateTimeRoundTrips checks the format handed out is the format accepted
// back. A plugin that echoes the call unchanged must not shift the timestamp.
func TestDateTimeRoundTrips(t *testing.T) {
	call := ingestTestCall()
	original := call.DateTime

	handedOut := pluginCallValue(call, false)

	applyPluginCallValue(call, map[string]any{"dateTime": handedOut["dateTime"]})

	if !call.DateTime.Equal(original) {
		t.Errorf("timestamp moved on a round trip: %v became %v", original, call.DateTime)
	}
}

// TestNumbersArriveInEitherForm covers the conversion that silently breaks
// otherwise. JavaScript hands back int64 for a literal and float64 once any
// arithmetic has happened, so a plugin computing a talkgroup would find its
// result ignored if only one were handled.
func TestNumbersArriveInEitherForm(t *testing.T) {
	for name, value := range map[string]any{
		"int64":   int64(555),
		"float64": float64(555),
		"int":     555,
	} {
		call := ingestTestCall()

		applyPluginCallValue(call, map[string]any{"talkgroup": value})

		if call.Talkgroup != 555 {
			t.Errorf("%s was not converted: talkgroup is %d", name, call.Talkgroup)
		}
	}
}

// TestZeroAndNegativeIdsAreRefused stops a plugin's arithmetic mistake from
// producing a call that no longer matches any talkgroup.
func TestZeroAndNegativeIdsAreRefused(t *testing.T) {
	for name, value := range map[string]any{
		"zero":     int64(0),
		"negative": int64(-1),
		"string":   "202",
	} {
		call := ingestTestCall()

		applyPluginCallValue(call, map[string]any{"system": value, "talkgroup": value})

		if call.System != 1 || call.Talkgroup != 101 {
			t.Errorf("%s was accepted: system %d talkgroup %d", name, call.System, call.Talkgroup)
		}
	}
}

// TestMetaIsStringified covers a plugin storing numbers or booleans in metadata,
// which the database layer expects as strings.
func TestMetaIsStringified(t *testing.T) {
	call := ingestTestCall()

	applyPluginCallValue(call, map[string]any{
		"meta": map[string]any{"score": int64(7), "flagged": true, "note": "ok", "skipped": nil},
	})

	if call.meta["score"] != "7" {
		t.Errorf("number was not stringified: %q", call.meta["score"])
	}
	if call.meta["flagged"] != "true" {
		t.Errorf("boolean was not stringified: %q", call.meta["flagged"])
	}
	if call.meta["note"] != "ok" {
		t.Errorf("string was altered: %q", call.meta["note"])
	}
	if _, present := call.meta["skipped"]; present {
		t.Error("a null value was stored rather than skipped")
	}
}

// TestNonMapResultIsIgnored covers a plugin returning a string or a number by
// mistake, which must leave the call untouched rather than panicking ingest.
func TestNonMapResultIsIgnored(t *testing.T) {
	for _, value := range []any{nil, "oops", 42, []any{1, 2}} {
		call := ingestTestCall()

		applyPluginCallValue(call, value)

		if call.Talkgroup != 101 || len(call.Audio) != 4 {
			t.Errorf("%v altered the call", value)
		}
	}
}
