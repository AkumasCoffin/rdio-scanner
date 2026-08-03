package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dop251/goja"
)

// A plugin piping a call through its own program is the whole point of
// rdio.exec for audio. Text mode destroys roughly half of all byte values, so
// this pins the binary path: every byte in, every byte out.
func TestExecBinaryOutputSurvives(t *testing.T) {
	original := make([]byte, 4096)
	for i := range original {
		original[i] = byte(i % 256)
	}

	vm := goja.New()

	stdout := &boundedBuffer{limit: 1 << 20}
	stdout.Write(original)

	// Text mode: what a plugin used to get.
	if err := vm.Set("asText", vm.ToValue(stdout.String())); err != nil {
		t.Fatal(err)
	}
	// Binary mode: what it gets now.
	if err := vm.Set("asBytes", vm.NewArrayBuffer(stdout.Bytes())); err != nil {
		t.Fatal(err)
	}

	value, err := vm.RunString(`
		var view = new Uint8Array(asBytes)
		var out = []
		for (var i = 0; i < view.length; i++) out.push(view[i])
		var textDiffers = 0
		for (var j = 0; j < asText.length; j++) if (asText.charCodeAt(j) !== out[j]) textDiffers++
		({ bytes: out, textDiffers: textDiffers })
	`)
	if err != nil {
		t.Fatal(err)
	}

	result, _ := value.Export().(map[string]any)
	list, _ := result["bytes"].([]any)

	if len(list) != len(original) {
		t.Fatalf("javascript saw %d bytes, expected %d", len(list), len(original))
	}

	got := make([]byte, len(list))
	for i, v := range list {
		switch n := v.(type) {
		case int64:
			got[i] = byte(n)
		case float64:
			got[i] = byte(n)
		}
	}

	if !bytes.Equal(original, got) {
		t.Fatal("binary output did not survive the crossing into javascript")
	}

	// Guard the reason this exists: if text mode ever stops corrupting, the
	// binary option is no longer load-bearing and this should be revisited.
	differs := int64(0)
	switch n := result["textDiffers"].(type) {
	case int64:
		differs = n
	case float64:
		differs = int64(n)
	}
	if differs == 0 {
		t.Fatal("text mode no longer corrupts binary; the binary option may be redundant")
	}
	t.Logf("binary mode: %d/%d bytes intact; text mode corrupts %d of them", len(got), len(original), differs)
}

// Half a file must not look like a whole one.
func TestExecReportsTruncation(t *testing.T) {
	buf := &boundedBuffer{limit: 10}

	buf.Write([]byte("0123456789"))
	if buf.truncated {
		t.Fatal("output that fit exactly was reported as truncated")
	}

	buf.Write([]byte("more"))
	if !buf.truncated {
		t.Fatal("output past the limit was discarded without saying so")
	}
	if len(buf.Bytes()) != 10 {
		t.Fatalf("buffer grew past its limit to %d bytes", len(buf.Bytes()))
	}

	// A write that straddles the limit keeps the part that fits.
	straddle := &boundedBuffer{limit: 4}
	straddle.Write([]byte("abcdef"))
	if !straddle.truncated || string(straddle.Bytes()) != "abcd" {
		t.Fatalf("straddling write kept %q, truncated=%v", straddle.Bytes(), straddle.truncated)
	}
}


// readText had no size guard at all while readFile refused above the limit,
// which made the unbounded call the natural one to reach for — a log file is
// exactly what a plugin reads as text, and exactly what is large enough to
// exhaust the server's memory.
func TestPluginFsReadsAreBounded(t *testing.T) {
	dir := t.TempDir()

	path := filepath.Join(dir, "big.log")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), 4096), 0o600); err != nil {
		t.Fatal(err)
	}

	const max = 1024

	// The whole-file form refuses, and says what to do instead.
	if _, err := readFileRange(path, 0, 0, max); err == nil {
		t.Fatal("a file past the limit was read in full")
	} else if !strings.Contains(err.Error(), "offset, length") {
		t.Errorf("the refusal does not say how to proceed: %v", err)
	}

	// And that advice works, which it did not when the message was written —
	// there was no ranged read to follow it with.
	body, err := readFileRange(path, 100, 200, max)
	if err != nil {
		t.Fatalf("a ranged read failed: %v", err)
	}
	if len(body) != 200 {
		t.Fatalf("a ranged read returned %d bytes, expected 200", len(body))
	}

	// Running off the end returns what is there rather than failing.
	if body, err = readFileRange(path, 4000, 500, max); err != nil {
		t.Fatalf("a read past the end failed: %v", err)
	}
	if len(body) != 96 {
		t.Fatalf("a read past the end returned %d bytes, expected 96", len(body))
	}

	// Starting past the end is empty, not an error.
	if body, err = readFileRange(path, 99999, 10, max); err != nil || len(body) != 0 {
		t.Fatalf("a read starting past the end returned %d bytes, %v", len(body), err)
	}

	// A length over the limit is refused even though it is a ranged read.
	if _, err = readFileRange(path, 0, max+1, max); err == nil {
		t.Fatal("a ranged read larger than the limit was allowed")
	}

	// A file within the limit still reads whole, which is the common case.
	small := filepath.Join(dir, "small.txt")
	if err = os.WriteFile(small, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if body, err = readFileRange(small, 0, 0, max); err != nil || string(body) != "hello" {
		t.Fatalf("a small file read as %q, %v", body, err)
	}
}
