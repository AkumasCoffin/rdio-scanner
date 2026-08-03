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
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/dop251/goja"
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

// goja exports each typed array as its Go slice equivalent, and only
// Uint8Array happened to land on []byte — so Int16Array, the one an audio
// plugin actually holds, was refused with "value cannot be used as binary
// data". The documented flow says to build one from decoded samples and hand it
// back to encode, and doing exactly that threw.
func TestTypedArraysCanBeUsedAsBinaryData(t *testing.T) {
	vm := goja.New()

	// What an author writes after decoding: a view over the returned samples.
	value, err := vm.RunString(`new Int16Array([0, 1, -1, 32767, -32768])`)
	if err != nil {
		t.Fatal(err)
	}

	body, err := pluginBytes(value.Export())
	if err != nil {
		t.Fatalf("an Int16Array was refused: %v", err)
	}

	// Little-endian 16-bit, which is what the audio pipeline reads and writes.
	want := []byte{
		0x00, 0x00,
		0x01, 0x00,
		0xff, 0xff,
		0xff, 0x7f,
		0x00, 0x80,
	}

	if !bytes.Equal(body, want) {
		t.Fatalf("samples encoded as % x, expected % x", body, want)
	}

	// Round trip: the bytes read back as the same samples.
	if err := vm.Set("raw", vm.NewArrayBuffer(body)); err != nil {
		t.Fatal(err)
	}

	back, err := vm.RunString(`Array.from(new Int16Array(raw)).join(',')`)
	if err != nil {
		t.Fatal(err)
	}
	if back.String() != "0,1,-1,32767,-32768" {
		t.Fatalf("the samples came back as %s", back.String())
	}
}

// Uint8Array kept working, and the other integer views work too.
func TestOtherTypedArraysAreAcceptedToo(t *testing.T) {
	vm := goja.New()

	for _, c := range []struct {
		src  string
		want []byte
	}{
		{`new Uint8Array([1, 2, 255])`, []byte{1, 2, 255}},
		{`new Int8Array([1, -1])`, []byte{0x01, 0xff}},
		{`new Uint16Array([1, 65535])`, []byte{0x01, 0x00, 0xff, 0xff}},
		{`new Int32Array([1])`, []byte{0x01, 0x00, 0x00, 0x00}},
	} {
		value, err := vm.RunString(c.src)
		if err != nil {
			t.Fatal(err)
		}

		body, err := pluginBytes(value.Export())
		if err != nil {
			t.Errorf("%s was refused: %v", c.src, err)
			continue
		}
		if !bytes.Equal(body, c.want) {
			t.Errorf("%s encoded as % x, expected % x", c.src, body, c.want)
		}
	}
}

// A float array is refused rather than reinterpreted. Its bytes are perfectly
// well defined, but every consumer here that cares about samples reads 16-bit
// PCM — so accepting one would not fail, it would produce noise, and an error
// naming the problem is worth far more than that.
func TestFloatArraysAreRefusedWithAnExplanation(t *testing.T) {
	vm := goja.New()

	for _, src := range []string{`new Float32Array([0.5])`, `new Float64Array([0.5])`} {
		value, err := vm.RunString(src)
		if err != nil {
			t.Fatal(err)
		}

		_, err = pluginBytes(value.Export())
		if err == nil {
			t.Errorf("%s was accepted and would have been read as 16-bit samples", src)
			continue
		}
		if !strings.Contains(err.Error(), "Int16Array") {
			t.Errorf("%s was refused without saying what to do instead: %v", src, err)
		}
	}
}
