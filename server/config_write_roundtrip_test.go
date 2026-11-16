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

import "testing"

// A scope limited to particular systems, as the admin form sends it.
func scopedToSystemOne() []any {
	return []any{map[string]any{"id": float64(1), "talkgroups": "*"}}
}

// Not every write comes straight from the admin form. The user-add and
// user-remove commands, and a plugin editing accesses, change the list that
// was read from the database and write it back — and what was read is a list,
// which the writer bound as it stood. No driver accepts that, so all of those
// failed as soon as one entry on the server was scoped.
func TestAScopedEntrySurvivesBeingWrittenBackAfterARead(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	t.Run("accesses", func(t *testing.T) {
		accesses := NewAccesses()
		accesses.FromMap([]any{map[string]any{"code": "alice", "ident": "Alice", "systems": scopedToSystemOne()}})
		if err := accesses.Write(db); err != nil {
			t.Fatal(err)
		}
		if err := accesses.Read(db); err != nil {
			t.Fatal(err)
		}

		// What user-add does.
		accesses.Add(NewAccess().FromMap(map[string]any{"code": "bob", "ident": "Bob", "systems": "*"}))
		if err := accesses.Write(db); err != nil {
			t.Fatalf("adding a user on a server with a scoped access failed: %v", err)
		}
		if err := accesses.Read(db); err != nil {
			t.Fatal(err)
		}

		alice, ok := accesses.GetAccess("alice")
		if !ok {
			t.Fatal("the scoped access is gone")
		}
		if !alice.HasAccess(&Call{System: 1, Talkgroup: 5}) || alice.HasAccess(&Call{System: 2, Talkgroup: 5}) {
			t.Fatalf("the scope did not survive the round trip: %#v", alice.Systems)
		}
		if bob, ok := accesses.GetAccess("bob"); !ok || !bob.HasAccess(&Call{System: 2, Talkgroup: 5}) {
			t.Fatal("the added access was not stored with access to everything")
		}
	})

	t.Run("api keys", func(t *testing.T) {
		apikeys := NewApikeys()
		apikeys.FromMap([]any{map[string]any{"key": "k", "ident": "recorder", "systems": scopedToSystemOne()}})
		if err := apikeys.Write(db); err != nil {
			t.Fatal(err)
		}
		if err := apikeys.Read(db); err != nil {
			t.Fatal(err)
		}
		if err := apikeys.Write(db); err != nil {
			t.Fatalf("writing API keys back after a read failed: %v", err)
		}
		if err := apikeys.Read(db); err != nil {
			t.Fatal(err)
		}

		key, ok := apikeys.GetApikey("k")
		if !ok || !key.HasAccess(&Call{System: 1, Talkgroup: 5}) || key.HasAccess(&Call{System: 2, Talkgroup: 5}) {
			t.Fatalf("the key's scope did not survive the round trip: %#v", key)
		}
	})

	t.Run("downstreams", func(t *testing.T) {
		downstreams := NewDownstreams()
		downstreams.FromMap([]any{map[string]any{"url": "https://one.example", "apiKey": "a", "systems": scopedToSystemOne()}})
		if err := downstreams.Write(db); err != nil {
			t.Fatal(err)
		}
		if err := downstreams.Read(db); err != nil {
			t.Fatal(err)
		}
		if err := downstreams.Write(db); err != nil {
			t.Fatalf("writing downstreams back after a read failed: %v", err)
		}
		if err := downstreams.Read(db); err != nil {
			t.Fatal(err)
		}
		if len(downstreams.List) != 1 {
			t.Fatalf("%d downstreams after the round trip", len(downstreams.List))
		}
		if _, isList := downstreams.List[0].Systems.([]any); !isList {
			t.Fatalf("the downstream's scope came back as %#v", downstreams.List[0].Systems)
		}
	})
}

// A save replaces the in-memory list first and re-reads the table last. In
// between, a scoped key has to go on working: it was held as JSON text until
// the re-read, which nothing matches, so a recorder uploading during a config
// save was refused and dropped the call.
func TestAScopedKeyWorksBetweenASaveAndItsReread(t *testing.T) {
	apikeys := NewApikeys()
	apikeys.FromMap([]any{map[string]any{"key": "k", "ident": "recorder", "systems": scopedToSystemOne()}})

	key, ok := apikeys.GetApikey("k")
	if !ok {
		t.Fatal("the key is not in the list")
	}
	if !key.HasAccess(&Call{System: 1, Talkgroup: 5}) {
		t.Fatal("a scoped key is refused for its own system until the save re-reads the table")
	}
	if key.HasAccess(&Call{System: 2, Talkgroup: 5}) {
		t.Fatal("a scoped key is accepted for a system outside its scope")
	}

	accesses := NewAccesses()
	accesses.FromMap([]any{map[string]any{"code": "c", "ident": "x", "systems": scopedToSystemOne()}})
	if access, ok := accesses.GetAccess("c"); !ok || !access.HasAccess(&Call{System: 1, Talkgroup: 5}) {
		t.Fatal("a scoped access code is refused for its own system until the save re-reads the table")
	}
}
