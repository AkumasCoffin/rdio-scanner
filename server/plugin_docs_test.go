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

// TestGeneratedDocsCoverEveryPoint is what stops the reference drifting away
// from the code again. Documentation was previously hand-written beside the
// implementation and ended up describing capabilities that did not exist while
// omitting six that did.
func TestGeneratedDocsCoverEveryPoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reference.md")

	if err := writePluginDocs(path); err != nil {
		t.Fatalf("cannot write reference: %v", err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read reference: %v", err)
	}

	reference := string(body)

	// A table row, not a mention anywhere in the file. The looser check passed
	// while plugins.ready was absent from the reference entirely, because the
	// literal string appears inside another point's note — so the one point the
	// getting-started guide tells every author to use was undiscoverable and
	// the test that should have caught it was structurally unable to.
	for _, point := range pluginPoints {
		if !strings.Contains(reference, "| `"+point+"` |") {
			t.Errorf("extension point %q has no row in the generated reference", point)
		}
	}

	for i := range pluginModels {
		if !strings.Contains(reference, "rdio.models."+pluginModels[i].name) {
			t.Errorf("model %q is missing from the generated reference", pluginModels[i].name)
		}
	}
}

// TestEveryPointHasANote catches a point added without a description, which
// would render as an empty cell rather than telling anyone what it does.
func TestEveryPointHasANote(t *testing.T) {
	for _, point := range pluginPoints {
		if strings.TrimSpace(pointNotes[point]) == "" {
			t.Errorf("extension point %q has no description in pointNotes", point)
		}
	}
}

// TestEveryModelIsDescribed does the same for models, and checks the sample
// value actually yields fields — a model whose sample is not a struct would
// document itself as having none.
func TestEveryModelIsDescribed(t *testing.T) {
	for i := range pluginModels {
		model := &pluginModels[i]

		if strings.TrimSpace(model.summary) == "" {
			t.Errorf("model %q has no summary", model.name)
		}

		if strings.TrimSpace(model.key) == "" {
			t.Errorf("model %q declares no key field", model.name)
		}

		if len(modelFields(model.sample)) == 0 {
			t.Errorf("model %q documents no fields; its sample is probably not a struct", model.name)
		}
	}
}

// TestEveryCapabilityIsDocumented reads the bindings straight out of the source
// and fails if one is not in the capability catalogue.
//
// This is the check that would have caught the previous drift: six capabilities
// were bound and working while being documented nowhere at all, because the
// bindings and the documentation were two lists maintained by different hands
// at different times.
func TestEveryCapabilityIsDocumented(t *testing.T) {
	documented := map[string]bool{}
	for _, capability := range pluginCapabilities {
		documented[capability.name] = true
	}

	// Every rdio.Set("name", ...) anywhere in the bindings is something a
	// plugin can reach, so every one has to appear in the catalogue.
	for name, file := range boundCapabilities(t) {
		if !documented[name] {
			t.Errorf("rdio.%s is bound in %s but missing from pluginCapabilities, so it appears in no documentation", name, file)
		}
	}
}

// boundCapabilities finds every rdio.Set("x", …) across the plugin source,
// mapping the capability name to the file that binds it. Scanning only one file
// would let a binding added elsewhere escape the check — which is exactly what
// happened the first time this test ran.
func boundCapabilities(t *testing.T) map[string]string {
	t.Helper()

	found := map[string]string{}

	for file, body := range readServerSources(t) {
		if !strings.HasPrefix(file, "plugin_") || strings.HasSuffix(file, "_test.go") {
			continue
		}

		for _, line := range strings.Split(body, "\n") {
			trimmed := strings.TrimSpace(line)
			if !strings.HasPrefix(trimmed, `rdio.Set("`) {
				continue
			}

			rest := trimmed[len(`rdio.Set("`):]
			end := strings.Index(rest, `"`)
			if end < 0 {
				continue
			}

			found[rest[:end]] = file
		}
	}

	return found
}

// TestNoPhantomCapabilities is the other direction: something documented that
// is not actually bound, which is how a reference ends up describing a feature
// that does nothing.
func TestNoPhantomCapabilities(t *testing.T) {
	bound := boundCapabilities(t)

	for _, capability := range pluginCapabilities {
		if _, ok := bound[capability.name]; !ok {
			t.Errorf("pluginCapabilities documents rdio.%s, but nothing binds it", capability.name)
		}
	}
}

// TestModelNamesAreUnique guards against a copy-paste shadowing an entity.
func TestModelNamesAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := range pluginModels {
		if seen[pluginModels[i].name] {
			t.Errorf("model %q is declared twice", pluginModels[i].name)
		}
		seen[pluginModels[i].name] = true
	}
}

// The reference is generated so it cannot drift. Nothing checked that the
// committed file matched what the generator currently produces, so it drifted
// anyway: the shipped reference described an RPC re-entrancy guard that had
// been removed from the server, and anyone reading it would have written retry
// handling for an error that cannot happen.
//
// Generating it is not the guarantee. Regenerating it and comparing is.
func TestCommittedReferenceMatchesTheGenerator(t *testing.T) {
	committed, err := os.ReadFile(pluginReferencePath)
	if err != nil {
		t.Skipf("the plugins repository is not checked out beside this one: %v", err)
	}

	generated := filepath.Join(t.TempDir(), "api-reference.md")
	if err := writePluginDocs(generated); err != nil {
		t.Fatalf("cannot generate the reference: %v", err)
	}

	fresh, err := os.ReadFile(generated)
	if err != nil {
		t.Fatal(err)
	}

	if normaliseDoc(string(committed)) == normaliseDoc(string(fresh)) {
		return
	}

	// Name the first line that differs, because "they differ" on a 300 line
	// file is not something anyone can act on.
	oldLines := strings.Split(normaliseDoc(string(committed)), "\n")
	newLines := strings.Split(normaliseDoc(string(fresh)), "\n")

	for i := 0; i < len(oldLines) || i < len(newLines); i++ {
		var was, now string
		if i < len(oldLines) {
			was = oldLines[i]
		}
		if i < len(newLines) {
			now = newLines[i]
		}

		if was != now {
			t.Fatalf("the committed reference is out of date at line %d.\n  committed: %s\n  generated: %s\n\nRun: rdio-scanner -plugin_docs %s", i+1, was, now, pluginReferencePath)
		}
	}
}

// pluginReferencePath is where the committed reference lives, relative to the
// server directory. The plugins repository is a sibling checkout.
const pluginReferencePath = "../../rdio-scanner-plugins/docs/api-reference.md"

// normaliseDoc ignores line endings and the version stamp, which changes on
// every release and would otherwise make this fail for a reason nobody needs to
// act on.
func normaliseDoc(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")

	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "Generated by Rdio Scanner ") {
			lines[i] = "Generated by Rdio Scanner <version>"
		}
	}

	return strings.Join(lines, "\n")
}
