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

// The Logs tab takes a date and time and a sort. Ascending shows the day that
// starts there; descending — the default — is supposed to show the day that
// ends there. The descending arithmetic subtracted the hours and minutes
// twice, so it showed a slice of a different day: pick 23:00 and it returned
// one hour, starting the midnight before the midnight before.
func TestLogsDateFilterCoversTheDayAroundThePickedTime(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	// One line an hour for four days, each named for its own time.
	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 96; i++ {
		at := base.Add(time.Duration(i) * time.Hour)
		if _, err := db.Exec("insert into `rdioScannerLogs` (`dateTime`, `level`, `message`) values (?, ?, ?)", at, LogLevelInfo, at.Format("02T15")); err != nil {
			t.Fatal(err)
		}
	}

	picked := time.Date(2026, 3, 3, 17, 30, 0, 0, time.UTC)

	search := func(sort int) []string {
		options := NewLogSearchOptions()
		options.Date = picked
		options.Sort = sort
		options.Limit = uint(500)

		results, err := (&Logs{}).Search(options, db)
		if err != nil {
			t.Fatal(err)
		}

		out := []string{}
		for _, entry := range results.Logs {
			out = append(out, entry.Message)
		}
		return out
	}

	t.Run("newest first shows the 24 hours up to the picked time", func(t *testing.T) {
		got := search(-1)

		// 17:30 on the 3rd back to 17:30 on the 2nd: hourly lines 02T18 … 03T17.
		if len(got) != 24 {
			t.Fatalf("returned %d lines, want 24: %v", len(got), got)
		}
		if got[0] != "03T17" || got[len(got)-1] != "02T18" {
			t.Fatalf("returned %s … %s, want 03T17 … 02T18", got[0], got[len(got)-1])
		}
	})

	t.Run("oldest first shows the 24 hours from the picked time", func(t *testing.T) {
		got := search(1)

		if len(got) != 24 {
			t.Fatalf("returned %d lines, want 24: %v", len(got), got)
		}
		if got[0] != "03T18" || got[len(got)-1] != "04T17" {
			t.Fatalf("returned %s … %s, want 03T18 … 04T17", got[0], got[len(got)-1])
		}
	})
}
