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
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/dop251/goja"
)

// Does the API a plugin is written against still exist?
//
// Every other test here drives the API from Go, so it exercises whatever the Go
// side currently offers — which is exactly the wrong direction to catch the
// failure that matters. A plugin is written once against the surface as it was,
// and then the server keeps changing underneath it. Rename a method, move it to
// another namespace, and every test still passes while the plugin dies at load
// with "undefined is not a function", in a log line the operator reads long
// after their transcripts stopped arriving.
//
// So this reads the shipped plugins as the source of truth and asks the server
// to satisfy them, rather than the other way round.

// rdioCall matches a call against the rdio global: the namespace path, up to the
// opening parenthesis. Deliberately not a JS parser — a false positive here is a
// method that must exist anyway, and the alternative is depending on one.
var rdioCall = regexp.MustCompile(`\brdio\.([a-zA-Z_][a-zA-Z0-9_.]*)\s*\(`)

// pluginsRepo is the sibling checkout. Absent on a machine that only has the
// server, which is a skip rather than a failure — the same treatment the
// documentation parity test gives it.
const pluginsRepo = "../../rdio-scanner-plugins/plugins"

func shippedPluginSources(t *testing.T) map[string][]string {
	t.Helper()

	entries, err := os.ReadDir(pluginsRepo)
	if err != nil {
		t.Skipf("plugins checkout not present alongside the server: %v", err)
	}

	sources := map[string][]string{}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		main := filepath.Join(pluginsRepo, entry.Name(), "main.js")

		body, err := os.ReadFile(main)
		if err != nil {
			// A frontend-only plugin has no backend to check.
			continue
		}

		found := map[string]bool{}

		for _, match := range rdioCall.FindAllStringSubmatch(string(body), -1) {
			found[match[1]] = true
		}

		paths := make([]string, 0, len(found))
		for path := range found {
			paths = append(paths, path)
		}
		sort.Strings(paths)

		sources[entry.Name()] = paths
	}

	if len(sources) == 0 {
		t.Skip("no shipped plugins with a backend to check")
	}

	return sources
}

// surfaceRuntime binds the whole host API onto a VM, with no controller behind
// it. Nothing is called — only looked up — so the methods never reach one.
func surfaceRuntime(t *testing.T) *goja.Runtime {
	t.Helper()

	manifest := &PluginManifest{Id: "surface"}

	rt := &PluginRuntime{
		manifest:   manifest,
		plugin:     &Plugin{Manifest: manifest, dir: t.TempDir()},
		dataDir:    t.TempDir(),
		controller: &Controller{Logs: &Logs{}},
	}

	vm := goja.New()

	if err := rt.bindHostApi(vm); err != nil {
		t.Fatalf("could not bind the host api: %v", err)
	}

	return vm
}

func TestEveryApiTheShippedPluginsCallExists(t *testing.T) {
	vm := surfaceRuntime(t)

	for plugin, paths := range shippedPluginSources(t) {
		for _, path := range paths {
			// Walked a segment at a time so the report names where the chain
			// broke. "rdio.calls is undefined" and "rdio.calls.findId is
			// undefined" are different bugs and want different fixes.
			value := vm.GlobalObject().Get("rdio")

			walked := "rdio"

			for _, segment := range strings.Split(path, ".") {
				if value == nil || goja.IsUndefined(value) || goja.IsNull(value) {
					t.Errorf("%s calls rdio.%s, but %s is undefined", plugin, path, walked)
					break
				}

				object, ok := value.(*goja.Object)
				if !ok {
					t.Errorf("%s calls rdio.%s, but %s is not an object", plugin, path, walked)
					break
				}

				value = object.Get(segment)
				walked += "." + segment
			}

			if value == nil || goja.IsUndefined(value) {
				t.Errorf("%s calls rdio.%s, which does not exist", plugin, path)
				continue
			}

			if _, callable := goja.AssertFunction(value); !callable {
				t.Errorf("%s calls rdio.%s, which exists but is not callable", plugin, path)
			}
		}
	}
}

// The points a plugin registers against are the other half of the contract, and
// they fail more quietly: register() throws at load, so the plugin does not run
// at all, and the only evidence is one line naming a point the author is sure
// they spelled right.
var rdioRegister = regexp.MustCompile(`\brdio\.(on|filter|override|provide)\s*\(\s*['"]([a-zA-Z0-9_.]+)['"]`)

// verbNamed resolves the verb a plugin author wrote to the one dispatch uses.
func verbNamed(name string) (pluginVerb, bool) {
	for _, verb := range []pluginVerb{verbOn, verbFilter, verbOverride, verbProvide} {
		if verb.String() == name {
			return verb, true
		}
	}
	return verbOn, false
}

// definesOwnPoint reports whether the plugin publishes this point itself, which
// is a legitimate reason for core not to know it.
func definesOwnPoint(source string, point string) bool {
	for _, quote := range []string{"'", `"`} {
		if strings.Contains(source, "definePoint("+quote+point+quote) {
			return true
		}
	}
	return false
}

func TestEveryPointTheShippedPluginsRegisterAccepts(t *testing.T) {
	entries, err := os.ReadDir(pluginsRepo)
	if err != nil {
		t.Skipf("plugins checkout not present alongside the server: %v", err)
	}

	checked := 0

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		body, err := os.ReadFile(filepath.Join(pluginsRepo, entry.Name(), "main.js"))
		if err != nil {
			continue
		}

		for _, match := range rdioRegister.FindAllStringSubmatch(string(body), -1) {
			name, point := match[1], match[2]

			checked++

			verb, known := verbNamed(name)
			if !known {
				t.Fatalf("the register pattern matched %q, which is not a verb", name)
			}

			if _, isPoint := pointVerbs[point]; !isPoint {
				// A plugin may also define its own point and register against
				// it, which core cannot know about statically.
				if !definesOwnPoint(string(body), point) {
					t.Errorf("%s registers %s on %q, which is not a point", entry.Name(), name, point)
				}
				continue
			}

			// The check that was missing until verb validation went in: a point
			// accepting the verb it is being registered with. `filter` on an
			// override-only point used to register happily and never run.
			if !pointAcceptsVerb(point, verb) {
				t.Errorf(
					"%s registers %s on %q, which only accepts %s — the handler would never run",
					entry.Name(), name, point, strings.Join(pointVerbNames(point), ", "),
				)
			}
		}
	}

	if checked == 0 {
		t.Skip("no shipped plugin registers against a point")
	}

	t.Logf("checked %d registrations across the shipped plugins", checked)
}
