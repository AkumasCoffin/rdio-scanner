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
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

// The index migration20260822100000 creates: the calls search filters on
// system/talkgroup and orders by dateTime, so those are the columns in that
// order.
const callsSearchIndexName = "rdio_scanner_calls_system_talkgroup_date_time"

// The statement the index exists for, written the way call.go writes it.
// Mirrors what Calls.Search actually emits, tiebreak included. The id in the
// ORDER BY is what makes a cursor stable across calls sharing a timestamp, and
// it is also why the index carries id as its fourth column — without it the
// index supplies the filter but not the ordering, and Postgres puts a Sort back
// on top for the ties.
const callsSearchPlanQuery = `select "id" from "rdioScannerCalls" where "system" = 3 and "talkgroup" = 17 order by "dateTime" desc, "id" desc limit 100`

// TestCallsSearchIndexCreatedByMigrations asserts the index is there after a
// normal startup, on whichever backend the suite is pointed at. The migration
// is deliberately tolerant — it logs a failed CREATE INDEX and records itself
// anyway so a boot is never blocked — which means a broken statement would
// otherwise leave no trace but a log line nobody reads.
func TestCallsSearchIndexCreatedByMigrations(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	if !callsSearchIndexPresent(t, db) {
		t.Fatalf("%s missing after migrations on %s", callsSearchIndexName, db.Config.DbType)
	}
}

// TestCallsSearchIndexMigrationIsIdempotent covers the second boot. The ledger
// row should short-circuit the whole thing; if it ever does not, the raw DDL
// has to survive being run against a database that already has the index,
// rather than turning a restart into a startup error.
func TestCallsSearchIndexMigrationIsIdempotent(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	if err := db.migration20260822100000(false); err != nil {
		t.Fatalf("re-running the migration failed: %v", err)
	}

	// And once more with the ledger row removed, which is the case where the
	// DDL actually gets executed a second time.
	if _, err := db.Exec("delete from `rdioScannerMeta` where `name` = ?", "20260822100000-calls-system-talkgroup-date-time-idx"); err != nil {
		t.Fatalf("cannot rewind the migration: %v", err)
	}
	if err := db.migration20260822100000(false); err != nil {
		t.Fatalf("re-running the migration against an existing index failed: %v", err)
	}

	if !callsSearchIndexPresent(t, db) {
		t.Fatalf("%s missing after the migration ran twice", callsSearchIndexName)
	}
}

// TestCallsSearchIndexServesTheSearchPlan is the assertion that matters: not
// that the index exists, but that Postgres will use it for the access pattern
// it was built for. An index on the wrong column order still exists.
func TestCallsSearchIndexServesTheSearchPlan(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	if db.Config.DbType != DbTypePostgres {
		t.Skipf("plan assertions are Postgres-specific; suite is running on %s", db.Config.DbType)
	}

	ctx := context.Background()

	// Enough rows, spread over enough systems and talkgroups, that a seq scan
	// is a real alternative and the planner has to make a choice. One row per
	// second going backwards keeps dateTime distinct and correlated with
	// insertion order, the way real traffic arrives.
	const seed = `insert into "rdioScannerCalls" ("audio", "dateTime", "frequencies", "patches", "sources", "system", "talkgroup")
		select ''::bytea, now() - (g * interval '1 second'), '[]', '[]', '[]', (g % 7) + 1, (g % 53) + 1
		from generate_series(1, 8000) g`
	if _, err := db.Sql.ExecContext(ctx, seed); err != nil {
		t.Fatalf("cannot seed calls: %v", err)
	}

	waitForValidPostgresIndex(ctx, t, db, callsSearchIndexName)

	if _, err := db.Sql.ExecContext(ctx, `analyze "rdioScannerCalls"`); err != nil {
		t.Fatalf("cannot analyze: %v", err)
	}

	// A dedicated connection, because the planner knobs below are per-session
	// and db.Sql hands out whatever pooled connection is free — set
	// enable_seqscan on one connection and EXPLAIN on another and the setting
	// simply is not there when it counts.
	conn, err := db.Sql.Conn(ctx)
	if err != nil {
		t.Fatalf("cannot take a dedicated connection: %v", err)
	}
	defer conn.Close()

	plan := explainOn(ctx, t, conn, callsSearchPlanQuery)
	t.Logf("plan with default planner settings:\n%s", plan)

	// On a table this size the planner may still prefer a scan; that is a
	// costing decision, not a statement about the index. Take the choice away
	// and the plan then shows whether the index can serve the query at all,
	// which is what this test is about.
	if strings.Contains(plan, "Seq Scan") {
		if _, err := conn.ExecContext(ctx, "set enable_seqscan = off"); err != nil {
			t.Fatalf("cannot disable seqscan: %v", err)
		}
		plan = explainOn(ctx, t, conn, callsSearchPlanQuery)
		t.Logf("plan with enable_seqscan = off:\n%s", plan)
	}

	if strings.Contains(plan, "Seq Scan") {
		t.Fatalf("planner still reads the whole table for the calls search:\n%s", plan)
	}
	if !strings.Contains(plan, callsSearchIndexName) {
		t.Fatalf("calls search does not use %s:\n%s", callsSearchIndexName, plan)
	}

	// The other half of the claim: the index supplies the ordering, so the page
	// falls out of the scan instead of being sorted afterwards. A test table of
	// a few thousand rows will not show that on its own — 22 matching rows are
	// cheaper to sort than to walk in order, so the planner sorts them and is
	// right to. What is being checked here is that an ordered path through this
	// index exists at all, which is what a table of production size relies on:
	// discourage sorting and bitmap scans, and if the column order were wrong
	// the planner would have to sort anyway and the Sort node would still be
	// there.
	for _, off := range []string{"set enable_sort = off", "set enable_bitmapscan = off"} {
		if _, err := conn.ExecContext(ctx, off); err != nil {
			t.Fatalf("cannot apply %q: %v", off, err)
		}
	}

	ordered := explainOn(ctx, t, conn, callsSearchPlanQuery)
	t.Logf("plan with sorting and bitmap scans discouraged:\n%s", ordered)

	if strings.Contains(ordered, "Sort") {
		t.Fatalf("no ordered path through %s, so the search must sort its matches:\n%s", callsSearchIndexName, ordered)
	}
	if !strings.Contains(ordered, callsSearchIndexName) {
		t.Fatalf("the ordered path does not go through %s:\n%s", callsSearchIndexName, ordered)
	}
}

// explainOn runs EXPLAIN on one specific connection and returns the plan as
// text.
func explainOn(ctx context.Context, t *testing.T, conn *sql.Conn, query string) string {
	t.Helper()

	rows, err := conn.QueryContext(ctx, "explain "+query)
	if err != nil {
		t.Fatalf("explain failed: %v", err)
	}
	defer rows.Close()

	lines := []string{}
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("cannot read plan: %v", err)
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("cannot read plan: %v", err)
	}

	return strings.Join(lines, "\n")
}

// waitForValidPostgresIndex waits for a CONCURRENTLY-built index to be usable.
//
// The catalog row appears before the index is finished, so testing for mere
// existence can hand a test a half-built index the planner will not touch.
// indisvalid and indisready together are what make it real.
func waitForValidPostgresIndex(ctx context.Context, t *testing.T, db *Database, name string) {
	t.Helper()

	const query = `select coalesce(bool_or(i."indisvalid" and i."indisready"), false)
		from pg_index i join pg_class c on c.oid = i.indexrelid where c.relname = $1`

	deadline := time.Now().Add(30 * time.Second)
	for {
		var ready bool
		if err := db.Sql.QueryRowContext(ctx, query, name).Scan(&ready); err != nil {
			t.Fatalf("cannot check %s: %v", name, err)
		}
		if ready {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never became valid and ready", name)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// callsSearchIndexPresent asks each backend's own catalog, since none of them
// agree on where indexes are listed. Postgres additionally has to be asked
// whether the index is finished, not just present.
func callsSearchIndexPresent(t *testing.T, db *Database) bool {
	t.Helper()

	var query string
	switch db.Config.DbType {
	case DbTypePostgres:
		query = `select count(*) from pg_index i join pg_class c on c.oid = i.indexrelid
			where c.relname = $1 and i."indisvalid" and i."indisready"`

	case DbTypeSqlite:
		query = "select count(*) from sqlite_master where type = 'index' and name = ?"

	default:
		query = "select count(*) from information_schema.statistics where table_schema = database() and table_name = 'rdioScannerCalls' and index_name = ?"
	}

	var count int
	if err := db.Sql.QueryRow(query, callsSearchIndexName).Scan(&count); err != nil {
		t.Fatalf("cannot look up %s: %v", callsSearchIndexName, err)
	}

	return count > 0
}
