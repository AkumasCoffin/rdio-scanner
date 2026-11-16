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
	"sort"
	"strings"
	"testing"
)

// Each of these is one admin save, the way the webapp sends it: the rows that
// are staying carry their `_id`, a row just added has none, and a row that was
// deleted is simply absent.
//
// The save used to keep every existing row whenever the list held a new one.
// Delete an access code and add its replacement in one go, and the save
// answered 200 with the old code still in the database and still working.

func sortedJoin(values []string) string {
	sort.Strings(values)
	return strings.Join(values, ",")
}

func TestSavingAccessesDeletesWhatWasRemovedWhileAdding(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	accesses := NewAccesses()
	accesses.FromMap([]any{
		map[string]any{"code": "alice", "ident": "Alice", "systems": "*"},
		map[string]any{"code": "bob", "ident": "Bob", "systems": "*"},
	})
	if err := accesses.Write(db); err != nil {
		t.Fatal(err)
	}
	if err := accesses.Read(db); err != nil {
		t.Fatal(err)
	}

	var aliceId any
	for _, access := range accesses.List {
		if access.Code == "alice" {
			aliceId = access.Id
		}
	}
	if aliceId == nil {
		t.Fatal("the seeded access has no id")
	}

	// Bob is deleted, Carol is added.
	accesses.FromMap([]any{
		map[string]any{"_id": float64(aliceId.(uint)), "code": "alice", "ident": "Alice", "systems": "*"},
		map[string]any{"code": "carol", "ident": "Carol", "systems": "*"},
	})
	if err := accesses.Write(db); err != nil {
		t.Fatal(err)
	}
	if err := accesses.Read(db); err != nil {
		t.Fatal(err)
	}

	codes := []string{}
	for _, access := range accesses.List {
		codes = append(codes, access.Code)
	}

	if got := sortedJoin(codes); got != "alice,carol" {
		t.Fatalf("after deleting bob and adding carol the access codes are %q", got)
	}
	if _, ok := accesses.GetAccess("bob"); ok {
		t.Fatal("a deleted access code still authenticates")
	}
}

func TestSavingApikeysDeletesWhatWasRemovedWhileAdding(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	apikeys := NewApikeys()
	apikeys.FromMap([]any{
		map[string]any{"key": "keep-me", "ident": "recorder", "systems": "*"},
		map[string]any{"key": "leaked", "ident": "old", "systems": "*"},
	})
	if err := apikeys.Write(db); err != nil {
		t.Fatal(err)
	}
	if err := apikeys.Read(db); err != nil {
		t.Fatal(err)
	}

	var keepId any
	for _, apikey := range apikeys.List {
		if apikey.Key == "keep-me" {
			keepId = apikey.Id
		}
	}

	apikeys.FromMap([]any{
		map[string]any{"_id": float64(keepId.(uint)), "key": "keep-me", "ident": "recorder", "systems": "*"},
		map[string]any{"key": "replacement", "ident": "new", "systems": "*"},
	})
	if err := apikeys.Write(db); err != nil {
		t.Fatal(err)
	}
	if err := apikeys.Read(db); err != nil {
		t.Fatal(err)
	}

	keys := []string{}
	for _, apikey := range apikeys.List {
		keys = append(keys, apikey.Key)
	}

	if got := sortedJoin(keys); got != "keep-me,replacement" {
		t.Fatalf("after revoking one key and adding another the keys are %q", got)
	}
}

func TestSavingDownstreamsDeletesWhatWasRemovedWhileAdding(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	downstreams := NewDownstreams()
	downstreams.FromMap([]any{
		map[string]any{"url": "https://one.example", "apiKey": "a", "systems": "*"},
		map[string]any{"url": "https://two.example", "apiKey": "b", "systems": "*"},
	})
	if err := downstreams.Write(db); err != nil {
		t.Fatal(err)
	}
	if err := downstreams.Read(db); err != nil {
		t.Fatal(err)
	}

	var oneId any
	for _, downstream := range downstreams.List {
		if downstream.Url == "https://one.example" {
			oneId = downstream.Id
		}
	}

	downstreams.FromMap([]any{
		map[string]any{"_id": float64(oneId.(uint)), "url": "https://one.example", "apiKey": "a", "systems": "*"},
		map[string]any{"url": "https://three.example", "apiKey": "c", "systems": "*"},
	})
	if err := downstreams.Write(db); err != nil {
		t.Fatal(err)
	}
	if err := downstreams.Read(db); err != nil {
		t.Fatal(err)
	}

	urls := []string{}
	for _, downstream := range downstreams.List {
		urls = append(urls, downstream.Url)
	}

	if got := sortedJoin(urls); got != "https://one.example,https://three.example" {
		t.Fatalf("after removing one downstream and adding another the list is %q", got)
	}
}

// Adding alone must still only add.
func TestSavingAccessesKeepsEverythingStillListed(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	accesses := NewAccesses()
	accesses.FromMap([]any{
		map[string]any{"code": "alice", "ident": "Alice", "systems": "*"},
		map[string]any{"code": "bob", "ident": "Bob", "systems": "*"},
	})
	if err := accesses.Write(db); err != nil {
		t.Fatal(err)
	}
	if err := accesses.Read(db); err != nil {
		t.Fatal(err)
	}

	list := []any{}
	for _, access := range accesses.List {
		list = append(list, map[string]any{"_id": float64(access.Id.(uint)), "code": access.Code, "ident": access.Ident, "systems": "*"})
	}
	list = append(list, map[string]any{"code": "carol", "ident": "Carol", "systems": "*"})

	accesses.FromMap(list)
	if err := accesses.Write(db); err != nil {
		t.Fatal(err)
	}
	if err := accesses.Read(db); err != nil {
		t.Fatal(err)
	}

	codes := []string{}
	for _, access := range accesses.List {
		codes = append(codes, access.Code)
	}

	if got := sortedJoin(codes); got != "alice,bob,carol" {
		t.Fatalf("adding one access changed the others: %q", got)
	}
}
