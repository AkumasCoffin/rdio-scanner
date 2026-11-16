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
	"strings"
	"testing"
	"time"
)

func schemaManifest(id string, tables ...PluginTable) *PluginManifest {
	return &PluginManifest{Id: id, Name: "Schema", Version: "1.0.0", Description: "x", Main: "main.js", Tables: tables}
}

// A plugin id and a table name at their own limits make a 72-character table.
// Postgres keeps 63 of them without a word, so two tables could become one;
// MySQL refuses. The manifest is where that is caught.
func TestAManifestWhoseTableNameWouldBeCutOffIsRefused(t *testing.T) {
	long := schemaManifest("a"+strings.Repeat("b", 31),
		PluginTable{Name: "t" + strings.Repeat("a", 30) + "1", Columns: []PluginColumn{{Name: "id", Type: "int", PrimaryKey: true}}},
	)

	err := long.Validate()
	if err == nil || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("a %d-character table name was accepted: %v", len(long.TableName(long.Tables[0].Name)), err)
	}

	// Right at the limit is fine: plugin_ (7) + id (24) + _ (1) + name (31).
	fits := schemaManifest("a"+strings.Repeat("b", 23),
		PluginTable{Name: "t" + strings.Repeat("a", 30), Columns: []PluginColumn{{Name: "id", Type: "int", PrimaryKey: true}}},
	)
	if got := len(fits.TableName(fits.Tables[0].Name)); got != pluginIdentifierLimit {
		t.Fatalf("the at-the-limit case is %d characters, not %d", got, pluginIdentifierLimit)
	}
	if err := fits.Validate(); err != nil {
		t.Fatalf("a table name exactly at the limit was refused: %v", err)
	}
}

func TestPluginIndexNamesFitAndStayDistinct(t *testing.T) {
	if got := pluginIndexName("plugin_transcripts_calls", "callId"); got != "plugin_transcripts_calls_callId_idx" {
		t.Fatalf("a short index name changed to %q; existing indexes would be rebuilt under a new name", got)
	}

	table := "plugin_" + strings.Repeat("a", 24) + "_" + strings.Repeat("t", 31)

	one := pluginIndexName(table, "firstLongColumnName")
	two := pluginIndexName(table, "secondLongColumnName")

	for _, name := range []string{one, two} {
		if len(name) > pluginIdentifierLimit {
			t.Fatalf("index name %q is %d characters", name, len(name))
		}
	}
	if one == two {
		t.Fatalf("two columns of one table were given the same index name %q", one)
	}
}

// An auto-increment column has to be a key. On Postgres it was a bare serial
// with no constraint, and on MySQL and MariaDB the table could not be created.
func TestAnAutoIncrementColumnIsAKeyOnEveryBackend(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	manifest := schemaManifest("autoinc", PluginTable{Name: "things", Columns: []PluginColumn{
		{Name: "id", Type: "int", PrimaryKey: true, AutoIncrement: true},
		{Name: "v", Type: "int"},
	}})

	if err := CreatePluginSchema(db, manifest); err != nil {
		t.Fatalf("a table with an auto-increment key could not be created: %v", err)
	}
	t.Cleanup(func() { DropPluginSchema(db, manifest) })

	pluginDb := NewPluginDb(db, manifest)

	for i := 0; i < 2; i++ {
		if _, err := pluginDb.exec("insert into `things` (`v`) values (?)", []any{i}, time.Minute); err != nil {
			t.Fatal(err)
		}
	}

	rows, err := pluginDb.query("select `id` from `things` order by `id`", nil, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0]["id"] == rows[1]["id"] {
		t.Fatalf("the generated ids are %v", rows)
	}

	if _, err := pluginDb.exec("insert into `things` (`id`, `v`) values (?, ?)", []any{rows[0]["id"], 9}, time.Minute); err == nil {
		t.Fatal("a second row was stored under an id already in use: the column has no key")
	}
}

// Starting a plugin creates its schema every time, so every statement has to
// survive being run again — including the index, on the backends that cannot
// be asked to check for it first.
func TestCreatingAPluginSchemaTwiceIsHarmless(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	manifest := schemaManifest("twice", PluginTable{Name: "things", Columns: []PluginColumn{
		{Name: "id", Type: "int", PrimaryKey: true},
		{Name: "owner", Type: "int", Index: true},
	}})
	t.Cleanup(func() { DropPluginSchema(db, manifest) })

	for i := 1; i <= 2; i++ {
		if err := CreatePluginSchema(db, manifest); err != nil {
			t.Fatalf("creating the schema, time %d: %v", i, err)
		}
	}
}

// What a plugin puts between quotes is data. On Postgres the statement is
// rewritten on its way out, and the rewrite used to reach inside.
func TestTextInsideAPluginsQuotesIsStoredAsWritten(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	manifest := schemaManifest("literal", PluginTable{Name: "notes", Columns: []PluginColumn{
		{Name: "id", Type: "int", PrimaryKey: true},
		{Name: "body", Type: "text"},
	}})
	if err := CreatePluginSchema(db, manifest); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { DropPluginSchema(db, manifest) })

	pluginDb := NewPluginDb(db, manifest)

	for _, statement := range []struct {
		sql  string
		args []any
		id   int
		want string
	}{
		{"insert into `notes` (`id`, `body`) values (1, 'see `x` for more')", nil, 1, "see `x` for more"},
		{"insert into `notes` (`id`, `body`) values (?, 'is it ok?')", []any{2}, 2, "is it ok?"},
		{"insert into `notes` (`id`, `body`) values (?, 'it''s `here`? yes')", []any{3}, 3, "it's `here`? yes"},
		{"insert into `notes` (`id`, `body`) values (?, ?)", []any{4, "bound `text`?"}, 4, "bound `text`?"},
	} {
		if _, err := pluginDb.exec(statement.sql, statement.args, time.Minute); err != nil {
			t.Fatalf("%s: %v", statement.sql, err)
		}

		rows, err := pluginDb.query("select `body` from `notes` where `id` = ?", []any{statement.id}, time.Minute)
		if err != nil || len(rows) != 1 {
			t.Fatalf("reading row %d back: %v %v", statement.id, rows, err)
		}
		if rows[0]["body"] != statement.want {
			t.Errorf("stored %q, want %q", rows[0]["body"], statement.want)
		}
	}
}

func TestFormatQueryLeavesLiteralsAlone(t *testing.T) {
	postgres := &Database{Config: &Config{DbType: DbTypePostgres}}

	for in, want := range map[string]string{
		"select `a` from `t` where `b` = ? and `c` = ?":      `select "a" from "t" where "b" = $1 and "c" = $2`,
		"select 1 from `t` where `a` like '%?%' and `b` = ?": `select 1 from "t" where "a" like '%?%' and "b" = $1`,
		"update `t` set `a` = 'it''s `x`?' where `b` = ?":    `update "t" set "a" = 'it''s ` + "`x`" + `?' where "b" = $1`,
		"select `key` from `c` where `key` like 'option.%'":  `select "key" from "c" where "key" like 'option.%'`,
	} {
		if got := postgres.formatQuery(in); got != want {
			t.Errorf("formatQuery(%s)\n  got  %s\n  want %s", in, got, want)
		}
	}

	sqlite := &Database{Config: &Config{DbType: DbTypeSqlite}}
	if in := "select `a` from `t` where `b` = ?"; sqlite.formatQuery(in) != in {
		t.Error("a statement for SQLite was rewritten")
	}
}
