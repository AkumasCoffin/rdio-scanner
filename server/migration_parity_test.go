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
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The plugin registry shipped for months with a column only the MySQL branch
// was missing. Nothing caught it, because nothing runs the schema on anything
// but SQLite and the three branches sit side by side where an absent entry
// reads as formatting.
//
// So: when one migration creates the same table on more than one backend, every
// backend has to declare the same columns. This parses the source rather than
// scraping it — the DDL lives in string literals, and go/parser knows where a
// literal starts and ends, which a regex sweeping for the next `)"` does not.
//
// It needs no database at all. The reason the original bug survived is that
// checking it appeared to require three servers.
func TestEveryBackendDeclaresTheSameColumns(t *testing.T) {
	fset := token.NewFileSet()

	file, err := parser.ParseFile(fset, "database.go", nil, 0)
	if err != nil {
		t.Fatalf("cannot parse database.go: %v", err)
	}

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}

		name := fn.Name.Name
		if !strings.Contains(strings.ToLower(name), "migration") && !strings.Contains(name, "TableSql") {
			continue
		}

		// Every create-table statement this function contains, grouped by the
		// table it creates. More than one means more than one backend.
		byTable := map[string][][]string{}

		ast.Inspect(fn, func(node ast.Node) bool {
			lit, ok := node.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}

			text, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}

			table, columns, ok := parseCreateTable(text)
			if !ok {
				return true
			}

			byTable[table] = append(byTable[table], columns)

			return true
		})

		for table, declarations := range byTable {
			if len(declarations) < 2 {
				continue
			}

			for i := 1; i < len(declarations); i++ {
				if missing := missingFrom(declarations[0], declarations[i]); len(missing) > 0 {
					t.Errorf("%s: one backend's %s does not declare %v, which another backend does",
						name, table, missing)
				}
				if missing := missingFrom(declarations[i], declarations[0]); len(missing) > 0 {
					t.Errorf("%s: one backend's %s does not declare %v, which another backend does",
						name, table, missing)
				}
			}
		}
	}
}

// parseCreateTable pulls the table name and its column names out of one
// statement, or reports that the string was not a create-table at all.
func parseCreateTable(sql string) (string, []string, bool) {
	trimmed := strings.TrimSpace(sql)

	lower := strings.ToLower(trimmed)
	if !strings.HasPrefix(lower, "create table") {
		return "", nil, false
	}

	open := strings.Index(trimmed, "(")
	if open < 0 || !strings.HasSuffix(trimmed, ")") {
		return "", nil, false
	}

	table := unquoteIdent(strings.TrimSpace(trimmed[len("create table"):open]))
	table = strings.TrimPrefix(table, "if not exists ")
	table = unquoteIdent(strings.TrimSpace(table))

	body := trimmed[open+1 : len(trimmed)-1]

	seen := map[string]bool{}
	var columns []string

	// Each top-level comma-separated part is either a column or a table
	// constraint. A column's name is its first token.
	for _, part := range splitTopLevel(body) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		first := strings.ToLower(strings.Fields(part)[0])
		// Constraints, not columns. Their names differ between backends
		// legitimately — Postgres index names are database-wide.
		if first == "primary" || first == "unique" || first == "key" || first == "constraint" || first == "index" || first == "foreign" {
			continue
		}

		column := unquoteIdent(strings.Fields(part)[0])
		if column == "" || seen[column] {
			continue
		}

		seen[column] = true
		columns = append(columns, column)
	}

	sort.Strings(columns)

	return table, columns, true
}

// splitTopLevel splits on commas that are not inside parentheses, so
// varchar(255) stays in one piece.
func splitTopLevel(body string) []string {
	var parts []string
	var current strings.Builder

	depth := 0

	for _, r := range body {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, current.String())
				current.Reset()
				continue
			}
		}

		current.WriteRune(r)
	}

	if current.Len() > 0 {
		parts = append(parts, current.String())
	}

	return parts
}

func unquoteIdent(text string) string {
	return strings.Trim(strings.TrimSpace(text), "`\"")
}

func missingFrom(want []string, have []string) []string {
	present := map[string]bool{}
	for _, name := range have {
		present[name] = true
	}

	var missing []string
	for _, name := range want {
		if !present[name] {
			missing = append(missing, name)
		}
	}

	return missing
}
