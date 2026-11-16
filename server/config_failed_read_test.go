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
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// A database that refuses every query, standing in for one that is briefly
// unreachable: a restart, a failover, SQLite held busy by a long prune.
func unreachable(t *testing.T, db *Database) *Database {
	t.Helper()

	closed, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()

	return &Database{Config: db.Config, Sql: closed, DateTimeFormat: db.DateTimeFormat}
}

// Every one of these lists is written back by deleting the rows the list does
// not hold. A read that failed used to leave the list empty — it was cleared
// before the query ran — so the failure itself lost nothing, and the next
// write lost everything: with auto-populate on, the next upload wrote a list
// holding one new system and every other system, talkgroup and unit went.
func TestAFailedReadLeavesTheConfigInMemoryAlone(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	down := unreachable(t, db)

	groups := NewGroups()
	groups.FromMap([]any{map[string]any{"_id": float64(101), "label": "Fire"}, map[string]any{"_id": float64(102), "label": "Law"}})
	if err := groups.Write(db); err != nil {
		t.Fatal(err)
	}
	if err := groups.Read(db); err != nil {
		t.Fatal(err)
	}
	before := len(groups.List)

	if err := groups.Read(down); err == nil {
		t.Fatal("reading groups from an unreachable database reported success")
	}
	if len(groups.List) != before {
		t.Fatalf("a failed read left %d groups in memory, want the %d that were there", len(groups.List), before)
	}

	tags := NewTags()
	if err := tags.Read(db); err != nil {
		t.Fatal(err)
	}
	beforeTags := len(tags.List)
	if tags.Read(down) == nil || len(tags.List) != beforeTags {
		t.Fatalf("a failed read left %d tags in memory, want %d", len(tags.List), beforeTags)
	}

	systems := NewSystems()
	systems.FromMap([]any{map[string]any{
		"id": float64(1), "label": "County",
		"talkgroups": []any{map[string]any{"id": float64(100), "label": "Dispatch", "name": "Dispatch"}},
	}})
	if err := systems.Write(db); err != nil {
		t.Fatal(err)
	}
	if err := systems.Read(db); err != nil {
		t.Fatal(err)
	}
	if len(systems.List) != 1 || len(systems.List[0].Talkgroups.List) != 1 {
		t.Fatalf("the seeded system did not read back: %d systems", len(systems.List))
	}

	if err := systems.Read(down); err == nil {
		t.Fatal("reading systems from an unreachable database reported success")
	}
	if len(systems.List) != 1 || len(systems.List[0].Talkgroups.List) != 1 {
		t.Fatal("a failed read emptied the systems held in memory")
	}

	// And so the write that follows deletes nothing.
	if err := systems.Write(db); err != nil {
		t.Fatal(err)
	}
	var talkgroups int
	if err := db.QueryRow("select count(*) from `rdioScannerTalkgroups`").Scan(&talkgroups); err != nil {
		t.Fatal(err)
	}
	if talkgroups != 1 {
		t.Fatalf("%d talkgroups left in the database after a failed read and the next write, want 1", talkgroups)
	}
}

// A server with access codes is closed to anyone without one. Whether it has
// any is asked of the list in memory, so a failed re-read that emptied the
// list opened the server to everyone until it was restarted.
func TestAFailedReadDoesNotOpenARestrictedServer(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	accesses := NewAccesses()
	accesses.FromMap([]any{map[string]any{"code": "secret", "ident": "members", "systems": "*"}})
	if err := accesses.Write(db); err != nil {
		t.Fatal(err)
	}
	if err := accesses.Read(db); err != nil {
		t.Fatal(err)
	}

	if err := accesses.Read(unreachable(t, db)); err == nil {
		t.Fatal("reading access codes from an unreachable database reported success")
	}
	if !accesses.IsRestricted() {
		t.Fatal("a failed read of the access codes left the server open to everyone")
	}
}

// Options used to fall back to their defaults on any read error, and report
// success: the admin password became "rdio-scanner" and retention seven days.
func TestAFailedReadLeavesTheOptionsAlone(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	hash, err := bcrypt.GenerateFromPassword([]byte("a-real-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}

	options := NewOptions()
	if err := options.Read(db); err != nil {
		t.Fatal(err)
	}
	options.adminPassword = string(hash)
	options.PruneDays = 365
	if err := options.Write(db); err != nil {
		t.Fatal(err)
	}
	if err := options.Read(db); err != nil {
		t.Fatal(err)
	}
	if options.PruneDays != 365 {
		t.Fatalf("the saved retention did not read back: %d", options.PruneDays)
	}

	if err := options.Read(unreachable(t, db)); err == nil {
		t.Fatal("reading options from an unreachable database reported success")
	}

	if options.PruneDays != 365 {
		t.Fatalf("a failed read changed retention to %d days", options.PruneDays)
	}
	if bcrypt.CompareHashAndPassword([]byte(options.adminPassword), []byte("rdio-scanner")) == nil {
		t.Fatal("a failed read reset the admin password to the default")
	}
	if bcrypt.CompareHashAndPassword([]byte(options.adminPassword), []byte("a-real-password")) != nil {
		t.Fatal("a failed read lost the admin password")
	}
}
