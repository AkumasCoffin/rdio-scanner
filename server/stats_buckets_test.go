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

// insertTestCall writes a minimal call row stamped at the given time, in the
// backend's own datetime format — the same shape the ingest path writes.
func insertTestCall(t *testing.T, db *Database, when time.Time) {
	t.Helper()

	if _, err := db.Sql.Exec(db.formatQuery(
		"insert into `rdioScannerCalls` (`dateTime`, `system`, `talkgroup`, `source`, `audio`,"+
			" `audioName`, `audioType`, `frequencies`, `frequency`, `patches`, `sources`)"+
			" values (?, 1, 1, 1, ?, 'test.m4a', 'audio/mp4', '[]', 154000000, '[]', '[]')"),
		when.Format(db.DateTimeFormat), []byte{0},
	); err != nil {
		t.Fatal(err)
	}
}

// A call made moments ago lands in the hour that is still in progress. The
// bucket list used to stop at the previous hour, so the newest calls were
// tallied and then thrown away — every "recent" rollup on the dashboard
// (the 1H card most visibly) read zero no matter how busy the system was.
func TestHourBucketsIncludeTheHourInProgress(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	now := time.Now().UTC()
	insertTestCall(t, db, now)

	stats := &Stats{}
	buckets, err := stats.GetHourBuckets(db)
	if err != nil {
		t.Fatal(err)
	}

	currentHour := now.Truncate(time.Hour).Format(time.RFC3339)

	var found bool
	for _, b := range buckets {
		if b.StartUtc == currentHour {
			found = true
			if b.Count != 1 {
				t.Errorf("current hour bucket has count %d, want 1", b.Count)
			}
		}
	}

	if !found {
		t.Errorf("no bucket for the current hour %s — the newest calls are invisible", currentHour)
	}
}

// On server backends, bucketing must happen in SQL: falling back means
// shipping every call in the window over the network. The fallback also
// swallows errors, so a grouped query the backend rejects — as MySQL did
// when the expression carried a doubled modulo — disables the optimization
// without failing a single test. This pins the grouped path itself.
//
// Runs only when RDIO_TEST_DB_TYPE points at a real server; SQLite prefers
// the row scan by design.
func TestBucketCountsGroupInSqlOnServerBackends(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	if db.Config.DbType == DbTypeSqlite {
		t.Skip("SQLite intentionally buckets in Go")
	}

	now := time.Now().UTC()
	insertTestCall(t, db, now)
	insertTestCall(t, db, now.Add(-90*time.Minute))

	for _, minutes := range []int{5, 10, 60} {
		tally, ok := statsBucketCounts(db, now.Add(-6*time.Hour), minutes)
		if !ok {
			t.Fatalf("%d-minute grouped query fell back on %s — the backend rejected the SQL",
				minutes, db.Config.DbType)
		}

		var total uint
		for _, n := range tally {
			total += n
		}
		if total != 2 {
			t.Errorf("%d-minute buckets tally %d calls, want 2", minutes, total)
		}
	}
}
