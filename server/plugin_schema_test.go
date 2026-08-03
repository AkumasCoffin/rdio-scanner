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
	"strings"
	"testing"
)

// The plugin registry shipped on MySQL and MariaDB without the `commit` column
// that SQLite and Postgres both declared. Plugins.Read selects it, so the read
// failed on every boot and Controller.Start skipped the entire plugin block —
// no auto-install, no plugins, one log line. Because 6.14 had just moved
// transcription into a plugin, the symptom users actually saw was transcripts
// vanishing after an upgrade.
//
// It shipped because nothing runs the schema on anything but SQLite. This test
// does not need a server of each kind: the columns a backend declares are
// readable straight out of the DDL.
func TestPluginRegistrySchemaMatchesOnEveryBackend(t *testing.T) {
	for _, dbType := range []string{DbTypeSqlite, DbTypePostgres, DbTypeMariadb, DbTypeMysql} {
		queries := pluginsTableSql(dbType)
		if len(queries) != 1 {
			t.Fatalf("%s: expected one create statement, got %d", dbType, len(queries))
		}

		ddl := queries[0]

		for _, column := range pluginRegistryColumns {
			// Both quoting styles, since Postgres uses double quotes.
			if !strings.Contains(ddl, "`"+column+"`") && !strings.Contains(ddl, `"`+column+`"`) {
				t.Errorf("%s: the registry schema does not declare %q, which Plugins.Read selects", dbType, column)
			}
		}
	}
}

// Re-running a migration must not be the thing that stops the server booting.
// The MySQL branch used a bare `create table`, and MySQL commits DDL
// implicitly, so a failure between the create and the ledger insert left the
// table present and the migration unrecorded — and every later boot then died
// on "table already exists" with no way forward.
func TestPluginRegistryCreateIsIdempotent(t *testing.T) {
	for _, dbType := range []string{DbTypeSqlite, DbTypePostgres, DbTypeMariadb, DbTypeMysql} {
		for _, query := range pluginsTableSql(dbType) {
			if !strings.Contains(strings.ToLower(query), "create table if not exists") {
				t.Errorf("%s: %q is not re-runnable; a crash between the DDL and the ledger insert bricks every later boot", dbType, query)
			}
		}
	}
}

// The forward migration exists so installs already created without the column
// gain it. It has to be wired into the migration list to do anything at all.
func TestCommitColumnMigrationIsRegistered(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	done, err := db.migrationDone("20260803090000-plugins-commit-column")
	if err != nil {
		t.Fatalf("cannot read the migration ledger: %v", err)
	}
	if !done {
		t.Fatal("the commit-column migration did not run during database setup, so an existing MySQL install would never gain the column")
	}

	// And the column really is selectable afterwards, which is the thing
	// Plugins.Read needs.
	present, err := db.columnPresent("rdioScannerPlugins", "commit")
	if err != nil {
		t.Fatalf("cannot probe the column: %v", err)
	}
	if !present {
		t.Fatal("the registry has no commit column after migration")
	}
}

// columnPresent has to tell "the column is not there" apart from "the question
// could not be answered". The transcripts migration records three steps as
// complete based on this answer, so collapsing both to false meant a transient
// database error could permanently strand a server's transcripts.
func TestColumnPresentSeparatesAbsenceFromFailure(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	present, err := db.columnPresent("rdioScannerPlugins", "pluginId")
	if err != nil || !present {
		t.Fatalf("an existing column read as present=%v err=%v", present, err)
	}

	present, err = db.columnPresent("rdioScannerPlugins", "noSuchColumn")
	if err != nil {
		t.Fatalf("a genuinely absent column reported an error: %v", err)
	}
	if present {
		t.Fatal("a column that does not exist reported as present")
	}

	// A table that cannot be read at all is a failure, not an absence — this is
	// the case that must never be mistaken for "nothing to migrate".
	if _, err = db.columnPresent("noSuchTable", "whatever"); err == nil {
		t.Fatal("an unreadable table reported absence rather than an error")
	}
}
