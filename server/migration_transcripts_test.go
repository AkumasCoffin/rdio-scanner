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
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// newTestDatabase opens a throwaway SQLite database with every migration
// applied, which is the state a real server starts from.
func newTestDatabase(t *testing.T) *Database {
	t.Helper()
	return newTestDatabaseWithDrops(t, false)
}

// newTestDatabaseWithDrops is the same, with the opt-in legacy column removal
// enabled — the mode an operator chooses when they can afford the rewrite.
func newTestDatabaseWithDrops(t *testing.T, dropLegacy bool) *Database {
	t.Helper()

	// Point the suite at a real server and every one of these tests runs
	// against it instead of SQLite.
	//
	// This exists because the plugin registry shipped broken on MySQL for
	// months and nothing noticed: the schema only ever ran on SQLite, so two
	// of the three supported backends were verified by reading. The static
	// column-parity check catches a branch that declares the wrong columns;
	// only a real server catches a statement that backend will not accept.
	//
	//	RDIO_TEST_DB_TYPE=postgresql 	//	RDIO_TEST_DB_HOST=localhost RDIO_TEST_DB_PORT=5432 	//	RDIO_TEST_DB_NAME=rdio_test RDIO_TEST_DB_USER=rdio RDIO_TEST_DB_PASS=... 	//	go test ./server -run 'Migration|Plugin|Transcript'
	//
	// The named database is emptied at the start of each test, so it must be
	// one kept for testing and nothing else.
	if config := testDatabaseConfigFromEnv(t, dropLegacy); config != nil {
		db := NewDatabase(config)
		emptyTestDatabase(t, db)
		return db
	}

	dir := t.TempDir()

	// Relative on purpose: GetDbFilePath uses path.IsAbs, which does not
	// recognise a Windows absolute path and would join it onto BaseDir.
	config := &Config{
		BaseDir:           dir,
		DbType:            DbTypeSqlite,
		DbFile:            "test.db",
		DropLegacyColumns: dropLegacy,
	}

	return NewDatabase(config)
}

// testDatabaseConfigFromEnv builds a config for a real server, or nil when the
// suite has not been pointed at one.
func testDatabaseConfigFromEnv(t *testing.T, dropLegacy bool) *Config {
	t.Helper()

	dbType := strings.TrimSpace(os.Getenv("RDIO_TEST_DB_TYPE"))
	if dbType == "" {
		return nil
	}

	switch dbType {
	case DbTypePostgres, DbTypeMysql, DbTypeMariadb:
	default:
		t.Fatalf("RDIO_TEST_DB_TYPE is %q; expected %s, %s or %s",
			dbType, DbTypePostgres, DbTypeMysql, DbTypeMariadb)
	}

	port := uint(5432)
	if dbType != DbTypePostgres {
		port = 3306
	}
	if text := os.Getenv("RDIO_TEST_DB_PORT"); text != "" {
		parsed, err := strconv.ParseUint(text, 10, 32)
		if err != nil {
			t.Fatalf("RDIO_TEST_DB_PORT is not a number: %v", err)
		}
		port = uint(parsed)
	}

	host := os.Getenv("RDIO_TEST_DB_HOST")
	if host == "" {
		host = "localhost"
	}

	name := os.Getenv("RDIO_TEST_DB_NAME")
	if name == "" {
		t.Fatal("RDIO_TEST_DB_NAME is required when RDIO_TEST_DB_TYPE is set")
	}

	return &Config{
		BaseDir:           t.TempDir(),
		DbType:            dbType,
		DbHost:            host,
		DbPort:            port,
		DbName:            name,
		DbUsername:        os.Getenv("RDIO_TEST_DB_USER"),
		DbPassword:        os.Getenv("RDIO_TEST_DB_PASS"),
		DropLegacyColumns: dropLegacy,
	}
}

// emptyTestDatabase drops everything rdio owns, so each test starts from
// nothing the way a SQLite temp file does.
func emptyTestDatabase(t *testing.T, db *Database) {
	t.Helper()

	rows, err := db.Sql.Query(testTableListQuery(db))
	if err != nil {
		t.Fatalf("cannot list tables: %v", err)
	}

	names := []string{}

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatalf("cannot read table name: %v", err)
		}
		// Only rdio's own tables and plugin tables. Anything else in the
		// database belongs to whoever put it there.
		if strings.HasPrefix(name, "rdioScanner") || strings.HasPrefix(name, "plugin_") {
			names = append(names, name)
		}
	}
	rows.Close()

	for _, name := range names {
		quoted := db.formatQuery("drop table if exists `" + name + "` cascade")
		if db.Config.DbType != DbTypePostgres {
			quoted = db.formatQuery("drop table if exists `" + name + "`")
		}
		if _, err := db.Sql.Exec(quoted); err != nil {
			t.Fatalf("cannot drop %s: %v", name, err)
		}
	}
}

func testTableListQuery(db *Database) string {
	if db.Config.DbType == DbTypePostgres {
		return "select tablename from pg_tables where schemaname = current_schema()"
	}
	return "select table_name from information_schema.tables where table_schema = database()"
}

// rewindTranscriptMigration puts a database back into the shape it had before
// transcripts moved out of core: the column present, the migration unrecorded.
// This is how the test reproduces an upgrade rather than a fresh install.
func rewindTranscriptMigration(t *testing.T, db *Database) {
	t.Helper()

	for _, name := range []string{
		transcriptMigrationTables,
		transcriptMigrationCopy,
		transcriptMigrationVerify,
		transcriptMigrationDrop,
	} {
		if _, err := db.Sql.Exec("delete from `rdioScannerMeta` where `name` = ?", name); err != nil {
			t.Fatalf("cannot rewind %s: %v", name, err)
		}
	}

	for _, query := range []string{
		"drop table if exists `plugin_transcripts_calls`",
		"drop table if exists `plugin_transcripts_systems`",
		"drop table if exists `plugin_transcripts_talkgroups`",
		"drop table if exists `plugin_transcripts_config`",
	} {
		if _, err := db.Sql.Exec(query); err != nil {
			t.Fatalf("cannot drop plugin table: %v", err)
		}
	}

	// The real migration will have dropped these already.
	if !db.columnExists("rdioScannerCalls", "transcript") {
		if _, err := db.Sql.Exec("alter table `rdioScannerCalls` add column `transcript` text"); err != nil {
			t.Fatalf("cannot restore transcript column: %v", err)
		}
	}
	if !db.columnExists("rdioScannerSystems", "transcribe") {
		if _, err := db.Sql.Exec("alter table `rdioScannerSystems` add column `transcribe` boolean default 1"); err != nil {
			t.Fatalf("cannot restore systems.transcribe: %v", err)
		}
	}
	if !db.columnExists("rdioScannerTalkgroups", "transcribe") {
		if _, err := db.Sql.Exec("alter table `rdioScannerTalkgroups` add column `transcribe` boolean default 1"); err != nil {
			t.Fatalf("cannot restore talkgroups.transcribe: %v", err)
		}
	}
}

func seedCallWithTranscript(t *testing.T, db *Database, id int, transcript string) {
	t.Helper()

	_, err := db.Sql.Exec(
		"insert into `rdioScannerCalls` (`id`, `audio`, `dateTime`, `frequencies`, `patches`, `sources`, `system`, `talkgroup`, `transcript`) "+
			"values (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		id, []byte("audio"), time.Now().UTC().Format("2006-01-02 15:04:05.000 -07:00"),
		"[]", "[]", "[]", 1, 100, transcript,
	)
	if err != nil {
		t.Fatalf("cannot seed call %d: %v", id, err)
	}
}

// seedBulkCalls inserts transcript-less filler rows, to push the calls table
// past the size where the migration stops dropping columns on its own.
func seedBulkCalls(t *testing.T, db *Database, firstId int, count int) {
	t.Helper()

	tx, err := db.Sql.Begin()
	if err != nil {
		t.Fatalf("cannot begin: %v", err)
	}

	stmt, err := tx.Prepare(
		"insert into `rdioScannerCalls` (`id`, `audio`, `dateTime`, `frequencies`, `patches`, `sources`, `system`, `talkgroup`) " +
			"values (?, ?, ?, ?, ?, ?, ?, ?)",
	)
	if err != nil {
		t.Fatalf("cannot prepare: %v", err)
	}

	now := time.Now().UTC().Format("2006-01-02 15:04:05.000 -07:00")
	for i := 0; i < count; i++ {
		if _, err := stmt.Exec(firstId+i, []byte("a"), now, "[]", "[]", "[]", 1, 100); err != nil {
			t.Fatalf("cannot seed filler call: %v", err)
		}
	}

	stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatalf("cannot commit: %v", err)
	}
}

func countRows(t *testing.T, db *Database, query string) int {
	t.Helper()

	var count int
	if err := db.Sql.QueryRow(query).Scan(&count); err != nil {
		t.Fatalf("count failed for %q: %v", query, err)
	}
	return count
}

// TestTranscriptMigrationMovesData is the case that matters most: an existing
// server with years of transcripts must come out the other side with all of
// them, and with the old column gone.
func TestTranscriptMigrationMovesData(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	rewindTranscriptMigration(t, db)

	seedCallWithTranscript(t, db, 1, "engine one responding")
	seedCallWithTranscript(t, db, 2, "copy that")
	// A call with no transcript must not produce a row.
	seedCallWithTranscript(t, db, 3, "")

	if _, err := db.Sql.Exec(
		"insert into `rdioScannerConfigs` (`key`, `val`) values ('option.transcriptionEnabled', 'true')",
	); err != nil {
		t.Fatalf("cannot seed option: %v", err)
	}

	if err := db.migrationTranscriptsToPlugin(false); err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	if got := countRows(t, db, "select count(*) from `plugin_transcripts_calls`"); got != 2 {
		t.Fatalf("expected 2 transcripts moved, got %d", got)
	}

	var transcript string
	if err := db.Sql.QueryRow(
		"select `transcript` from `plugin_transcripts_calls` where `callId` = 1",
	).Scan(&transcript); err != nil {
		t.Fatalf("cannot read migrated transcript: %v", err)
	}
	if transcript != "engine one responding" {
		t.Fatalf("transcript text changed in transit: %q", transcript)
	}

	// The setting has to arrive under the plugin's own name.
	var value string
	if err := db.Sql.QueryRow(
		"select `value` from `plugin_transcripts_config` where `key` = 'enabled'",
	).Scan(&value); err != nil {
		t.Fatalf("transcription setting was not migrated: %v", err)
	}
	if value != "true" {
		t.Fatalf("setting value changed in transit: %q", value)
	}

	// The legacy option rows stay: they are how the server knows this install
	// used to have transcription and should be offered the plugin.
	if got := countRows(t, db,
		"select count(*) from `rdioScannerConfigs` where `key` = 'option.transcriptionEnabled'"); got != 1 {
		t.Fatal("the legacy option row was removed; nothing would signal that this install had transcription")
	}
}

// TestTranscriptMigrationDropsColumnsWhenAsked covers the opt-in path, where an
// operator has accepted the table rewrite in exchange for the space.
func TestTranscriptMigrationDropsColumnsWhenAsked(t *testing.T) {
	db := newTestDatabaseWithDrops(t, true)
	defer db.Sql.Close()

	rewindTranscriptMigration(t, db)
	seedCallWithTranscript(t, db, 1, "engine one responding")

	if err := db.migrationTranscriptsToPlugin(false); err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	if got := countRows(t, db, "select count(*) from `plugin_transcripts_calls`"); got != 1 {
		t.Fatalf("expected 1 transcript moved, got %d", got)
	}

	for _, column := range []string{"transcript"} {
		if db.columnExists("rdioScannerCalls", column) {
			t.Fatalf("rdioScannerCalls.%s survived an explicit drop", column)
		}
	}
	if db.columnExists("rdioScannerSystems", "transcribe") {
		t.Fatal("rdioScannerSystems.transcribe survived an explicit drop")
	}
	if db.columnExists("rdioScannerTalkgroups", "transcribe") {
		t.Fatal("rdioScannerTalkgroups.transcribe survived an explicit drop")
	}
}

// TestTranscriptMigrationDefersDropOnLargeTable is the case that protects a
// live database: a table big enough for the rewrite to hurt is left alone, and
// the step stays available so an operator can run it deliberately.
func TestTranscriptMigrationDefersDropOnLargeTable(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	rewindTranscriptMigration(t, db)
	seedCallWithTranscript(t, db, 1, "deferred")

	// Stand in for a large table without inserting ten thousand rows.
	seedBulkCalls(t, db, 2, transcriptDropAutoThreshold+1)

	if err := db.migrationTranscriptsToPlugin(false); err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	if !db.columnExists("rdioScannerCalls", "transcript") {
		t.Fatal("a large table had its column dropped without being asked")
	}

	done, err := db.migrationDone(transcriptMigrationDrop)
	if err != nil {
		t.Fatalf("cannot check the drop step: %v", err)
	}
	if done {
		t.Fatal("the drop was recorded as done despite being skipped, so it could never run later")
	}

	// Opting in on a later start completes it.
	db.Config.DropLegacyColumns = true
	if err := db.migrationTranscriptsToPlugin(false); err != nil {
		t.Fatalf("opt-in run failed: %v", err)
	}
	if db.columnExists("rdioScannerCalls", "transcript") {
		t.Fatal("the deferred drop did not run once enabled")
	}
	if got := countRows(t, db, "select count(*) from `plugin_transcripts_calls`"); got != 1 {
		t.Fatalf("the deferred drop changed the migrated data: %d rows", got)
	}
}

// TestTranscriptMigrationIsIdempotent covers the resume path. MySQL commits
// every DDL statement on the spot, so a crash part way through is a real
// scenario and re-running must not duplicate or fail.
func TestTranscriptMigrationIsIdempotent(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	rewindTranscriptMigration(t, db)
	seedCallWithTranscript(t, db, 1, "first")

	if err := db.migrationTranscriptsToPlugin(false); err != nil {
		t.Fatalf("first run failed: %v", err)
	}
	if err := db.migrationTranscriptsToPlugin(false); err != nil {
		t.Fatalf("second run failed: %v", err)
	}

	if got := countRows(t, db, "select count(*) from `plugin_transcripts_calls`"); got != 1 {
		t.Fatalf("re-running the migration changed the row count to %d", got)
	}
}

// TestTranscriptMigrationResumesAfterPartialCopy simulates a crash between the
// copy and the drop: the copy is recorded, the drop is not.
func TestTranscriptMigrationResumesAfterPartialCopy(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	rewindTranscriptMigration(t, db)
	seedCallWithTranscript(t, db, 1, "interrupted")

	if err := db.migrationTranscriptsToPlugin(false); err != nil {
		t.Fatalf("initial run failed: %v", err)
	}

	// Roll back the verify and drop steps, as a crash at that point would, and
	// re-run. The copy is already recorded, so this exercises resuming into the
	// verification rather than repeating the copy.
	for _, name := range []string{transcriptMigrationVerify, transcriptMigrationDrop} {
		if _, err := db.Sql.Exec("delete from `rdioScannerMeta` where `name` = ?", name); err != nil {
			t.Fatalf("cannot rewind %s: %v", name, err)
		}
	}

	if err := db.migrationTranscriptsToPlugin(false); err != nil {
		t.Fatalf("resumed run failed: %v", err)
	}

	if got := countRows(t, db, "select count(*) from `plugin_transcripts_calls`"); got != 1 {
		t.Fatalf("the resumed run changed the data: %d rows", got)
	}

	done, err := db.migrationDone(transcriptMigrationVerify)
	if err != nil {
		t.Fatalf("cannot check the verify step: %v", err)
	}
	if !done {
		t.Fatal("the resumed run did not complete verification")
	}
}

// TestTranscriptMigrationFreshInstall covers a server that never had
// transcripts: the steps are recorded so it never looks half-migrated, and
// nothing is created that shouldn't be.
func TestTranscriptMigrationFreshInstall(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	// A fresh database has already been through the migration during
	// NewDatabase. There is nothing to move, so every step is recorded —
	// including the drop, which has no work to do and must not leave the
	// install looking half-migrated forever.
	for _, name := range []string{
		transcriptMigrationTables,
		transcriptMigrationCopy,
		transcriptMigrationVerify,
		transcriptMigrationDrop,
	} {
		done, err := db.migrationDone(name)
		if err != nil {
			t.Fatalf("cannot check %s: %v", name, err)
		}
		if !done {
			t.Fatalf("%s was not recorded on a fresh install", name)
		}
	}

	if got := countRows(t, db, "select count(*) from `plugin_transcripts_calls`"); got != 0 {
		t.Fatalf("a fresh install created %d transcript rows", got)
	}
}
