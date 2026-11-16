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
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// Calls are stored in UTC. A time that reaches the database in another zone
// is the right instant written down wrongly, and on Postgres the zone is
// simply dropped.
func TestAPluginSuppliedCallTimeIsStoredAsTheInstantItNames(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	want := time.Date(2026, 3, 1, 4, 0, 0, 0, time.UTC)

	for name, value := range map[string]any{
		"a string with an offset": "2026-03-01T15:00:00+11:00",
		"a time in another zone":  want.In(time.FixedZone("AEDT", 11*3600)),
	} {
		t.Run(name, func(t *testing.T) {
			call := NewCall()
			call.Audio = []byte{1, 2, 3}
			call.AudioName = "a.m4a"
			call.AudioType = "audio/mp4"
			call.System = 1
			call.Talkgroup = 100

			applyPluginCallValue(call, map[string]any{"dateTime": value})

			id, err := NewCalls().WriteCall(call, db)
			if err != nil {
				t.Fatal(err)
			}

			stored, err := NewCalls().GetCall(id, db)
			if err != nil {
				t.Fatal(err)
			}

			if !stored.DateTime.UTC().Equal(want) {
				t.Fatalf("stored as %s, want %s", stored.DateTime.UTC().Format(time.RFC3339), want.Format(time.RFC3339))
			}
		})
	}
}

// The public API's from and to are instants too, however the caller writes
// them.
func TestThePublicApiDateWindowHonoursTheCallersOffset(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	controller := &Controller{Database: db, Apikeys: NewApikeys(), Plugins: NewPlugins(), Logs: &Logs{}}
	controller.Apikeys.FromMap([]any{map[string]any{"key": "k", "ident": "reader", "systems": "*"}})

	at := time.Date(2026, 3, 1, 4, 0, 0, 0, time.UTC)
	wanted := insertSearchTestCall(t, db, at, 1, 100)
	insertSearchTestCall(t, db, at.Add(3*time.Hour), 1, 100)

	list := func(from string, to string) []float64 {
		query := url.Values{"key": {"k"}, "from": {from}, "to": {to}}

		recorder := httptest.NewRecorder()
		NewPublicApi(controller).CallsRouter(recorder, httptest.NewRequest("GET", "/api/v1/calls?"+query.Encode(), nil))

		var body struct {
			Results []struct {
				Id float64 `json:"id"`
			} `json:"results"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatalf("status %d, body %s", recorder.Code, recorder.Body.String())
		}

		ids := []float64{}
		for _, result := range body.Results {
			ids = append(ids, result.Id)
		}
		return ids
	}

	for name, window := range map[string][2]string{
		"in UTC":            {"2026-03-01T03:59:00Z", "2026-03-01T04:01:00Z"},
		"in Sydney time":    {"2026-03-01T14:59:00+11:00", "2026-03-01T15:01:00+11:00"},
		"starting on the call": {"2026-03-01T04:00:00Z", "2026-03-01T04:01:00Z"},
	} {
		got := list(window[0], window[1])
		if len(got) != 1 || uint(got[0]) != wanted {
			t.Errorf("%s: the window returned calls %v, want only call %d", name, got, wanted)
		}
	}
}
