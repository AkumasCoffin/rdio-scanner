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
	"time"
)

// Restores a feature that used to be built in, after it moved to a plugin.
//
// A server that had transcription configured and then upgraded would otherwise
// go quiet: the settings are still there, the transcripts are still there, and
// nothing transcribes. Rather than leave that to be discovered, the plugin that
// now provides the feature is fetched and enabled on first start.
//
// This is deliberately narrow. It fires once, only for a feature that genuinely
// used to ship in the server, and only when that feature was actually in use.
// It is an upgrade path, not a mechanism for installing plugins on a user's
// behalf in general.

// legacyFeature describes a feature that left the server for a plugin.
type legacyFeature struct {
	// optionKey is the configuration row that proves the feature was in use.
	optionKey string
	// pluginId is the plugin that now provides it.
	pluginId string
	// migrationName records that the offer was made, so it happens once
	// whether or not it succeeded.
	migrationName string
	// label is what the log calls the feature.
	label string
}

var legacyFeatures = []legacyFeature{
	{
		optionKey:     "option.transcriptionEnabled",
		pluginId:      "transcripts",
		migrationName: "20260802110000-transcripts-plugin-offered",
		label:         "call transcription",
	},
}

// RestoreLegacyPlugins installs and enables plugins for features that used to
// be built into the server and were in use before the upgrade.
//
// Failures are never fatal and never destructive. An offline server simply
// keeps its settings and its existing transcripts, logs what happened, and can
// install the plugin by hand whenever it suits.
func (controller *Controller) RestoreLegacyPlugins() {
	for _, feature := range legacyFeatures {
		controller.restoreLegacyPlugin(feature)
	}
}

func (controller *Controller) restoreLegacyPlugin(feature legacyFeature) {
	db := controller.Database

	// Already offered on a previous start.
	if done, err := db.migrationDone(feature.migrationName); err != nil || done {
		return
	}

	// Was the feature actually in use? A server that never enabled it should
	// not gain a plugin it never asked for.
	var raw string
	err := db.QueryRow(
		db.formatQuery("select `val` from `rdioScannerConfigs` where `key` = ?"), feature.optionKey,
	).Scan(&raw)
	if err != nil {
		// No such row: the feature was never configured here. Record the offer
		// as made so this lookup doesn't repeat on every start.
		db.recordMigration(feature.migrationName)
		return
	}

	// The value is stored as JSON, so "false" and "" both mean not in use.
	if raw != "true" {
		db.recordMigration(feature.migrationName)
		return
	}

	// Already installed by hand between the upgrade and this start.
	if _, ok := controller.Plugins.Get(feature.pluginId); ok {
		db.recordMigration(feature.migrationName)
		return
	}

	controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf(
		"%s has moved into a plugin; installing %s to restore it", feature.label, feature.pluginId,
	))

	manifest, commit, err := controller.PluginStore.Install("", "main", feature.pluginId)
	if err != nil {
		// Nothing is recorded, so this is retried on the next start — an
		// offline server that comes back online later still gets the plugin.
		controller.Logs.LogEvent(LogLevelWarn, fmt.Sprintf(
			"could not install the %s plugin automatically: %v. Your %s settings and existing data are untouched — "+
				"install the plugin from the admin panel under Plugins, or restart once this server can reach the internet",
			feature.pluginId, err, feature.label,
		))
		return
	}

	plugin := &Plugin{
		PluginId:    manifest.Id,
		Name:        manifest.Name,
		Version:     manifest.Version,
		Source:      DefaultPluginRepo,
		Branch:      "main",
		Enabled:     true,
		InstalledAt: time.Now().UTC(),
		Manifest:    manifest,
		Commit:      commit,
	}

	if err := controller.Plugins.Write(db, plugin); err != nil {
		controller.Logs.LogEvent(LogLevelError, fmt.Sprintf(
			"could not register the %s plugin: %v", feature.pluginId, err,
		))
		return
	}

	if err := CreatePluginSchema(db, manifest); err != nil {
		controller.Logs.LogEvent(LogLevelError, fmt.Sprintf(
			"could not create tables for the %s plugin: %v", feature.pluginId, err,
		))
		return
	}

	// Settings were already moved into the plugin's table by the migration, so
	// only fill in defaults for keys that migration didn't carry across.
	if config, err := ReadPluginConfig(db, manifest); err == nil {
		for key, value := range manifest.DefaultConfig() {
			if _, ok := config[key]; !ok {
				WritePluginConfigValue(db, manifest, key, value)
			}
		}
	}

	db.recordMigration(feature.migrationName)

	controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf(
		"%s plugin %s installed and enabled, carrying over your existing settings", feature.pluginId, manifest.Version,
	))
}
