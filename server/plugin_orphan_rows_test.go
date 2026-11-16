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
	"testing"
	"time"
)

// Retention removes calls. What a plugin stored against those calls has to go
// with them, or the transcripts table keeps every transcript ever made.
func TestPruningCallsTakesTheirPluginRowsToo(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	extensions := transcriptExtensionFor(t, db)

	now := time.Now().UTC().Truncate(time.Second)

	// Six old calls and four recent ones, each with a transcript.
	ids := []uint{}
	for i := 0; i < 10; i++ {
		age := 30 * time.Hour
		if i >= 6 {
			age = time.Hour
		}

		id := insertSearchTestCall(t, db, now.Add(-age).Add(time.Duration(i)*time.Second), 1, 100)
		ids = append(ids, id)

		if _, err := db.Exec("insert into `plugin_transcripts_calls` (`callId`, `transcript`) values (?, ?)", id, "text"); err != nil {
			t.Fatal(err)
		}
	}

	transcripts := func() int {
		var n int
		if err := db.QueryRow("select count(*) from `plugin_transcripts_calls`").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Before any call is pruned there is nothing stale.
	if removed, err := pruneCallExtensionRows(db, extensions); err != nil || removed != 0 {
		t.Fatalf("removed %d rows (err %v) while every call still existed", removed, err)
	}

	if err := NewCalls().Prune(db, 1); err != nil {
		t.Fatal(err)
	}

	removed, err := pruneCallExtensionRows(db, extensions)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 6 || transcripts() != 4 {
		t.Fatalf("removed %d rows leaving %d, want 6 removed and 4 left", removed, transcripts())
	}

	// The survivors are the ones whose calls survived.
	var orphans int
	if err := db.QueryRow("select count(*) from `plugin_transcripts_calls` where `callId` not in (select `id` from `rdioScannerCalls`)").Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	if orphans != 0 {
		t.Fatalf("%d transcripts still belong to calls that no longer exist", orphans)
	}

	// The same table registered twice — two fields of one plugin — is swept once.
	if removed, err := pruneCallExtensionRows(db, append(extensions, extensions...)); err != nil || removed != 0 {
		t.Fatalf("a second pass removed %d rows (err %v)", removed, err)
	}
}

// With no calls left there is no telling a new database from an emptied one,
// so nothing is assumed stale.
func TestPluginRowsAreLeftAloneWhenThereAreNoCalls(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	extensions := transcriptExtensionFor(t, db)

	if _, err := db.Exec("insert into `plugin_transcripts_calls` (`callId`, `transcript`) values (?, ?)", 5, "text"); err != nil {
		t.Fatal(err)
	}

	if removed, err := pruneCallExtensionRows(db, extensions); err != nil || removed != 0 {
		t.Fatalf("removed %d rows (err %v) from a database with no calls", removed, err)
	}
}

// Leftovers from months of pruning are cleared a slice at a time, and a table
// with more than one cycle's worth is finished by the cycles after.
func TestPluginRowsAreRemovedInBoundedSlices(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	extensions := transcriptExtensionFor(t, db)

	// One surviving call, far up the id range.
	survivor := insertSearchTestCall(t, db, time.Now().UTC(), 1, 100)
	if _, err := db.Exec("update `rdioScannerCalls` set `id` = ? where `id` = ?", 5000000, survivor); err != nil {
		t.Fatal(err)
	}

	// Leftovers spread over more id range than one cycle covers.
	span := int64(pluginOrphanSlice) * int64(pluginOrphanMaxSlices)
	for _, id := range []int64{1, span / 2, span + 10, span * 2, 4999999, 5000000} {
		if _, err := db.Exec("insert into `plugin_transcripts_calls` (`callId`, `transcript`) values (?, ?)", id, "text"); err != nil {
			t.Fatal(err)
		}
	}

	total := int64(0)
	for cycle := 0; cycle < 200; cycle++ {
		removed, err := pruneCallExtensionRows(db, extensions)
		if err != nil {
			t.Fatal(err)
		}
		if removed == 0 {
			break
		}
		total += removed
	}

	var left int
	if err := db.QueryRow("select count(*) from `plugin_transcripts_calls`").Scan(&left); err != nil {
		t.Fatal(err)
	}
	if total != 5 || left != 1 {
		t.Fatalf("removed %d and left %d, want every leftover gone and the surviving call's row kept", total, left)
	}
}
