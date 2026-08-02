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

// TestCloneAccessBreaksSharing is the check that matters most in this file.
//
// GetAccess hands back the live entry from Accesses.List, shared by every client
// using that code. Scoping one client by writing to it would rewrite what all of
// them can see, and the change would outlive the connection — so one listener's
// narrowed view would quietly become everyone's until the next reload.
func TestCloneAccessBreaksSharing(t *testing.T) {
	shared := &Access{
		Code:    "1234",
		Ident:   "shared",
		Systems: []any{map[string]any{"id": float64(1)}},
	}

	copied := cloneAccess(shared)

	applyPluginAccess(copied, map[string]any{
		"ident":   "narrowed",
		"systems": []any{},
	})

	if shared.Ident != "shared" {
		t.Errorf("the shared entry's ident was rewritten to %q", shared.Ident)
	}

	if systems, ok := shared.Systems.([]any); !ok || len(systems) != 1 {
		t.Errorf("the shared entry's systems were rewritten to %v", shared.Systems)
	}
}

// TestCloneAccessCopiesTheSystemsSlice covers the subtler half: the slice header
// is copied by assignment, but the backing array is not. A plugin appending to
// its copy would otherwise reach the shared entry through it.
func TestCloneAccessCopiesTheSystemsSlice(t *testing.T) {
	shared := &Access{Systems: []any{"a", "b"}}

	copied := cloneAccess(shared)

	systems, ok := copied.Systems.([]any)
	if !ok {
		t.Fatalf("systems came back as %T", copied.Systems)
	}

	systems[0] = "mutated"

	if original := shared.Systems.([]any); original[0] != "a" {
		t.Errorf("writing to the copy reached the shared entry: %v", original[0])
	}
}

// TestApplyPluginAccessReadsIntegerLimits covers the conversion Access.FromMap
// gets wrong for this purpose. FromMap decodes JSON, where every number is a
// float64; a JavaScript runtime hands back an int64 for a literal, so reusing it
// would silently ignore every limit a plugin ever set.
func TestApplyPluginAccessReadsIntegerLimits(t *testing.T) {
	for name, value := range map[string]any{
		"int64":   int64(5),
		"float64": float64(5),
	} {
		access := &Access{}

		applyPluginAccess(access, map[string]any{"limit": value})

		if access.Limit != uint(5) {
			t.Errorf("%s limit was not applied: got %v", name, access.Limit)
		}
	}
}

// TestApplyPluginAccessKeepsSystemsUnmarshalled guards the shape HasAccess
// reads. Storing the JSON text of the array instead of the array would leave a
// grant that matches nothing, and it would fail silently as a denial rather than
// as an error.
func TestApplyPluginAccessKeepsSystemsUnmarshalled(t *testing.T) {
	access := &Access{}

	applyPluginAccess(access, map[string]any{
		"systems": []any{
			map[string]any{"id": float64(1), "talkgroups": "*"},
		},
	})

	systems, ok := access.Systems.([]any)
	if !ok {
		t.Fatalf("systems became %T, which HasAccess cannot read", access.Systems)
	}

	call := NewCall()
	call.System = 1
	call.Talkgroup = 101

	if !access.HasAccess(call) {
		t.Errorf("a grant for system 1 did not match a call on system 1: %v", systems)
	}
}

// TestPluginIntegerIdsMatchSystems is the regression for the worst-shaped bug in
// this file. A plugin returning [{id: 1, talkgroups: '*'}] hands back int64 for
// the id, while every consumer type-switches on float64 because it was written
// against JSON. The grant then matches nothing — and it fails as a silent denial,
// so the listener is admitted and simply sees an empty server.
func TestPluginIntegerIdsMatchSystems(t *testing.T) {
	access := &Access{}

	// int64 is exactly what goja produces for a JavaScript integer literal.
	applyPluginAccess(access, map[string]any{
		"systems": []any{
			map[string]any{"id": int64(1), "talkgroups": "*"},
		},
	})

	call := NewCall()
	call.System = 1
	call.Talkgroup = 101

	if !access.HasAccess(call) {
		t.Fatalf("an integer system id did not match: %#v", access.Systems)
	}

	other := NewCall()
	other.System = 2
	other.Talkgroup = 101

	if access.HasAccess(other) {
		t.Error("the grant matched a system it did not name")
	}
}

// TestPluginIntegerTalkgroupsMatch covers the same conversion one level deeper,
// where the talkgroup list is numbers rather than a wildcard.
func TestPluginIntegerTalkgroupsMatch(t *testing.T) {
	access := &Access{}

	applyPluginAccess(access, map[string]any{
		"systems": []any{
			map[string]any{"id": int64(1), "talkgroups": []any{int64(101), int64(202)}},
		},
	})

	for _, talkgroup := range []uint{101, 202} {
		call := NewCall()
		call.System = 1
		call.Talkgroup = talkgroup

		if !access.HasAccess(call) {
			t.Errorf("talkgroup %d was not matched: %#v", talkgroup, access.Systems)
		}
	}

	call := NewCall()
	call.System = 1
	call.Talkgroup = 303

	if access.HasAccess(call) {
		t.Error("a talkgroup outside the grant was matched")
	}
}

// TestNormalizeJsonNumbersLeavesOtherValuesAlone keeps the conversion from
// stringifying or reshaping anything it should not touch.
func TestNormalizeJsonNumbersLeavesOtherValuesAlone(t *testing.T) {
	input := map[string]any{
		"text":  "unchanged",
		"flag":  true,
		"real":  float64(1.5),
		"empty": nil,
		"deep":  []any{map[string]any{"n": int64(3)}},
	}

	out, ok := normalizeJsonNumbers(input).(map[string]any)
	if !ok {
		t.Fatal("a map did not come back as a map")
	}

	if out["text"] != "unchanged" || out["flag"] != true || out["real"] != 1.5 {
		t.Errorf("a non-integer value was altered: %#v", out)
	}
	if out["empty"] != nil {
		t.Errorf("nil became %#v", out["empty"])
	}

	deep := out["deep"].([]any)[0].(map[string]any)
	if deep["n"] != float64(3) {
		t.Errorf("a nested integer was not converted: %#v", deep["n"])
	}
}

// TestApplyPluginAccessIgnoresPartialResults keeps the same contract the ingest
// filters have: naming one field means changing one field.
func TestApplyPluginAccessIgnoresPartialResults(t *testing.T) {
	access := &Access{Ident: "original", Systems: "*", Limit: uint(3)}

	applyPluginAccess(access, map[string]any{"ident": "renamed"})

	if access.Ident != "renamed" {
		t.Errorf("ident was not applied: %q", access.Ident)
	}
	if access.Systems != "*" {
		t.Errorf("systems changed to %v when it was not in the result", access.Systems)
	}
	if access.Limit != uint(3) {
		t.Errorf("limit changed to %v when it was not in the result", access.Limit)
	}
}

// TestApplyPluginAccessParsesExpiration covers the round trip, since expiry is
// handed out as RFC3339 and a plugin extending a session will echo it back.
func TestApplyPluginAccessParsesExpiration(t *testing.T) {
	access := &Access{}

	applyPluginAccess(access, map[string]any{"expiration": "2030-01-02T03:04:05Z"})

	when, ok := access.Expiration.(time.Time)
	if !ok {
		t.Fatalf("expiration became %T", access.Expiration)
	}

	if when.Year() != 2030 || when.Month() != time.January || when.Day() != 2 {
		t.Errorf("expiration parsed to %v", when)
	}

	if access.HasExpired() {
		t.Error("a date in 2030 was treated as expired")
	}
}
