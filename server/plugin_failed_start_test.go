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

// A plugin whose main.js throws part way through has already registered
// whatever it reached before the throw. Starting it fails and its runtime is
// stopped — but the registrations used to stay, pointing at a runtime that no
// longer exists. An override on call.convert left that way answered every
// call with "plugin is stopped", so audio was stored unconverted; a filter on
// call.emit failed once per listener per call, each failure a log row. And it
// happened again on every boot, because the plugin was still enabled.
func TestAPluginThatFailsToStartLeavesNothingRegistered(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	controller := &Controller{
		Config:   &Config{BaseDir: t.TempDir()},
		Database: db,
		Logs:     &Logs{},
		Plugins:  NewPlugins(),
	}
	controller.PluginDispatch = NewPluginDispatch(controller)
	controller.PluginRpc = NewPluginRpc(controller)

	dir := filepath.Join(controller.Plugins.Dir(controller.Config), "halfway")
	if err := os.MkdirAll(dir, 0o770); err != nil {
		t.Fatal(err)
	}

	manifest := `{"id": "halfway", "name": "Halfway", "version": "1.0.0", "apiVersion": 1,
		"description": "Registers a handler, then throws.", "main": "main.js"}`
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifest), 0o660); err != nil {
		t.Fatal(err)
	}

	main := `
		rdio.filter('call.store', function (value) { return value; });
		rdio.plugins.handle('ping', function () { return 'pong'; });
		thisIsNotDefined.anywhere;
	`
	if err := os.WriteFile(filepath.Join(dir, "main.js"), []byte(main), 0o660); err != nil {
		t.Fatal(err)
	}

	if err := controller.Plugins.Read(db, controller.Config); err != nil {
		t.Fatal(err)
	}
	if err := controller.Plugins.SetEnabled(db, "halfway", true); err != nil {
		t.Fatal(err)
	}
	if err := controller.Plugins.Read(db, controller.Config); err != nil {
		t.Fatal(err)
	}

	if err := controller.Plugins.Start(controller); err != nil {
		t.Fatal(err)
	}
	defer controller.Plugins.Stop(controller)

	plugin, ok := controller.Plugins.Get("halfway")
	if !ok {
		t.Fatal("the plugin was not read")
	}
	if plugin.Running || plugin.Error == "" {
		t.Fatalf("the plugin was expected to fail to start (running=%v, error=%q)", plugin.Running, plugin.Error)
	}

	if controller.PluginDispatch.Active(PointCallStore) {
		t.Error("a plugin that failed to start still has a handler on call.store")
	}

	if _, err := controller.PluginRpc.Call("other", "halfway", "ping", nil); err == nil {
		t.Error("a plugin that failed to start still answers plugin-to-plugin calls")
	}
}
