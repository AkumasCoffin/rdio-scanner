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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// PluginManifestName is the file every plugin must provide at the root of its
// directory. It is the only thing read before deciding whether a plugin may be
// installed at all, so it has to be self-describing.
const PluginManifestName = "plugin.json"

// A plugin id becomes a directory name, an HTTP route segment and a SQL table
// prefix, so it is deliberately narrow: lowercase, no dots, no underscores (the
// underscore is reserved as the separator in `plugin_<id>_<table>`), and short
// enough that the longest generated table name stays inside the 64-character
// identifier limit every supported backend shares.
var pluginIdRegexp = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

// Declared table names are appended to the plugin prefix, so they get the same
// treatment. Underscores are allowed here because the prefix has already been
// established by that point.
var pluginTableNameRegexp = regexp.MustCompile(`^[a-z][a-z0-9_]{1,31}$`)

var pluginColumnNameRegexp = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{0,63}$`)

// Permissions a manifest may request. Anything outside this set is rejected at
// parse time rather than silently ignored, so a typo can't quietly leave a
// plugin without the access it expects.
const (
	PluginPermissionCallsRead      = "calls-read"
	PluginPermissionCallsWrite     = "calls-write"
	PluginPermissionConfigExpose   = "config-expose"
	PluginPermissionHttp           = "http"
	PluginPermissionRoutes         = "routes"
	PluginPermissionRoutesAbsolute = "routes-absolute"
	PluginPermissionWs             = "ws"
)

var pluginPermissions = map[string]bool{
	PluginPermissionCallsRead:      true,
	PluginPermissionCallsWrite:     true,
	PluginPermissionConfigExpose:   true,
	PluginPermissionHttp:           true,
	PluginPermissionRoutes:         true,
	PluginPermissionRoutesAbsolute: true,
	PluginPermissionWs:             true,
}

// Config field types the admin panel knows how to render. Kept in sync with
// the form builder in the webapp's plugin config component.
var pluginConfigTypes = map[string]bool{
	"boolean":   true,
	"number":    true,
	"password":  true,
	"select":    true,
	"system":    true,
	"talkgroup": true,
	"text":      true,
	"textarea":  true,
}

// Dialect-neutral column types. plugin_db.go maps these onto the concrete SQL
// each backend wants; plugins never write backend-specific DDL.
var pluginColumnTypes = map[string]bool{
	"bigint":   true,
	"blob":     true,
	"boolean":  true,
	"datetime": true,
	"float":    true,
	"int":      true,
	"text":     true,
	"varchar":  true,
}

type PluginManifest struct {
	Id               string                `json:"id"`
	Name             string                `json:"name"`
	Version          string                `json:"version"`
	Description      string                `json:"description"`
	Author           string                `json:"author,omitempty"`
	License          string                `json:"license,omitempty"`
	Homepage         string                `json:"homepage,omitempty"`
	MinServerVersion string                `json:"minServerVersion,omitempty"`
	MaxServerVersion string                `json:"maxServerVersion,omitempty"`
	Main             string                `json:"main,omitempty"`
	Web              string                `json:"web,omitempty"`
	Permissions      []string              `json:"permissions,omitempty"`
	Config           []PluginConfigField   `json:"config,omitempty"`
	Tables           []PluginTable         `json:"tables,omitempty"`
}

type PluginConfigField struct {
	Key         string               `json:"key"`
	Type        string               `json:"type"`
	Label       string               `json:"label"`
	Help        string               `json:"help,omitempty"`
	Default     any                  `json:"default,omitempty"`
	Options     []PluginConfigOption `json:"options,omitempty"`
	Min         *float64             `json:"min,omitempty"`
	Max         *float64             `json:"max,omitempty"`
	MaxLength   *int                 `json:"maxLength,omitempty"`
	Required    bool                 `json:"required,omitempty"`
	Placeholder string               `json:"placeholder,omitempty"`
}

type PluginConfigOption struct {
	Value any    `json:"value"`
	Label string `json:"label"`
}

type PluginTable struct {
	Name    string         `json:"name"`
	Columns []PluginColumn `json:"columns"`
}

type PluginColumn struct {
	Name          string `json:"name"`
	Type          string `json:"type"`
	Length        int    `json:"length,omitempty"`
	PrimaryKey    bool   `json:"primaryKey,omitempty"`
	AutoIncrement bool   `json:"autoIncrement,omitempty"`
	Nullable      *bool  `json:"nullable,omitempty"`
	Default       any    `json:"default,omitempty"`
	Index         bool   `json:"index,omitempty"`
}

// IsNullable defaults to true when the manifest doesn't say. Columns are
// nullable unless a plugin deliberately opts out, which keeps schema changes
// between plugin versions from failing on existing rows.
func (c *PluginColumn) IsNullable() bool {
	if c.Nullable == nil {
		return true
	}
	return *c.Nullable
}

// ParsePluginManifest decodes and fully validates a manifest. Validation is
// strict and happens before anything is written to disk or the database: a
// malformed manifest must fail at install time, not halfway through creating
// tables.
func ParsePluginManifest(b []byte) (*PluginManifest, error) {
	manifest := &PluginManifest{}

	decoder := json.NewDecoder(strings.NewReader(string(b)))
	if err := decoder.Decode(manifest); err != nil {
		return nil, fmt.Errorf("%s is not valid json: %v", PluginManifestName, err)
	}

	if err := manifest.Validate(); err != nil {
		return nil, err
	}

	return manifest, nil
}

// ReadPluginManifest loads the manifest from a plugin directory on disk.
func ReadPluginManifest(dir string) (*PluginManifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, PluginManifestName))
	if err != nil {
		return nil, err
	}
	return ParsePluginManifest(b)
}

func (manifest *PluginManifest) Validate() error {
	if !pluginIdRegexp.MatchString(manifest.Id) {
		return fmt.Errorf("invalid plugin id %q: must be 2-32 characters, lowercase letters, digits and hyphens, starting with a letter", manifest.Id)
	}

	if strings.TrimSpace(manifest.Name) == "" {
		return fmt.Errorf("plugin %s: name is required", manifest.Id)
	}

	if strings.TrimSpace(manifest.Version) == "" {
		return fmt.Errorf("plugin %s: version is required", manifest.Id)
	}

	if strings.TrimSpace(manifest.Description) == "" {
		return fmt.Errorf("plugin %s: description is required", manifest.Id)
	}

	// A plugin with neither half does nothing at all — almost certainly a
	// mistake in the manifest rather than an intentional no-op.
	if strings.TrimSpace(manifest.Main) == "" && strings.TrimSpace(manifest.Web) == "" {
		return fmt.Errorf("plugin %s: must declare at least one of main or web", manifest.Id)
	}

	// Entry points are resolved relative to the plugin directory, so they must
	// not be able to escape it.
	for label, entry := range map[string]string{"main": manifest.Main, "web": manifest.Web} {
		if entry == "" {
			continue
		}
		if err := validatePluginRelPath(entry); err != nil {
			return fmt.Errorf("plugin %s: %s: %v", manifest.Id, label, err)
		}
	}

	for _, permission := range manifest.Permissions {
		if !pluginPermissions[permission] {
			return fmt.Errorf("plugin %s: unknown permission %q", manifest.Id, permission)
		}
	}

	seenKeys := map[string]bool{}
	for i := range manifest.Config {
		field := &manifest.Config[i]

		if strings.TrimSpace(field.Key) == "" {
			return fmt.Errorf("plugin %s: config field %d has no key", manifest.Id, i)
		}
		if seenKeys[field.Key] {
			return fmt.Errorf("plugin %s: duplicate config key %q", manifest.Id, field.Key)
		}
		seenKeys[field.Key] = true

		if !pluginConfigTypes[field.Type] {
			return fmt.Errorf("plugin %s: config field %q has unknown type %q", manifest.Id, field.Key, field.Type)
		}
		if strings.TrimSpace(field.Label) == "" {
			return fmt.Errorf("plugin %s: config field %q has no label", manifest.Id, field.Key)
		}
		if field.Type == "select" && len(field.Options) == 0 {
			return fmt.Errorf("plugin %s: config field %q is a select but declares no options", manifest.Id, field.Key)
		}
	}

	seenTables := map[string]bool{}
	for i := range manifest.Tables {
		table := &manifest.Tables[i]

		if !pluginTableNameRegexp.MatchString(table.Name) {
			return fmt.Errorf("plugin %s: invalid table name %q: must be 2-32 characters, lowercase letters, digits and underscores, starting with a letter", manifest.Id, table.Name)
		}
		// `config` is created and owned by the host for every plugin; letting a
		// manifest redeclare it would let a plugin reshape its own settings
		// storage out from under the admin panel.
		if table.Name == pluginConfigTableName {
			return fmt.Errorf("plugin %s: table name %q is reserved", manifest.Id, table.Name)
		}
		if seenTables[table.Name] {
			return fmt.Errorf("plugin %s: duplicate table %q", manifest.Id, table.Name)
		}
		seenTables[table.Name] = true

		if len(table.Columns) == 0 {
			return fmt.Errorf("plugin %s: table %q declares no columns", manifest.Id, table.Name)
		}

		seenColumns := map[string]bool{}
		autoIncrements := 0
		for j := range table.Columns {
			column := &table.Columns[j]

			if !pluginColumnNameRegexp.MatchString(column.Name) {
				return fmt.Errorf("plugin %s: table %q has invalid column name %q", manifest.Id, table.Name, column.Name)
			}
			if seenColumns[column.Name] {
				return fmt.Errorf("plugin %s: table %q has duplicate column %q", manifest.Id, table.Name, column.Name)
			}
			seenColumns[column.Name] = true

			if !pluginColumnTypes[column.Type] {
				return fmt.Errorf("plugin %s: table %q column %q has unknown type %q", manifest.Id, table.Name, column.Name, column.Type)
			}
			if column.Type == "varchar" && column.Length <= 0 {
				return fmt.Errorf("plugin %s: table %q column %q is varchar and must declare a positive length", manifest.Id, table.Name, column.Name)
			}
			if column.AutoIncrement {
				autoIncrements++
				if column.Type != "int" && column.Type != "bigint" {
					return fmt.Errorf("plugin %s: table %q column %q is autoIncrement and must be int or bigint", manifest.Id, table.Name, column.Name)
				}
				if !column.PrimaryKey {
					return fmt.Errorf("plugin %s: table %q column %q is autoIncrement and must be the primary key", manifest.Id, table.Name, column.Name)
				}
			}
		}

		// SQLite in particular only supports one AUTOINCREMENT column, and it
		// must be the primary key. Rejecting here keeps the error next to the
		// mistake instead of surfacing as a confusing DDL failure later.
		if autoIncrements > 1 {
			return fmt.Errorf("plugin %s: table %q declares more than one autoIncrement column", manifest.Id, table.Name)
		}
	}

	return nil
}

// validatePluginRelPath rejects anything that could resolve outside the plugin
// directory. Manifests are third-party input and entry points are joined onto a
// filesystem path, so this is a traversal guard, not a style check.
func validatePluginRelPath(p string) error {
	if strings.TrimSpace(p) == "" {
		return fmt.Errorf("path is empty")
	}
	if strings.Contains(p, "\\") {
		return fmt.Errorf("path %q must use forward slashes", p)
	}
	if strings.HasPrefix(p, "/") {
		return fmt.Errorf("path %q must be relative", p)
	}
	if filepath.IsAbs(p) {
		return fmt.Errorf("path %q must be relative", p)
	}
	// path.Clean would collapse "a/../../b" to "../b", which Contains(".."")
	// alone would miss on the raw string; check the cleaned form.
	cleaned := filepath.ToSlash(filepath.Clean(p))
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("path %q escapes the plugin directory", p)
	}
	return nil
}

// TablePrefix is the namespace every table this plugin owns lives under. The
// hyphen-to-underscore swap keeps generated identifiers valid without quoting
// on all three backends.
func (manifest *PluginManifest) TablePrefix() string {
	return "plugin_" + strings.ReplaceAll(manifest.Id, "-", "_") + "_"
}

// TableName maps a manifest-declared table name onto its real, namespaced name.
func (manifest *PluginManifest) TableName(declared string) string {
	return manifest.TablePrefix() + declared
}

func (manifest *PluginManifest) HasPermission(permission string) bool {
	for _, p := range manifest.Permissions {
		if p == permission {
			return true
		}
	}
	return false
}

// CompatibleWith reports whether this plugin can run on the given server
// version, and if not, why. The reason is surfaced in the admin panel so a
// user isn't left wondering why Install is greyed out.
func (manifest *PluginManifest) CompatibleWith(serverVersion string) (bool, string) {
	if v := strings.TrimSpace(manifest.MinServerVersion); v != "" {
		if compareVersions(serverVersion, v) < 0 {
			return false, fmt.Sprintf("requires Rdio Scanner %s or newer", v)
		}
	}
	if v := strings.TrimSpace(manifest.MaxServerVersion); v != "" {
		if compareVersions(serverVersion, v) > 0 {
			return false, fmt.Sprintf("requires Rdio Scanner %s or older", v)
		}
	}
	return true, ""
}

// DefaultConfig is the starting configuration written when a plugin is
// installed, taken from the manifest's declared defaults.
func (manifest *PluginManifest) DefaultConfig() map[string]any {
	config := map[string]any{}
	for i := range manifest.Config {
		field := &manifest.Config[i]
		if field.Default != nil {
			config[field.Key] = field.Default
		}
	}
	return config
}
