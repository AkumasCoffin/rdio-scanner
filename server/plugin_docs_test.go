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

	for _, point := range pluginPoints {
		if !strings.Contains(reference, "`"+point+"`") {
			t.Errorf("extension point %q is missing from the generated reference", point)
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
