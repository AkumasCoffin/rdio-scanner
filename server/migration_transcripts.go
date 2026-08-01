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
	"fmt"
	"log"
)

// Moves transcript data out of the core schema and into the tables the
// transcripts plugin owns.
//
// This lives in the server, not in the plugin, for two reasons. A plugin cannot
// read core tables — that isolation is the point of the plugin database layer —
// and more importantly the data has to survive whether or not the plugin is
// ever installed. Someone upgrading an offline server with no plugin available
// must not lose years of transcripts.
//
// Each step is recorded separately in rdioScannerMeta and each is written to be
// safe to re-run, because MySQL and MariaDB commit every DDL statement on the
// spot: no transaction can make this atomic on all three backends, so it is
// built to be resumable instead. The destructive steps are gated behind a row
// count comparison, so a copy that did not fully succeed leaves the originals
// untouched.

const (
	transcriptMigrationTables = "20260802100000-transcripts-plugin-tables"
	transcriptMigrationCopy   = "20260802100001-transcripts-plugin-copy"
	transcriptMigrationVerify = "20260802100002-transcripts-plugin-verify"
	transcriptMigrationDrop   = "20260802100003-transcripts-plugin-drop"
)

// The plugin declares these same tables in its manifest. Both paths use
// "create table if not exists", so whichever runs first wins and the other is a
// no-op — the plugin does not need this migration to have run, and this
// migration does not need the plugin to be installed.
func transcriptPluginTableSql(db *Database) []string {
	switch db.Config.DbType {
	case DbTypeSqlite:
		return []string{
			"create table if not exists `plugin_transcripts_calls` (`callId` integer not null, `transcript` text, primary key (`callId`))",
			"create table if not exists `plugin_transcripts_systems` (`systemId` integer not null, `transcribe` boolean default 1, `prompt` text, primary key (`systemId`))",
			"create table if not exists `plugin_transcripts_talkgroups` (`systemId` integer not null, `talkgroupId` integer not null, `transcribe` boolean default 1, primary key (`systemId`, `talkgroupId`))",
			"create table if not exists `plugin_transcripts_config` (`key` varchar(255) not null, `value` text, primary key (`key`))",
		}
	case DbTypePostgres:
		return []string{
			`create table if not exists "plugin_transcripts_calls" ("callId" integer not null, "transcript" text, primary key ("callId"))`,
			`create table if not exists "plugin_transcripts_systems" ("systemId" integer not null, "transcribe" boolean default true, "prompt" text, primary key ("systemId"))`,
			`create table if not exists "plugin_transcripts_talkgroups" ("systemId" integer not null, "talkgroupId" integer not null, "transcribe" boolean default true, primary key ("systemId", "talkgroupId"))`,
			`create table if not exists "plugin_transcripts_config" ("key" varchar(255) not null, "value" text, primary key ("key"))`,
		}
	default:
		return []string{
			"create table if not exists `plugin_transcripts_calls` (`callId` integer not null, `transcript` longtext, primary key (`callId`))",
			"create table if not exists `plugin_transcripts_systems` (`systemId` integer not null, `transcribe` boolean default 1, `prompt` text, primary key (`systemId`))",
			"create table if not exists `plugin_transcripts_talkgroups` (`systemId` integer not null, `talkgroupId` integer not null, `transcribe` boolean default 1, primary key (`systemId`, `talkgroupId`))",
			"create table if not exists `plugin_transcripts_config` (`key` varchar(255) not null, `value` text, primary key (`key`))",
		}
	}
}

// The option rows the plugin re-reads under its own names, so a server that had
// transcription configured keeps working once the plugin loads.
var transcriptOptionMapping = map[string]string{
	"option.transcriptionEnabled":         "enabled",
	"option.transcriptionProvider":        "provider",
	"option.transcriptionBaseUrl":         "groqBaseUrl",
	"option.transcriptionApiKey":          "groqApiKey",
	"option.transcriptionModel":           "groqModel",
	"option.transcriptionOpenAIBaseUrl":   "openaiBaseUrl",
	"option.transcriptionOpenAIApiKey":    "openaiApiKey",
	"option.transcriptionOpenAIModel":     "openaiModel",
	"option.transcriptionWhisperBaseUrl":  "whisperBaseUrl",
	"option.transcriptionWhisperApiKey":   "whisperApiKey",
	"option.transcriptionWhisperModel":    "whisperModel",
	"option.transcriptionLanguage":        "language",
	"option.transcriptionPrompt":          "prompt",
	"option.transcriptionMaxPerMinute":    "maxPerMinute",
	"option.transcriptionMinAudioBytes":   "minAudioBytes",
	"option.waitForTranscript":            "waitForTranscript",
	"option.showRetranscribeButton":       "showRetranscribeButton",
}

func (db *Database) migrationDone(name string) (bool, error) {
	var count int
	query := db.formatQuery(fmt.Sprintf("select count(*) from `rdioScannerMeta` where `name` = '%s'", name))
	if err := db.Sql.QueryRow(query).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func (db *Database) recordMigration(name string) error {
	_, err := db.Sql.Exec(db.formatQuery(fmt.Sprintf("insert into `rdioScannerMeta` (`name`) values ('%s')", name)))
	return err
}

// columnExists reports whether a column is still present, so the migration can
// tell "already dropped" apart from "never existed".
func (db *Database) columnExists(table string, column string) bool {
	query := db.formatQuery(fmt.Sprintf("select `%s` from `%s` limit 1", column, table))
	rows, err := db.Sql.Query(query)
	if err != nil {
		return false
	}
	rows.Close()
	return true
}

func (db *Database) migrationTranscriptsToPlugin(verbose bool) error {
	// Step 1 — create the destination tables.
	if done, err := db.migrationDone(transcriptMigrationTables); err != nil {
		return err
	} else if !done {
		if verbose {
			log.Printf("running database migration %s", transcriptMigrationTables)
		}
		for _, query := range transcriptPluginTableSql(db) {
			if _, err := db.Sql.Exec(query); err != nil {
				return fmt.Errorf("%s: %v while doing %s", transcriptMigrationTables, err, query)
			}
		}
		if err := db.recordMigration(transcriptMigrationTables); err != nil {
			return err
		}
	}

	// Nothing to move on a fresh install.
	if !db.columnExists("rdioScannerCalls", "transcript") {
		for _, name := range []string{transcriptMigrationCopy, transcriptMigrationVerify, transcriptMigrationDrop} {
			if done, err := db.migrationDone(name); err == nil && !done {
				db.recordMigration(name)
			}
		}
		return nil
	}

	// Step 2 — copy. Every insert is guarded against an existing row, so a run
	// interrupted part way through resumes rather than duplicating.
	if done, err := db.migrationDone(transcriptMigrationCopy); err != nil {
		return err
	} else if !done {
		if verbose {
			log.Printf("running database migration %s", transcriptMigrationCopy)
		}
		if err := db.copyTranscriptData(verbose); err != nil {
			return err
		}
		if err := db.recordMigration(transcriptMigrationCopy); err != nil {
			return err
		}
	}

	// Step 3 — verify before anything destructive happens.
	if done, err := db.migrationDone(transcriptMigrationVerify); err != nil {
		return err
	} else if !done {
		var source, copied int

		if err := db.Sql.QueryRow(db.formatQuery(
			"select count(*) from `rdioScannerCalls` where `transcript` is not null and `transcript` <> ''",
		)).Scan(&source); err != nil {
			return fmt.Errorf("%s source count: %v", transcriptMigrationVerify, err)
		}

		if err := db.Sql.QueryRow(db.formatQuery(
			"select count(*) from `plugin_transcripts_calls` where `transcript` is not null and `transcript` <> ''",
		)).Scan(&copied); err != nil {
			return fmt.Errorf("%s copied count: %v", transcriptMigrationVerify, err)
		}

		if copied < source {
			// Abort without recording. The originals are untouched, so the next
			// start retries rather than losing anything.
			return fmt.Errorf(
				"transcript migration aborted: copied %d of %d transcripts. The original data has not been touched; "+
					"resolve the cause and restart to retry", copied, source,
			)
		}

		log.Printf("transcript migration: verified %d transcripts moved to the plugin tables", copied)

		if err := db.recordMigration(transcriptMigrationVerify); err != nil {
			return err
		}
	}

	// Step 4 — drop the originals.
	if done, err := db.migrationDone(transcriptMigrationDrop); err != nil {
		return err
	} else if !done {
		if verbose {
			log.Printf("running database migration %s", transcriptMigrationDrop)
		}
		db.dropTranscriptColumns(verbose)
		if err := db.recordMigration(transcriptMigrationDrop); err != nil {
			return err
		}
	}

	return nil
}

func (db *Database) copyTranscriptData(verbose bool) error {
	// Calls. "where not exists" makes a resumed run skip what already landed.
	callsQuery := "insert into `plugin_transcripts_calls` (`callId`, `transcript`) " +
		"select `id`, `transcript` from `rdioScannerCalls` " +
		"where `transcript` is not null and `transcript` <> '' " +
		"and not exists (select 1 from `plugin_transcripts_calls` where `plugin_transcripts_calls`.`callId` = `rdioScannerCalls`.`id`)"

	if _, err := db.Sql.Exec(db.formatQuery(callsQuery)); err != nil {
		return fmt.Errorf("%s calls: %v", transcriptMigrationCopy, err)
	}

	// Systems: the transcribe flag and any per-system prompt.
	if db.columnExists("rdioScannerSystems", "transcribe") {
		promptColumn := "''"
		if db.columnExists("rdioScannerSystems", "transcriptionPrompt") {
			promptColumn = "`transcriptionPrompt`"
		}

		systemsQuery := fmt.Sprintf(
			"insert into `plugin_transcripts_systems` (`systemId`, `transcribe`, `prompt`) "+
				"select `id`, `transcribe`, %s from `rdioScannerSystems` "+
				"where not exists (select 1 from `plugin_transcripts_systems` where `plugin_transcripts_systems`.`systemId` = `rdioScannerSystems`.`id`)",
			promptColumn,
		)

		if _, err := db.Sql.Exec(db.formatQuery(systemsQuery)); err != nil {
			return fmt.Errorf("%s systems: %v", transcriptMigrationCopy, err)
		}
	}

	// Talkgroups.
	if db.columnExists("rdioScannerTalkgroups", "transcribe") {
		talkgroupsQuery := "insert into `plugin_transcripts_talkgroups` (`systemId`, `talkgroupId`, `transcribe`) " +
			"select `systemId`, `id`, `transcribe` from `rdioScannerTalkgroups` " +
			"where not exists (select 1 from `plugin_transcripts_talkgroups` " +
			"where `plugin_transcripts_talkgroups`.`systemId` = `rdioScannerTalkgroups`.`systemId` " +
			"and `plugin_transcripts_talkgroups`.`talkgroupId` = `rdioScannerTalkgroups`.`id`)"

		if _, err := db.Sql.Exec(db.formatQuery(talkgroupsQuery)); err != nil {
			return fmt.Errorf("%s talkgroups: %v", transcriptMigrationCopy, err)
		}
	}

	// Settings. Values are stored as JSON in both places, so they carry across
	// unchanged and keep their types.
	moved := 0
	for optionKey, pluginKey := range transcriptOptionMapping {
		var value string

		err := db.Sql.QueryRow(
			db.formatQuery("select `val` from `rdioScannerConfigs` where `key` = ?"), optionKey,
		).Scan(&value)
		if err != nil {
			continue
		}

		var exists int
		if err := db.Sql.QueryRow(
			db.formatQuery("select count(*) from `plugin_transcripts_config` where `key` = ?"), pluginKey,
		).Scan(&exists); err != nil || exists > 0 {
			continue
		}

		if _, err := db.Sql.Exec(
			db.formatQuery("insert into `plugin_transcripts_config` (`key`, `value`) values (?, ?)"),
			pluginKey, value,
		); err == nil {
			moved++
		}
	}

	if moved > 0 && verbose {
		log.Printf("transcript migration: moved %d settings into the plugin's configuration", moved)
	}

	return nil
}

// dropTranscriptColumns removes the originals. Failures are logged rather than
// returned: by this point the data is copied and verified, and a server that
// cannot drop a column should still start.
func (db *Database) dropTranscriptColumns(verbose bool) {
	// Postgres keeps the transcript search index on the old column; move it to
	// the plugin's table so free-text search stays index-backed.
	if db.Config.DbType == DbTypePostgres {
		if _, err := db.Sql.Exec(`drop index if exists "rdio_scanner_calls_transcript_trgm"`); err != nil {
			log.Printf("transcript migration: could not drop the old transcript index: %v", err)
		}
		if _, err := db.Sql.Exec("create extension if not exists pg_trgm"); err != nil {
			log.Printf("transcript migration: pg_trgm unavailable, transcript search will fall back to a scan: %v", err)
		} else if _, err := db.Sql.Exec(
			`create index if not exists "plugin_transcripts_calls_trgm" on "plugin_transcripts_calls" using gin ("transcript" gin_trgm_ops)`,
		); err != nil {
			log.Printf("transcript migration: could not create the plugin transcript index: %v", err)
		} else if verbose {
			log.Printf("transcript migration: transcript search index moved to the plugin table")
		}
	}

	drops := []struct{ table, column string }{
		{"rdioScannerCalls", "transcript"},
		{"rdioScannerSystems", "transcribe"},
		{"rdioScannerSystems", "transcriptionPrompt"},
		{"rdioScannerTalkgroups", "transcribe"},
	}

	for _, drop := range drops {
		if !db.columnExists(drop.table, drop.column) {
			continue
		}

		query := db.formatQuery(fmt.Sprintf("alter table `%s` drop column `%s`", drop.table, drop.column))
		if _, err := db.Sql.Exec(query); err != nil {
			log.Printf("transcript migration: could not drop %s.%s: %v", drop.table, drop.column, err)
		} else if verbose {
			log.Printf("transcript migration: dropped %s.%s", drop.table, drop.column)
		}
	}

	// The old option rows are left in place deliberately: they are what tells
	// the server on next start that this install used to have transcription,
	// which is how the plugin gets offered or auto-installed.
}
