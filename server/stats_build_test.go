// Copyright (C) 2019-2024 Chrystian Huot <chrystian.huot@saubeo.solutions>
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

// statsTestController is the smallest controller build() needs: options for
// the category lens and a log sink for sub-query failures.
func statsTestController(db *Database) *Controller {
	controller := &Controller{
		Options: NewOptions(),
		Logs:    NewLogs(),
		Systems: NewSystems(),
		Groups:  NewGroups(),
		Tags:    NewTags(),
	}
	controller.Logs.database = db

	return controller
}

// The build ran its sub-queries in parallel for wall-clock speed, and paid for
// it by occupying most of the connection pool at once — on a large table that
// starved call inserts for seconds. It now runs them one after another; this
// pins the part that must not regress with that change: every panel of the
// response is still populated.
func TestStatsBuildPopulatesEveryPanel(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	now := time.Now().UTC()
	insertTestCall(t, db, now.Add(-10*time.Minute))
	insertTestCall(t, db, now.Add(-30*time.Minute))

	stats := &Stats{Controller: statsTestController(db)}

	resp := stats.build(db)

	if resp.Overview.TotalCalls == 0 {
		t.Error("overview total is zero with calls in the table")
	}

	if len(resp.HourBuckets) == 0 {
		t.Error("hour buckets are empty")
	}

	if len(resp.TopTalkgroups) == 0 {
		t.Error("top talkgroups are empty")
	}

	if len(resp.TopSystems) == 0 {
		t.Error("top systems are empty")
	}

	if resp.TopCategoriesKind != "systems" {
		t.Errorf("category lens is %q, want systems", resp.TopCategoriesKind)
	}

	if len(resp.TopCategories) == 0 {
		t.Error("top categories are empty — the systems lens should mirror top systems")
	}

	if len(resp.TopUnits) == 0 {
		t.Error("top units are empty with source set on the seeded calls")
	}

	if len(resp.LastHourTalkgroups) == 0 {
		t.Error("last-hour talkgroups are empty with calls minutes old")
	}

	if len(resp.CallFineBuckets) == 0 {
		t.Error("fine buckets are empty")
	}

	if len(resp.CallMicroBuckets) == 0 {
		t.Error("micro buckets are empty")
	}
}

// A build that takes a minute must not be asked for again two minutes later —
// that treadmill is what kept a large database saturated. The refresh interval
// scales with the last build; fast builds keep the floor.
func TestStatsRefreshIntervalScalesWithBuildCost(t *testing.T) {
	stats := &Stats{}

	if got := stats.refreshInterval(); got != statsCacheTTL {
		t.Errorf("interval with no build history is %v, want the %v floor", got, statsCacheTTL)
	}

	stats.lastBuild = 10 * time.Second

	if got := stats.refreshInterval(); got != statsCacheTTL {
		t.Errorf("interval after a fast build is %v, want the %v floor", got, statsCacheTTL)
	}

	stats.lastBuild = time.Minute

	if got, want := stats.refreshInterval(), 4*time.Minute; got != want {
		t.Errorf("interval after a %v build is %v, want %v", stats.lastBuild, got, want)
	}
}

// seedUnitCall writes a call with a chosen scalar source and sources JSON, the
// two places a unit id can live.
func seedUnitCall(t *testing.T, db *Database, system uint, source int, sources string, when time.Time) {
	t.Helper()

	if _, err := db.Sql.Exec(db.formatQuery(
		"insert into `rdioScannerCalls` (`dateTime`, `system`, `talkgroup`, `source`, `audio`,"+
			" `audioName`, `audioType`, `frequencies`, `frequency`, `patches`, `sources`)"+
			" values (?, ?, 1, ?, ?, 'test.m4a', 'audio/mp4', '[]', 154000000, '[]', ?)"),
		when.Format(db.DateTimeFormat), system, source, []byte{0}, sources,
	); err != nil {
		t.Fatal(err)
	}
}

// The SQL and scan tallies must agree call for call: scalar-only units,
// JSON-only units, a scalar that repeats inside its own JSON (one count, not
// two), and a unit heard on several calls. On Postgres this exercises the SQL
// path against the scan; on SQLite both sides are the scan and the test pins
// the semantics the SQL path must reproduce.
func TestTopUnitsCountsBothSourceShapesOnce(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	now := time.Now().UTC()

	// Unit 10: scalar only.
	seedUnitCall(t, db, 1, 10, "[]", now.Add(-time.Hour))
	// Unit 20: JSON only, scalar left at zero (the DSD-FME shape).
	seedUnitCall(t, db, 1, 0, `[{"pos":0,"src":20}]`, now.Add(-time.Hour))
	// Unit 30: scalar AND repeated in JSON — one call, one count.
	seedUnitCall(t, db, 1, 30, `[{"pos":0,"src":30},{"pos":1.5,"src":30}]`, now.Add(-time.Hour))
	// Unit 20 again on a second call, another system.
	seedUnitCall(t, db, 2, 0, `[{"pos":0,"src":20}]`, now.Add(-2*time.Hour))
	// A call with both a scalar and a different JSON unit: both count.
	seedUnitCall(t, db, 1, 10, `[{"pos":0,"src":40}]`, now.Add(-3*time.Hour))
	// Outside the 7-day window: invisible.
	seedUnitCall(t, db, 1, 99, "[]", now.AddDate(0, 0, -8))

	stats := &Stats{Controller: statsTestController(db)}

	result, err := stats.GetTopUnits(db, 10)
	if err != nil {
		t.Fatal(err)
	}

	got := map[[2]uint]uint{}
	for _, item := range result {
		got[[2]uint{item.SystemId, item.UnitId}] = item.Count
	}

	want := map[[2]uint]uint{
		{1, 10}: 2,
		{1, 20}: 1,
		{1, 30}: 1,
		{1, 40}: 1,
		{2, 20}: 1,
	}

	for k, n := range want {
		if got[k] != n {
			t.Errorf("system %v unit %v counted %v, want %v", k[0], k[1], got[k], n)
		}
	}

	if len(got) != len(want) {
		t.Errorf("got %v entries (%v), want %v", len(got), got, want)
	}

	// On Postgres, the SQL tally must agree with the scan exactly.
	if db.Config.DbType == DbTypePostgres {
		since := now.AddDate(0, 0, -7)

		fromSql, err := stats.topUnitsSql(db, since)
		if err != nil {
			t.Fatalf("sql tally: %v", err)
		}

		fromScan, err := stats.topUnitsScan(db, since)
		if err != nil {
			t.Fatalf("scan tally: %v", err)
		}

		if len(fromSql) != len(fromScan) {
			t.Fatalf("sql tally %v, scan tally %v", fromSql, fromScan)
		}

		for k, n := range fromScan {
			if fromSql[k] != n {
				t.Errorf("sql counted %v for %v, scan counted %v", fromSql[k], k, n)
			}
		}
	}
}

// A sources value that is not valid JSON must not take the panel down: on
// Postgres the jsonb cast fails and the build falls back to the scan, which
// parses defensively.
func TestTopUnitsSurvivesMalformedSources(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	now := time.Now().UTC()

	seedUnitCall(t, db, 1, 10, "[]", now.Add(-time.Hour))
	seedUnitCall(t, db, 1, 0, `[{"pos":0,`, now.Add(-time.Hour)) // truncated JSON

	stats := &Stats{Controller: statsTestController(db)}

	result, err := stats.GetTopUnits(db, 10)
	if err != nil {
		t.Fatalf("GetTopUnits errored on malformed sources: %v", err)
	}

	if len(result) != 1 || result[0].UnitId != 10 {
		t.Errorf("got %v, want just unit 10", result)
	}
}

// The estimate must never replace the exact count when it cannot be trusted:
// on SQLite there is no estimate at all, and on a small Postgres table the
// floor keeps the exact count.
func TestApproxCallCountIsAbsentOnSqlite(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	if db.Config.DbType == DbTypeSqlite {
		if _, ok := db.ApproxCallCount(); ok {
			t.Fatal("SQLite offered a call-count estimate; there is no such thing")
		}
	}

	if db.Config.DbType == DbTypePostgres {
		// The test table is far below the floor, so the estimate must decline
		// and leave the caller on the exact count.
		if n, ok := db.ApproxCallCount(); ok {
			t.Fatalf("a near-empty table offered estimate %v; the floor should have declined it", n)
		}
	}
}

// The talkgroup-units endpoint is public; without a cache every click was an
// hour-wide row scan. Within the TTL, the second ask must be the cached one.
func TestTalkgroupUnitsAreCached(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	now := time.Now().UTC()
	seedUnitCall(t, db, 1, 10, "[]", now.Add(-10*time.Minute))

	stats := &Stats{Controller: statsTestController(db)}

	first, err := stats.cachedTalkgroupUnits(db, 1, 1)
	if err != nil {
		t.Fatal(err)
	}

	if len(first) != 1 {
		t.Fatalf("got %v units, want 1", len(first))
	}

	// A second call inside the same window would see this too — unless it is
	// answered from the cache, which is the point.
	seedUnitCall(t, db, 1, 20, "[]", now.Add(-5*time.Minute))

	second, err := stats.cachedTalkgroupUnits(db, 1, 1)
	if err != nil {
		t.Fatal(err)
	}

	if len(second) != 1 {
		t.Fatalf("second ask returned %v units — it went back to the database inside the TTL", len(second))
	}

	// Expire the entry and the new call appears.
	stats.tgUnitsMu.Lock()
	for _, entry := range stats.tgUnits {
		entry.expires = time.Now().Add(-time.Second)
	}
	stats.tgUnitsMu.Unlock()

	third, err := stats.cachedTalkgroupUnits(db, 1, 1)
	if err != nil {
		t.Fatal(err)
	}

	if len(third) != 2 {
		t.Fatalf("after expiry got %v units, want 2", len(third))
	}
}
