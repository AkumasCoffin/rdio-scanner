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
	"encoding/json"
	"testing"
	"time"
)

// A call's sources and frequencies arrive as []map[string]any from an upload
// and as []any from a plugin or an ingest filter. Both have to be stored.
func TestACallsSourcesAreStoredWhateverBuiltTheList(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	calls := NewCalls()

	for name, build := range map[string]func() (any, any){
		"from an upload": func() (any, any) {
			return []map[string]any{{"pos": 0, "src": 1234}}, []map[string]any{{"freq": 851000000, "pos": 0}}
		},
		"from a plugin": func() (any, any) {
			return []any{map[string]any{"pos": 0.0, "src": 1234.0}}, []any{map[string]any{"freq": 851000000.0, "pos": 0.0}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			sources, frequencies := build()

			call := NewCall()
			call.Audio = []byte{1, 2, 3}
			call.AudioName = "a.m4a"
			call.AudioType = "audio/mp4"
			call.DateTime = time.Now().UTC()
			call.System = 1
			call.Talkgroup = 100
			call.Sources = sources
			call.Frequencies = frequencies

			id, err := calls.WriteCall(call, db)
			if err != nil {
				t.Fatal(err)
			}

			stored, err := calls.GetCall(id, db)
			if err != nil {
				t.Fatal(err)
			}

			for field, value := range map[string]any{"sources": stored.Sources, "frequencies": stored.Frequencies} {
				b, _ := json.Marshal(value)

				var list []map[string]any
				if err := json.Unmarshal(b, &list); err != nil || len(list) != 1 {
					t.Errorf("the stored call's %s are %s, want the one entry it was given", field, b)
				}
			}
		})
	}
}

// "No unit" is sent as -1 by some recorders. It is not a unit, and it must not
// cost the call.
func TestANegativeSourceIsNotAUnitAndDoesNotLoseTheCall(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	call := NewCall()
	call.Audio = []byte{1, 2, 3}
	call.AudioName = "a.m4a"
	call.AudioType = "audio/mp4"
	call.DateTime = time.Now().UTC()
	call.System = 1
	call.Talkgroup = 100

	part, body := partNamed(t, "source", "-1")
	ParseMultipartContent(call, part, body)

	if id, err := NewCalls().WriteCall(call, db); err != nil || id == 0 {
		t.Fatalf("a call uploaded with source=-1 could not be stored: %v", err)
	}
}

// The duplicate window is "this long either side". As text with whole seconds
// it was "up to a second before, and nothing after".
func TestTheDuplicateWindowIsAsWideAsItSays(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	calls := NewCalls()
	base := time.Date(2026, 3, 1, 4, 0, 0, 0, time.UTC)

	insertSearchTestCall(t, db, base.Add(100*time.Millisecond), 1, 100)

	later := &Call{DateTime: base.Add(1400 * time.Millisecond), System: 1, Talkgroup: 100}
	if id, found := calls.GetDuplicateId(later, 500, db); found {
		t.Errorf("a call 1.3 seconds after another was taken for a duplicate of call %d with a half-second window", id)
	}

	// Whole-second columns cannot hold what the next check needs.
	if db.Config.DbType == DbTypeMysql || db.Config.DbType == DbTypeMariadb {
		return
	}

	insertSearchTestCall(t, db, base.Add(10*time.Second+600*time.Millisecond), 1, 100)

	sooner := &Call{DateTime: base.Add(10*time.Second + 400*time.Millisecond), System: 1, Talkgroup: 100}
	if _, found := calls.GetDuplicateId(sooner, 500, db); !found {
		t.Error("a call 200 milliseconds before another was not recognised as its duplicate")
	}

	other := &Call{DateTime: base.Add(10*time.Second + 400*time.Millisecond), System: 1, Talkgroup: 101}
	if _, found := calls.GetDuplicateId(other, 500, db); found {
		t.Error("a call on another talkgroup was taken for a duplicate")
	}
}

// A transcript pushed from upstream is matched to its call by system,
// talkgroup and time. The call a second earlier is a different call.
func TestFindingACallByItsTimeDoesNotReturnItsNeighbour(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	calls := NewCalls()
	base := time.Date(2026, 3, 1, 4, 0, 0, 0, time.UTC)

	neighbour := insertSearchTestCall(t, db, base, 1, 100)
	wanted := insertSearchTestCall(t, db, base.Add(time.Second), 1, 100)

	got, err := calls.GetIdByKey(1, 100, base.Add(time.Second), db)
	if err != nil {
		t.Fatal(err)
	}
	if got != wanted {
		t.Fatalf("looking up the call at :01 returned call %d, want %d (the call at :00 is %d)", got, wanted, neighbour)
	}

	if got, _ := calls.GetIdByKey(1, 100, base.Add(5*time.Second), db); got != 0 {
		t.Fatalf("looking up a time with no call returned call %d", got)
	}
}
