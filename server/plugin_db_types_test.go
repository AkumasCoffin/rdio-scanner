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
	"testing"
	"time"
)

// What a plugin reads back from its own tables has to be usable as what it
// stored, on every backend, whether or not the query carried arguments.
//
// The second half matters on MySQL and MariaDB. With no arguments the driver
// sends the query as text and gets every column back as text, numbers
// included; with arguments it prepares the statement and gets typed values.
// The text form reached plugins as strings, so a boolean column stored as 0
// came back as "0" — and "0" is true in JavaScript. A plugin that read its
// switches with no arguments, as the transcripts plugin reads which systems
// to skip, could never see one turned off.
func TestPluginQueryReturnsNumbersAsNumbers(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	manifest := &PluginManifest{
		Id: "typed", Name: "Typed", Version: "1.0.0", Description: "x", Main: "main.js",
		Tables: []PluginTable{{
			Name: "flags",
			Columns: []PluginColumn{
				{Name: "id", Type: "int", PrimaryKey: true},
				{Name: "enabled", Type: "boolean"},
				{Name: "ratio", Type: "float"},
				{Name: "label", Type: "text"},
			},
		}},
	}

	if err := CreatePluginSchema(db, manifest); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { DropPluginSchema(db, manifest) })

	pluginDb := NewPluginDb(db, manifest)

	for _, row := range []struct {
		id      int
		enabled bool
	}{{1, false}, {2, true}} {
		if _, err := pluginDb.exec("insert into `flags` (`id`, `enabled`, `ratio`, `label`) values (?, ?, ?, ?)", []any{row.id, row.enabled, 1.5, "007"}, time.Minute); err != nil {
			t.Fatal(err)
		}
	}

	// How JavaScript will read the value: !!v.
	truthy := func(v any) bool {
		switch t := v.(type) {
		case bool:
			return t
		case int64:
			return t != 0
		case int:
			return t != 0
		case float64:
			return t != 0
		case string:
			return t != ""
		case nil:
			return false
		}
		return true
	}

	for _, shape := range []struct {
		name  string
		query string
		args  []any
	}{
		{"no arguments", "select `id`, `enabled`, `ratio`, `label` from `flags` order by `id`", []any{}},
		{"with arguments", "select `id`, `enabled`, `ratio`, `label` from `flags` where `id` > ? order by `id`", []any{0}},
	} {
		t.Run(shape.name, func(t *testing.T) {
			rows, err := pluginDb.query(shape.query, shape.args, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 2 {
				t.Fatalf("read %d rows, want 2", len(rows))
			}

			if truthy(rows[0]["enabled"]) {
				t.Errorf("a boolean stored as false reads back as %#v, which is true to a plugin", rows[0]["enabled"])
			}
			if !truthy(rows[1]["enabled"]) {
				t.Errorf("a boolean stored as true reads back as %#v", rows[1]["enabled"])
			}

			if _, isString := rows[0]["id"].(string); isString {
				t.Errorf("an int column reads back as the string %#v", rows[0]["id"])
			}
			if _, isString := rows[0]["ratio"].(string); isString {
				t.Errorf("a float column reads back as the string %#v", rows[0]["ratio"])
			}

			// And text stays text, leading zeros and all.
			if rows[0]["label"] != "007" {
				t.Errorf("a text column reads back as %#v, want \"007\"", rows[0]["label"])
			}
		})
	}
}
