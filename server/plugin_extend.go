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
}

// PluginFieldExtensions returns every registered call-field extension, resolved
// to real table names. Returns nil when no plugin registers one, which lets the
// callers skip the work entirely on a normal install.
func (controller *Controller) PluginFieldExtensions() []pluginResolvedField {
	var resolved []pluginResolvedField

	for _, plugin := range controller.Plugins.Enabled() {
		if plugin.runtime == nil || plugin.Manifest == nil {
			continue
		}
		for _, extension := range plugin.runtime.FieldExtensions() {
			resolved = append(resolved, pluginResolvedField{
				field: extension.Field,
				table: plugin.Manifest.TableName(extension.Table),
				key:   extension.KeyColumn,
				value: extension.ValueColumn,
			})
		}
	}

	return resolved
}

// pluginResolvedSearch is a search extension paired with its real table name.
type pluginResolvedSearch struct {
	table       string
	key         string
	text        string
	resultField string
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
			})
		}
	}

	return resolved
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

		query := fmt.Sprintf(
			"select `%s` from `%s` where `%s` = ?",
			extension.value, extension.table, extension.key,
		)

		if err := controller.Database.QueryRow(query, id).Scan(&value); err != nil {
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

// SetPluginField writes a single field value onto a call without a database
// round trip. Used where the value is already in hand.
func (call *Call) SetPluginField(field string, value any) {
	if call.pluginFields == nil {
		call.pluginFields = map[string]any{}
	}
	call.pluginFields[field] = value
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
