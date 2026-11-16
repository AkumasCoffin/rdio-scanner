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

// Retention is counted from now, and now is the same instant in every
// timezone. The cutoff used to be the host's wall clock written out with no
// zone and compared against UTC rows, so a server east of Greenwich pruned
// early by its offset — eleven hours early in Sydney in summer — and one west
// of it kept everything that much longer.
//
// The host's clock is handed in rather than set by changing time.Local: that
// is a process-wide variable other goroutines read, and writing it from a test
// is a data race with every one of them.
func hostClockIn(name string, offsetHours int) time.Time {
	return time.Now().In(time.FixedZone(name, offsetHours*3600))
}

func TestPruningCallsCountsFromNowWhateverTheHostTimezone(t *testing.T) {
	for _, zone := range []struct {
		name   string
		offset int
	}{
		{"east of UTC", 11},
		{"west of UTC", -8},
	} {
		t.Run(zone.name, func(t *testing.T) {
			db := newTestDatabase(t)
			defer db.Sql.Close()

			now := time.Now().UTC().Truncate(time.Second)

			// One day of retention: twenty hours old stays, thirty goes.
			kept := insertSearchTestCall(t, db, now.Add(-20*time.Hour), 1, 100)
			gone := insertSearchTestCall(t, db, now.Add(-30*time.Hour), 1, 100)

			if err := NewCalls().pruneAsOf(db, 1, hostClockIn(zone.name, zone.offset)); err != nil {
				t.Fatal(err)
			}

			exists := func(id uint) bool {
				var n int
				if err := db.QueryRow("select count(*) from `rdioScannerCalls` where `id` = ?", id).Scan(&n); err != nil {
					t.Fatal(err)
				}
				return n == 1
			}

			if !exists(kept) {
				t.Error("a call twenty hours old was pruned with one day of retention")
			}
			if exists(gone) {
				t.Error("a call thirty hours old survived one day of retention")
			}
		})
	}
}

func TestPruningLogsCountsFromNowWhateverTheHostTimezone(t *testing.T) {
	for _, zone := range []struct {
		name   string
		offset int
	}{
		{"east of UTC", 11},
		{"west of UTC", -8},
	} {
		t.Run(zone.name, func(t *testing.T) {
			db := newTestDatabase(t)
			defer db.Sql.Close()

			now := time.Now().UTC().Truncate(time.Second)

			for _, entry := range []struct {
				at      time.Time
				message string
			}{
				{now.Add(-20 * time.Hour), "kept"},
				{now.Add(-30 * time.Hour), "gone"},
			} {
				if _, err := db.Exec("insert into `rdioScannerLogs` (`dateTime`, `level`, `message`) values (?, ?, ?)", entry.at, LogLevelInfo, entry.message); err != nil {
					t.Fatal(err)
				}
			}

			if err := (&Logs{}).pruneAsOf(db, 1, hostClockIn(zone.name, zone.offset)); err != nil {
				t.Fatal(err)
			}

			count := func(message string) int {
				var n int
				if err := db.QueryRow("select count(*) from `rdioScannerLogs` where `message` = ?", message).Scan(&n); err != nil {
					t.Fatal(err)
				}
				return n
			}

			if count("kept") != 1 {
				t.Error("a log line twenty hours old was pruned with one day of retention")
			}
			if count("gone") != 0 {
				t.Error("a log line thirty hours old survived one day of retention")
			}
		})
	}
}
