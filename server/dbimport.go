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
	"path/filepath"
	"sort"
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
	// perCall marks a plugin table keyed by call id. Those rows only make
	// sense alongside the calls they describe, so they travel with them.
	perCall bool
}

// importTables is the migration set, ordered so configuration lands before the
// rows that reference it.
//
// Deliberately excluded, and this list is the whole of it: rdioScannerMeta
// (schema bookkeeping — the target has already run its own migrations and must
// keep its own record), rdioScannerDelayed (a transient queue rebuilt at
// startup), and rdioScannerLogs (large, purely historical, and the target
// prunes it on its own schedule anyway).
//
// Everything else the schema declares is copied, including each plugin's own
// tables. TestImportCoversTheSchema holds that: a new table has to be listed
// here or excluded there on purpose, so the next one cannot be forgotten the
// way patches and listener history were.
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
	{name: "rdioScannerPatches", key: "_id", booleans: []string{"disabled"}},
	// Listener counts are history rather than configuration, but they are two
	// integers a row and they are what the stats page draws, so losing them on
	// a migration is a visible gap for no saving worth having.
	{name: "rdioScannerListeners", key: "timestamp"},
	// The registry itself. Each plugin's own `plugin_*` tables are copied too,
	// discovered from the source rather than listed here — see
	// pluginImportTables.
	{name: "rdioScannerPlugins", key: "_id", booleans: []string{"enabled"}, dateTimes: []string{"installedAt"}},
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
	sqliteFile = resolveImportPath(target.Config, sqliteFile)

	if _, err := os.Stat(sqliteFile); err != nil {
		return fmt.Errorf("import: cannot read %s: %v", sqliteFile, err)
	}

	// Resolved both sides, because the source path is taken as the user typed
	// it while GetDbFilePath always answers base-dir-joined. Running
	// `-import_sqlite rdio-scanner.db` from inside the base dir compared
	// "rdio-scanner.db" against "<base>/rdio-scanner.db", missed, and went on
	// to delete the configuration out of the file it was reading.
	if target.Config.DbType == DbTypeSqlite {
		sourcePath, sourceErr := filepath.Abs(sqliteFile)
		targetPath, targetErr := filepath.Abs(target.Config.GetDbFilePath())

		same := sourceErr == nil && targetErr == nil && sourcePath == targetPath

		if !same {
			// Different names can still be one file — a symlink, a hard link, a
			// bind mount.
			if sourceInfo, err := os.Stat(sqliteFile); err == nil {
				if targetInfo, err := os.Stat(target.Config.GetDbFilePath()); err == nil {
					same = os.SameFile(sourceInfo, targetInfo)
				}
			}
		}

		if same {
			return fmt.Errorf("import: source and target are the same database (%s)", sqliteFile)
		}
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

	// Before anything else inserts a row. Keys were preserved by the copy, so
	// on Postgres every sequence is now behind its table and the next insert
	// collides — which is exactly what the option conversion below does. Safe
	// to run twice; it runs again after the calls.
	if err := target.repairSequences(); err != nil {
		return fmt.Errorf("import: %v", err)
	}

	// The configuration the source carried may predate the migration that
	// split the single `options` blob into one row per setting. The target has
	// already recorded that migration as done — it ran on its own first boot —
	// so without this the blob sits there unread and every option silently
	// reverts to its default: branding, email, maxClients, pruneDays, the
	// lot. The admin password survives, because it is read by exact key, which
	// is what made this look like a clean import.
	if err := splitImportedOptions(target); err != nil {
		return err
	}

	// Each plugin's own tables. Per-call rows wait for the calls.
	pluginTables, err := pluginImportTables(source, target)
	if err != nil {
		return err
	}

	for _, table := range pluginTables {
		if table.perCall && !withCalls {
			fmt.Printf("  %-24s skipped — keyed by call id, and the calls are staying behind\n", table.name)
			continue
		}

		n, err := copyPluginTable(source, target, table)
		if err != nil {
			return err
		}

		fmt.Printf("  %-24s %d row(s)\n", table.name, n)
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

// resolveImportPath finds the source file the way a user expects: a bare
// `-import_sqlite rdio-scanner.db` should work when the database sits beside
// the executable, which is where a default install keeps it, regardless of
// which directory the command was run from.
//
// The path as typed is tried first, so an absolute path always wins. That
// ordering also sidesteps Config.GetPath, which tests with path.IsAbs and so
// does not recognise a Windows drive-letter path as absolute.
func resolveImportPath(config *Config, given string) string {
	if _, err := os.Stat(given); err == nil {
		return given
	}

	if config != nil {
		if beside := config.GetPath(given); beside != given {
			if _, err := os.Stat(beside); err == nil {
				return beside
			}
		}
	}

	return given
}

// checkTargetEmpty refuses to replace the configuration of a target that is
// already in use, since that half of the import is destructive. A fresh
// install has no systems — seeded groups and tags don't count.
func checkTargetEmpty(target *Database, force bool) error {
	// Every table the configuration half replaces, not just systems. An
	// install with API keys, downstreams, dirwatches, access codes or patches
	// but no systems yet is in use — reading "no systems" as "fresh" deleted
	// all of it without asking.
	//
	// Seeded groups and tags are left out on purpose: a fresh install has
	// those, so counting them would mean every target needed -import_force.
	populated := []string{}

	for _, table := range importTables {
		switch table.name {
		case "rdioScannerGroups", "rdioScannerTags", "rdioScannerListeners", "rdioScannerConfigs":
			// Seeded or historical on a fresh install; see above.
			continue
		}

		var rows uint
		if err := target.QueryRow(fmt.Sprintf("select count(*) from `%s`", table.name)).Scan(&rows); err != nil {
			return fmt.Errorf("import: cannot inspect target: %v", err)
		}

		if rows > 0 {
			populated = append(populated, fmt.Sprintf("%s (%d)", strings.TrimPrefix(table.name, "rdioScanner"), rows))
		}
	}

	if len(populated) == 0 {
		return nil
	}

	if force {
		fmt.Printf("warning: replacing the target configuration, which already holds %s\n", strings.Join(populated, ", "))
		return nil
	}

	return fmt.Errorf("import: the target is already configured — it holds %s, and importing would replace all of "+
		"it. Re-run with -import_force if that is intended", strings.Join(populated, ", "))
}

// splitImportedOptions re-runs the option-splitting migration over whatever
// configuration the import just wrote.
//
// Options moved from one JSON blob under the key `options` to a row per
// setting, and that migration is recorded in the target's ledger from its own
// first boot. An imported configuration older than the split therefore arrived
// in a format nothing reads any more, and the migration that would fix it had
// already been marked done. Clearing that one ledger row and running it again
// converts the blob in place; it deletes the blob when it is finished, so a
// second import is a no-op rather than a second conversion.
func splitImportedOptions(target *Database) error {
	const migration = "20260421130000-split-option-rows"

	var blobs uint
	if err := target.QueryRow(
		"select count(*) from `rdioScannerConfigs` where `key` = 'options'").Scan(&blobs); err != nil {
		return fmt.Errorf("import: inspecting imported options: %v", err)
	}

	if blobs == 0 {
		return nil
	}

	fmt.Printf("  imported configuration predates the option split — converting it\n")

	if _, err := target.Exec(
		"delete from `rdioScannerMeta` where `name` = ?", migration); err != nil {
		return fmt.Errorf("import: re-arming the option split: %v", err)
	}

	if err := target.migration20260421130000(false); err != nil {
		return fmt.Errorf("import: converting options: %v", err)
	}

	return nil
}

// copyPluginTable replaces one plugin table, committing as it goes.
//
// Its own transactions rather than the configuration's single one: a plugin's
// per-call table holds a row per call — the transcript archive is the obvious
// case — and that does not belong in one transaction with the systems list.
func copyPluginTable(source *Database, target *Database, table importTable) (uint, error) {
	if _, err := tableColumns(source, table.name); err != nil {
		return 0, nil
	}

	columns, err := sharedColumns(source, target, table.name)
	if err != nil {
		return 0, fmt.Errorf("import: cannot read %s on the target: %v", table.name, err)
	}

	if len(columns) == 0 {
		return 0, fmt.Errorf("import: %s has no columns in common between source and target", table.name)
	}

	rows, err := source.Query(fmt.Sprintf("select %s from `%s`", backtickList(columns), table.name))
	if err != nil {
		return 0, fmt.Errorf("import: reading %s: %v", table.name, err)
	}
	defer rows.Close()

	insert := target.formatQuery(fmt.Sprintf("insert into `%s` (%s) values (%s)",
		table.name, backtickList(columns), placeholders(len(columns))))

	var (
		written uint
		tx      *sql.Tx
	)

	defer func() {
		if tx != nil {
			tx.Rollback()
		}
	}()

	if tx, err = target.Sql.Begin(); err != nil {
		return 0, fmt.Errorf("import: cannot start transaction: %v", err)
	}

	if _, err := tx.Exec(target.formatQuery(fmt.Sprintf("delete from `%s`", table.name))); err != nil {
		return 0, fmt.Errorf("import: clearing %s: %v", table.name, err)
	}

	for rows.Next() {
		values, err := scanRow(rows, len(columns))
		if err != nil {
			return 0, fmt.Errorf("import: reading %s: %v", table.name, err)
		}

		if err := coerceRow(source, table, columns, values); err != nil {
			return 0, fmt.Errorf("import: reading %s: %v", table.name, err)
		}

		if tx == nil {
			if tx, err = target.Sql.Begin(); err != nil {
				return 0, fmt.Errorf("import: cannot start transaction: %v", err)
			}
		}

		if _, err := tx.Exec(insert, values...); err != nil {
			return 0, fmt.Errorf("import: writing %s: %v", table.name, err)
		}

		written++

		if written%importCommitEvery == 0 {
			if err := tx.Commit(); err != nil {
				tx = nil
				return 0, fmt.Errorf("import: commit failed after %d rows of %s: %v", written, table.name, err)
			}
			tx = nil
		}
	}

	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("import: reading %s: %v", table.name, err)
	}

	if tx != nil {
		if err := tx.Commit(); err != nil {
			tx = nil
			return 0, fmt.Errorf("import: final commit failed for %s: %v", table.name, err)
		}
		tx = nil
	}

	return written, nil
}

// callsAgreeAt reports whether source and target hold the same call at one id,
// which is how an interrupted import is told apart from a target that has been
// recording calls of its own.
//
// dateTime is the comparison: it is set at ingest and never rewritten, so two
// rows sharing an id and a timestamp came from the same recording.
func callsAgreeAt(source *Database, target *Database, key string, id int64) (bool, error) {
	read := func(db *Database) (time.Time, bool, error) {
		var raw any

		query := fmt.Sprintf("select `dateTime` from `rdioScannerCalls` where `%s` = %d", key, id)
		if err := db.QueryRow(query).Scan(&raw); err != nil {
			if err == sql.ErrNoRows {
				return time.Time{}, false, nil
			}
			return time.Time{}, false, fmt.Errorf("import: comparing call %d: %v", id, err)
		}

		at, err := db.ParseDateTime(raw)
		if err != nil {
			return time.Time{}, false, nil
		}

		return at.UTC(), true, nil
	}

	sourceAt, sourceOk, err := read(source)
	if err != nil {
		return false, err
	}

	targetAt, targetOk, err := read(target)
	if err != nil {
		return false, err
	}

	// A target id the source does not have cannot be a resume of this file.
	if !sourceOk || !targetOk {
		return false, nil
	}

	return sourceAt.Equal(targetAt), nil
}

// pluginImportTables lists the plugin tables the source holds, described well
// enough to copy.
//
// A plugin's tables are created from its own manifest, so core keeps no list
// of them — but it does not need one. The names come from the source, and the
// target's own column types say which values need converting on the way in.
//
// Leaving them behind meant a migrated install lost every transcript it had
// ever made: the most expensive thing in the database to regenerate, moved out
// of rdioScannerCalls by an earlier migration, and the last thing an operator
// would think to check after an import that said it was complete.
func pluginImportTables(source *Database, target *Database) ([]importTable, error) {
	rows, err := source.Query(
		"select `name` from `sqlite_master` where `type` = 'table' and `name` like 'plugin!_%' escape '!' order by `name`")
	if err != nil {
		return nil, fmt.Errorf("import: listing plugin tables: %v", err)
	}
	defer rows.Close()

	names := []string{}

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("import: listing plugin tables: %v", err)
		}
		names = append(names, name)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("import: listing plugin tables: %v", err)
	}

	tables := []importTable{}

	for _, name := range names {
		types, err := tableColumnTypes(target, name)
		if err != nil {
			// The plugin that owns this table is not installed on the target
			// yet, so there is nowhere to put its rows. Worth saying out loud:
			// the data is still in the source, and a second import after
			// installing the plugin will bring it across.
			fmt.Printf("  %s: no such table on the target — install the plugin there, then re-run the import to bring its rows across\n", name)
			continue
		}

		table := importTable{name: name}

		for column, kind := range types {
			switch {
			case kind == "BOOL" || kind == "BOOLEAN":
				table.booleans = append(table.booleans, column)
			case kind == "TIMESTAMP" || kind == "TIMESTAMPTZ" || kind == "DATETIME":
				table.dateTimes = append(table.dateTimes, column)
			}

			// Rows keyed by call id describe a specific call. Copying them when
			// the calls stay behind would be worse than dropping them: the
			// target's call ids restart from the beginning, so an old
			// transcript would surface on an unrelated new call.
			if column == "callId" {
				table.perCall = true
			}
		}

		sort.Strings(table.booleans)
		sort.Strings(table.dateTimes)

		tables = append(tables, table)
	}

	return tables, nil
}

// tableColumnTypes maps each column to the type the backend reports for it,
// which is how the plugin tables above learn what needs converting without
// anyone having to list it.
func tableColumnTypes(db *Database, table string) (map[string]string, error) {
	rows, err := db.Query(fmt.Sprintf("select * from `%s` where 1 = 0", table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns, err := rows.ColumnTypes()
	if err != nil {
		return nil, err
	}

	types := map[string]string{}
	for _, column := range columns {
		types[column.Name()] = strings.ToUpper(column.DatabaseTypeName())
	}

	return types, nil
}

// copyTable empties the target table and refills it from the source, returning
// the number of rows written. A table absent from either side is skipped.
func copyTable(tx *sql.Tx, source *Database, target *Database, table importTable) (uint, error) {
	// A table the source does not have is the one absence worth forgiving: an
	// older install predates it, so there is nothing to copy. Anything else —
	// the table missing from the *target*, a permission error, a dropped
	// connection — used to land here too and report "0 row(s)" next to a
	// cheerful "import complete", which is how a failed migration could look
	// like a successful import.
	if _, err := tableColumns(source, table.name); err != nil {
		return 0, nil
	}

	columns, err := sharedColumns(source, target, table.name)
	if err != nil {
		return 0, fmt.Errorf("import: cannot read %s on the target (a migration may have failed): %v", table.name, err)
	}

	if len(columns) == 0 {
		return 0, fmt.Errorf("import: %s has no columns in common between source and target", table.name)
	}

	if _, err := tx.Exec(target.formatQuery(fmt.Sprintf("delete from `%s`", table.name))); err != nil {
		return 0, fmt.Errorf("import: clearing %s: %v", table.name, err)
	}

	// Plugin tables are copied without an ORDER BY: their key column varies by
	// manifest, and a full replace does not care about row order.
	order := ""
	if table.key != "" {
		order = fmt.Sprintf(" order by `%s`", table.key)
	}

	rows, err := source.Query(fmt.Sprintf("select %s from `%s`%s",
		backtickList(columns), table.name, order))
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

		// Resuming after the target's highest id is right when the target's
		// calls came from this same source — an interrupted run picking up
		// where it stopped. It is badly wrong when they did not: an install
		// that has been ingesting on its own has its own ids, and skipping
		// "up to" them drops that many of the user's recorded calls without
		// a word. An operator who stood up the new database, ran it for a
		// week, then read the migration guide would have seen
		// "imported 0 call(s)" and "import complete".
		//
		// The two are told apart by asking whether the row at that id is the
		// same call on both sides.
		resumable, err := callsAgreeAt(source, target, importCallsTable.key, after)
		if err != nil {
			return err
		}

		if !resumable {
			var behind uint
			if err := source.QueryRow(fmt.Sprintf("select count(*) from `%s` where `%s` <= %d",
				table.name, table.key, after)).Scan(&behind); err != nil {
				return fmt.Errorf("import: inspecting source calls: %v", err)
			}

			return fmt.Errorf("import: the target already holds its own calls up to id %d, which do not come from "+
				"this file. Copying would have to resume after that id and would silently skip %d source call(s). "+
				"Import into a database whose calls table is empty, or empty it first", after, behind)
		}

		fmt.Printf("target already holds calls up to id %d from this file — resuming after it\n", after)
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
