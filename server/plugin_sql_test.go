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

func sqlRewriter() *PluginDb {
	return NewPluginDb(nil, &PluginManifest{
		Id:     "notes",
		Tables: []PluginTable{{Name: "entries"}, {Name: "calls"}},
	})
}

// The rewrite was a regex over the whole statement, which had no idea what a
// string literal was. A plugin storing the words "see `entries` for detail" had
// its own prose silently rewritten — data corruption with nothing to indicate
// it, in the one place a plugin is most likely to put words a person reads.
func TestRewriteLeavesStringLiteralsAlone(t *testing.T) {
	db := sqlRewriter()

	cases := []struct {
		query string
		keeps string
	}{
		{"insert into `entries` (`body`) values ('see `entries` for detail')", "'see `entries` for detail'"},
		{`insert into "entries" ("body") values ("the ` + "`calls`" + ` table")`, "`calls`"},
		{"update `entries` set `body` = 'it''s in `calls`' where `id` = 1", "'it''s in `calls`'"},
	}

	for _, c := range cases {
		out, err := db.rewrite(c.query)
		if err != nil {
			t.Fatalf("%q: %v", c.query, err)
		}
		if !strings.Contains(out, c.keeps) {
			t.Errorf("the literal was rewritten.\n  query: %s\n  got:   %s\n  wanted to keep: %s", c.query, out, c.keeps)
		}
	}
}

// The table reference outside the literal still has to be rewritten, or the
// scanner has traded one bug for a worse one.
func TestRewriteStillNamespacesRealReferences(t *testing.T) {
	db := sqlRewriter()

	out, err := db.rewrite("insert into `entries` (`body`) values ('see `entries` for detail')")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(out, "insert into `plugin_notes_entries`") {
		t.Fatalf("the real table reference was not namespaced: %s", out)
	}

	// Joins across a plugin table and a core one work: core names are not
	// declared tables, so they pass through.
	out, err = db.rewrite("select * from `entries` join `rdioScannerCalls` on `rdioScannerCalls`.`id` = `entries`.`callId`")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "`plugin_notes_entries`") {
		t.Errorf("the plugin table was not namespaced in a join: %s", out)
	}
	if !strings.Contains(out, "`rdioScannerCalls`") {
		t.Errorf("a core table was rewritten: %s", out)
	}
}

// A comment is a note, not a reference.
func TestRewriteLeavesCommentsAlone(t *testing.T) {
	db := sqlRewriter()

	for _, query := range []string{
		"select 1 -- `entries` is where notes live\n",
		"select 1 /* `entries` is where notes live */",
	} {
		out, err := db.rewrite(query)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, "plugin_notes_entries") {
			t.Errorf("a table name inside a comment was rewritten: %s", out)
		}
	}
}

// Column names are backtick-quoted too, so the rule is simply that an
// identifier matching a declared table is rewritten. A plugin that names a
// table `calls` — the transcripts plugin does — must still be able to select a
// column called something else.
func TestRewriteHandlesQuotedIdentifiers(t *testing.T) {
	db := sqlRewriter()

	out, err := db.rewrite("select `callId`, `transcript` from `calls` where `callId` = ?")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out, "from `plugin_notes_calls`") {
		t.Errorf("the table was not namespaced: %s", out)
	}
	if !strings.Contains(out, "`callId`") || !strings.Contains(out, "`transcript`") {
		t.Errorf("a column name was mangled: %s", out)
	}
}

// An unterminated quote is a broken statement. Copying the rest verbatim lets
// the database report it, which is a better error than anything guessed here.
func TestRewriteSurvivesMalformedSql(t *testing.T) {
	db := sqlRewriter()

	for _, query := range []string{
		"select * from `entries",
		"insert into `entries` values ('unterminated",
		"",
		"`",
	} {
		if _, err := db.rewrite(query); err != nil {
			t.Errorf("%q produced an error rather than being passed through: %v", query, err)
		}
	}
}
