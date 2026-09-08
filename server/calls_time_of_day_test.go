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
	"fmt"
	"testing"
	"time"
)

func TestParseMinuteOfDay(t *testing.T) {
	for _, tc := range []struct {
		in    any
		want  int
		valid bool
	}{
		{"00:00", 0, true},
		{"04:00", 240, true},
		{"23:59", 1439, true},
		{" 9:05 ", 545, true},
		{"24:00", 0, false},
		{"12:60", 0, false},
		{"noon", 0, false},
		{"12", 0, false},
		{nil, 0, false},
		{600, 0, false},
	} {
		got, ok := parseMinuteOfDay(tc.in)
		if ok != tc.valid {
			t.Errorf("parseMinuteOfDay(%v) valid = %v, want %v", tc.in, ok, tc.valid)
			continue
		}
		if ok && got != tc.want {
			t.Errorf("parseMinuteOfDay(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// The filter is only worth anything if the SQL selects the right rows, so this
// runs it against whichever backend the suite is pointed at rather than
// asserting on the generated text.
func TestTimeOfDayPredicateSelectsTheRightHours(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	// One call every hour across two days, at UTC midnight boundaries, so each
	// hour of the clock is represented exactly twice.
	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 48; i++ {
		at := base.Add(time.Duration(i) * time.Hour)
		if _, err := db.Exec(
			"insert into `rdioScannerCalls` (`audio`, `audioName`, `audioType`, `dateTime`, `frequencies`, `patches`, `sources`, `system`, `talkgroup`) "+
				"values (?, ?, ?, ?, ?, ?, ?, ?, ?)",
			[]byte{}, "x.m4a", "audio/mp4", at.Format(db.DateTimeFormat), "[]", "[]", "[]", 1, 100,
		); err != nil {
			t.Fatalf("cannot seed hour %d: %v", i, err)
		}
	}

	count := func(predicate string) int {
		var n int
		query := fmt.Sprintf("select count(*) from `rdioScannerCalls` where %s", predicate)
		if err := db.QueryRow(query).Scan(&n); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return n
	}

	// 04:00–04:59 with no offset: one hour of the clock, twice over.
	if got := count(timeOfDayPredicate(db, 4*60, 4*60+59, 0)); got != 2 {
		t.Errorf("04:00-04:59 matched %d calls, want 2", got)
	}

	// A three-hour window covers three hourly calls per day.
	if got := count(timeOfDayPredicate(db, 4*60, 6*60+59, 0)); got != 6 {
		t.Errorf("04:00-06:59 matched %d calls, want 6", got)
	}

	// Crossing midnight is four hours of night (22, 23, 00, 01), not twenty of
	// day — the reading that makes an overnight window expressible at all.
	if got := count(timeOfDayPredicate(db, 22*60, 1*60+59, 0)); got != 8 {
		t.Errorf("22:00-01:59 matched %d calls, want 8", got)
	}

	// The offset moves the clock: with the viewer an hour ahead of UTC, the
	// call stored at 03:00 UTC is the one that reads 04:00 locally.
	if got := count(timeOfDayPredicate(db, 4*60, 4*60+59, 60)); got != 2 {
		t.Errorf("04:00-04:59 at +60 matched %d calls, want 2", got)
	}

	// And it selects genuinely different rows from the unshifted window.
	shifted := fmt.Sprintf("%s and %s",
		timeOfDayPredicate(db, 4*60, 4*60+59, 60),
		timeOfDayPredicate(db, 4*60, 4*60+59, 0))
	if got := count(shifted); got != 0 {
		t.Errorf("the shifted and unshifted 04:00 windows overlap on %d calls, want 0", got)
	}
}

// A time filter has to reach the plan, or the UI is claiming a filter the
// query never applies — which is the bug this feature exists to fix.
func TestSearchPlanCarriesTheTimeOfDayFilter(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	client := &Client{}

	options := CallsSearchOptions{TimeStart: "04:00", TimeStop: "05:00", TimeOffset: float64(600)}
	plan := buildCallsSearchPlan(&options, client, db, nil)

	if plan.where == "true" {
		t.Fatal("a time-of-day filter produced no where clause")
	}

	// Carried by the probes too: unlike the plugin-table filters, this reads a
	// column the scan already has, so the reported date span stays honest.
	if plan.probeWhere == "true" {
		t.Fatal("the bound probes ignore the time-of-day filter")
	}

	// It cannot be expressed as a system/talkgroup pair set, so the fast probe
	// form must stand down.
	if plan.probePairs != nil {
		t.Fatalf("probePairs = %v, want nil when a time filter is set", plan.probePairs)
	}
}

// An unparseable time is dropped rather than becoming a filter that matches
// nothing, which would look identical to a search with no results.
func TestSearchPlanIgnoresAnUnparseableTime(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	options := CallsSearchOptions{TimeStart: "half four"}
	plan := buildCallsSearchPlan(&options, &Client{}, db, nil)

	if plan.where != "true" {
		t.Fatalf("where = %q, want an unparseable time to be ignored", plan.where)
	}
}
