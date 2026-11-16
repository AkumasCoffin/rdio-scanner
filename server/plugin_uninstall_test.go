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
	"testing"
)

// An installed plugin with a table of its own holding a row, and a file in its
// data directory — everything "Remove data" is supposed to take away.
func installPluginWithData(t *testing.T) (controller *Controller, dataDir string) {
	t.Helper()

	db := newTestDatabase(t)
	t.Cleanup(func() { db.Sql.Close() })

	controller = &Controller{
		Config:   &Config{BaseDir: t.TempDir()},
		Database: db,
		Logs:     &Logs{},
		Plugins:  NewPlugins(),
	}
	controller.PluginDispatch = NewPluginDispatch(controller)
	controller.PluginRpc = NewPluginRpc(controller)

	dir := filepath.Join(controller.Plugins.Dir(controller.Config), "keeper")
	if err := os.MkdirAll(dir, 0o770); err != nil {
		t.Fatal(err)
	}

	manifest := `{
		"id": "keeper", "name": "Keeper", "version": "1.0.0", "apiVersion": 1, "main": "main.js",
		"description": "Keeps a note.",
		"tables": [{"name": "notes", "columns": [
			{"name": "id", "type": "int", "primaryKey": true},
			{"name": "body", "type": "text"}
		]}]
	}`
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifest), 0o660); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.js"), []byte("// nothing\n"), 0o660); err != nil {
		t.Fatal(err)
	}

	if err := controller.Plugins.Read(db, controller.Config); err != nil {
		t.Fatal(err)
	}

	plugin, ok := controller.Plugins.Get("keeper")
	if !ok || plugin.Manifest == nil {
		t.Fatal("the plugin on disk was not picked up")
	}

	if err := CreatePluginSchema(db, plugin.Manifest); err != nil {
		t.Fatalf("cannot create the plugin's tables: %v", err)
	}
	if _, err := db.Exec("insert into `plugin_keeper_notes` (`id`, `body`) values (?, ?)", 1, "remember this"); err != nil {
		t.Fatalf("cannot store a row: %v", err)
	}

	dataDir, err := controller.Plugins.DataDir(controller.Config, "keeper")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "state.json"), []byte("{}"), 0o660); err != nil {
		t.Fatal(err)
	}

	return controller, dataDir
}

func pluginTableReadable(controller *Controller) bool {
	var n int
	return controller.Database.QueryRow("select count(*) from `plugin_keeper_notes`").Scan(&n) == nil
}

// The uninstall dialog offers "Remove data" and says it is the only chance to
// do it, which is true: the manifest that names the tables goes with the
// registry row. The server decoded that choice and never looked at it.
func TestUninstallRemovesTheDataWhenAsked(t *testing.T) {
	controller, dataDir := installPluginWithData(t)

	if _, err := controller.uninstallPlugin("keeper", true); err != nil {
		t.Fatalf("uninstall failed: %v", err)
	}

	if pluginTableReadable(controller) {
		t.Error("the plugin's table is still there after uninstalling with its data")
	}
	if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
		t.Errorf("the plugin's data directory is still there (stat: %v)", err)
	}
	if _, ok := controller.Plugins.Get("keeper"); ok {
		t.Error("the plugin is still registered")
	}
	if _, err := os.Stat(filepath.Join(controller.Plugins.Dir(controller.Config), "keeper")); !os.IsNotExist(err) {
		t.Error("the plugin's code is still on disk")
	}
}

// And the other button has to keep meaning what it says: reinstalling finds
// the settings and data where they were left.
func TestUninstallKeepsTheDataWhenAsked(t *testing.T) {
	controller, dataDir := installPluginWithData(t)

	if _, err := controller.uninstallPlugin("keeper", false); err != nil {
		t.Fatalf("uninstall failed: %v", err)
	}

	if !pluginTableReadable(controller) {
		t.Error("the plugin's table was dropped by an uninstall that was asked to keep it")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "state.json")); err != nil {
		t.Errorf("the plugin's data directory was removed by an uninstall that was asked to keep it: %v", err)
	}
	if _, ok := controller.Plugins.Get("keeper"); ok {
		t.Error("the plugin is still registered")
	}
}
