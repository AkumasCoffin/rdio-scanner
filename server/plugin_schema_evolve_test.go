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

// `create table if not exists` is a no-op against a table that already exists,
// so a column a plugin added in v2 appeared on fresh installs and never on
// existing ones. Every insert naming it then failed at runtime, and the only
// recovery was a purge that destroys the data. Indexes on existing tables were
// being created all along, so half the schema evolved and half did not.
func TestPluginSchemaGainsColumnsOnUpdate(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	v1 := &PluginManifest{
		Id: "notes",
		Tables: []PluginTable{{
			Name: "entries",
			Columns: []PluginColumn{
				{Name: "callId", Type: "int", PrimaryKey: true},
				{Name: "body", Type: "text"},
			},
		}},
	}

	if err := CreatePluginSchema(db, v1); err != nil {
		t.Fatalf("creating the initial schema failed: %v", err)
	}

	table := v1.TableName("entries")

	// Data the update must not disturb.
	if _, err := db.Exec("insert into `" + table + "` (`callId`, `body`) values (1, 'first')"); err != nil {
		t.Fatal(err)
	}

	// v2 adds a column and an index on it.
	v2 := &PluginManifest{
		Id: "notes",
		Tables: []PluginTable{{
			Name: "entries",
			Columns: []PluginColumn{
				{Name: "callId", Type: "int", PrimaryKey: true},
				{Name: "body", Type: "text"},
				{Name: "author", Type: "text", Index: true},
			},
		}},
	}

	if err := CreatePluginSchema(db, v2); err != nil {
		t.Fatalf("updating the schema failed: %v", err)
	}

	present, err := db.columnPresent(table, "author")
	if err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Fatal("the column added in v2 was never created; every insert naming it would fail at runtime")
	}

	// The new column is usable, and the existing row survived.
	if _, err = db.Exec("insert into `" + table + "` (`callId`, `body`, `author`) values (2, 'second', 'alice')"); err != nil {
		t.Fatalf("the new column is not usable: %v", err)
	}

	var count int
	if err = db.QueryRow("select count(*) from `" + table + "`").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("the table holds %d rows, expected 2 — the update lost data", count)
	}

	var body string
	if err = db.QueryRow("select `body` from `" + table + "` where `callId` = 1").Scan(&body); err != nil {
		t.Fatal(err)
	}
	if body != "first" {
		t.Fatalf("the pre-existing row reads %q", body)
	}

	// Re-running is a no-op, which is what lets this run on every start
	// without a ledger — MySQL auto-commits DDL and cannot roll a batch back,
	// so repeatability is the only safe design.
	if err = CreatePluginSchema(db, v2); err != nil {
		t.Fatalf("re-running the schema failed: %v", err)
	}
}

// A key cannot be introduced to a table that already has rows on any backend we
// support. Saying so beats leaving the table subtly wrong — the plugin card
// shows this, which is the only way an admin would find out.
func TestAddingAPrimaryKeyColumnIsRefusedClearly(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	v1 := &PluginManifest{
		Id:     "keyed",
		Tables: []PluginTable{{Name: "t", Columns: []PluginColumn{{Name: "a", Type: "int", PrimaryKey: true}}}},
	}
	if err := CreatePluginSchema(db, v1); err != nil {
		t.Fatal(err)
	}

	v2 := &PluginManifest{
		Id: "keyed",
		Tables: []PluginTable{{Name: "t", Columns: []PluginColumn{
			{Name: "a", Type: "int", PrimaryKey: true},
			{Name: "b", Type: "int", PrimaryKey: true},
		}}},
	}

	err := CreatePluginSchema(db, v2)
	if err == nil {
		t.Fatal("adding a primary key column to an existing table was accepted")
	}
	if !strings.Contains(err.Error(), "primary key") || !strings.Contains(err.Error(), "purge") {
		t.Errorf("the refusal does not explain the situation or the way out: %v", err)
	}
}

// A brand new table in a later version is still created — that part always
// worked and must keep working.
func TestPluginSchemaGainsWholeTablesOnUpdate(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	v1 := &PluginManifest{
		Id:     "grow",
		Tables: []PluginTable{{Name: "one", Columns: []PluginColumn{{Name: "a", Type: "int"}}}},
	}
	if err := CreatePluginSchema(db, v1); err != nil {
		t.Fatal(err)
	}

	v2 := &PluginManifest{
		Id: "grow",
		Tables: []PluginTable{
			{Name: "one", Columns: []PluginColumn{{Name: "a", Type: "int"}}},
			{Name: "two", Columns: []PluginColumn{{Name: "b", Type: "text"}}},
		},
	}
	if err := CreatePluginSchema(db, v2); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec("insert into `" + v2.TableName("two") + "` (`b`) values ('x')"); err != nil {
		t.Fatalf("the table added in v2 is not usable: %v", err)
	}
}
