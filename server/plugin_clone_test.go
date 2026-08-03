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
	"testing"
	"time"
)

// Everything handed to a plugin has to be a copy. goja gives JavaScript the Go
// value itself, so a shared map is a plugin writing directly into state core is
// still using — and two plugins doing it at once is a fatal concurrent map
// write, not an error the server can recover from.
//
// The switch missed []map[string]any, which is exactly the type a call's
// sources and frequencies have on every ingest path. It only becomes []any
// after a round trip through the database, so the safe-looking case covered the
// cold path and the live one went through by reference.
func TestClonePluginValueCopiesCallSourcesAndFrequencies(t *testing.T) {
	call := NewCall()
	call.Sources = []map[string]any{
		{"src": 1234, "pos": 0.0},
		{"src": 5678, "pos": 4.5},
	}
	call.Frequencies = []map[string]any{
		{"freq": 851000000, "pos": 0.0},
	}

	value := pluginCallValue(call, false)
	cloned, ok := clonePluginValue(value).(map[string]any)
	if !ok {
		t.Fatal("cloning a call value did not produce a map")
	}

	sources, ok := cloned["sources"].([]map[string]any)
	if !ok {
		t.Fatalf("sources came back as %T, so the clone did not preserve its shape", cloned["sources"])
	}

	// What a plugin does: reach into an element and write.
	sources[0]["src"] = 9999
	sources[0]["tag"] = "annotated"

	original := call.Sources.([]map[string]any)
	if original[0]["src"] != 1234 {
		t.Fatal("a plugin writing to sources[0] changed the call core is still holding")
	}
	if _, added := original[0]["tag"]; added {
		t.Fatal("a plugin adding a key to sources[0] added it to the call core is still holding")
	}

	frequencies, ok := cloned["frequencies"].([]map[string]any)
	if !ok {
		t.Fatalf("frequencies came back as %T", cloned["frequencies"])
	}
	frequencies[0]["freq"] = 0

	if call.Frequencies.([]map[string]any)[0]["freq"] != 851000000 {
		t.Fatal("a plugin writing to frequencies changed the call core is still holding")
	}
}

// The same guarantee, exercised the way it actually breaks: one goroutine
// standing in for a plugin observer writing to its copy, another for core
// marshalling the call for a downstream. Under -race this is the test that
// fails if the clone is ever weakened again.
func TestClonedCallValueIsSafeAgainstConcurrentCoreAccess(t *testing.T) {
	call := NewCall()
	call.Sources = []map[string]any{{"src": 1}}

	value := pluginCallValue(call, false)

	done := make(chan struct{}, 2)

	go func() {
		defer func() { done <- struct{}{} }()
		for i := 0; i < 500; i++ {
			cloned := clonePluginValue(value).(map[string]any)
			cloned["sources"].([]map[string]any)[0]["src"] = i
		}
	}()

	go func() {
		defer func() { done <- struct{}{} }()
		for i := 0; i < 500; i++ {
			for range call.Sources.([]map[string]any)[0] {
			}
		}
	}()

	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("timed out")
		}
	}
}

// Nested values inside a slice element are copied too, not just the element.
func TestClonePluginValueIsDeepThroughSlices(t *testing.T) {
	source := []map[string]any{
		{"meta": map[string]any{"unit": "alpha"}},
	}

	cloned, ok := clonePluginValue(source).([]map[string]any)
	if !ok {
		t.Fatalf("clone produced %T", clonePluginValue(source))
	}

	cloned[0]["meta"].(map[string]any)["unit"] = "bravo"

	if source[0]["meta"].(map[string]any)["unit"] != "alpha" {
		t.Fatal("a nested map inside a slice element was shared rather than copied")
	}
}
