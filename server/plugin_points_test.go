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

// pointsNotYetDispatched are declared but not yet wired, because the phase that
// wires them has not landed. Every entry here is a promise the server has not
// kept, so the list only ever shrinks — and the test below fails the moment a
// point is declared without either being dispatched or being listed here
// deliberately.
//
// This exists because the previous design shipped a `call.emitted` event that
// nothing ever fired. A plugin could register for it and wait forever with no
// error and no indication anything was wrong.
var pointsNotYetDispatched = map[string]string{
}

// TestEveryPointIsDispatched checks that a point a plugin can register against
// is one the server actually reaches.
func TestEveryPointIsDispatched(t *testing.T) {
	sources := readServerSources(t)

	for _, point := range pluginPoints {
		if reason, expected := pointsNotYetDispatched[point]; expected {
			// Make sure the exemption is still needed — an entry left behind
			// after its phase landed would hide a regression.
			if pointIsDispatched(sources, point) {
				t.Errorf("%q is dispatched now; remove it from pointsNotYetDispatched (was marked %q)", point, reason)
			}
			continue
		}

		if !pointIsDispatched(sources, point) {
			t.Errorf("%q can be registered against but is never dispatched — a plugin would wait forever", point)
		}
	}
}

// TestNoUnknownPointExemptions guards the exemption list itself against drift.
func TestNoUnknownPointExemptions(t *testing.T) {
	known := map[string]bool{}
	for _, point := range pluginPoints {
		known[point] = true
	}

	for point := range pointsNotYetDispatched {
		if !known[point] {
			t.Errorf("pointsNotYetDispatched names %q, which is not a declared point", point)
		}
	}
}

// TestPointNamesAreUnique catches a copy-paste that would silently merge two
// points into one.
func TestPointNamesAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, point := range pluginPoints {
		if seen[point] {
			t.Errorf("point %q is declared twice", point)
		}
		seen[point] = true
	}
}

// pointIsDispatched looks for the point's constant being handed to one of the
// dispatch verbs anywhere outside the declaration and the tests.
func pointIsDispatched(sources map[string]string, point string) bool {
	name := pointConstantName(point)
	if name == "" {
		return false
	}

	for file, body := range sources {
		if file == "plugin_points.go" || strings.HasSuffix(file, "_test.go") {
			continue
		}
		// FilterCall is the ingest wrapper: it notifies observers and runs the
		// filter chain for one point. It has to be listed, because the verbs it
		// calls internally take a variable rather than the constant, and without
		// it every point wired through the wrapper would read as unreachable.
		// FilterCall and NotifyClient are the two wrappers that take the point as
		// a parameter, so the verbs they call internally see a variable rather
		// than the constant. Without listing them, every point wired through a
		// wrapper would read as unreachable.
		for _, verb := range []string{"Notify(", "NotifyClient(", "Filter(", "FilterCall(", "Override(", "Provide(", "Emit(", "dispatchSync("} {
			if dispatchesPoint(body, verb, name) {
				return true
			}
		}
	}

	return false
}

// dispatchesPoint reports whether body passes the named constant as the first
// argument to verb.
//
// Whitespace between the paren and the constant is skipped, because gofmt wraps
// a long call across lines and a literal match would then read a perfectly good
// dispatch as missing. That is a false alarm in the direction that matters
// least — it would push someone to reformat working code to satisfy a test.
func dispatchesPoint(body string, verb string, name string) bool {
	from := 0

	for {
		i := strings.Index(body[from:], verb)
		if i < 0 {
			return false
		}

		rest := strings.TrimLeft(body[from+i+len(verb):], " \t\r\n")

		if strings.HasPrefix(rest, name) &&
			(len(rest) == len(name) || !isIdentifierByte(rest[len(name)])) {
			return true
		}

		from += i + len(verb)
	}
}

// containsCall finds needle in body only where it is not followed by another
// identifier character. Without that check PointCallStore matches every
// dispatch of PointCallStored, and a point would look wired when it is not.
func containsCall(body string, needle string) bool {
	from := 0
	for {
		i := strings.Index(body[from:], needle)
		if i < 0 {
			return false
		}
		end := from + i + len(needle)
		if end >= len(body) || !isIdentifierByte(body[end]) {
			return true
		}
		from += i + len(needle)
	}
}

func isIdentifierByte(b byte) bool {
	return b == '_' ||
		(b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9')
}

// pointConstantName maps a point's string value back to its Go constant, by
// reading the declarations out of plugin_points.go.
func pointConstantName(point string) string {
	for name, value := range pointConstants {
		if value == point {
			return name
		}
	}
	return ""
}

var pointConstants = map[string]string{
	"PointStartup":          PointStartup,
	"PointShutdown":         PointShutdown,
	"PointTick":             PointTick,
	"PointConfigChanged":    PointConfigChanged,
	"PointClientConnect":    PointClientConnect,
	"PointClientDisconnect": PointClientDisconnect,
	"PointCallReceive":      PointCallReceive,
	"PointCallAccept":       PointCallAccept,
	"PointCallDuplicate":    PointCallDuplicate,
	"PointCallConvert":      PointCallConvert,
	"PointCallStore":        PointCallStore,
	"PointCallStored":       PointCallStored,
	"PointCallDelay":        PointCallDelay,
	"PointCallEmit":         PointCallEmit,
	"PointCallPayload":      PointCallPayload,
	"PointCallEmitted":      PointCallEmitted,
	"PointDownstreamSend":   PointDownstreamSend,
	"PointClientConfig":     PointClientConfig,
	"PointAccessCheck":      PointAccessCheck,
	"PointAccessScope":      PointAccessScope,
	"PointApikeyCheck":      PointApikeyCheck,
	"PointAdminCheck":       PointAdminCheck,
	"PointCallSearch":       PointCallSearch,
	"PointCallPrune":        PointCallPrune,
	"PointCallAudio":        PointCallAudio,
	"PointConfigSave":       PointConfigSave,
}

func readServerSources(t *testing.T) map[string]string {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("cannot read source directory: %v", err)
	}

	sources := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" {
			continue
		}
		body, err := os.ReadFile(entry.Name())
		if err != nil {
			t.Fatalf("cannot read %s: %v", entry.Name(), err)
		}
		sources[entry.Name()] = string(body)
	}

	return sources
}
