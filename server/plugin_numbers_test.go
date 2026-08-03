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

	"github.com/dop251/goja"
)

// goja gives an integer literal an int64; encoding/json gives every number a
// float64. Both reach the same FromMap. Naming only float64 read the admin
// panel perfectly and silently ignored everything a plugin set.
//
// This test uses goja rather than hand-written int64s, so it stays true to what
// a plugin actually produces rather than to what we currently believe it
// produces.
func gojaObject(t *testing.T, src string) map[string]any {
	t.Helper()

	vm := goja.New()
	value, err := vm.RunString("(" + src + ")")
	if err != nil {
		t.Fatalf("cannot evaluate %s: %v", src, err)
	}

	m, ok := value.Export().(map[string]any)
	if !ok {
		t.Fatalf("%s did not export as a map", src)
	}

	return m
}

func TestModelsAcceptNumbersFromJavaScript(t *testing.T) {
	t.Run("system", func(t *testing.T) {
		system := NewSystem().FromMap(gojaObject(t, `{_id: 7, id: 3, label: 'Metro', order: 2, delay: 5}`))

		if system.RowId != uint(7) || system.Id != 3 || system.Order != 2 || system.Delay != 5 {
			t.Fatalf("rowId=%v id=%d order=%d delay=%d — a plugin's numbers were dropped",
				system.RowId, system.Id, system.Order, system.Delay)
		}
		if system.Label != "Metro" {
			t.Fatalf("label was %q", system.Label)
		}
	})

	t.Run("talkgroup", func(t *testing.T) {
		talkgroup := (&Talkgroup{}).FromMap(gojaObject(t,
			`{id: 10329, label: 'SW Ops', frequency: 851000000, tagId: 2, groupId: 3, order: 1, delay: 9}`))

		if talkgroup.Id != 10329 {
			t.Fatalf("id=%d", talkgroup.Id)
		}
		if freq, ok := jsonUint(talkgroup.Frequency); !ok || freq != 851000000 {
			t.Fatalf("frequency came through as %#v", talkgroup.Frequency)
		}
		if talkgroup.TagId != 2 || talkgroup.GroupId != 3 {
			t.Fatalf("tagId=%d groupId=%d", talkgroup.TagId, talkgroup.GroupId)
		}
		if talkgroup.Order != 1 || talkgroup.Delay != 9 {
			t.Fatalf("order=%d delay=%d", talkgroup.Order, talkgroup.Delay)
		}
	})

	t.Run("access", func(t *testing.T) {
		access := NewAccess().FromMap(gojaObject(t, `{_id: 2, code: 'abc', limit: 25, order: 4}`))

		if access.Id != uint(2) || access.Order != uint(4) {
			t.Fatalf("_id=%v order=%v", access.Id, access.Order)
		}
		// The connection limit is the one that matters most: dropped, it reads
		// as "no limit" rather than as an error.
		if limit, ok := jsonUint(access.Limit); !ok || limit != 25 {
			t.Fatalf("limit came through as %#v", access.Limit)
		}
	})

	t.Run("unit", func(t *testing.T) {
		unit := (&Unit{}).FromMap(gojaObject(t, `{id: 2319714, label: 'Car 12', order: 3}`))

		if unit.Id != 2319714 || unit.Order != 3 {
			t.Fatalf("id=%d order=%d", unit.Id, unit.Order)
		}
	})

	t.Run("dirwatch", func(t *testing.T) {
		dirwatch := NewDirwatch().FromMap(gojaObject(t,
			`{_id: 1, delay: 2, frequency: 851000000, order: 3, systemId: 4, talkgroupId: 5}`))

		if dirwatch.Id != uint(1) || dirwatch.SystemId == nil || dirwatch.TalkgroupId == nil {
			t.Fatalf("_id=%v systemId=%v talkgroupId=%v", dirwatch.Id, dirwatch.SystemId, dirwatch.TalkgroupId)
		}
	})
}

// The admin panel's float64s must keep working — this is not a swap, it is a
// widening.
func TestModelsStillAcceptNumbersFromJson(t *testing.T) {
	system := NewSystem().FromMap(map[string]any{
		"_id": float64(7), "id": float64(3), "order": float64(2), "delay": float64(5),
	})

	if system.RowId != uint(7) || system.Id != 3 || system.Order != 2 || system.Delay != 5 {
		t.Fatalf("rowId=%v id=%d order=%d delay=%d", system.RowId, system.Id, system.Order, system.Delay)
	}
}

// rdio.calls.search is the case with no error and a plausible-looking result:
// every numeric filter was ignored, so a plugin asking for one talkgroup's last
// 500 calls silently got the newest 200 from the whole server.
func TestCallsSearchOptionsAcceptNumbersFromJavaScript(t *testing.T) {
	options := CallsSearchOptions{}
	options.fromMap(gojaObject(t, `{system: 3, talkgroup: 10329, limit: 500, offset: 100, sort: -1}`))

	// Every field is `any`, so read them the way the query builder does.
	if v, ok := jsonUint(options.System); !ok || v != 3 {
		t.Errorf("system filter was %#v, so the search would span every system", options.System)
	}
	if v, ok := jsonUint(options.Talkgroup); !ok || v != 10329 {
		t.Errorf("talkgroup filter was %#v, so the search would span every talkgroup", options.Talkgroup)
	}
	if v, ok := jsonUint(options.Limit); !ok || v != 500 {
		t.Errorf("limit was %#v, so the caller would silently get the default page", options.Limit)
	}
	if v, ok := jsonUint(options.Offset); !ok || v != 100 {
		t.Errorf("offset was %#v", options.Offset)
	}
	// Sort is stored as `any` and type-switched later, so it has to arrive as a
	// float64 whichever side it came from.
	if sort, ok := options.Sort.(float64); !ok || sort != -1 {
		t.Errorf("sort came through as %#v, and the reader only understands float64", options.Sort)
	}
}

// A grant handed back by an auth plugin carries int64 ids. Comparing them
// against a call decides who hears what, so a silent mismatch is a listener
// admitted to an empty server.
func TestAccessGrantFromJavaScriptMatchesACall(t *testing.T) {
	call := &Call{System: 3, Talkgroup: 10329}

	access := &Access{}
	access.Systems = gojaValue(t, `[{ id: 3, talkgroups: [10329, 10325] }]`)

	if !access.HasAccess(call) {
		t.Fatal("a grant written in javascript did not match the call it names")
	}

	other := &Call{System: 3, Talkgroup: 9999}
	if access.HasAccess(other) {
		t.Fatal("a grant matched a talkgroup it does not name")
	}
}

func gojaValue(t *testing.T, src string) any {
	t.Helper()

	vm := goja.New()
	value, err := vm.RunString("(" + src + ")")
	if err != nil {
		t.Fatalf("cannot evaluate %s: %v", src, err)
	}

	return value.Export()
}

func TestJsonUintRefusesNegativesRatherThanWrapping(t *testing.T) {
	if _, ok := jsonUint(int64(-1)); ok {
		t.Error("a negative id was accepted and would wrap to an enormous number")
	}
	if _, ok := jsonUint("12"); ok {
		t.Error("a string was accepted as a number")
	}
	if v, ok := jsonUint(int64(12)); !ok || v != 12 {
		t.Errorf("int64 12 came through as %d, %v", v, ok)
	}
	if v, ok := jsonUint(float64(12)); !ok || v != 12 {
		t.Errorf("float64 12 came through as %d, %v", v, ok)
	}
}
