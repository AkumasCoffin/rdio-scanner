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
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"
)

// importCommitEvery bounds how many calls ride in one target transaction.
// Audio blobs dominate row size, so committing periodically keeps memory and
// the target's write-ahead log from growing without limit on a large import.
const importCommitEvery = 200

// importTable describes one table to copy. Columns are discovered at runtime
// and intersected between source and target, so a schema difference between
// the two builds degrades to "copy what they share" rather than failing.
//
// dateTimes and booleans exist because values cannot be copied across
// verbatim: SQLite writes timestamps as text in its own layout and booleans as
// 0/1 integers, and Postgres rejects both against timestamp and boolean
// columns. They are listed explicitly rather than introspected so the
// behaviour is identical on every target backend.
type importTable struct {
	name      string
	dateTimes []string
	booleans  []string
	// appendOnly tables are resumed rather than replaced — used for calls,
	// where the table is large and re-copying it would be wasteful.
	appendOnly bool
	key        string
}

// importTables is the migration set, ordered so configuration lands before the
// rows that reference it.
//
// Deliberately excluded: rdioScannerMeta (schema bookkeeping — the target has
// already run its own migrations and must keep its own record), and
// rdioScannerDelayed (a transient queue that is rebuilt at startup).
// rdioScannerLogs is skipped too: it is large, purely historical, and the
// target prunes it on its own schedule anyway.
var importTables = []importTable{
	{name: "rdioScannerConfigs", key: "_id"},
	{name: "rdioScannerGroups", key: "_id"},
	{name: "rdioScannerTags", key: "_id"},
	{name: "rdioScannerSystems", key: "_id", booleans: []string{"autoPopulate", "transcribe"}},
	{name: "rdioScannerTalkgroups", key: "_id", booleans: []string{"transcribe"}},
	{name: "rdioScannerUnits", key: "_id"},
	{name: "rdioScannerAccesses", key: "_id", dateTimes: []string{"expiration"}},
	{name: "rdioScannerApiKeys", key: "_id", booleans: []string{"disabled"}},
	{name: "rdioScannerDirWatches", key: "_id", booleans: []string{"deleteAfter", "disabled", "usePolling"}},
	{name: "rdioScannerDownstreams", key: "_id", booleans: []string{"disabled"}},
}

var importCallsTable = importTable{
	name:       "rdioScannerCalls",
	key:        "id",
	dateTimes:  []string{"dateTime"},
	appendOnly: true,
}

// ImportSqlite copies an Rdio Scanner SQLite database into the configured
// database. It exists because there was no supported path off SQLite: the
// built-in config-get / config-set pair moves only what the admin UI exposes,
// over HTTP, against a running server — and nothing at all moved the call
// history.
//
// Configuration tables are REPLACED: each is emptied and refilled from the
// source, so the result matches the origin exactly rather than merging with
// the defaults a fresh install seeds. Everything is done in one transaction,
// so a failure leaves the target as it was.
//
// Calls are only copied when withCalls is set, and are appended rather than
// replaced — the table carries every audio blob and is usually orders of
// magnitude larger than the rest combined. That copy is resumable: it starts
// after the highest call id already present, so an interrupted run continues
// instead of duplicating.
//
// Primary keys are preserved throughout, which on Postgres leaves the
// sequences behind their tables; they are realigned before returning.
func ImportSqlite(target *Database, sqliteFile string, withCalls bool, force bool) error {
	if _, err := os.Stat(sqliteFile); err != nil {
		return fmt.Errorf("import: cannot read %s: %v", sqliteFile, err)
	}

	if target.Config.DbType == DbTypeSqlite && target.Config.GetDbFilePath() == sqliteFile {
		return fmt.Errorf("import: source and target are the same database (%s)", sqliteFile)
	}

	sourceSql, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=busy_timeout%%3d10000", sqliteFile))
	if err != nil {
		return fmt.Errorf("import: cannot open %s: %v", sqliteFile, err)
	}
	defer sourceSql.Close()

	if err := sourceSql.Ping(); err != nil {
		return fmt.Errorf("import: cannot open %s: %v", sqliteFile, err)
	}

	// Assembled by hand rather than through NewDatabase: running migrate() and
	// seed() against the old database would modify the very thing being
	// migrated away from.
	source := &Database{
		Config:         &Config{DbType: DbTypeSqlite},
		DateTimeFormat: "2006-01-02 15:04:05.000 -07:00",
		Sql:            sourceSql,
	}

	if _, err := tableColumns(source, "rdioScannerSystems"); err != nil {
		return fmt.Errorf("import: %s does not look like an Rdio Scanner database: %v", sqliteFile, err)
	}

	if err := checkTargetEmpty(target, force); err != nil {
		return err
	}

	fmt.Printf("importing configuration from %s\n", sqliteFile)

	tx, err := target.Sql.Begin()
	if err != nil {
		return fmt.Errorf("import: cannot start transaction: %v", err)
	}

	for _, table := range importTables {
		n, err := copyTable(tx, source, target, table)
		if err != nil {
			tx.Rollback()
			return err
		}
		fmt.Printf("  %-24s %d row(s)\n", table.name, n)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("import: commit failed: %v", err)
	}

	if withCalls {
		if err := copyCalls(source, target); err != nil {
			return err
		}
	} else {
		fmt.Printf("recorded calls were NOT copied — re-run with -import_calls to include them\n")
	}

	// Keys were preserved, so on Postgres every sequence is now behind its
	// table and the next insert would collide.
	if err := target.repairSequences(); err != nil {
		return fmt.Errorf("import: %v", err)
	}

	fmt.Printf("import complete\n")

	return nil
}

// checkTargetEmpty refuses to replace the configuration of a target that is
// already in use, since that half of the import is destructive. A fresh
// install has no systems — seeded groups and tags don't count.
func checkTargetEmpty(target *Database, force bool) error {
	var systems uint
	if err := target.QueryRow("select count(*) from `rdioScannerSystems`").Scan(&systems); err != nil {
		return fmt.Errorf("import: cannot inspect target: %v", err)
	}

	if systems == 0 || force {
		if systems > 0 {
			fmt.Printf("warning: target already has %d system(s); replacing its configuration\n", systems)
		}
		return nil
	}

	return fmt.Errorf("import: target already has %d configured system(s) — importing would replace its "+
		"configuration. Re-run with -import_force if that is intended", systems)
}

// copyTable empties the target table and refills it from the source, returning
// the number of rows written. A table absent from either side is skipped.
func copyTable(tx *sql.Tx, source *Database, target *Database, table importTable) (uint, error) {
	columns, err := sharedColumns(source, target, table.name)
	if err != nil || len(columns) == 0 {
		// Not an error: an older source may predate the table entirely.
		return 0, nil
	}

	if _, err := tx.Exec(target.formatQuery(fmt.Sprintf("delete from `%s`", table.name))); err != nil {
		return 0, fmt.Errorf("import: clearing %s: %v", table.name, err)
	}

	rows, err := source.Query(fmt.Sprintf("select %s from `%s` order by `%s`",
		backtickList(columns), table.name, table.key))
	if err != nil {
		return 0, fmt.Errorf("import: reading %s: %v", table.name, err)
	}
	defer rows.Close()

	insert := target.formatQuery(fmt.Sprintf("insert into `%s` (%s) values (%s)",
		table.name, backtickList(columns), placeholders(len(columns))))

	var written uint

	for rows.Next() {
		values, err := scanRow(rows, len(columns))
		if err != nil {
			return 0, fmt.Errorf("import: reading %s: %v", table.name, err)
		}

		if err := coerceRow(source, table, columns, values); err != nil {
			return 0, fmt.Errorf("import: %s: %v", table.name, err)
		}

		if _, err := tx.Exec(insert, values...); err != nil {
			return 0, fmt.Errorf("import: writing %s: %v", table.name, err)
		}

		written++
	}

	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("import: reading %s: %v", table.name, err)
	}

	return written, nil
}

// copyCalls appends recorded calls, committing in batches and resuming after
// whatever is already present.
func copyCalls(source *Database, target *Database) error {
	table := importCallsTable

	columns, err := sharedColumns(source, target, table.name)
	if err != nil || len(columns) == 0 {
		return fmt.Errorf("import: source has no %s table", table.name)
	}

	var total uint
	if err := source.QueryRow(fmt.Sprintf("select count(*) from `%s`", table.name)).Scan(&total); err != nil {
		return fmt.Errorf("import: counting calls: %v", err)
	}

	var resumeFrom sql.NullInt64
	if err := target.QueryRow(fmt.Sprintf("select max(`%s`) from `%s`", table.key, table.name)).Scan(&resumeFrom); err != nil {
		return fmt.Errorf("import: inspecting target calls: %v", err)
	}

	after := int64(0)
	if resumeFrom.Valid {
		after = resumeFrom.Int64
		fmt.Printf("target already holds calls up to id %d — resuming after it\n", after)
	}

	fmt.Printf("importing %d call(s) — this is the slow part\n", total)

	rows, err := source.Query(fmt.Sprintf("select %s from `%s` where `%s` > %d order by `%s`",
		backtickList(columns), table.name, table.key, after, table.key))
	if err != nil {
		return fmt.Errorf("import: reading calls: %v", err)
	}
	defer rows.Close()

	insert := target.formatQuery(fmt.Sprintf("insert into `%s` (%s) values (%s)",
		table.name, backtickList(columns), placeholders(len(columns))))

	var (
		copied  uint
		skipped uint
		tx      *sql.Tx
		started = time.Now()
	)

	defer func() {
		if tx != nil {
			tx.Rollback()
		}
	}()

	for rows.Next() {
		values, err := scanRow(rows, len(columns))
		if err != nil {
			return fmt.Errorf("import: reading calls: %v", err)
		}

		if err := coerceRow(source, table, columns, values); err != nil {
			// One unreadable timestamp shouldn't abandon an otherwise good
			// import of tens of thousands of calls.
			fmt.Printf("  skipping a call: %v\n", err)
			skipped++
			continue
		}

		if tx == nil {
			if tx, err = target.Sql.Begin(); err != nil {
				return fmt.Errorf("import: cannot start transaction: %v", err)
			}
		}

		if _, err := tx.Exec(insert, values...); err != nil {
			tx.Rollback()
			tx = nil
			return fmt.Errorf("import: writing call: %v", err)
		}

		copied++

		if copied%importCommitEvery == 0 {
			if err := tx.Commit(); err != nil {
				tx = nil
				return fmt.Errorf("import: commit failed after %d calls: %v", copied, err)
			}
			tx = nil
			fmt.Printf("  %d/%d calls\n", copied, total)
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("import: reading calls: %v", err)
	}

	if tx != nil {
		if err := tx.Commit(); err != nil {
			tx = nil
			return fmt.Errorf("import: final commit failed: %v", err)
		}
		tx = nil
	}

	fmt.Printf("imported %d call(s) in %s\n", copied, time.Since(started).Round(time.Second))
	if skipped > 0 {
		fmt.Printf("skipped %d unreadable call(s)\n", skipped)
	}

	return nil
}

// scanRow reads one row into a slice of untyped values.
func scanRow(rows *sql.Rows, n int) ([]any, error) {
	values := make([]any, n)
	into := make([]any, n)
	for i := range values {
		into[i] = &values[i]
	}

	if err := rows.Scan(into...); err != nil {
		return nil, err
	}

	return values, nil
}

// coerceRow rewrites the values a SQLite source produces into what the target's
// column types accept, in place.
func coerceRow(source *Database, table importTable, columns []string, values []any) error {
	for i, column := range columns {
		if values[i] == nil {
			continue
		}

		if contains(table.dateTimes, column) {
			t, err := source.ParseDateTime(values[i])
			if err != nil {
				return fmt.Errorf("unreadable %s (%v)", column, err)
			}
			values[i] = t.UTC()
			continue
		}

		if contains(table.booleans, column) {
			values[i] = truthy(values[i])
		}
	}

	return nil
}

// truthy converts SQLite's 0/1 integer booleans (and the odd text variant) into
// a real bool, which is what a Postgres boolean column requires.
func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case int64:
		return t != 0
	case float64:
		return t != 0
	case []byte:
		return truthyString(string(t))
	case string:
		return truthyString(t)
	}
	return false
}

func truthyString(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "0", "false", "f", "no", "null":
		return false
	}
	return true
}

// sharedColumns returns the columns present in both databases for a table, in
// the source's order. Discovered with a zero-row select rather than a
// backend-specific catalogue query, so it works the same everywhere.
func sharedColumns(source *Database, target *Database, table string) ([]string, error) {
	sourceColumns, err := tableColumns(source, table)
	if err != nil {
		return nil, err
	}

	targetColumns, err := tableColumns(target, table)
	if err != nil {
		return nil, err
	}

	inTarget := map[string]bool{}
	for _, column := range targetColumns {
		inTarget[column] = true
	}

	shared := []string{}
	dropped := []string{}
	for _, column := range sourceColumns {
		if inTarget[column] {
			shared = append(shared, column)
		} else {
			dropped = append(dropped, column)
		}
	}

	if len(dropped) > 0 {
		fmt.Printf("  %s: source column(s) %v are not in this schema, ignoring\n", table, dropped)
	}

	return shared, nil
}

func tableColumns(db *Database, table string) ([]string, error) {
	rows, err := db.Query(fmt.Sprintf("select * from `%s` where 1 = 0", table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return rows.Columns()
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func backtickList(columns []string) string {
	quoted := make([]string, len(columns))
	for i, column := range columns {
		quoted[i] = "`" + column + "`"
	}
	return strings.Join(quoted, ", ")
}

func placeholders(n int) string {
	marks := make([]string, n)
	for i := range marks {
		marks[i] = "?"
	}
	return strings.Join(marks, ", ")
}
