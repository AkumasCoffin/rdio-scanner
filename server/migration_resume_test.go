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
	"errors"
	"testing"

	"github.com/go-sql-driver/mysql"
)

// MySQL and MariaDB commit DDL statement by statement, so a server stopped
// part way through a migration has the schema change and no ledger row. This
// puts a database in exactly that state — the column there, the migration
// unrecorded — and starts on it. It used to be fatal on every start, which a
// test cannot survive either: NewDatabase exits the process.
func TestAnInterruptedMigrationResumesOnMysql(t *testing.T) {
	config := testDatabaseConfigFromEnv(t, false)
	if config == nil || (config.DbType != DbTypeMysql && config.DbType != DbTypeMariadb) {
		t.Skip("needs RDIO_TEST_DB_TYPE=mysql or mariadb pointed at a disposable database")
	}

	db := NewDatabase(config)
	emptyTestDatabase(t, db)
	db.Sql.Close()

	db = NewDatabase(config)

	const interrupted = "20260822150000-patches-primary-talkgroup"

	result, err := db.Exec("delete from `rdioScannerMeta` where `name` = ?", interrupted)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := result.RowsAffected(); n != 1 {
		t.Fatalf("expected to rewind one ledger row for %s, rewound %d", interrupted, n)
	}
	db.Sql.Close()

	// The column is still there; only the record of adding it is gone.
	db = NewDatabase(config)
	defer db.Sql.Close()

	done, err := db.migrationDone(interrupted)
	if err != nil {
		t.Fatal(err)
	}
	if !done {
		t.Fatal("the interrupted migration was not recorded as complete after the restart")
	}
}

// Only the errors that mean "already that way", and only where DDL cannot be
// rolled back. Anywhere else the same error is a real conflict.
func TestAlreadyAppliedIsNarrow(t *testing.T) {
	duplicateColumn := &mysql.MySQLError{Number: 1060, Message: "Duplicate column name 'x'"}
	syntax := &mysql.MySQLError{Number: 1064, Message: "You have an error in your SQL syntax"}

	for _, tc := range []struct {
		dbType string
		err    error
		want   bool
	}{
		{DbTypeMariadb, duplicateColumn, true},
		{DbTypeMysql, duplicateColumn, true},
		{DbTypeMysql, &mysql.MySQLError{Number: 1050}, true},
		{DbTypeMysql, &mysql.MySQLError{Number: 1061}, true},
		{DbTypeMysql, &mysql.MySQLError{Number: 1091}, true},
		{DbTypeMariadb, syntax, false},
		{DbTypeMariadb, errors.New("duplicate column name: x"), false},
		{DbTypePostgres, duplicateColumn, false},
		{DbTypeSqlite, duplicateColumn, false},
	} {
		db := &Database{Config: &Config{DbType: tc.dbType}}

		if got := db.alreadyApplied(tc.err); got != tc.want {
			t.Errorf("%s, %v: alreadyApplied = %v, want %v", tc.dbType, tc.err, got, tc.want)
		}
	}
}
