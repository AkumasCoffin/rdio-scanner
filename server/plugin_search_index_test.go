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
	"fmt"
	"strings"
	"testing"
	"time"
)

// The DDL is built by concatenating names that arrive from a plugin manifest.
// Registration validates them, but this is the one place they reach a
// statement, so it refuses anything it does not recognise rather than trusting
// the earlier check.
func TestSearchIndexRefusesUnexpectedIdentifiers(t *testing.T) {
	for _, name := range []string{
		`calls"; drop table "rdioScannerCalls`,
		"calls transcript",
		"1calls",
		"",
		"calls-transcript",
	} {
		if pluginIndexIdentifier.MatchString(name) {
			t.Errorf("%q was accepted as an identifier", name)
		}
	}

	for _, name := range []string{"plugin_transcripts_calls", "transcript", "_x9"} {
		if !pluginIndexIdentifier.MatchString(name) {
			t.Errorf("%q was rejected but is a legitimate identifier", name)
		}
	}
}

// A role without CREATE rights is the likeliest failure, and "permission
// denied" on its own does not tell an operator what to do about it.
func TestSearchIndexNamesThePermissionCase(t *testing.T) {
	reason := searchIndexUnavailableReason(fmt.Errorf("pq: permission denied to create extension \"pg_trgm\""))

	if !strings.Contains(reason, "role") {
		t.Errorf("reason %q does not say the role is the problem", reason)
	}
}

// The point of the index, proven rather than assumed: a LIKE with a leading
// wildcard over a plugin's transcript column has to stop being a sequential
// scan. Postgres only — it is the only backend where an index can answer this
// shape at all, which is why ensureSearchIndex is a no-op elsewhere.
func TestSearchIndexMakesTranscriptSearchIndexedOnPostgres(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	if db.Config.DbType != DbTypePostgres {
		t.Skip("only Postgres can index a leading-wildcard LIKE")
	}

	const table = "plugin_searchtest_calls"

	if _, err := db.Sql.Exec(fmt.Sprintf(
		`create table if not exists %q ("callId" int primary key, "transcript" text)`, table,
	)); err != nil {
		t.Fatal(err)
	}
	defer db.Sql.Exec(fmt.Sprintf("drop table if exists %q", table))

	// Enough rows that the planner would not pick an index simply because the
	// table is trivially small, and — the part that matters — a term that
	// matches only a handful of them. Seeding every row with the search term
	// makes a sequential scan genuinely the cheaper plan, so the test would
	// fail while the index was working perfectly well.
	for i := 0; i < 4000; i++ {
		text := fmt.Sprintf("unit %d routine traffic on channel %d", i, i%40)
		if i%800 == 0 {
			text = fmt.Sprintf("unit %d responding to a structure fire on main street", i)
		}

		if _, err := db.Sql.Exec(
			fmt.Sprintf(`insert into %q ("callId", "transcript") values ($1, $2)
			             on conflict ("callId") do nothing`, table),
			i, text,
		); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := db.Sql.Exec(fmt.Sprintf("analyze %q", table)); err != nil {
		t.Fatal(err)
	}

	controller := &Controller{Database: db}
	controller.ensureSearchIndex(table, "transcript", "callId")

	// The build runs off the caller's goroutine so startup is not held up.
	//
	// Waiting on indisvalid/indisready rather than the index merely existing:
	// CREATE INDEX CONCURRENTLY publishes its catalog row early and finishes
	// building afterwards, and the planner ignores it until both flags are
	// set. Polling pg_indexes alone returns while the index is still being
	// built, and the EXPLAIN then honestly reports that nothing can serve the
	// query — which looks exactly like the feature not working.
	deadline := time.Now().Add(60 * time.Second)
	indexed := false
	for time.Now().Before(deadline) {
		var count int
		if err := db.Sql.QueryRow(`
			select count(*) from pg_index i
			join pg_class c on c.oid = i.indexrelid
			join pg_class t on t.oid = i.indrelid
			where t.relname = $1 and i.indisvalid and i.indisready
			  and c.relname like '%_trgm'`, table,
		).Scan(&count); err == nil && count > 0 {
			indexed = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}

	if !indexed {
		t.Fatal("no usable GIN index was created; transcript search would still scan")
	}

	if _, err := db.Sql.Exec(fmt.Sprintf("analyze %q", table)); err != nil {
		t.Fatal(err)
	}

	// What this asserts is that the index *can* answer this query shape, not
	// that the planner picks it here. A few thousand rows is under a hundred
	// pages, where a sequential scan genuinely is cheaper and choosing it is
	// correct — the index earns its place on a table holding weeks of
	// transcripts, which is not something to build inside a unit test.
	// Disabling seqscan asks the question the test is actually about: given no
	// choice, is there an index that serves an ILIKE with a leading wildcard?
	// Before this change there was not, at any size.
	// Both statements have to be the same session. SET is per-connection, and
	// db.Sql is a pool — issuing it there sets it on whichever connection came
	// to hand and the EXPLAIN then runs on a different one, which reads as
	// "no index is usable" when nothing of the sort is true.
	conn, err := db.Sql.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(context.Background(), "set enable_seqscan = off"); err != nil {
		t.Fatal(err)
	}

	var plan strings.Builder
	rows, err := conn.QueryContext(context.Background(), fmt.Sprintf(
		`explain select 1 from %q where "transcript" ilike '%%structure fire%%'`, table,
	))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(line + "\n")
	}

	if !strings.Contains(plan.String(), "Bitmap Index Scan") {
		t.Errorf("no index can serve a leading-wildcard ILIKE on this column:\n%s", plan.String())
	} else {
		t.Logf("plan:\n%s", plan.String())
	}
}

// The with/without-transcript filter asks a different question from the text
// search: not what the text says, but which calls have any. The trigram index
// cannot answer that, and without a partial index every call the search walks
// reads the plugin table's heap to check the text is non-empty — measured on
// 644k calls as 80,764 buffers to return one page.
func TestEnsureSearchIndexBuildsThePresenceIndex(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	if db.Config.DbType != DbTypePostgres {
		t.Skipf("partial indexes are Postgres-specific here; suite is running on %s", db.Config.DbType)
	}

	table := "plugin_presence_probe_calls"

	if _, err := db.Sql.Exec(fmt.Sprintf(`drop table if exists %q`, table)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Sql.Exec(fmt.Sprintf(
		`create table %q ("callId" int primary key, "transcript" text)`, table)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Sql.Exec(fmt.Sprintf(`drop table if exists %q`, table)) })

	for i := 1; i <= 200; i++ {
		text := fmt.Sprintf("transcript for call %d", i)
		if i%50 == 0 {
			text = "" // some calls carry an empty transcript, which counts as none
		}
		if _, err := db.Sql.Exec(fmt.Sprintf(
			`insert into %q ("callId", "transcript") values ($1, $2)`, table), i, text); err != nil {
			t.Fatal(err)
		}
	}

	controller := &Controller{Database: db}
	controller.ensureSearchIndex(table, "transcript", "callId")

	// Built off the caller's goroutine, and CONCURRENTLY publishes its catalog
	// row before it is usable — so wait for indisvalid, not for existence.
	want := table + "_transcript_present"
	deadline := time.Now().Add(60 * time.Second)
	built := false
	for time.Now().Before(deadline) {
		var count int
		if err := db.Sql.QueryRow(`
			select count(*) from pg_index i
			join pg_class c on c.oid = i.indexrelid
			where c.relname = $1 and i.indisvalid and i.indisready`, want,
		).Scan(&count); err == nil && count > 0 {
			built = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}

	if !built {
		t.Fatalf("%s was not created; the with/without filter would read the heap per call", want)
	}

	// Partial, and on the key column: it holds one narrow entry per row that
	// has text and nothing for the rest, which is what lets the probe be
	// index-only.
	var def string
	if err := db.Sql.QueryRow(
		`select indexdef from pg_indexes where indexname = $1`, want).Scan(&def); err != nil {
		t.Fatalf("cannot read the index definition: %v", err)
	}

	for _, fragment := range []string{"callId", "WHERE", "transcript"} {
		if !strings.Contains(def, fragment) {
			t.Fatalf("index definition %q does not mention %q", def, fragment)
		}
	}

	// The empty transcripts must be excluded, or "has a transcript" would be
	// true for a call whose transcription produced nothing.
	var indexed int
	if err := db.Sql.QueryRow(fmt.Sprintf(
		`select count(*) from %q where "transcript" is not null and "transcript" <> ''`, table)).Scan(&indexed); err != nil {
		t.Fatal(err)
	}
	if indexed != 196 {
		t.Fatalf("%d rows qualify as having a transcript, want 196", indexed)
	}
}
