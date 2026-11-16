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
	"fmt"
	"log"
	"regexp"
	"strings"
)

// ensureSearchIndex makes a plugin's searchable column actually searchable at
// scale.
//
// rdio.search.extend means "run LIKE '%term%' over this column on every
// search". The server writes that query (call.go, the q= branch), so the
// server is what has to care whether the column can answer it. Nobody was:
// transcripts moved out of rdioScannerCalls into the transcripts plugin's own
// table, the trigram index the server maintains stayed behind on the old
// column, and every keyword search became an unindexed scan over the whole
// transcript history. On a database holding weeks of calls that is seconds per
// search, and it competes with everything else for the connection pool.
//
// Doing it here rather than in a migration is what makes it correct in
// general: migrations run before any plugin has started, so at that point the
// server does not yet know which tables are searchable. Registration is the
// moment it learns.
//
// Postgres only, and deliberately so. A LIKE with a leading wildcard cannot use
// a B-tree on any engine; Postgres answers it with a trigram GIN index, while
// MySQL and SQLite would need full-text tables and a different query, which is
// a larger change than an index.
func (controller *Controller) ensureSearchIndex(table string, column string, keyColumn string) {
	if controller.Database == nil || controller.Database.Config.DbType != DbTypePostgres {
		return
	}

	if !pluginIndexIdentifier.MatchString(table) || !pluginIndexIdentifier.MatchString(column) ||
		!pluginIndexIdentifier.MatchString(keyColumn) {
		// Registration validates these already; refusing anything unexpected
		// here too, because this is the one place a plugin-supplied name is
		// concatenated into DDL.
		log.Printf("search index: refusing unexpected identifier %q.%q", table, column)
		return
	}

	db := controller.Database

	// Off the caller's goroutine: this runs while the plugin is starting, and
	// building an index over a large table takes as long as it takes. Blocking
	// startup on it would trade a slow search for a slow boot.
	go func() {
		if _, err := db.Sql.Exec("create extension if not exists pg_trgm"); err != nil {
			log.Printf("search index: pg_trgm is unavailable, %s.%s searches will scan — %s",
				table, column, searchIndexUnavailableReason(err))
			return
		}

		// The index name is derived from the table and column so two plugins
		// registering the same column name cannot collide — index names are
		// database-wide on Postgres.
		name := indexName(fmt.Sprintf("%s_%s_trgm", table, column))

		// A build that was interrupted leaves its index behind, invalid, and
		// `if not exists` then steps over it on every later start while the
		// log line below says the column is indexed. Clear it so this run
		// builds a real one.
		if invalid, err := trigramIndexes(db, table, column, false); err == nil {
			for _, leftover := range invalid {
				if leftover != name {
					continue
				}
				if _, err := db.Sql.Exec(fmt.Sprintf(`drop index concurrently if exists %q`, leftover)); err != nil {
					log.Printf("search index: could not clear the unfinished index %s: %v", leftover, err)
				}
			}
		}

		// Whatever already indexes this column this way. The transcripts
		// migration builds exactly this index under its own name before any
		// plugin has started, so on every install with that plugin this
		// function went on to build a second, identical one beside it: twice
		// the write cost on every transcript and twice the disk, for nothing.
		existing, err := trigramIndexes(db, table, column, true)
		if err != nil {
			log.Printf("search index: could not look for an existing index on %s.%s: %v", table, column, err)
		}

		var others []string
		ours := false
		for _, found := range existing {
			if found == name {
				ours = true
			} else {
				others = append(others, found)
			}
		}

		switch {
		case len(others) > 0 && ours:
			// Both are there, from a version that built the duplicate. Only
			// ever the one this function made is removed; the other is not
			// ours to touch.
			if _, err := db.Sql.Exec(fmt.Sprintf(`drop index concurrently if exists %q`, name)); err != nil {
				log.Printf("search index: %s duplicates %s but could not be dropped: %v", name, others[0], err)
			} else {
				log.Printf("search index: dropped %s, a duplicate of %s", name, others[0])
			}

		case len(others) > 0:
			// Already indexed. Nothing to build.

		default:
			// CONCURRENTLY so an existing server keeps taking calls while this
			// builds. It cannot run inside a transaction, which is why this
			// uses the pool directly rather than the migration helper.
			statement := fmt.Sprintf(
				`create index concurrently if not exists %q on %q using gin (%q gin_trgm_ops)`,
				name, table, column,
			)

			if _, err := db.Sql.Exec(statement); err != nil {
				log.Printf("search index: could not index %s.%s, searches will scan — %s",
					table, column, searchIndexUnavailableReason(err))
				return
			}
		}

		log.Printf("search index: %s.%s is indexed for text search", table, column)

		// A second, much smaller index for the has-it / has-none filter.
		//
		// That filter asks which calls carry this text at all, which the
		// trigram index cannot answer — it indexes the contents, not the
		// presence. Partial and keys-only, so it holds one narrow entry per
		// row that has text and nothing for the rest: the search reads it once
		// to build its membership set instead of visiting the table.
		presence := indexName(fmt.Sprintf("%s_%s_present", table, column))

		statement := fmt.Sprintf(
			`create index concurrently if not exists %q on %q (%q) where %q is not null and %q <> ''`,
			presence, table, keyColumn, column, column,
		)

		if _, err := db.Sql.Exec(statement); err != nil {
			log.Printf("search index: could not index which %s rows have %s, the with/without filter will scan — %s",
				table, column, searchIndexUnavailableReason(err))
			return
		}

		log.Printf("search index: %s.%s presence is indexed for the with/without filter", table, column)
	}()
}

// trigramIndexes lists the single-column trigram GIN indexes on a column, by
// name: the usable ones when valid is true, the ones an interrupted build left
// unusable when it is false.
func trigramIndexes(db *Database, table string, column string, valid bool) ([]string, error) {
	rows, err := db.Sql.Query(`
		select c.relname
		from pg_index i
		join pg_class c on c.oid = i.indexrelid
		join pg_class t on t.oid = i.indrelid
		join pg_am am on am.oid = c.relam
		join pg_opclass opc on opc.oid = i.indclass[0]
		join pg_attribute a on a.attrelid = t.oid and a.attnum = i.indkey[0]
		where t.relname = $1
		  and pg_table_is_visible(t.oid)
		  and a.attname = $2
		  and i.indnatts = 1
		  and i.indpred is null
		  and am.amname = 'gin'
		  and opc.opcname = 'gin_trgm_ops'
		  and (i.indisvalid and i.indisready) = $3
		order by c.relname`, table, column, valid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}

	return names, rows.Err()
}

// indexName keeps a derived name inside Postgres's 63-byte identifier limit,
// which it enforces by truncating — silently merging two long names into one
// index if they are left to collide.
func indexName(name string) string {
	if len(name) > 63 {
		return name[:63]
	}

	return name
}

// pluginIndexIdentifier is what a table or column may be called for the DDL
// above: the shape plugin table and column names are already restricted to.
var pluginIndexIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// searchIndexUnavailableReason turns a failure into something an operator can
// act on. "permission denied" is by far the likeliest cause and says nothing
// about what to do, so it is named explicitly.
func searchIndexUnavailableReason(err error) string {
	if err == nil {
		return ""
	}

	if strings.Contains(err.Error(), "permission denied") {
		return "the database role may not create extensions or indexes"
	}

	return err.Error()
}
