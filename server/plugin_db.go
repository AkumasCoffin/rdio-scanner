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

// pluginTableIdentifier matches a backtick-quoted identifier in plugin SQL.
// Plugins write their declared table names; the host rewrites them to the real
// namespaced names and rejects anything that resolves outside the plugin.
var pluginTableIdentifier = regexp.MustCompile("`([a-zA-Z_][a-zA-Z0-9_]*)`")

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
// rewritten so unqualified table names resolve to that plugin's namespace, and
// anything referencing a table outside it is refused. This is what makes
// "plugins get their own tables" an enforced boundary rather than a convention.
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
// Nothing is refused. A plugin may read and write any table in the database,
// core's included — a query it could not run here it could run by other means,
// and pretending otherwise only made the boundary look stronger than it was.
// The prefix mapping stays because it is a convenience: a plugin writes the
// table name it declared and does not have to know or repeat its own namespace.
//
// Column names are backtick-quoted too, so the rule is simply: an identifier
// matching a declared table name is rewritten, anything else is left alone.
func (pluginDb *PluginDb) rewrite(query string) (string, error) {
	rewritten := pluginTableIdentifier.ReplaceAllStringFunc(query, func(match string) string {
		name := strings.Trim(match, "`")

		if pluginDb.tables[name] {
			return "`" + pluginDb.prefix + name + "`"
		}

		return match
	})

	return rewritten, nil
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

	rows, err := pluginDb.database.Query(rewritten, args...)
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
func pluginTableSql(db *Database, manifest *PluginManifest, table *PluginTable) ([]string, error) {
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
			return nil, fmt.Errorf("table %q: %v", table.Name, err)
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

	queries := []string{
		fmt.Sprintf("create table if not exists `%s` (%s)", name, strings.Join(parts, ", ")),
	}

	for i := range table.Columns {
		column := &table.Columns[i]
		if !column.Index || column.PrimaryKey {
			continue
		}
		// Index names share the table's namespace so two plugins can declare
		// the same column name without colliding on Postgres, where index
		// names are database-wide rather than per-table.
		queries = append(queries, fmt.Sprintf(
			"create index if not exists `%s_%s_idx` on `%s` (`%s`)",
			name, column.Name, name, column.Name,
		))
	}

	return queries, nil
}

// pluginConfigTableSql is the host-owned settings table. Identical for every
// plugin, so it isn't declared in manifests.
func pluginConfigTableSql(db *Database, manifest *PluginManifest) string {
	return fmt.Sprintf(
		"create table if not exists `%s` (`key` varchar(255) not null, `value` text, primary key (`key`))",
		manifest.TableName(pluginConfigTableName),
	)
}

// CreatePluginSchema creates the config table and every manifest-declared table.
//
// This is recorded in rdioScannerMeta keyed by plugin id *and* version, so a
// plugin that adds a table in a later version gets it created on upgrade while
// already-created tables are left alone. Statements are all "if not exists", so
// a partially-applied run is safe to repeat — which matters because MySQL
// auto-commits DDL and cannot roll the batch back.
func CreatePluginSchema(db *Database, manifest *PluginManifest) error {
	queries := []string{pluginConfigTableSql(db, manifest)}

	for i := range manifest.Tables {
		tableQueries, err := pluginTableSql(db, manifest, &manifest.Tables[i])
		if err != nil {
			return err
		}
		queries = append(queries, tableQueries...)
	}

	for _, query := range queries {
		if _, err := db.Exec(query); err != nil {
			return fmt.Errorf("%v while doing %s", err, query)
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
