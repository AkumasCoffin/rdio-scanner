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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A branch name is allowed to contain slashes, and feature/x is the commonest
// convention there is. url.PathEscape turns those into %2F, which GitHub does
// not accept as a path — so such a branch listed in the admin panel and then
// 404ed for both its manifests and its tarball, with nothing explaining why.
func TestBranchesWithSlashesStayUsable(t *testing.T) {
	if got := escapeBranch("feature/audio"); got != "feature/audio" {
		t.Errorf("a branch with a slash became %q, which GitHub reads as one path segment", got)
	}
	if got := escapeBranch("main"); got != "main" {
		t.Errorf("an ordinary branch became %q", got)
	}
	// Everything else still has to be escaped — a branch may contain a space
	// or a character that would otherwise change the URL's meaning.
	if got := escapeBranch("fix/a b"); got != "fix/a%20b" {
		t.Errorf("a branch with a space became %q", got)
	}
	if got := escapeBranch("release/1.0?x"); !strings.HasPrefix(got, "release/1.0%3F") {
		t.Errorf("a branch with a query character became %q", got)
	}
}

// A crashed install used to leave a staging directory inside the plugins
// directory, which the scan then reported as a broken plugin on every boot —
// and which could not be removed from the admin panel either, because the same
// id validator rejects a leading dot.
func TestScanIgnoresDirectoriesThatCannotBePluginIds(t *testing.T) {
	dir := t.TempDir()
	config := &Config{BaseDir: dir}

	plugins := NewPlugins()
	pluginsDir := plugins.Dir(config)

	if err := os.MkdirAll(pluginsDir, 0o770); err != nil {
		t.Fatal(err)
	}

	// Leftovers of the two shapes an interrupted install can produce, each
	// carrying a manifest so they would otherwise be taken for plugins.
	for _, name := range []string{".install-123456", "good.replacing", "Not_An_Id"} {
		path := filepath.Join(pluginsDir, name)
		if err := os.MkdirAll(path, 0o770); err != nil {
			t.Fatal(err)
		}
		manifest := `{"id":"good","name":"Good","version":"1.0.0","description":"d","main":"main.js"}`
		if err := os.WriteFile(filepath.Join(path, PluginManifestName), []byte(manifest), 0o660); err != nil {
			t.Fatal(err)
		}
	}

	// And one real plugin, to be sure the guard has not swallowed everything.
	real := filepath.Join(pluginsDir, "good")
	if err := os.MkdirAll(real, 0o770); err != nil {
		t.Fatal(err)
	}
	manifest := `{"id":"good","name":"Good","version":"1.0.0","description":"d","main":"main.js"}`
	if err := os.WriteFile(filepath.Join(real, PluginManifestName), []byte(manifest), 0o660); err != nil {
		t.Fatal(err)
	}

	found, err := plugins.scan(config)
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{".install-123456", "good.replacing", "Not_An_Id"} {
		if _, present := found[name]; present {
			t.Errorf("%q was taken for a plugin; it would show as broken on every boot and could not be removed from the panel", name)
		}
	}

	if _, present := found["good"]; !present {
		t.Fatal("the real plugin was swallowed by the guard")
	}
}

// The install swap must never leave a working plugin deleted. It used to remove
// the old directory and then rename the new one into place, so a crash between
// the two — or a rename that failed for any reason — destroyed the plugin, and
// the deferred cleanup then removed the staging copy too.
func TestInstallSwapKeepsTheOldVersionUntilTheNewOneLands(t *testing.T) {
	dir := t.TempDir()

	target := filepath.Join(dir, "plugin")
	staging := filepath.Join(dir, "staging")

	if err := os.MkdirAll(target, 0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "main.js"), []byte("old"), 0o660); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(staging, 0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "main.js"), []byte("new"), 0o660); err != nil {
		t.Fatal(err)
	}

	// The sequence Install performs.
	aside := target + ".replacing"

	if err := os.Rename(target, aside); err != nil {
		t.Fatal(err)
	}

	// At this instant — the crash window — the old version still exists.
	if _, err := os.Stat(filepath.Join(aside, "main.js")); err != nil {
		t.Fatal("the old version was not preserved during the swap")
	}

	if err := os.Rename(staging, target); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(aside); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(filepath.Join(target, "main.js"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "new" {
		t.Fatalf("the installed version is %q", body)
	}
}
