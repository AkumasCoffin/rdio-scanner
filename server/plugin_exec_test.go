package main

import (
	"bytes"
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
