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
	"regexp"
	"sort"
	"testing"
)

// importExcludedTables is the deliberate exclusion list, and the reason for
// each. A table named here is not copied by -import_sqlite on purpose.
var importExcludedTables = map[string]string{
	"rdioScannerMeta":    "schema bookkeeping: the target has run its own migrations and must keep its own record",
	"rdioScannerDelayed": "a transient queue, rebuilt at startup",
	"rdioScannerLogs":    "large, purely historical, and the target prunes it on its own schedule",
}

// Every table the schema creates is either imported or excluded on purpose.
//
// Two were neither, and both were silent losses: talkgroup patches — a
// first-class part of the admin configuration — and the listener-count
// history behind the stats page. Nothing failed; the import printed its table
// list, said "import complete", and the patches were simply gone. A plugin's
// own tables are discovered at run time instead (see pluginImportTables), so
// they are exempt here.
//
// Static, so it costs nothing and covers the tables no test touches.
func TestImportCoversTheSchema(t *testing.T) {
	declared := regexp.MustCompile("create table (?:if not exists )?[`\"](rdioScanner[a-zA-Z0-9_]*)[`\"]")

	schema := map[string]bool{}
	for _, file := range []string{"database.go", "migration_transcripts.go"} {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range declared.FindAllStringSubmatch(string(body), -1) {
			schema[match[1]] = true
		}
	}

	if len(schema) == 0 {
		t.Fatal("found no create table statements, so this test is checking nothing")
	}

	imported := map[string]bool{importCallsTable.name: true}
	for _, table := range importTables {
		imported[table.name] = true
	}

	missing := []string{}
	for table := range schema {
		// v4-era intermediates: created under one name and renamed within the
		// same migration, so they never exist on a migrated database.
		if schema[table] && regexp.MustCompile(`[0-9]$`).MatchString(table) {
			continue
		}
		if imported[table] {
			continue
		}
		if _, excluded := importExcludedTables[table]; excluded {
			continue
		}
		missing = append(missing, table)
	}

	sort.Strings(missing)

	for _, table := range missing {
		t.Errorf("%s is created by the schema but is neither imported nor in importExcludedTables — "+
			"a migrating user would lose it silently. Add it to importTables, or name it in "+
			"importExcludedTables with the reason.", table)
	}

	// And the exclusion list must not rot: a name that no longer exists in the
	// schema is a stale exemption that could hide a future table.
	for table := range importExcludedTables {
		if !schema[table] {
			t.Errorf("importExcludedTables names %s, which the schema no longer creates", table)
		}
	}
}
