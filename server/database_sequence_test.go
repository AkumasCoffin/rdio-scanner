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
	"errors"
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
