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
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lib/pq"
)

// Retrying an insert is only safe for one specific failure.
//
// A skewed sequence hands out a key that is already taken, and repairing it
// makes the same insert succeed. Every other unique violation means the data
// itself is duplicated — two accesses sharing a code, two systems sharing an
// id — and retrying that would either fail identically or, worse, quietly write
// a second row the operator never learns about. So the classification has to be
// exact, and it is the one piece of this that can be tested without a Postgres.

func TestOnlyAPrimaryKeyCollisionIsRetried(t *testing.T) {
	collision := &pq.Error{
		Code:       "23505",
		Constraint: "rdioScannerConfigs_pkey",
		Message:    `duplicate key value violates unique constraint "rdioScannerConfigs_pkey"`,
	}

	if !isSequenceCollision(collision, "rdioScannerConfigs") {
		t.Error("a primary key collision on the named table was not recognised")
	}
}

func TestADuplicateOnAnotherConstraintIsNotRetried(t *testing.T) {
	// The configs table has `key` unique as well as its primary key. A
	// collision there means two rows genuinely claim the same option name,
	// which no amount of sequence repair fixes.
	duplicate := &pq.Error{
		Code:       "23505",
		Constraint: "rdioScannerConfigs_key_key",
		Message:    `duplicate key value violates unique constraint "rdioScannerConfigs_key_key"`,
	}

	if isSequenceCollision(duplicate, "rdioScannerConfigs") {
		t.Error("a duplicate on the key column was mistaken for a skewed sequence")
	}
}

func TestAnotherTablesPrimaryKeyIsNotRetried(t *testing.T) {
	// The repair is per table, so acting on a collision reported for a
	// different one would realign a sequence that was never the problem.
	other := &pq.Error{
		Code:       "23505",
		Constraint: "rdioScannerGroups_pkey",
	}

	if isSequenceCollision(other, "rdioScannerConfigs") {
		t.Error("a collision in another table was accepted")
	}
}

func TestNonUniqueViolationsAreNotRetried(t *testing.T) {
	for _, err := range []error{
		&pq.Error{Code: "23503", Constraint: "rdioScannerConfigs_pkey"}, // foreign key
		&pq.Error{Code: "23502"},                                       // not null
		&pq.Error{Code: "42P01"},                                       // undefined table
		errors.New("connection refused"),
		nil,
	} {
		if isSequenceCollision(err, "rdioScannerConfigs") {
			t.Errorf("%v was treated as a skewed sequence", err)
		}
	}
}

// Sqlite and MySQL never reach the retry, but the classifier must not panic on
// their errors on the way past.
func TestOtherBackendsErrorsAreHandled(t *testing.T) {
	if isSequenceCollision(errors.New("UNIQUE constraint failed: rdioScannerConfigs._id"), "rdioScannerConfigs") {
		t.Error("a sqlite unique violation was treated as a postgres sequence collision")
	}
}

// Everything below needs a real Postgres, because it is about Postgres
// transaction semantics: a failed statement aborts the whole transaction, and
// no other backend does that.

func newTestPostgresDatabase(t *testing.T) *Database {
	t.Helper()

	config := testDatabaseConfigFromEnv(t, false)
	if config == nil || config.DbType != DbTypePostgres {
		t.Skip("needs RDIO_TEST_DB_TYPE=postgresql pointed at a disposable database")
	}

	db := NewDatabase(config)
	emptyTestDatabase(t, db)

	return NewDatabase(config)
}

// The configuration save runs every section inside one transaction, which is
// precisely where a skewed sequence used to be unfixable: the collision
// aborted the transaction, so the realign-and-retry could only come back with
// "current transaction is aborted". The savepoint is what makes the retry able
// to run at all — this is the scenario from the field, end to end.
func TestASkewedSequenceHealsInsideTheSavingTransaction(t *testing.T) {
	db := newTestPostgresDatabase(t)

	if _, err := db.Exec(
		"insert into `rdioScannerConfigs` (`_id`, `key`, `val`) values (?, ?, ?)",
		100, "test.skew", `"x"`,
	); err != nil {
		t.Fatalf("cannot plant the explicit-key row: %v", err)
	}

	// is_called = false makes the next nextval() hand out exactly 100, which
	// the planted row already holds.
	if _, err := db.Sql.Exec(
		`select setval(pg_get_serial_sequence('"rdioScannerConfigs"', '_id'), 100, false)`,
	); err != nil {
		t.Fatalf("cannot skew the sequence: %v", err)
	}

	err := db.WithTx(func(tx *Database) error {
		return tx.ExecInsert(
			"rdioScannerConfigs", "_id",
			"insert into `rdioScannerConfigs` (`key`, `val`) values (?, ?)",
			"test.new", `"y"`,
		)
	})
	if err != nil {
		t.Fatalf("the insert did not survive a skewed sequence inside a transaction: %v", err)
	}

	var count int
	if err := db.QueryRow(
		"select count(*) from `rdioScannerConfigs` where `key` = ?", "test.new",
	).Scan(&count); err != nil || count != 1 {
		t.Fatalf("the retried insert never committed (count %d, err %v)", count, err)
	}
}

// A genuine duplicate — here, two rows claiming the same option name — must
// come back as itself, and must not leave the caller's transaction aborted so
// that every later statement drowns it in "current transaction is aborted".
func TestAGenuineDuplicateSurvivesTheTransactionIntact(t *testing.T) {
	db := newTestPostgresDatabase(t)

	if _, err := db.Exec(
		"insert into `rdioScannerConfigs` (`key`, `val`) values (?, ?)",
		"test.dup", `"x"`,
	); err != nil {
		t.Fatalf("cannot plant the original row: %v", err)
	}

	err := db.WithTx(func(tx *Database) error {
		dupErr := tx.ExecInsert(
			"rdioScannerConfigs", "_id",
			"insert into `rdioScannerConfigs` (`key`, `val`) values (?, ?)",
			"test.dup", `"y"`,
		)
		if dupErr == nil {
			t.Error("a duplicate option name was accepted")
		} else if !strings.Contains(dupErr.Error(), "rdioScannerConfigs_key_key") {
			t.Errorf("the duplicate surfaced as %q; want the key constraint by name", dupErr)
		}

		// The transaction has to still be usable after the failure.
		_, err := tx.Exec(
			"update `rdioScannerConfigs` set `val` = ? where `key` = ?", `"z"`, "test.dup",
		)
		return err
	})
	if err != nil {
		t.Fatalf("the transaction did not survive the duplicate: %v", err)
	}
}

// The nextval walk is the realignment of last resort, for roles that can use
// a sequence but not setval it. Privileges cannot be arranged from inside the
// test suite, so this covers the walk's own arithmetic: it must leave the
// sequence handing out keys past the table's highest, not at it.
func TestTheNextvalWalkClearsTheTable(t *testing.T) {
	db := newTestPostgresDatabase(t)

	if _, err := db.Exec(
		"insert into `rdioScannerConfigs` (`_id`, `key`, `val`) values (?, ?, ?)",
		50, "test.walk", `"x"`,
	); err != nil {
		t.Fatalf("cannot plant the explicit-key row: %v", err)
	}

	seq, err := db.sequenceFor("rdioScannerConfigs", "_id")
	if err != nil || seq == "" {
		t.Fatalf("cannot resolve the sequence (%q, %v)", seq, err)
	}

	if _, err := db.Sql.Exec(
		fmt.Sprintf(`select setval('%s'::regclass, 3, true)`, seq),
	); err != nil {
		t.Fatalf("cannot skew the sequence: %v", err)
	}

	if err := db.advanceSequence(seq, 50); err != nil {
		t.Fatalf("the walk failed: %v", err)
	}

	var id int
	if err := db.Sql.QueryRow(
		`insert into "rdioScannerConfigs" ("key", "val") values ('test.walked', '"y"') returning "_id"`,
	).Scan(&id); err != nil {
		t.Fatalf("the insert still collides after the walk: %v", err)
	}
	if id != 51 {
		t.Errorf("the walked sequence handed out %d; want 51", id)
	}
}

// A schema that came through an external migration tool often has the column
// default drawing from a sequence that ownership never got attached to.
// pg_get_serial_sequence returns NULL there, and setval(NULL) succeeds while
// touching nothing — so the repair has to find the sequence through the
// default expression instead.
func TestAnUnownedSequenceIsStillRealigned(t *testing.T) {
	db := newTestPostgresDatabase(t)

	for _, q := range []string{
		`drop table if exists "testUnowned"`,
		`drop sequence if exists "testUnownedSeq"`,
		`create sequence "testUnownedSeq"`,
		`create table "testUnowned" ("_id" integer primary key default nextval('"testUnownedSeq"'::regclass), "val" text)`,
	} {
		if _, err := db.Sql.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	t.Cleanup(func() {
		db.Sql.Exec(`drop table if exists "testUnowned"`)
		db.Sql.Exec(`drop sequence if exists "testUnownedSeq"`)
	})

	// The premise: this really is the ownership gap, not a serial in disguise.
	var owned sql.NullString
	if err := db.Sql.QueryRow(
		`select pg_get_serial_sequence('"testUnowned"', '_id')`,
	).Scan(&owned); err != nil || owned.Valid {
		t.Fatalf("expected no owned sequence (got %v, err %v)", owned, err)
	}

	if _, err := db.Sql.Exec(`insert into "testUnowned" ("_id", "val") values (5, 'x')`); err != nil {
		t.Fatalf("cannot plant the explicit-key row: %v", err)
	}

	moved, err := db.repairSequence("testUnowned", "_id")
	if err != nil {
		t.Fatalf("repair failed: %v", err)
	}
	if moved == "" {
		t.Error("the repair reported nothing to do against a skewed unowned sequence")
	}

	var id int
	if err := db.Sql.QueryRow(
		`insert into "testUnowned" ("val") values ('y') returning "_id"`,
	).Scan(&id); err != nil {
		t.Fatalf("the default-key insert still collides after repair: %v", err)
	}
	if id != 6 {
		t.Errorf("the realigned sequence handed out %d; want 6", id)
	}
}
