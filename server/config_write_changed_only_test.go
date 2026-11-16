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

// A call that teaches the server one new unit must cost one row, not a rewrite
// of every unit and talkgroup there is. That rewrite ran on the goroutine that
// ingests calls — two seconds of it on Postgres for a system with a few
// thousand of each — so every other call waited each time a radio nobody had
// seen before keyed up.
//
// Whether a row was rewritten is observed directly: each untouched row is
// changed behind the server's back first, and a rewrite would put it back.
func TestWritingTheSystemsOnlyWritesWhatChanged(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	systems := NewSystems()
	systems.FromMap([]any{map[string]any{
		"id": float64(1), "label": "County",
		"talkgroups": []any{
			map[string]any{"id": float64(100), "label": "Dispatch", "name": "Dispatch"},
			map[string]any{"id": float64(101), "label": "Tac 1", "name": "Tac 1"},
			map[string]any{"id": float64(102), "label": "Tac 2", "name": "Tac 2"},
		},
		"units": []any{
			map[string]any{"id": float64(7), "label": "Engine 7"},
			map[string]any{"id": float64(8), "label": "Engine 8"},
			map[string]any{"id": float64(9), "label": "Engine 9"},
		},
	}})
	if err := systems.Write(db); err != nil {
		t.Fatal(err)
	}
	if err := systems.Read(db); err != nil {
		t.Fatal(err)
	}

	system := systems.List[0]

	label := func(table string, id uint) string {
		var s string
		if err := db.QueryRow("select `label` from `"+table+"` where `id` = ? and `systemId` = ?", id, system.RowId).Scan(&s); err != nil {
			return "(missing: " + err.Error() + ")"
		}
		return s
	}

	mark := func(table string, id uint) {
		if _, err := db.Exec("update `"+table+"` set `label` = ? where `id` = ? and `systemId` = ?", "left alone", id, system.RowId); err != nil {
			t.Fatal(err)
		}
	}

	// Rows nothing is about to change, marked so a rewrite would show.
	mark("rdioScannerUnits", 7)
	mark("rdioScannerTalkgroups", 100)

	// What a call does: one unit nobody has seen, one unit relabelled, one
	// talkgroup renamed.
	system.Units.Add(10, "Medic 10")
	for _, unit := range system.Units.List {
		if unit.Id == 8 {
			unit.Label = "Engine 8 (reserve)"
		}
	}
	for _, talkgroup := range system.Talkgroups.List {
		if talkgroup.Id == 101 {
			talkgroup.Label = "Tac One"
		}
	}

	// And one row removed from the table behind the server's back, which it
	// still holds in memory and has to put back.
	if _, err := db.Exec("delete from `rdioScannerUnits` where `id` = ? and `systemId` = ?", 9, system.RowId); err != nil {
		t.Fatal(err)
	}

	if err := systems.Write(db); err != nil {
		t.Fatal(err)
	}

	for _, check := range []struct {
		table string
		id    uint
		want  string
		why   string
	}{
		{"rdioScannerUnits", 7, "left alone", "an unchanged unit was rewritten"},
		{"rdioScannerTalkgroups", 100, "left alone", "an unchanged talkgroup was rewritten"},
		{"rdioScannerUnits", 10, "Medic 10", "the new unit was not stored"},
		{"rdioScannerUnits", 8, "Engine 8 (reserve)", "the relabelled unit was not updated"},
		{"rdioScannerTalkgroups", 101, "Tac One", "the renamed talkgroup was not updated"},
		{"rdioScannerUnits", 9, "Engine 9", "a unit missing from the table was not put back"},
		{"rdioScannerTalkgroups", 102, "Tac 2", "an untouched talkgroup was lost"},
	} {
		if got := label(check.table, check.id); got != check.want {
			t.Errorf("%s: %s %d is %q, want %q", check.why, check.table, check.id, got, check.want)
		}
	}

	// Removing one from the list still removes its row.
	system.Units.List = system.Units.List[:1]
	if err := systems.Write(db); err != nil {
		t.Fatal(err)
	}

	var units int
	if err := db.QueryRow("select count(*) from `rdioScannerUnits` where `systemId` = ?", system.RowId).Scan(&units); err != nil {
		t.Fatal(err)
	}
	if units != 1 {
		t.Fatalf("%d units left after the list was cut to one", units)
	}
}

// A save from the admin form carries no memory of what was stored, and writes
// everything it was given — including a value changed back to what the table
// happens to hold.
func TestAConfigSaveStillWritesEveryRowItWasGiven(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	form := func(label string) []any {
		return []any{map[string]any{
			"id": float64(1), "label": "County",
			"talkgroups": []any{map[string]any{"id": float64(100), "label": label, "name": label}},
			"units":      []any{map[string]any{"id": float64(7), "label": label}},
		}}
	}

	systems := NewSystems()
	systems.FromMap(form("first"))
	if err := systems.Write(db); err != nil {
		t.Fatal(err)
	}
	if err := systems.Read(db); err != nil {
		t.Fatal(err)
	}

	rowId := systems.List[0].RowId

	saved := form("second")
	saved[0].(map[string]any)["_id"] = float64(rowId.(uint))

	systems.FromMap(saved)
	if err := systems.Write(db); err != nil {
		t.Fatal(err)
	}

	for _, table := range []string{"rdioScannerUnits", "rdioScannerTalkgroups"} {
		var got string
		if err := db.QueryRow("select `label` from `" + table + "` where `systemId` = ?", rowId).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != "second" {
			t.Errorf("%s holds %q after a save that changed it to \"second\"", table, got)
		}
	}
}
