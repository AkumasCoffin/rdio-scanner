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
	"testing"
	"time"
)

// A plugin asking for a call without its audio should not be charged for the
// audio. rdio.calls.get runs on the plugin's single event loop, so a 50–200 KB
// blob fetched and then discarded is time every other thing that plugin is
// trying to do spends waiting.
//
// Runs against whichever backend the suite is pointed at, because the query
// substitutes a literal for the column and "select null, …" is the kind of
// thing one backend accepts and another argues about.
func TestGetCallMetaLeavesTheAudioBehind(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	calls := NewCalls()
	when := time.Now().UTC()

	insertTestCall(t, db, when)

	var id uint
	if err := db.QueryRow("select `id` from `rdioScannerCalls` order by `id` desc limit 1").Scan(&id); err != nil {
		t.Fatal(err)
	}

	meta, err := calls.GetCallMeta(id, db)
	if err != nil {
		t.Fatalf("GetCallMeta: %v", err)
	}

	if meta == nil {
		t.Fatal("no call returned")
	}

	if len(meta.Audio) != 0 {
		t.Errorf("carried %d bytes of audio; want none", len(meta.Audio))
	}

	// Everything else still has to be there — a metadata read that loses the
	// metadata would be worse than the cost it saves.
	if meta.System != 1 || meta.Talkgroup != 1 {
		t.Errorf("system/talkgroup are %d/%d; want 1/1", meta.System, meta.Talkgroup)
	}

	if meta.DateTime.IsZero() {
		t.Error("dateTime came back zero")
	}

	// And the audio-carrying path must still carry it.
	full, err := calls.GetCall(id, db)
	if err != nil {
		t.Fatalf("GetCall: %v", err)
	}

	if len(full.Audio) == 0 {
		t.Error("GetCall returned no audio; the blob read is broken")
	}
}
