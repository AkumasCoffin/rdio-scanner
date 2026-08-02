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
	"testing"

	"github.com/dop251/goja"
)

// TestPluginAudioRoundTrip is the load-bearing test for any plugin that uploads
// call audio — a transcription plugin does nothing else. Audio is a []byte in
// Go, becomes a JavaScript value, is handed back to rdio.http.multipart, and
// has to arrive byte-identical. A silent corruption here would produce
// plausible-looking uploads that the upstream rejects or mis-transcribes.
func TestPluginAudioRoundTrip(t *testing.T) {
	// Deliberately includes 0x00 and high bytes: a naive string conversion
	// mangles exactly these, and real m4a data is full of them.
	original := make([]byte, 512)
	for i := range original {
		original[i] = byte(i % 256)
	}

	vm := goja.New()
	vm.SetFieldNameMapper(goja.UncapFieldNameMapper())

	call := &Call{Id: uint(1), Audio: original}

	// The same conversion the host does when handing a call to a plugin.
	value := pluginCallValue(call, true)

	if err := vm.Set("call", vm.ToValue(value)); err != nil {
		t.Fatalf("cannot bind call: %v", err)
	}

	// Plugins gate on the audio length before deciding to upload, so the
	// wrapper has to expose one. Without this a size check silently compares
	// undefined and never filters anything.
	length, err := vm.RunString(`call.audio.length`)
	if err != nil {
		t.Fatalf("cannot read call.audio.length from javascript: %v", err)
	}
	if n := length.ToInteger(); n != int64(len(original)) {
		t.Fatalf("call.audio.length was %d in javascript, expected %d", n, len(original))
	}

	// A plugin would do exactly this: read call.audio and pass it straight to
	// the multipart helper.
	result, err := vm.RunString(`call.audio`)
	if err != nil {
		t.Fatalf("cannot read call.audio from javascript: %v", err)
	}

	got, err := pluginBytes(result.Export())
	if err != nil {
		t.Fatalf("audio could not be converted back to bytes: %v", err)
	}

	if !bytes.Equal(original, got) {
		t.Fatalf("audio round trip corrupted the data: sent %d bytes, got %d back; first difference at %d",
			len(original), len(got), firstDifference(original, got))
	}
}

// TestPluginAudioSizeReported checks that a plugin can decide whether to bother
// fetching audio without fetching it, which is what audioSize is for.
func TestPluginAudioSizeReported(t *testing.T) {
	call := &Call{Id: uint(1), Audio: make([]byte, 1234)}

	withoutAudio := pluginCallValue(call, false)
	if _, present := withoutAudio["audio"]; present {
		t.Fatal("audio was included when it was not asked for")
	}
	if size, _ := withoutAudio["audioSize"].(int); size != 1234 {
		t.Fatalf("audioSize was %v, expected 1234", withoutAudio["audioSize"])
	}

	withAudio := pluginCallValue(call, true)
	if _, present := withAudio["audio"]; !present {
		t.Fatal("audio was missing when it was asked for")
	}
}

// TestPluginBytesAcceptsJavascriptArray covers a plugin that builds a byte
// array by hand rather than passing one straight through.
func TestPluginBytesAcceptsJavascriptArray(t *testing.T) {
	vm := goja.New()

	value, err := vm.RunString(`[0, 127, 128, 255]`)
	if err != nil {
		t.Fatalf("cannot evaluate: %v", err)
	}

	got, err := pluginBytes(value.Export())
	if err != nil {
		t.Fatalf("array could not be converted: %v", err)
	}

	want := []byte{0, 127, 128, 255}
	if !bytes.Equal(want, got) {
		t.Fatalf("got %v, expected %v", got, want)
	}
}

// TestPluginTableRewriting checks the boundary that makes "plugins get their
// own tables" an enforced rule rather than a convention.
func TestPluginTableRewriting(t *testing.T) {
	manifest := &PluginManifest{
		Id:     "my-plugin",
		Tables: []PluginTable{{Name: "notes"}},
	}

	pluginDb := NewPluginDb(nil, manifest)

	rewritten, err := pluginDb.rewrite("select `text` from `notes` where `callId` = ?")
	if err != nil {
		t.Fatalf("rewriting a plugin's own table failed: %v", err)
	}
	if !bytes.Contains([]byte(rewritten), []byte("`plugin_my_plugin_notes`")) {
		t.Fatalf("table was not namespaced: %s", rewritten)
	}
	// Column identifiers must survive untouched.
	if !bytes.Contains([]byte(rewritten), []byte("`text`")) {
		t.Fatalf("column name was mangled: %s", rewritten)
	}

	// Core tables are reachable and pass through untouched. A plugin can read
	// and write anything in the database; the prefix mapping is a convenience
	// for its own tables, not a fence around everyone else's.
	core, err := pluginDb.rewrite("select * from `rdioScannerCalls`")
	if err != nil {
		t.Fatalf("a core table was refused: %v", err)
	}
	if !bytes.Contains([]byte(core), []byte("`rdioScannerCalls`")) {
		t.Fatalf("a core table name was rewritten: %s", core)
	}

	// The config table is host-owned but legitimately addressable.
	if _, err := pluginDb.rewrite("select `value` from `config` where `key` = ?"); err != nil {
		t.Fatalf("plugin could not read its own config table: %v", err)
	}
}

// TestPluginStatementKinds checks the one remaining guard on rdio.db, which
// exists to prevent confusion rather than to withhold capability: a write sent
// to the read path would run and return nothing, looking like a query that
// matched no rows.
func TestPluginStatementKinds(t *testing.T) {
	manifest := &PluginManifest{Id: "my-plugin", Tables: []PluginTable{{Name: "notes"}}}
	pluginDb := NewPluginDb(nil, manifest)

	if _, err := pluginDb.Query("delete from `notes`", nil); err == nil {
		t.Fatal("a write through the read path should be refused with an explanation")
	}

	// Everything else is allowed, including schema changes. Rewriting is all
	// that happens here — these do not touch a database.
	for _, statement := range []string{
		"drop table `notes`",
		"create table `extra` (`a` int)",
		"alter table `notes` add column `b` text",
	} {
		if _, err := pluginDb.rewrite(statement); err != nil {
			t.Errorf("%q was refused: %v", statement, err)
		}
	}
}

// TestPluginManifestValidation covers the rules that protect the filesystem and
// the database from a hostile or careless manifest.
func TestPluginManifestValidation(t *testing.T) {
	cases := []struct {
		name string
		json string
	}{
		{"empty id", `{"id":"","name":"x","version":"1","description":"d","main":"main.js"}`},
		{"uppercase id", `{"id":"MyPlugin","name":"x","version":"1","description":"d","main":"main.js"}`},
		{"underscore id", `{"id":"my_plugin","name":"x","version":"1","description":"d","main":"main.js"}`},
		{"traversing main", `{"id":"ok","name":"x","version":"1","description":"d","main":"../../etc/passwd"}`},
		{"absolute main", `{"id":"ok","name":"x","version":"1","description":"d","main":"/etc/passwd"}`},
		{"no entry point", `{"id":"ok","name":"x","version":"1","description":"d"}`},
		{"future api version", `{"id":"ok","name":"x","version":"1","description":"d","main":"main.js","apiVersion":99}`},
		{"reserved table", `{"id":"ok","name":"x","version":"1","description":"d","main":"main.js","tables":[{"name":"config","columns":[{"name":"a","type":"int"}]}]}`},
		{"unknown column type", `{"id":"ok","name":"x","version":"1","description":"d","main":"main.js","tables":[{"name":"t","columns":[{"name":"a","type":"money"}]}]}`},
		{"varchar without length", `{"id":"ok","name":"x","version":"1","description":"d","main":"main.js","tables":[{"name":"t","columns":[{"name":"a","type":"varchar"}]}]}`},
		{"select without options", `{"id":"ok","name":"x","version":"1","description":"d","main":"main.js","config":[{"key":"k","type":"select","label":"L"}]}`},
		{"showIf on unknown field", `{"id":"ok","name":"x","version":"1","description":"d","main":"main.js","config":[{"key":"k","type":"text","label":"L","showIf":{"key":"nope","equals":["a"]}}]}`},
		{"showIf on itself", `{"id":"ok","name":"x","version":"1","description":"d","main":"main.js","config":[{"key":"k","type":"text","label":"L","showIf":{"key":"k","equals":["a"]}}]}`},
		{"showIf with no values", `{"id":"ok","name":"x","version":"1","description":"d","main":"main.js","config":[{"key":"a","type":"text","label":"A"},{"key":"k","type":"text","label":"L","showIf":{"key":"a","equals":[]}}]}`},
	}

	for _, c := range cases {
		if _, err := ParsePluginManifest([]byte(c.json)); err == nil {
			t.Errorf("%s: manifest was accepted but should have been rejected", c.name)
		}
	}

	valid := `{"id":"my-plugin","name":"Mine","version":"1.0.0","description":"d","main":"main.js",
		"permissions":["http"],
		"config":[{"key":"k","type":"text","label":"L","default":"v"}],
		"tables":[{"name":"notes","columns":[{"name":"callId","type":"int","primaryKey":true},{"name":"body","type":"text"}]}]}`

	manifest, err := ParsePluginManifest([]byte(valid))
	if err != nil {
		t.Fatalf("a valid manifest was rejected: %v", err)
	}

	if got := manifest.TableName("notes"); got != "plugin_my_plugin_notes" {
		t.Fatalf("table name was %q, expected plugin_my_plugin_notes", got)
	}

	// A showIf may point at a field declared later in the list, so validation
	// cannot simply check the keys it has seen so far.
	forward := `{"id":"ok","name":"x","version":"1","description":"d","main":"main.js","config":[
		{"key":"k","type":"text","label":"L","showIf":{"key":"provider","equals":["a","b"]}},
		{"key":"provider","type":"select","label":"P","options":[{"value":"a","label":"A"}]}]}`

	if _, err := ParsePluginManifest([]byte(forward)); err != nil {
		t.Fatalf("a forward showIf reference was rejected: %v", err)
	}

	// A manifest with no apiVersion is treated as version 1, so plugins written
	// before the field existed keep loading.
	if manifest.ApiVersion != 1 {
		t.Fatalf("apiVersion defaulted to %d, expected 1", manifest.ApiVersion)
	}

	// permissions is accepted and ignored rather than rejected, so a manifest
	// written against the earlier design still loads.
	if len(manifest.Permissions) != 1 {
		t.Fatal("a legacy permissions field should be accepted and carried, not dropped or rejected")
	}

	if v, _ := manifest.DefaultConfig()["k"].(string); v != "v" {
		t.Fatal("manifest defaults were not picked up")
	}
}

// TestPluginVersionGate covers the compatibility check, including the case that
// actually bit during development: a prerelease sorts below its release, so a
// plugin requiring 6.14.0 is correctly unavailable on 6.14.0-beta.1.
func TestPluginVersionGate(t *testing.T) {
	manifest := &PluginManifest{Id: "x", MinServerVersion: "6.14.0"}

	if ok, _ := manifest.CompatibleWith("6.14.0-beta.1"); ok {
		t.Fatal("a prerelease was treated as satisfying its own release requirement")
	}
	if ok, _ := manifest.CompatibleWith("6.14.0"); !ok {
		t.Fatal("the exact required version was rejected")
	}
	if ok, _ := manifest.CompatibleWith("6.15.0"); !ok {
		t.Fatal("a newer version was rejected")
	}
	if ok, _ := manifest.CompatibleWith("6.13.5"); ok {
		t.Fatal("an older version was accepted")
	}

	capped := &PluginManifest{Id: "x", MaxServerVersion: "6.14.0"}
	if ok, _ := capped.CompatibleWith("6.15.0"); ok {
		t.Fatal("a version above the maximum was accepted")
	}
}

func firstDifference(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// A stored password is never sent to the browser, but the form still has to be
// able to say that one exists — otherwise a configured API key is
// indistinguishable from an empty field and reads as lost settings.
func TestRedactPluginConfigReportsWhichSecretsAreSet(t *testing.T) {
	manifest := &PluginManifest{
		Id: "transcripts",
		Config: []PluginConfigField{
			{Key: "groqApiKey", Type: "password", Label: "Groq"},
			{Key: "openaiApiKey", Type: "password", Label: "OpenAI"},
			{Key: "model", Type: "text", Label: "Model"},
		},
	}

	config := map[string]any{
		"groqApiKey":   "gsk_secret",
		"openaiApiKey": "",
		"model":        "whisper-1",
	}

	redacted, set := redactPluginConfig(manifest, config)

	if got := redacted["groqApiKey"]; got != "" {
		t.Fatalf("the stored key reached the browser as %q", got)
	}
	if !set["groqApiKey"] {
		t.Fatal("a key that is set was not reported as set, so the form shows it as empty")
	}
	if set["openaiApiKey"] {
		t.Fatal("an empty key was reported as set")
	}
	if set["model"] {
		t.Fatal("a non-password field was reported as a set secret")
	}
	if got := redacted["model"]; got != "whisper-1" {
		t.Fatalf("a plain field was altered: %v", got)
	}
}
