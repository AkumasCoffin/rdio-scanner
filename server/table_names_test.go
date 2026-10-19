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
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every table name in a query must match the case the schema declares.
//
// SQLite and MySQL fold identifier case, so a name like `rdioScannerApikeys`
// works on two of the three backends and fails only on PostgreSQL, where a
// quoted identifier is case-sensitive. That is how two of these reached a
// release: deleting an API key or a dirwatch entry — the one code path that
// named the table that way — failed on Postgres with "relation does not
// exist", aborting the whole save, while both other backends were fine.
//
// Static check rather than a query, so it covers paths no test exercises and
// costs nothing to run.
func TestTableNamesMatchTheSchemaCase(t *testing.T) {
	declared := regexp.MustCompile("create table (?:if not exists )?[`\"]([a-zA-Z_][a-zA-Z0-9_]*)[`\"]")
	referenced := regexp.MustCompile("`(rdioScanner[A-Za-z0-9_]*|plugin_[a-z_]+)`")

	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}

	// The schema is the authority on spelling: whatever `create table` says.
	canonical := map[string]string{}
	for _, name := range sources {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range declared.FindAllStringSubmatch(string(body), -1) {
			canonical[strings.ToLower(match[1])] = match[1]
		}
	}

	if len(canonical) == 0 {
		t.Fatal("found no create table statements, so this test is checking nothing")
	}

	known := map[string]bool{}
	for _, name := range canonical {
		known[name] = true
	}

	for _, name := range sources {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}

		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}

		for line, text := range strings.Split(string(body), "\n") {
			for _, match := range referenced.FindAllStringSubmatch(text, -1) {
				table := match[1]
				if known[table] {
					continue
				}

				// A name the schema never declares in any case is a table this
				// check does not know about — a plugin's own, or one built at
				// runtime — not a case error.
				if want, ok := canonical[strings.ToLower(table)]; ok {
					t.Errorf("%s:%d: `%s` should be `%s` — the schema declares that case, and Postgres is case-sensitive",
						name, line+1, table, want)
				}
			}
		}
	}
}
