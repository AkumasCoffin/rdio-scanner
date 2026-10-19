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
	"strings"
	"testing"
	"time"
)

// Each test here covers something the import used to lose or corrupt without
// saying a word. They share a shape: build a source that looks like a real
// install, import it, and check what arrived.

// Transcripts are the most expensive thing in the database to regenerate, and
// they live in a table the import did not know about.
func TestImportCopiesPluginTables(t *testing.T) {
	dir := t.TempDir()
	source := seedSource(t, dir, "source.db", 4)

	// The transcripts plugin's tables, as its own migration creates them.
	sourceDb := newSqliteAt(t, dir, "source.db")
	for _, query := range []string{
		"insert into `plugin_transcripts_calls` (`callId`, `transcript`) values (1, 'engine 41 responding')",
		"insert into `plugin_transcripts_calls` (`callId`, `transcript`) values (2, '')",
		"insert into `plugin_transcripts_systems` (`systemId`, `transcribe`, `prompt`) values (42, 0, 'fire ground')",
		"insert into `plugin_transcripts_config` (`key`, `value`) values ('provider', 'openai')",
	} {
		if _, err := sourceDb.Exec(query); err != nil {
			t.Fatalf("seeding plugin data: %v", err)
		}
	}
	sourceDb.Sql.Close()

	t.Run("with calls, everything comes across", func(t *testing.T) {
		target := newSqliteAt(t, dir, "with-calls.db")
		defer target.Sql.Close()

		if err := ImportSqlite(target, source, true, false); err != nil {
			t.Fatal(err)
		}

		var transcripts, settings, config uint
		target.QueryRow("select count(*) from `plugin_transcripts_calls`").Scan(&transcripts)
		target.QueryRow("select count(*) from `plugin_transcripts_systems`").Scan(&settings)
		target.QueryRow("select count(*) from `plugin_transcripts_config`").Scan(&config)

		if transcripts != 2 {
			t.Errorf("transcripts = %d, want 2 — the archive did not migrate", transcripts)
		}
		if settings != 1 || config != 1 {
			t.Errorf("plugin settings = %d, config = %d, want 1 and 1", settings, config)
		}

		// Including the empty marker, which records "transcribed, no words" —
		// not the same thing as never attempted.
		var marker string
		if err := target.QueryRow("select `transcript` from `plugin_transcripts_calls` where `callId` = 2").Scan(&marker); err != nil {
			t.Fatalf("the silence marker did not survive: %v", err)
		}
		if marker != "" {
			t.Errorf("marker transcript = %q, want empty", marker)
		}
	})

	t.Run("without calls, per-call rows stay behind", func(t *testing.T) {
		target := newSqliteAt(t, dir, "config-only.db")
		defer target.Sql.Close()

		if err := ImportSqlite(target, source, false, false); err != nil {
			t.Fatal(err)
		}

		// Copying them would be worse than leaving them: the target's call ids
		// restart from 1, so these transcripts would surface on unrelated new
		// calls.
		var transcripts uint
		target.QueryRow("select count(*) from `plugin_transcripts_calls`").Scan(&transcripts)
		if transcripts != 0 {
			t.Errorf("per-call transcripts = %d, want 0 when the calls stay behind", transcripts)
		}

		// Settings are not per-call and should still arrive.
		var settings uint
		target.QueryRow("select count(*) from `plugin_transcripts_systems`").Scan(&settings)
		if settings != 1 {
			t.Errorf("plugin settings = %d, want 1", settings)
		}
	})
}

// Talkgroup patches are configuration, and the import simply did not list the
// table.
func TestImportCopiesPatches(t *testing.T) {
	dir := t.TempDir()
	source := seedSource(t, dir, "source.db", 1)

	sourceDb := newSqliteAt(t, dir, "source.db")
	if _, err := sourceDb.Exec("insert into `rdioScannerPatches` (`disabled`, `label`, `order`, `systemId`, `talkgroupId`, `talkgroups`) " +
		"values (0, 'Fire Ground', 1, 42, 1001, '[1002]')"); err != nil {
		t.Fatalf("seeding a patch: %v", err)
	}
	sourceDb.Sql.Close()

	target := newSqliteAt(t, dir, "target.db")
	defer target.Sql.Close()

	if err := ImportSqlite(target, source, false, false); err != nil {
		t.Fatal(err)
	}

	var label, talkgroups string
	if err := target.QueryRow("select `label`, `talkgroups` from `rdioScannerPatches`").Scan(&label, &talkgroups); err != nil {
		t.Fatalf("the patch did not migrate: %v", err)
	}
	if label != "Fire Ground" || talkgroups != "[1002]" {
		t.Errorf("patch = %q / %q, want Fire Ground / [1002]", label, talkgroups)
	}
}

// A configuration older than the option split arrives as one JSON blob, and
// the migration that would convert it is already recorded as done on the
// target. Every setting used to revert to its default — with the admin
// password still working, so nothing looked wrong.
func TestImportConvertsLegacyOptionsBlob(t *testing.T) {
	dir := t.TempDir()
	source := seedSource(t, dir, "source.db", 1)

	sourceDb := newSqliteAt(t, dir, "source.db")

	// Put the source back into the pre-split shape.
	if _, err := sourceDb.Exec("delete from `rdioScannerConfigs` where `key` like 'option.%'"); err != nil {
		t.Fatalf("clearing split options: %v", err)
	}

	blob := `{"branding":"My Scanner","maxClients":500,"pruneDays":21}`
	if _, err := sourceDb.Exec("insert into `rdioScannerConfigs` (`key`, `val`) values ('options', ?)", blob); err != nil {
		t.Fatalf("seeding a legacy options blob: %v", err)
	}
	sourceDb.Sql.Close()

	target := newSqliteAt(t, dir, "target.db")
	defer target.Sql.Close()

	if err := ImportSqlite(target, source, false, false); err != nil {
		t.Fatal(err)
	}

	for key, want := range map[string]string{
		"option.branding":   `"My Scanner"`,
		"option.maxClients": "500",
		"option.pruneDays":  "21",
	} {
		var got string
		if err := target.QueryRow("select `val` from `rdioScannerConfigs` where `key` = ?", key).Scan(&got); err != nil {
			t.Errorf("%s was lost: %v", key, err)
			continue
		}
		if got != want {
			t.Errorf("%s = %s, want %s", key, got, want)
		}
	}

	// And the blob is gone, so a second import is not a second conversion.
	var blobs uint
	target.QueryRow("select count(*) from `rdioScannerConfigs` where `key` = 'options'").Scan(&blobs)
	if blobs != 0 {
		t.Errorf("the legacy options blob is still there (%d row(s))", blobs)
	}
}

// Resuming after the target's highest call id is right for an interrupted run
// of the same file, and wrong for a target that has been recording on its own
// — where it silently skipped that many of the user's calls and reported
// success.
func TestImportRefusesToResumeOverForeignCalls(t *testing.T) {
	dir := t.TempDir()
	source := seedSource(t, dir, "source.db", 6)

	target := newSqliteAt(t, dir, "target.db")
	defer target.Sql.Close()

	if err := ImportSqlite(target, source, false, false); err != nil {
		t.Fatal(err)
	}

	// Calls of the target's own, occupying the same id space.
	store := NewCalls()
	for i := 1; i <= 3; i++ {
		call := &Call{
			Audio:     []byte("local"),
			AudioName: "local.wav",
			AudioType: "audio/wav",
			// Deliberately a different instant from the source's call at this id.
			DateTime:  time.Date(2030, 1, 1, 0, 0, i, 0, time.UTC),
			System:    42,
			Talkgroup: 1001,
		}
		if _, err := store.WriteCall(call, target); err != nil {
			t.Fatalf("seeding a local call: %v", err)
		}
	}

	err := ImportSqlite(target, source, true, true)
	if err == nil {
		t.Fatal("the import accepted a target holding its own calls, silently skipping source calls")
	}
	if !strings.Contains(err.Error(), "do not come from this file") {
		t.Errorf("error does not explain the overlap: %v", err)
	}

	// Nothing from the source was written.
	var calls uint
	target.QueryRow("select count(*) from `rdioScannerCalls`").Scan(&calls)
	if calls != 3 {
		t.Errorf("calls = %d, want the target's own 3 left untouched", calls)
	}
}

// A target with no systems but a configuration of its own is in use. Reading
// "no systems" as "fresh" deleted its API keys, downstreams and dirwatches
// without asking.
func TestImportGuardsMoreThanSystems(t *testing.T) {
	dir := t.TempDir()
	source := seedSource(t, dir, "source.db", 1)

	target := newSqliteAt(t, dir, "target.db")
	defer target.Sql.Close()

	apikeys := NewApikeys()
	apikeys.List = append(apikeys.List, &Apikey{Ident: "mine", Key: "local-key", Systems: "*"})
	if err := apikeys.Write(target); err != nil {
		t.Fatalf("seeding a target api key: %v", err)
	}

	err := ImportSqlite(target, source, false, false)
	if err == nil {
		t.Fatal("the import replaced a configured target without -import_force")
	}
	if !strings.Contains(err.Error(), "ApiKeys") {
		t.Errorf("error does not name what would be replaced: %v", err)
	}

	// The user's own key is still there.
	var ident string
	if err := target.QueryRow("select `ident` from `rdioScannerApiKeys`").Scan(&ident); err != nil {
		t.Fatalf("the target's api key was destroyed: %v", err)
	}
	if ident != "mine" {
		t.Errorf("api key ident = %q, want mine", ident)
	}

	// With force it goes ahead, which is the point of the flag.
	if err := ImportSqlite(target, source, false, true); err != nil {
		t.Fatalf("-import_force should proceed: %v", err)
	}
}
