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
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func newSqliteAt(t *testing.T, dir string, name string) *Database {
	t.Helper()

	return NewDatabase(&Config{
		BaseDir: dir + string(filepath.Separator),
		DbType:  DbTypeSqlite,
		DbFile:  name,
	})
}

// seedSource builds a SQLite database holding both configuration and calls,
// the way a real installation would.
func seedSource(t *testing.T, dir string, name string, calls int) string {
	t.Helper()

	db := newSqliteAt(t, dir, name)
	defer db.Sql.Close()

	systems := NewSystems()
	system := NewSystem()
	system.Id = 42
	system.Label = "Metro"
	system.AutoPopulate = true
	system.Talkgroups.List = append(system.Talkgroups.List, &Talkgroup{
		GroupId: 1, Id: 1001, Label: "TAC1", Name: "Tactical 1", TagId: 1,
	})
	systems.List = append(systems.List, system)
	if err := systems.Write(db); err != nil {
		t.Fatalf("seeding systems: %v", err)
	}

	apikeys := NewApikeys()
	apikeys.List = append(apikeys.List, &Apikey{Ident: "recorder", Key: "secret-key", Disabled: true, Systems: "*"})
	if err := apikeys.Write(db); err != nil {
		t.Fatalf("seeding api keys: %v", err)
	}

	store := NewCalls()
	for i := 1; i <= calls; i++ {
		call := &Call{
			// A NUL and a 0xFF, so a blob that got round-tripped through text
			// anywhere would come back wrong.
			Audio:     []byte(fmt.Sprintf("AUDIO-%d\x00\xff\x01binary", i)),
			AudioName: fmt.Sprintf("call-%d.wav", i),
			AudioType: "audio/wav",
			DateTime:  time.Date(2026, 3, 4, 5, 6, i, 0, time.UTC),
			System:    42,
			Talkgroup: uint(1000 + i),
			Frequency: uint(851000000),
		}
		if _, err := store.WriteCall(call, db); err != nil {
			t.Fatalf("seeding call %d: %v", i, err)
		}
	}

	return filepath.Join(dir, name)
}

// Configuration migrates by default; calls do not, because the calls table
// carries every audio blob.
func TestImportSqliteConfigOnlyByDefault(t *testing.T) {
	dir := t.TempDir()
	source := seedSource(t, dir, "source.db", 4)

	target := newSqliteAt(t, dir, "target.db")
	defer target.Sql.Close()

	if err := ImportSqlite(target, source, false, false); err != nil {
		t.Fatalf("import: %v", err)
	}

	systems := NewSystems()
	if err := systems.Read(target); err != nil {
		t.Fatalf("reading systems: %v", err)
	}
	if len(systems.List) != 1 {
		t.Fatalf("expected 1 system, got %d", len(systems.List))
	}
	if systems.List[0].Label != "Metro" || systems.List[0].Id != 42 {
		t.Errorf("system wrong: %+v", systems.List[0])
	}
	if len(systems.List[0].Talkgroups.List) != 1 {
		t.Errorf("expected the talkgroup to come across, got %d", len(systems.List[0].Talkgroups.List))
	}

	// Booleans are 0/1 integers on SQLite and must arrive as real booleans —
	// this is what a Postgres target rejects if the coercion is missed.
	if !systems.List[0].AutoPopulate {
		t.Error("autoPopulate should have survived as true")
	}

	apikeys := NewApikeys()
	if err := apikeys.Read(target); err != nil {
		t.Fatalf("reading api keys: %v", err)
	}
	if len(apikeys.List) != 1 || apikeys.List[0].Key != "secret-key" {
		t.Fatalf("api key did not migrate: %+v", apikeys.List)
	}
	if !apikeys.List[0].Disabled {
		t.Error("disabled flag should have survived as true")
	}

	var calls uint
	if err := target.QueryRow("select count(*) from `rdioScannerCalls`").Scan(&calls); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Errorf("calls should not migrate without the flag, got %d", calls)
	}
}

// With the flag, calls come across with their audio and timestamps intact.
func TestImportSqliteWithCalls(t *testing.T) {
	dir := t.TempDir()
	const total = 6
	source := seedSource(t, dir, "source.db", total)

	target := newSqliteAt(t, dir, "target.db")
	defer target.Sql.Close()

	if err := ImportSqlite(target, source, true, false); err != nil {
		t.Fatalf("import: %v", err)
	}

	var count uint
	if err := target.QueryRow("select count(*) from `rdioScannerCalls`").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != total {
		t.Fatalf("expected %d calls, got %d", total, count)
	}

	// Call ids must be preserved — share links and downstream references use them.
	got, err := NewCalls().GetCall(3, target)
	if err != nil {
		t.Fatalf("reading imported call id 3: %v", err)
	}
	if want := "AUDIO-3\x00\xff\x01binary"; string(got.Audio) != want {
		t.Errorf("audio corrupted:\n got %q\nwant %q", got.Audio, want)
	}
	if want := time.Date(2026, 3, 4, 5, 6, 3, 0, time.UTC); !got.DateTime.UTC().Equal(want) {
		t.Errorf("dateTime wrong: got %v, want %v", got.DateTime.UTC(), want)
	}
}

// Replacing configuration is destructive, so a target that is already in use
// must not be overwritten by accident.
func TestImportSqliteRefusesPopulatedTarget(t *testing.T) {
	dir := t.TempDir()
	source := seedSource(t, dir, "source.db", 1)
	occupied := seedSource(t, dir, "target.db", 1)

	target := newSqliteAt(t, dir, "target.db")
	defer target.Sql.Close()

	if err := ImportSqlite(target, source, false, false); err == nil {
		t.Error("expected a refusal when the target already has systems")
	}

	if err := ImportSqlite(target, source, false, true); err != nil {
		t.Errorf("-import_force should have allowed it: %v", err)
	}

	_ = occupied
}

// Re-running must resume rather than duplicate: configuration is replaced, and
// calls pick up after what already landed.
func TestImportSqliteIsRepeatable(t *testing.T) {
	dir := t.TempDir()
	const total = 5
	source := seedSource(t, dir, "source.db", total)

	target := newSqliteAt(t, dir, "target.db")
	defer target.Sql.Close()

	if err := ImportSqlite(target, source, true, false); err != nil {
		t.Fatalf("first import: %v", err)
	}
	if err := ImportSqlite(target, source, true, true); err != nil {
		t.Fatalf("second import: %v", err)
	}

	var calls uint
	if err := target.QueryRow("select count(*) from `rdioScannerCalls`").Scan(&calls); err != nil {
		t.Fatal(err)
	}
	if calls != total {
		t.Errorf("calls duplicated on re-run: expected %d, got %d", total, calls)
	}

	systems := NewSystems()
	if err := systems.Read(target); err != nil {
		t.Fatal(err)
	}
	if len(systems.List) != 1 {
		t.Errorf("systems duplicated on re-run: expected 1, got %d", len(systems.List))
	}
}

func TestImportSqliteRejectsBadSource(t *testing.T) {
	dir := t.TempDir()

	target := newSqliteAt(t, dir, "target.db")
	defer target.Sql.Close()

	if err := ImportSqlite(target, filepath.Join(dir, "nope.db"), false, false); err == nil {
		t.Error("expected an error for a missing source file")
	}

	if err := ImportSqlite(target, filepath.Join(dir, "target.db"), false, false); err == nil {
		t.Error("expected an error when source and target are the same database")
	}
}

func TestTruthyHandlesSqliteBooleans(t *testing.T) {
	for _, tc := range []struct {
		in   any
		want bool
	}{
		{int64(1), true}, {int64(0), false},
		{float64(1), true}, {float64(0), false},
		{true, true}, {false, false},
		{"1", true}, {"0", false}, {"true", true}, {"false", false},
		{[]byte("1"), true}, {[]byte("0"), false},
	} {
		if got := truthy(tc.in); got != tc.want {
			t.Errorf("truthy(%#v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// A bare filename must resolve against the directory holding the executable,
// which is where a default install keeps rdio-scanner.db — so the command
// works without a full path and regardless of the current directory.
func TestImportResolvesBareFilenameNextToBinary(t *testing.T) {
	dir := t.TempDir()
	seedSource(t, dir, "rdio-scanner.db", 2)

	config := &Config{BaseDir: dir + string(filepath.Separator), DbType: DbTypeSqlite, DbFile: "target.db"}

	if got := resolveImportPath(config, "rdio-scanner.db"); got != filepath.Join(dir, "rdio-scanner.db") {
		t.Errorf("bare filename did not resolve beside the binary: got %q", got)
	}

	// An absolute path is returned untouched.
	abs := filepath.Join(dir, "rdio-scanner.db")
	if got := resolveImportPath(config, abs); got != abs {
		t.Errorf("absolute path was rewritten: got %q, want %q", got, abs)
	}

	// Something that exists nowhere comes back as typed, so the error names
	// what the user actually asked for.
	if got := resolveImportPath(config, "missing.db"); got != "missing.db" {
		t.Errorf("unresolvable path should be returned as given: got %q", got)
	}
}

// End to end: a bare filename actually imports.
func TestImportSqliteAcceptsBareFilename(t *testing.T) {
	dir := t.TempDir()
	seedSource(t, dir, "rdio-scanner.db", 3)

	target := newSqliteAt(t, dir, "target.db")
	defer target.Sql.Close()

	if err := ImportSqlite(target, "rdio-scanner.db", true, false); err != nil {
		t.Fatalf("bare filename import failed: %v", err)
	}

	var calls uint
	if err := target.QueryRow("select count(*) from `rdioScannerCalls`").Scan(&calls); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
}
