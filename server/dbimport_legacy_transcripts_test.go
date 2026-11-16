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
	"testing"
)

// seedLegacyTranscriptSource makes a source file shaped like an install that
// never booted 6.14: transcripts, the per-system and per-talkgroup switches
// and the provider settings all still live in the core tables.
func seedLegacyTranscriptSource(t *testing.T, dir string) string {
	t.Helper()

	path := seedSource(t, dir, "legacy.db", 4)

	db := newSqliteAt(t, dir, "legacy.db")
	defer db.Sql.Close()

	for _, query := range []string{
		"alter table `rdioScannerCalls` add column `transcript` text",
		"update `rdioScannerCalls` set `transcript` = 'engine 41 responding' where `id` = 1",
		"update `rdioScannerCalls` set `transcript` = 'medic 12 en route' where `id` = 2",
		"update `rdioScannerCalls` set `transcript` = '' where `id` = 3",
		"alter table `rdioScannerSystems` add column `transcribe` boolean default true",
		"alter table `rdioScannerSystems` add column `transcriptionPrompt` text",
		"update `rdioScannerSystems` set `transcribe` = 0, `transcriptionPrompt` = 'fire ground'",
		"alter table `rdioScannerTalkgroups` add column `transcribe` boolean default true",
		"update `rdioScannerTalkgroups` set `transcribe` = 0",
		// What the old install's own plugin tables would not have held.
		"delete from `plugin_transcripts_calls`",
		"delete from `plugin_transcripts_systems`",
		"delete from `plugin_transcripts_talkgroups`",
		"delete from `plugin_transcripts_config`",
		"insert into `rdioScannerConfigs` (`key`, `val`) values ('option.transcriptionProvider', '\"openai\"')",
		"insert into `rdioScannerConfigs` (`key`, `val`) values ('option.transcriptionOpenAIApiKey', '\"sk-legacy\"')",
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatalf("shaping the legacy source (%s): %v", query, err)
		}
	}

	return path
}

// Transcripts are the one thing in the database that costs money to make
// again. A source that predates the move to the plugin still keeps them in a
// column of the calls table, which a current target no longer has — and a
// column the target lacks was simply left behind, with a line saying it was
// being ignored.
func TestImportingAPre614FileBringsItsTranscripts(t *testing.T) {
	dir := t.TempDir()
	source := seedLegacyTranscriptSource(t, dir)

	// The target is whichever backend the suite is pointed at: importing an
	// old SQLite file into a new Postgres or MariaDB is the usual reason to
	// run the import at all.
	target := newTestDatabase(t)
	defer target.Sql.Close()

	if err := ImportSqlite(target, source, true, false); err != nil {
		t.Fatal(err)
	}

	read := func(id int) (string, bool) {
		var text string
		err := target.QueryRow("select `transcript` from `plugin_transcripts_calls` where `callId` = ?", id).Scan(&text)
		return text, err == nil
	}

	if text, ok := read(1); !ok || text != "engine 41 responding" {
		t.Errorf("call 1's transcript arrived as %q (found %v)", text, ok)
	}
	if text, ok := read(2); !ok || text != "medic 12 en route" {
		t.Errorf("call 2's transcript arrived as %q (found %v)", text, ok)
	}
	if _, ok := read(3); ok {
		t.Error("a call whose old transcript was empty was given a row")
	}
	if _, ok := read(4); ok {
		t.Error("a call that never had a transcript was given a row")
	}

	var transcribe bool
	var prompt string
	if err := target.QueryRow("select `transcribe`, `prompt` from `plugin_transcripts_systems` where `systemId` = 42").Scan(&transcribe, &prompt); err != nil {
		t.Errorf("the system's transcription switch did not arrive: %v", err)
	} else if transcribe || prompt != "fire ground" {
		t.Errorf("the system arrived as transcribe=%v prompt=%q, want off with its prompt", transcribe, prompt)
	}

	var talkgroups int
	target.QueryRow("select count(*) from `plugin_transcripts_talkgroups` where `transcribe` = ?", false).Scan(&talkgroups)
	if talkgroups == 0 {
		t.Error("the talkgroups' transcription switches did not arrive")
	}

	var provider, key string
	target.QueryRow("select `value` from `plugin_transcripts_config` where `key` = 'provider'").Scan(&provider)
	target.QueryRow("select `value` from `plugin_transcripts_config` where `key` = 'openaiApiKey'").Scan(&key)
	if provider != `"openai"` || key != `"sk-legacy"` {
		t.Errorf("the provider settings arrived as provider=%s key=%s", provider, key)
	}
}

// Without the calls, the per-call transcripts stay behind with them; the
// switches and settings, which belong to the configuration, still come.
func TestImportingAPre614FileWithoutCallsStillBringsTheSettings(t *testing.T) {
	dir := t.TempDir()
	source := seedLegacyTranscriptSource(t, dir)

	target := newTestDatabase(t)
	defer target.Sql.Close()

	if err := ImportSqlite(target, source, false, false); err != nil {
		t.Fatal(err)
	}

	var transcripts, systems int
	target.QueryRow("select count(*) from `plugin_transcripts_calls`").Scan(&transcripts)
	target.QueryRow("select count(*) from `plugin_transcripts_systems` where `systemId` = 42").Scan(&systems)

	if transcripts != 0 {
		t.Errorf("%d transcripts arrived for calls that were not imported", transcripts)
	}
	if systems != 1 {
		t.Error("the system's transcription switch did not arrive")
	}
}
