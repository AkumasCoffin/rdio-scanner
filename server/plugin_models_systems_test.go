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

// A plugin that adds or changes one system through rdio.models must leave the
// rest of the configuration as it found it. The systems were encoded with
// their talkgroups as an object instead of a list, decoded back with none, and
// written — so one plugin adding one system deleted every talkgroup and unit
// on the server.
func TestAPluginEditingOneSystemKeepsEveryTalkgroupAndUnit(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	controller := &Controller{Database: db, Systems: NewSystems(), Logs: &Logs{}}
	controller.Systems.FromMap([]any{map[string]any{
		"id": float64(1), "label": "County",
		"talkgroups": []any{
			map[string]any{"id": float64(100), "label": "Dispatch", "name": "Dispatch"},
			map[string]any{"id": float64(101), "label": "Tac 1", "name": "Tac 1"},
		},
		"units": []any{map[string]any{"id": float64(7), "label": "Engine 7"}},
	}})
	if err := controller.Systems.Write(db); err != nil {
		t.Fatal(err)
	}
	if err := controller.Systems.Read(db); err != nil {
		t.Fatal(err)
	}

	count := func(table string) int {
		var n int
		if err := db.QueryRow("select count(*) from `" + table + "`").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	list, err := controller.modelList("systems")
	if err != nil {
		t.Fatal(err)
	}
	entry := list[0].(map[string]any)

	talkgroups, isList := entry["talkgroups"].([]any)
	if !isList || len(talkgroups) != 2 {
		t.Fatalf("a plugin sees the system's talkgroups as %#v, want a list of two", entry["talkgroups"])
	}
	if units, isList := entry["units"].([]any); !isList || len(units) != 1 {
		t.Fatalf("a plugin sees the system's units as %#v, want a list of one", entry["units"])
	}

	if err := controller.modelSet("systems", map[string]any{"id": float64(99), "label": "New"}); err != nil {
		t.Fatalf("adding a system through the model failed: %v", err)
	}

	if got := count("rdioScannerSystems"); got != 2 {
		t.Fatalf("%d systems after adding one to one", got)
	}
	if got := count("rdioScannerTalkgroups"); got != 2 {
		t.Fatalf("adding a system left %d talkgroups, want the 2 that were there", got)
	}
	if got := count("rdioScannerUnits"); got != 1 {
		t.Fatalf("adding a system left %d units, want the 1 that was there", got)
	}

	// And in memory, after the re-read.
	county, ok := controller.Systems.GetSystem(uint(1))
	if !ok || len(county.Talkgroups.List) != 2 || len(county.Units.List) != 1 {
		t.Fatal("the existing system lost its talkgroups or units in memory")
	}
}
