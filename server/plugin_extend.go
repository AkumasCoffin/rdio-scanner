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
	"fmt"
	"strings"
)

// This file is the read side of the declarative extension points. Everything
// here runs in Go on paths that execute per call or per search row; none of it
// enters a plugin's JavaScript runtime. That is the whole point — a plugin
// computes values when it has work to do, and the host reads them back at
// native speed when serving clients.

// pluginResolvedField is a field extension paired with the real, namespaced
// table name it reads from.
type pluginResolvedField struct {
	field string
	table string
	key   string
	value string
	// query is built once when the extension is resolved rather than per call.
	// This runs on the emit dispatcher, which sits upstream of ingest, so
	// formatting a string per extension per call was work paid for on the one
	// goroutine everything else waits behind.
	query string
}

// PluginFieldExtensions returns every registered call-field extension, resolved
// to real table names. Returns nil when no plugin registers one, which lets the
// callers skip the work entirely on a normal install.
func (controller *Controller) PluginFieldExtensions() []pluginResolvedField {
	// Cached, because this is read once per call on the emit path and the
	// answer only changes when a plugin starts or stops. Rebuilding it meant
	// walking the plugin list and allocating three slices under two locks for
	// every call served.
	if cached := controller.pluginFieldCache.Load(); cached != nil {
		return *cached
	}

	var resolved []pluginResolvedField

	for _, plugin := range controller.Plugins.Enabled() {
		_, runtime, ok := controller.Plugins.RunningRuntime(plugin.PluginId)
		if !ok || plugin.Manifest == nil {
			continue
		}
		for _, extension := range runtime.FieldExtensions() {
			table := plugin.Manifest.TableName(extension.Table)

			resolved = append(resolved, pluginResolvedField{
				field: extension.Field,
				table: table,
				key:   extension.KeyColumn,
				value: extension.ValueColumn,
				query: fmt.Sprintf(
					"select `%s` from `%s` where `%s` = ?",
					extension.ValueColumn, table, extension.KeyColumn,
				),
			})
		}
	}

	controller.pluginFieldCache.Store(&resolved)

	return resolved
}

// InvalidatePluginExtensions drops the cached extension list. Called whenever a
// plugin starts or stops, which is the only thing that can change it.
func (controller *Controller) InvalidatePluginExtensions() {
	controller.pluginFieldCache.Store(nil)
}

// pluginResolvedSearch is a search extension paired with its real table name.
type pluginResolvedSearch struct {
	table       string
	key         string
	text        string
	resultField string
	label       string
}

// PluginSearchFilter is one "has it / has none" filter offered to the webapp,
// named by the plugin that registered the text behind it.
type PluginSearchFilter struct {
	Field string `json:"field"`
	Label string `json:"label"`
}

// PluginSearchExtensions returns every registered search extension, resolved to
// real table names.
func (controller *Controller) PluginSearchExtensions() []pluginResolvedSearch {
	var resolved []pluginResolvedSearch

	for _, plugin := range controller.Plugins.Enabled() {
		if plugin.runtime == nil || plugin.Manifest == nil {
			continue
		}
		for _, extension := range plugin.runtime.SearchExtensions() {
			field := extension.ResultField
			if field == "" {
				field = extension.TextColumn
			}
			resolved = append(resolved, pluginResolvedSearch{
				table:       plugin.Manifest.TableName(extension.Table),
				key:         extension.KeyColumn,
				text:        extension.TextColumn,
				resultField: field,
				label:       extension.Label,
			})
		}
	}

	return resolved
}

// PluginSearchFilters lists the presence filters the webapp may offer.
//
// The server contributes the mechanism and the plugin contributes the noun:
// core knows a call either has some registered text or does not, and nothing
// about what that text is. Without this the webapp would have to hardcode a
// word like "transcript" for a feature no part of core implements, and the
// filter would still be there with the plugin uninstalled.
func (controller *Controller) PluginSearchFilters() []PluginSearchFilter {
	filters := []PluginSearchFilter{}
	seen := map[string]bool{}

	for _, extension := range controller.PluginSearchExtensions() {
		if extension.label == "" || extension.resultField == "" || seen[extension.resultField] {
			continue
		}
		seen[extension.resultField] = true
		filters = append(filters, PluginSearchFilter{Field: extension.resultField, Label: extension.label})
	}

	return filters
}

// ApplyPluginFields fills in a call's plugin-contributed fields. Called on the
// paths that hand a call to a client; a no-op when nothing is registered.
func (controller *Controller) ApplyPluginFields(call *Call) {
	if call == nil {
		return
	}

	extensions := controller.PluginFieldExtensions()
	if len(extensions) == 0 {
		return
	}

	id, ok := call.Id.(uint)
	if !ok {
		return
	}

	for _, extension := range extensions {
		var value sql.NullString

		if err := controller.Database.QueryRow(extension.query, id).Scan(&value); err != nil {
			// A missing row is the normal case — most calls have no value for
			// most extensions. Anything else is a plugin's own schema problem
			// and shouldn't break serving the call.
			continue
		}

		if !value.Valid {
			continue
		}

		if call.pluginFields == nil {
			call.pluginFields = map[string]any{}
		}
		call.pluginFields[extension.field] = value.String
	}
}

// PluginWebEntries lists the frontend entry points of enabled plugins, in the
// shape the webapp's plugin loader expects. Sent on the CFG payload because the
// webapp needs it before it has an admin session.
func (controller *Controller) PluginWebEntries() []map[string]any {
	entries := []map[string]any{}

	for _, plugin := range controller.Plugins.Enabled() {
		if plugin.Manifest == nil || strings.TrimSpace(plugin.Manifest.Web) == "" {
			continue
		}

		entries = append(entries, map[string]any{
			"id":      plugin.Manifest.Id,
			"name":    plugin.Manifest.Name,
			"version": plugin.Manifest.Version,
			"entry":   fmt.Sprintf("plugins/%s/%s", plugin.Manifest.Id, plugin.Manifest.Web),
			"base":    fmt.Sprintf("plugins/%s/", plugin.Manifest.Id),
		})
	}

	return entries
}

// PluginCapabilities returns the feature names plugins advertise, for
// /api/capabilities. Peer servers use this to decide what they can rely on.
func (controller *Controller) PluginCapabilities() []string {
	features := []string{}
	seen := map[string]bool{}

	for _, plugin := range controller.Plugins.Enabled() {
		if plugin.runtime == nil {
			continue
		}
		for _, name := range plugin.runtime.Capabilities() {
			if seen[name] {
				continue
			}
			seen[name] = true
			features = append(features, name)
		}
	}

	return features
}

// Per-call plugin rows are removed in slices of this many call ids, at most
// this many slices per prune cycle, so the first cycle after an upgrade —
// which may find months of leftovers — cannot hold a connection for minutes
// or bloat a single transaction. A cycle that does not finish carries on at
// the next one, an hour later.
const (
	pluginOrphanSlice     = 50000
	pluginOrphanMaxSlices = 20
)

// pruneCallExtensionRows deletes what plugins stored against calls that
// retention has since removed.
//
// A plugin that registers a searchable column keeps one row per call, keyed by
// the call's id. Pruning deleted the calls and nothing deleted those rows: the
// transcripts table kept every transcript ever made, its search index with
// it, for as long as the install lived — and a call id handed out again after
// a restart found the old call's text already waiting for it.
//
// Only rows keyed below the oldest surviving call are taken. Ids rise with
// time and retention removes the oldest, so that is nearly all of them, and it
// is a range the primary key answers directly on every backend — where
// "every key with no matching call" is an anti-join over both tables, hourly,
// on the largest table there is. A call back-filled out of order and pruned
// early leaves its row behind until the ids around it age out too.
func pruneCallExtensionRows(db *Database, extensions []pluginResolvedSearch) (int64, error) {
	var oldest sql.NullInt64

	if err := db.QueryRow("select min(`id`) from `rdioScannerCalls`").Scan(&oldest); err != nil {
		return 0, err
	}

	// No calls at all says nothing about what is stale — a fresh database and
	// an emptied one look the same from here — so nothing is removed.
	if !oldest.Valid {
		return 0, nil
	}

	var removed int64
	seen := map[string]bool{}

	for _, extension := range extensions {
		// Two fields of one plugin are commonly columns of the same table.
		target := extension.table + "\x00" + extension.key
		if seen[target] {
			continue
		}
		seen[target] = true

		if !pluginIndexIdentifier.MatchString(extension.table) || !pluginIndexIdentifier.MatchString(extension.key) {
			continue
		}

		for slice := 0; slice < pluginOrphanMaxSlices; slice++ {
			var first sql.NullInt64

			query := fmt.Sprintf("select min(`%s`) from `%s`", extension.key, extension.table)
			if err := db.QueryRow(query).Scan(&first); err != nil {
				return removed, fmt.Errorf("%s: %v", extension.table, err)
			}

			if !first.Valid || first.Int64 >= oldest.Int64 {
				break
			}

			until := first.Int64 + pluginOrphanSlice
			if until > oldest.Int64 {
				until = oldest.Int64
			}

			query = fmt.Sprintf("delete from `%s` where `%s` < ?", extension.table, extension.key)
			result, err := db.Exec(query, until)
			if err != nil {
				return removed, fmt.Errorf("%s: %v", extension.table, err)
			}

			if n, err := result.RowsAffected(); err == nil {
				removed += n
			}
		}
	}

	return removed, nil
}
