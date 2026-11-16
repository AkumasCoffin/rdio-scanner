// Copyright (C) 2019-2026 Chrystian Huot <chrystian.huot@saubeo.solutions>
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
	"mime/multipart"
	"testing"
	"time"
)

// Anything can land in a watched folder. A name DSDPlus did not write must be
// ignored, not crash the goroutine that found it — which has no recover, so
// the crash was the server's.
func TestDsdPlusParserSurvivesANameWithNoUnderscore(t *testing.T) {
	for _, name := range []string{"/x/test.mp3", "/x/.mp3", "/x/a", "/x/[label].wav"} {
		if err := ParseDSDPlusMeta(NewCall(), name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A call with no timestamp is not a call that happened at the start of year 1.
func TestACallWithNoTimestampIsNotValid(t *testing.T) {
	call := NewCall()
	call.Audio = bytes.Repeat([]byte{1}, 100)
	call.System = 1
	call.Talkgroup = 100

	if ok, _ := call.IsValid(); ok {
		t.Fatal("a call with no dateTime passed validation; it would be stored dated 0001-01-01")
	}

	call.DateTime = time.Now().UTC()

	if ok, err := call.IsValid(); !ok {
		t.Fatalf("a complete call was refused: %v", err)
	}
}

// partNamed builds a real multipart part, since the parser reads the field
// name off it.
func partNamed(t *testing.T, name string, value string) (*multipart.Part, []byte) {
	t.Helper()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	if err := writer.WriteField(name, value); err != nil {
		t.Fatal(err)
	}
	writer.Close()

	part, err := multipart.NewReader(&buf, writer.Boundary()).NextPart()
	if err != nil {
		t.Fatal(err)
	}

	return part, []byte(value)
}

// Unit names ride along with an upload as tags on its sources. They were
// added to a list that did not exist yet and so were all dropped.
func TestUploadedSourceTagsBecomeUnits(t *testing.T) {
	call := NewCall()

	part, body := partNamed(t, "sources", `[{"pos":0,"src":4021,"tag":"Engine 5"},{"pos":1.5,"src":4022,"tag":"Medic 2"}]`)
	ParseMultipartContent(call, part, body)

	units, ok := call.units.(*Units)
	if !ok || units == nil {
		t.Fatalf("the call carries no units: %#v", call.units)
	}

	labels := map[uint]string{}
	for _, unit := range units.List {
		labels[unit.Id] = unit.Label
	}

	if labels[4021] != "Engine 5" || labels[4022] != "Medic 2" {
		t.Fatalf("the source tags were not learned as units: %v", labels)
	}
}
