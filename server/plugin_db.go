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
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// pluginConfigTableName is the per-plugin settings table every plugin gets,
// created on install and deliberately never dropped on uninstall — reinstalling
// a plugin restores the settings the user already entered.
const pluginConfigTableName = "config"

// pluginQueryMaxRows bounds what a single rdio.db.query can materialise. The
// results are converted into JavaScript values, so an unbounded read is a
// memory multiplier, not just a large slice.
const pluginQueryMaxRows = 50000

// pluginSqlLeadingKeyword grabs the first word of a statement so exec/query can
// be held to the statement kinds each is meant for.
var pluginSqlLeadingKeyword = regexp.MustCompile(`^\s*([a-zA-Z]+)`)

// Statements that return rows, so query and exec can give a useful error when
// one is used for the other's job. Neither list restricts what may be run —
// anything not named here is allowed through to whichever method was called.
var pluginQueryStatements = map[string]bool{
	"select":  true,
	"with":    true,
	"pragma":  true,
	"explain": true,
	"show":    true,
	"values":  true,
}

// PluginDb is the database handle handed to one plugin. Every statement is
// rewritten so the plugin's declared table names resolve to its own namespace.
//
// It is a convenience, not a fence: a plugin may read and write any table in
// the database, core's included. Saying otherwise here — as this comment used
// to, twice, seventeen lines before the code that contradicts it — made the
// boundary look stronger than it is to anyone reading the source to find out.
type PluginDb struct {
	database *Database
	prefix   string
	// tables is the set of declared names this plugin may reference, including
	// the host-created config table.
	tables map[string]bool
}

func NewPluginDb(database *Database, manifest *PluginManifest) *PluginDb {
	tables := map[string]bool{pluginConfigTableName: true}
	for i := range manifest.Tables {
		tables[manifest.Tables[i].Name] = true
	}

	return &PluginDb{
		database: database,
		prefix:   manifest.TablePrefix(),
		tables:   tables,
	}
}

// rewrite maps every backtick-quoted identifier that names one of this plugin's
// declared tables onto its namespaced form.
//
// It scans rather than pattern-matches. The regex it replaced had no idea what
// a string literal was, so it rewrote inside quoted text and comments as
// happily as it did in a FROM clause — a plugin storing the words
// "see `notes` for detail" got them silently altered. Scanning is barely more
// code and is the difference between a rewrite and a find-and-replace.
//
// Nothing is refused. A plugin may read and write any table in the database,
// core's included — a query it could not run here it could run by other means,
// and pretending otherwise only made the boundary look stronger than it was.
// The prefix mapping stays because it is a convenience: a plugin writes the
// table name it declared and does not have to know or repeat its own namespace.
//
// Column names are backtick-quoted too, so the rule is simply: an identifier
// matching a declared table name is rewritten, anything else is left alone.
func (pluginDb *PluginDb) rewrite(query string) (string, error) {
	var out strings.Builder
	out.Grow(len(query) + 32)

	runes := []rune(query)

	for i := 0; i < len(runes); {
		switch runes[i] {
		// String literals are copied through untouched. A regex over the whole
		// statement rewrote inside them, so a plugin storing the text
		// "see `notes` for detail" had its own prose silently rewritten to
		// "see `plugin_x_notes` for detail" — data corruption with nothing to
		// indicate it, in the one place a plugin is most likely to put words a
		// person will read.
		case 0x27, '"':
			quote := runes[i]
			out.WriteRune(quote)
			i++

			for i < len(runes) {
				out.WriteRune(runes[i])

				// Doubling is how SQL escapes a quote inside a literal.
				if runes[i] == quote {
					if i+1 < len(runes) && runes[i+1] == quote {
						out.WriteRune(quote)
						i += 2
						continue
					}
					i++
					break
				}

				i++
			}

		// Comments likewise: a table name mentioned in one is a note, not a
		// reference.
		case '-':
			if i+1 < len(runes) && runes[i+1] == '-' {
				for i < len(runes) && runes[i] != 0x0a {
					out.WriteRune(runes[i])
					i++
				}
				continue
			}
			out.WriteRune(runes[i])
			i++

		case '/':
			if i+1 < len(runes) && runes[i+1] == '*' {
				out.WriteString("/*")
				i += 2
				for i < len(runes) {
					if runes[i] == '*' && i+1 < len(runes) && runes[i+1] == '/' {
						out.WriteString("*/")
						i += 2
						break
					}
					out.WriteRune(runes[i])
					i++
				}
				continue
			}
			out.WriteRune(runes[i])
			i++

		case '`':
			end := i + 1
			for end < len(runes) && runes[end] != '`' {
				end++
			}

			if end >= len(runes) {
				// Unterminated. Copy the rest verbatim rather than guessing.
				out.WriteString(string(runes[i:]))
				i = len(runes)
				continue
			}

			name := string(runes[i+1 : end])

			if pluginDb.tables[name] {
				out.WriteString("`" + pluginDb.prefix + name + "`")
			} else {
				out.WriteString("`" + name + "`")
			}

			i = end + 1

		default:
			out.WriteRune(runes[i])
			i++
		}
	}

	return out.String(), nil
}

func leadingKeyword(query string) string {
	m := pluginSqlLeadingKeyword.FindStringSubmatch(query)
	if len(m) != 2 {
		return ""
	}
	return strings.ToLower(m[1])
}

// Query runs a statement that returns rows.
//
// A write sent here would run but return nothing, which looks like a query that
// found no rows rather than a mistake — so it is rejected with an explanation
// instead. That is a guard against confusion, not against capability: the same
// statement works through Exec.
func (pluginDb *PluginDb) Query(query string, args []any) ([]map[string]any, error) {
	if keyword := leadingKeyword(query); keyword != "" && !pluginQueryStatements[keyword] {
		return nil, fmt.Errorf("%s returns no rows; use rdio.db.exec for it", strings.ToUpper(keyword))
	}

	rewritten, err := pluginDb.rewrite(query)
	if err != nil {
		return nil, err
	}

	// Bounded, unlike the bare Query most of the server uses.
	//
	// rdio.db.query is synchronous on the plugin's event loop, and that loop is
	// shared by everything the plugin does — its routes, its observers, its
	// timers. An unbounded wait for a pooled connection therefore does not
	// stall one query, it stalls the whole plugin, and no watchdog can help:
	// goja can only interrupt at a JS instruction boundary, and this is a Go
	// call that has not returned. An error the plugin can catch is a far better
	// outcome than a runtime that has silently stopped answering.
	//
	// Safe to cancel on return because the rows are fully drained below and
	// never handed to a caller.
	ctx, cancel := context.WithTimeout(context.Background(), statementTimeout)
	defer cancel()

	rows, err := pluginDb.database.QueryContext(ctx, rewritten, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	results := []map[string]any{}

	for rows.Next() {
		// A plugin that forgets a LIMIT should get an error, not silently pull
		// a million rows into the interpreter and take the process with it.
		if len(results) >= pluginQueryMaxRows {
			return nil, fmt.Errorf("query returned more than %d rows; add a LIMIT", pluginQueryMaxRows)
		}

		// Scanning into []any of *any lets us hand back whatever the driver
		// produced without the plugin having to declare types up front.
		holders := make([]any, len(columns))
		for i := range holders {
			holders[i] = new(any)
		}

		if err := rows.Scan(holders...); err != nil {
			return nil, err
		}

		row := map[string]any{}
		for i, name := range columns {
			row[name] = normalizePluginValue(*(holders[i].(*any)))
		}
		results = append(results, row)
	}

	return results, rows.Err()
}

// Exec runs any statement, including schema changes.
//
// Declaring tables in the manifest is still the better path — the server then
// creates them, namespaces them, and knows to remove them on purge — but a
// plugin that needs to build schema at runtime is not stopped from doing so.
func (pluginDb *PluginDb) Exec(query string, args []any) (int64, error) {
	rewritten, err := pluginDb.rewrite(query)
	if err != nil {
		return 0, err
	}

	result, err := pluginDb.database.Exec(rewritten, args...)
	if err != nil {
		return 0, err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		// Not every driver reports this; a successful write that can't report
		// a count is still a successful write.
		return 0, nil
	}

	return affected, nil
}

// normalizePluginValue converts driver-specific representations into shapes the
// JavaScript runtime can use directly. The main offender is []byte, which every
// backend uses for text in at least some situations and which would otherwise
// reach a plugin as an array of numbers.
func normalizePluginValue(v any) any {
	switch t := v.(type) {
	case []byte:
		return string(t)
	default:
		return v
	}
}

// --- schema creation ------------------------------------------------------

// pluginColumnSql renders one dialect-neutral column declaration into the SQL
// the active backend expects.
func pluginColumnSql(db *Database, column *PluginColumn, singlePrimaryKey bool) (string, error) {
	var typeSql string

	switch column.Type {
	case "int":
		typeSql = "integer"
	case "bigint":
		typeSql = "bigint"
	case "text":
		typeSql = "text"
	case "varchar":
		typeSql = fmt.Sprintf("varchar(%d)", column.Length)
	case "boolean":
		// All three backends accept "boolean" in DDL — SQLite treats it as
		// numeric affinity and MySQL aliases it to tinyint(1) — so the core
		// schema's existing spelling works here unchanged.
		typeSql = "boolean"
	case "datetime":
		if db.Config.DbType == DbTypePostgres {
			typeSql = "timestamptz"
		} else {
			typeSql = "datetime"
		}
	case "float":
		if db.Config.DbType == DbTypePostgres {
			typeSql = "double precision"
		} else {
			typeSql = "double"
		}
	case "blob":
		if db.Config.DbType == DbTypePostgres {
			typeSql = "bytea"
		} else {
			typeSql = "longblob"
		}
	default:
		return "", fmt.Errorf("unknown column type %q", column.Type)
	}

	// Auto-increment is spelled three different ways, and on Postgres it
	// replaces the column type entirely.
	if column.AutoIncrement {
		switch db.Config.DbType {
		case DbTypePostgres:
			if column.Type == "bigint" {
				typeSql = "bigserial"
			} else {
				typeSql = "serial"
			}
		case DbTypeSqlite:
			// SQLite only honours AUTOINCREMENT on "integer primary key", and
			// the primary key has to be declared inline for that to work.
			return fmt.Sprintf("`%s` integer primary key autoincrement", column.Name), nil
		default:
			typeSql += " auto_increment"
		}
	}

	sqlText := fmt.Sprintf("`%s` %s", column.Name, typeSql)

	if !column.IsNullable() || column.PrimaryKey {
		sqlText += " not null"
	}

	if column.Default != nil && !column.AutoIncrement {
		literal, err := pluginDefaultLiteral(column)
		if err != nil {
			return "", err
		}
		sqlText += " default " + literal
	}

	// A single-column primary key that isn't auto-increment can be declared
	// inline; composite keys need the table-level constraint instead.
	if column.PrimaryKey && singlePrimaryKey && !column.AutoIncrement && db.Config.DbType != DbTypeSqlite {
		sqlText += " primary key"
	}

	return sqlText, nil
}

// pluginDefaultLiteral renders a manifest default as a SQL literal. Strings are
// quoted defensively because manifests are third-party input that reaches DDL,
// which cannot be parameterised.
func pluginDefaultLiteral(column *PluginColumn) (string, error) {
	switch v := column.Default.(type) {
	case bool:
		if v {
			return "true", nil
		}
		return "false", nil
	case float64:
		// JSON numbers all decode as float64; render integers without a
		// trailing ".0" so integer columns get an integer default.
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d", int64(v)), nil
		}
		return fmt.Sprintf("%g", v), nil
	case string:
		if strings.ContainsAny(v, "'\\\x00\n\r") {
			return "", fmt.Errorf("column %q has an unsupported default value", column.Name)
		}
		return "'" + v + "'", nil
	case nil:
		return "null", nil
	default:
		return "", fmt.Errorf("column %q has an unsupported default value type", column.Name)
	}
}

// pluginTableSql builds the create-table statement plus any index statements
// for one declared table.
// pluginTableSql renders one table's DDL, keeping the table and its indexes
// apart.
//
// They have to run in separate phases: an index on a column added by a later
// version cannot be created until that column exists, and columns are only
// added after every table is known to be there.
func pluginTableSql(db *Database, manifest *PluginManifest, table *PluginTable) (string, []string, error) {
	name := manifest.TableName(table.Name)

	primaryKeys := []string{}
	hasAutoIncrement := false
	for i := range table.Columns {
		if table.Columns[i].PrimaryKey {
			primaryKeys = append(primaryKeys, table.Columns[i].Name)
		}
		if table.Columns[i].AutoIncrement {
			hasAutoIncrement = true
		}
	}

	// SQLite folds an auto-increment primary key into the column declaration,
	// so a table-level constraint would be a duplicate.
	inlinePrimaryKey := len(primaryKeys) == 1 && !(db.Config.DbType == DbTypeSqlite && hasAutoIncrement)

	parts := []string{}
	for i := range table.Columns {
		columnSql, err := pluginColumnSql(db, &table.Columns[i], inlinePrimaryKey)
		if err != nil {
			return "", nil, fmt.Errorf("table %q: %v", table.Name, err)
		}
		parts = append(parts, columnSql)
	}

	needsTableLevelKey := len(primaryKeys) > 1 ||
		(len(primaryKeys) == 1 && !hasAutoIncrement && db.Config.DbType == DbTypeSqlite)

	if needsTableLevelKey {
		quoted := make([]string, len(primaryKeys))
		for i, k := range primaryKeys {
			quoted[i] = "`" + k + "`"
		}
		parts = append(parts, fmt.Sprintf("primary key (%s)", strings.Join(quoted, ", ")))
	}

	create := fmt.Sprintf("create table if not exists `%s` (%s)", name, strings.Join(parts, ", "))

	indexes := []string{}

	for i := range table.Columns {
		column := &table.Columns[i]
		if !column.Index || column.PrimaryKey {
			continue
		}
		// Index names share the table's namespace so two plugins can declare
		// the same column name without colliding on Postgres, where index
		// names are database-wide rather than per-table.
		indexes = append(indexes, fmt.Sprintf(
			"create index if not exists `%s_%s_idx` on `%s` (`%s`)",
			name, column.Name, name, column.Name,
		))
	}

	return create, indexes, nil
}

// pluginConfigTableSql is the host-owned settings table. Identical for every
// plugin, so it isn't declared in manifests.
func pluginConfigTableSql(db *Database, manifest *PluginManifest) string {
	return fmt.Sprintf(
		"create table if not exists `%s` (`key` varchar(255) not null, `value` text, primary key (`key`))",
		manifest.TableName(pluginConfigTableName),
	)
}

// CreatePluginSchema creates the config table and every manifest-declared table,
// and brings existing tables up to what the manifest now declares.
//
// Every statement is idempotent, so this runs on install and on every start
// without needing a ledger to say what has been applied. That matters because
// MySQL auto-commits DDL and cannot roll a batch back: the only safe design is
// one where repeating the whole thing is a no-op.
//
// New columns are the part that was missing. `create table if not exists` is a
// no-op against a table that already exists, so a plugin that added a column in
// a later version got it on fresh installs and never on existing ones — and
// every insert naming it failed at runtime, with the only recovery being a
// purge that destroys the data. Indexes on existing tables were being created
// all along, so half the schema evolved and half did not.
func CreatePluginSchema(db *Database, manifest *PluginManifest) error {
	// Three phases, in this order, because each depends on the one before:
	// a column cannot be added to a table that does not exist, and an index
	// cannot be created on a column that does not exist.
	queries := []string{pluginConfigTableSql(db, manifest)}
	indexes := []string{}

	for i := range manifest.Tables {
		create, tableIndexes, err := pluginTableSql(db, manifest, &manifest.Tables[i])
		if err != nil {
			return err
		}
		queries = append(queries, create)
		indexes = append(indexes, tableIndexes...)
	}

	for _, query := range queries {
		if _, err := db.Exec(query); err != nil {
			return fmt.Errorf("%v while doing %s", err, query)
		}
	}

	for i := range manifest.Tables {
		if err := addMissingPluginColumns(db, manifest, &manifest.Tables[i]); err != nil {
			return err
		}
	}

	for _, query := range indexes {
		if _, err := db.Exec(query); err != nil {
			return fmt.Errorf("%v while doing %s", err, query)
		}
	}

	return nil
}

// addMissingPluginColumns adds manifest columns that the live table does not
// have yet.
//
// Only additions. A column that changed type, or one the manifest dropped, is
// left exactly as it is: rewriting a column loses data on some backends and
// silently truncates on others, and there is no version of "guess what the
// author meant" that is safe to do unattended. A plugin needing that can do it
// from its own startup handler, where it can decide.
func addMissingPluginColumns(db *Database, manifest *PluginManifest, table *PluginTable) error {
	name := manifest.TableName(table.Name)

	for i := range table.Columns {
		column := &table.Columns[i]

		present, err := db.columnPresent(name, column.Name)
		if err != nil {
			return fmt.Errorf("table %q: cannot tell whether column %q exists: %v", table.Name, column.Name, err)
		}
		if present {
			continue
		}

		// A key cannot be introduced after the fact on any backend we support,
		// and pretending otherwise would leave the table subtly wrong. Say so
		// instead: the plugin card shows this, which is the only way an admin
		// would ever find out.
		if column.PrimaryKey {
			return fmt.Errorf(
				"table %q declares %q as a primary key, but the table already exists without it; a key cannot be added to a table that has rows — purge this plugin's data to recreate it",
				table.Name, column.Name,
			)
		}

		columnSql, err := pluginColumnSql(db, column, false)
		if err != nil {
			return fmt.Errorf("table %q: %v", table.Name, err)
		}

		query := fmt.Sprintf("alter table `%s` add column %s", name, columnSql)

		if _, err := db.Exec(query); err != nil {
			return fmt.Errorf(
				"table %q: cannot add column %q: %v. A column that is not nullable needs a default before it can be added to a table that already has rows.",
				table.Name, column.Name, err,
			)
		}
	}

	return nil
}

// DropPluginSchema removes every table a plugin owns, including its settings.
// Only ever called from the explicit "purge plugin data" admin action —
// uninstalling deliberately leaves all of this in place.
func DropPluginSchema(db *Database, manifest *PluginManifest) error {
	names := []string{manifest.TableName(pluginConfigTableName)}
	for i := range manifest.Tables {
		names = append(names, manifest.TableName(manifest.Tables[i].Name))
	}

	for _, name := range names {
		if _, err := db.Exec(fmt.Sprintf("drop table if exists `%s`", name)); err != nil {
			return err
		}
	}

	return nil
}

// --- configuration --------------------------------------------------------

// ReadPluginConfig loads a plugin's settings. Values are stored as JSON so
// booleans and numbers survive the round trip instead of degrading to strings.
func ReadPluginConfig(db *Database, manifest *PluginManifest) (map[string]any, error) {
	config := map[string]any{}

	rows, err := db.Query(fmt.Sprintf("select `key`, `value` from `%s`", manifest.TableName(pluginConfigTableName)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			key   string
			value sql.NullString
		)
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		if !value.Valid {
			config[key] = nil
			continue
		}

		var decoded any
		if err := json.Unmarshal([]byte(value.String), &decoded); err != nil {
			// Tolerate a value that isn't valid JSON rather than failing the
			// whole read — treat it as the plain string it appears to be.
			config[key] = value.String
			continue
		}
		config[key] = decoded
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Manifest defaults fill anything the user has never set, so a plugin can
	// rely on its declared defaults without re-checking for nil everywhere.
	for key, value := range manifest.DefaultConfig() {
		if _, ok := config[key]; !ok {
			config[key] = value
		}
	}

	return config, nil
}

// WritePluginConfigValue upserts a single setting.
func WritePluginConfigValue(db *Database, manifest *PluginManifest, key string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}

	table := manifest.TableName(pluginConfigTableName)

	var count int
	if err := db.QueryRow(fmt.Sprintf("select count(*) from `%s` where `key` = ?", table), key).Scan(&count); err != nil {
		return err
	}

	if count == 0 {
		_, err = db.Exec(fmt.Sprintf("insert into `%s` (`key`, `value`) values (?, ?)", table), key, string(encoded))
	} else {
		_, err = db.Exec(fmt.Sprintf("update `%s` set `value` = ? where `key` = ?", table), string(encoded), key)
	}

	return err
}

// WritePluginConfig replaces the stored settings with the given map.
func WritePluginConfig(db *Database, manifest *PluginManifest, config map[string]any) error {
	for key, value := range config {
		if err := WritePluginConfigValue(db, manifest, key, value); err != nil {
			return err
		}
	}
	return nil
}
