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

// newTestDatabase opens a throwaway SQLite database with every migration
// applied, which is the state a real server starts from.
func newTestDatabase(t *testing.T) *Database {
	t.Helper()

	dir := t.TempDir()

	// Relative on purpose: GetDbFilePath uses path.IsAbs, which does not
	// recognise a Windows absolute path and would join it onto BaseDir.
	config := &Config{
		BaseDir: dir,
		DbType:  DbTypeSqlite,
		DbFile:  "test.db",
	}

	return NewDatabase(config)
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

	if db.columnExists("rdioScannerCalls", "transcript") {
		t.Fatal("the core transcript column survived the migration")
	}

	// The legacy option rows stay: they are how the server knows this install
	// used to have transcription and should be offered the plugin.
	if got := countRows(t, db,
		"select count(*) from `rdioScannerConfigs` where `key` = 'option.transcriptionEnabled'"); got != 1 {
		t.Fatal("the legacy option row was removed; nothing would signal that this install had transcription")
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

	// Roll back only the drop step, as a crash at that point would.
	if _, err := db.Sql.Exec("delete from `rdioScannerMeta` where `name` = ?", transcriptMigrationDrop); err != nil {
		t.Fatalf("cannot rewind the drop step: %v", err)
	}
	if _, err := db.Sql.Exec("alter table `rdioScannerCalls` add column `transcript` text"); err != nil {
		t.Fatalf("cannot restore the column: %v", err)
	}

	if err := db.migrationTranscriptsToPlugin(false); err != nil {
		t.Fatalf("resumed run failed: %v", err)
	}

	if db.columnExists("rdioScannerCalls", "transcript") {
		t.Fatal("the resumed run did not complete the drop")
	}
	if got := countRows(t, db, "select count(*) from `plugin_transcripts_calls`"); got != 1 {
		t.Fatalf("the resumed run changed the data: %d rows", got)
	}
}

// TestTranscriptMigrationFreshInstall covers a server that never had
// transcripts: the steps are recorded so it never looks half-migrated, and
// nothing is created that shouldn't be.
func TestTranscriptMigrationFreshInstall(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	// A fresh database has already been through the migration during
	// NewDatabase, so every step should be recorded and the column gone.
	if db.columnExists("rdioScannerCalls", "transcript") {
		t.Fatal("a fresh install still has the core transcript column")
	}

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
