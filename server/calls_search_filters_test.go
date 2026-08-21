// Copyright (C) 2019-2024 Chrystian Huot <chrystian.huot@saubeo.solutions>
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
	"strings"
	"testing"
	"time"
)

// searchTestClient is the scoping context Calls.Search reads filters against:
// the group and tag maps a client is allowed to see, and no access
// restriction unless a case sets one.
func searchTestClient(db *Database) *Client {
	return &Client{
		Controller: &Controller{Database: db, Plugins: &Plugins{}},
		GroupsMap: GroupsMap{
			// Single-system on purpose: the legacy `group` filter walks the map
			// in Go's randomized order, so only a one-entry group has a
			// deterministic rendering to assert on. The plural `groups` filter
			// sorts, which is what lets the multi-system case below be pinned.
			"Ems":  {3: []uint{300, 301}},
			"Fire": {2: []uint{200}, 1: []uint{100, 101}},
		},
		TagsMap: TagsMap{
			"Dispatch": {4: []uint{400}},
			"Ops":      {2: []uint{210}, 1: []uint{110}},
		},
	}
}

// TestCallsSearchPlanConstruction pins the where/order/cursor SQL for every
// filter, old and new.
//
// The "old shape" cases are the important half: their expected strings are
// transcribed from the implementation as it stood before cursor paging existed,
// so a change that alters what the Android app or a plugin gets out of the
// existing fields fails here. The only deliberate difference across the whole
// suite is the id tiebreak in the order clause, which every case carries.
func TestCallsSearchPlanConstruction(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	// Rendered through the backend's own format so the expectations hold on
	// SQLite, MariaDB and Postgres alike.
	at := func(rfc3339 string) string {
		t.Helper()
		v, err := time.Parse(time.RFC3339, rfc3339)
		if err != nil {
			t.Fatal(err)
		}
		return v.UTC().Format(db.DateTimeFormat)
	}

	// The ±24h window `date` has always produced: anchored on the date, opened
	// forwards when sorting ascending and backwards when sorting descending.
	dateAsc := fmt.Sprintf(" and (`dateTime` between '%v' and '%v')", at("2024-03-05T10:30:00Z"), at("2024-03-06T10:29:59.999Z"))
	dateDesc := fmt.Sprintf(" and (`dateTime` between '%v' and '%v')", at("2024-03-04T10:30:00.001Z"), at("2024-03-05T10:30:00Z"))

	date := func(rfc3339 string) time.Time {
		t.Helper()
		v, err := time.Parse(time.RFC3339, rfc3339)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}

	// The date window and the cursor are bound parameters, not literals, and
	// always normalised to UTC — the same instant carrying another zone is a
	// different value to an equality test on SQLite.
	utc := func(rfc3339 string) time.Time {
		t.Helper()
		return date(rfc3339).UTC()
	}

	tests := []struct {
		name    string
		options CallsSearchOptions
		access  *Access
		// wantWhere and wantPage default to wantProbe when empty, which is the
		// common case: no date window and no cursor. wantPageArgs defaults to
		// wantWhereArgs, which is every case that does not carry a cursor.
		wantProbe     string
		wantWhere     string
		wantWhereArgs []any
		wantPage      string
		wantPageArgs  []any
		wantOrder     string
		wantLimit     uint
		wantOffset    uint
		wantCount     bool
	}{
		{
			name:      "old shape: no options at all",
			options:   CallsSearchOptions{},
			wantProbe: "true",
			wantOrder: "`dateTime` asc, `id` asc",
			wantLimit: 200,
			wantCount: true,
		},
		{
			name:      "old shape: system only",
			options:   CallsSearchOptions{System: uint(5)},
			wantProbe: "true and (`system` = 5)",
			wantOrder: "`dateTime` asc, `id` asc",
			wantLimit: 200,
			wantCount: true,
		},
		{
			name:      "old shape: system and talkgroup",
			options:   CallsSearchOptions{System: uint(5), Talkgroup: uint(100)},
			wantProbe: "true and (`system` = 5 and `talkgroup` = 100)",
			wantOrder: "`dateTime` asc, `id` asc",
			wantLimit: 200,
			wantCount: true,
		},
		{
			name:      "old shape: talkgroup alone is ignored without a system",
			options:   CallsSearchOptions{Talkgroup: uint(100)},
			wantProbe: "true",
			wantOrder: "`dateTime` asc, `id` asc",
			wantLimit: 200,
			wantCount: true,
		},
		{
			name:      "old shape: patched talkgroups",
			options:   CallsSearchOptions{System: uint(5), Talkgroup: uint(100), searchPatchedTalkgroups: true},
			wantProbe: "true and (`system` = 5 and `talkgroup` = 100 or patches = '100' or patches like '[100]' or patches like '[100,%' or patches like '%,100,%' or patches like '%,100]')",
			wantOrder: "`dateTime` asc, `id` asc",
			wantLimit: 200,
			wantCount: true,
		},
		{
			name:      "old shape: group",
			options:   CallsSearchOptions{Group: "Ems"},
			wantProbe: "true and ((`system` = 3 and `talkgroup` in (300, 301)))",
			wantOrder: "`dateTime` asc, `id` asc",
			wantLimit: 200,
			wantCount: true,
		},
		{
			name:      "old shape: unknown group drops the clause",
			options:   CallsSearchOptions{Group: "Nope"},
			wantProbe: "true",
			wantOrder: "`dateTime` asc, `id` asc",
			wantLimit: 200,
			wantCount: true,
		},
		{
			name:      "old shape: tag",
			options:   CallsSearchOptions{Tag: "Dispatch"},
			wantProbe: "true and ((`system` = 4 and `talkgroup` in (400)))",
			wantOrder: "`dateTime` asc, `id` asc",
			wantLimit: 200,
			wantCount: true,
		},
		{
			name:      "old shape: access scoping",
			options:   CallsSearchOptions{},
			access:    &Access{Systems: []any{map[string]any{"id": 1, "talkgroups": []any{100, 101}}, map[string]any{"id": 2, "talkgroups": "*"}}},
			wantProbe: "((`system` = 1 and `talkgroup` in (100, 101)) or `system` = 2)",
			wantOrder: "`dateTime` asc, `id` asc",
			wantLimit: 200,
			wantCount: true,
		},
		{
			name:      "old shape: date window, ascending",
			options:   CallsSearchOptions{Date: date("2024-03-05T10:30:00Z")},
			wantProbe: "true",
			wantWhere: "true" + dateAsc,
			wantOrder: "`dateTime` asc, `id` asc",
			wantLimit: 200,
			wantCount: true,
		},
		{
			name:      "old shape: date window, descending",
			options:   CallsSearchOptions{Date: date("2024-03-05T10:30:00Z"), Sort: float64(-1)},
			wantProbe: "true",
			wantWhere: "true" + dateDesc,
			wantOrder: "`dateTime` desc, `id` desc",
			wantLimit: 200,
			wantCount: true,
		},
		{
			name:       "old shape: limit clamped, offset honoured",
			options:    CallsSearchOptions{Limit: uint(900), Offset: uint(40)},
			wantProbe:  "true",
			wantOrder:  "`dateTime` asc, `id` asc",
			wantLimit:  500,
			wantOffset: 40,
			wantCount:  true,
		},
		{
			name:      "old shape: q with no searchable plugin matches nothing",
			options:   CallsSearchOptions{Q: "fire"},
			wantProbe: "true and 1 = 0",
			wantOrder: "`dateTime` asc, `id` asc",
			wantLimit: 200,
			wantCount: true,
		},
		{
			name:          "new: dateStart alone is open-ended",
			options:       CallsSearchOptions{DateStart: date("2024-03-05T00:00:00Z")},
			wantProbe:     "true",
			wantWhere:     "true and (`dateTime` >= ?)",
			wantWhereArgs: []any{utc("2024-03-05T00:00:00Z")},
			wantOrder:     "`dateTime` asc, `id` asc",
			wantLimit:     200,
			wantCount:     true,
		},
		{
			name:          "new: dateStop alone is open-ended",
			options:       CallsSearchOptions{DateStop: date("2024-03-05T23:59:59Z")},
			wantProbe:     "true",
			wantWhere:     "true and (`dateTime` <= ?)",
			wantWhereArgs: []any{utc("2024-03-05T23:59:59Z")},
			wantOrder:     "`dateTime` asc, `id` asc",
			wantLimit:     200,
			wantCount:     true,
		},
		{
			name:          "new: a span of days",
			options:       CallsSearchOptions{DateStart: date("2024-03-05T00:00:00Z"), DateStop: date("2024-03-08T23:59:59Z")},
			wantProbe:     "true",
			wantWhere:     "true and (`dateTime` between ? and ?)",
			wantWhereArgs: []any{utc("2024-03-05T00:00:00Z"), utc("2024-03-08T23:59:59Z")},
			wantOrder:     "`dateTime` asc, `id` asc",
			wantLimit:     200,
			wantCount:     true,
		},
		{
			name:          "new: part of one day, offsets normalised to UTC",
			options:       CallsSearchOptions{DateStart: date("2024-03-05T08:00:00-05:00"), DateStop: date("2024-03-05T12:00:00-05:00")},
			wantProbe:     "true",
			wantWhere:     "true and (`dateTime` between ? and ?)",
			wantWhereArgs: []any{utc("2024-03-05T13:00:00Z"), utc("2024-03-05T17:00:00Z")},
			wantOrder:     "`dateTime` asc, `id` asc",
			wantLimit:     200,
			wantCount:     true,
		},
		{
			name:      "new: systems",
			options:   CallsSearchOptions{Systems: []uint{7, 3}},
			wantProbe: "true and (`system` in (7, 3))",
			wantOrder: "`dateTime` asc, `id` asc",
			wantLimit: 200,
			wantCount: true,
		},
		{
			name:      "new: an empty systems array matches nothing",
			options:   CallsSearchOptions{Systems: []uint{}},
			wantProbe: "true and 1 = 0",
			wantOrder: "`dateTime` asc, `id` asc",
			wantLimit: 200,
			wantCount: true,
		},
		{
			name: "new: talkgroup pairs group by system",
			options: CallsSearchOptions{Talkgroups: []CallsSearchTalkgroup{
				{System: 1, Talkgroup: 100},
				{System: 2, Talkgroup: 100},
				{System: 1, Talkgroup: 101},
			}},
			wantProbe: "true and ((`system` = 1 and `talkgroup` in (100, 101)) or (`system` = 2 and `talkgroup` in (100)))",
			wantOrder: "`dateTime` asc, `id` asc",
			wantLimit: 200,
			wantCount: true,
		},
		{
			name:      "new: an empty talkgroups array matches nothing",
			options:   CallsSearchOptions{Talkgroups: []CallsSearchTalkgroup{}},
			wantProbe: "true and 1 = 0",
			wantOrder: "`dateTime` asc, `id` asc",
			wantLimit: 200,
			wantCount: true,
		},
		{
			name:      "new: groups merge and sort",
			options:   CallsSearchOptions{Groups: []string{"Fire", "Ems"}},
			wantProbe: "true and ((`system` = 1 and `talkgroup` in (100, 101)) or (`system` = 2 and `talkgroup` in (200)) or (`system` = 3 and `talkgroup` in (300, 301)))",
			wantOrder: "`dateTime` asc, `id` asc",
			wantLimit: 200,
			wantCount: true,
		},
		{
			name:      "new: groups the client cannot see match nothing",
			options:   CallsSearchOptions{Groups: []string{"Nope"}},
			wantProbe: "true and 1 = 0",
			wantOrder: "`dateTime` asc, `id` asc",
			wantLimit: 200,
			wantCount: true,
		},
		{
			name:      "new: tags merge and sort",
			options:   CallsSearchOptions{Tags: []string{"Ops", "Dispatch"}},
			wantProbe: "true and ((`system` = 1 and `talkgroup` in (110)) or (`system` = 2 and `talkgroup` in (210)) or (`system` = 4 and `talkgroup` in (400)))",
			wantOrder: "`dateTime` asc, `id` asc",
			wantLimit: 200,
			wantCount: true,
		},
		{
			name:      "new: cursor mode alone drops the count",
			options:   CallsSearchOptions{Cursor: true},
			wantProbe: "true",
			wantOrder: "`dateTime` asc, `id` asc",
			wantLimit: 200,
			wantCount: false,
		},
		{
			name:         "new: after, ascending",
			options:      CallsSearchOptions{After: &CallsSearchCursor{DateTime: date("2024-03-05T10:30:00Z"), Id: 4242}},
			wantProbe:    "true",
			wantPage:     "true and (`dateTime` > ? or (`dateTime` = ? and `id` > ?))",
			wantPageArgs: []any{utc("2024-03-05T10:30:00Z"), utc("2024-03-05T10:30:00Z"), uint(4242)},
			wantOrder:    "`dateTime` asc, `id` asc",
			wantLimit:    200,
			wantCount:    false,
		},
		{
			name:         "new: after, descending",
			options:      CallsSearchOptions{After: &CallsSearchCursor{DateTime: date("2024-03-05T10:30:00Z"), Id: 4242}, Sort: float64(-1)},
			wantProbe:    "true",
			wantPage:     "true and (`dateTime` < ? or (`dateTime` = ? and `id` < ?))",
			wantPageArgs: []any{utc("2024-03-05T10:30:00Z"), utc("2024-03-05T10:30:00Z"), uint(4242)},
			wantOrder:    "`dateTime` desc, `id` desc",
			wantLimit:    200,
			wantCount:    false,
		},
		{
			name: "new: after wins over offset",
			options: CallsSearchOptions{
				After:  &CallsSearchCursor{DateTime: date("2024-03-05T10:30:00Z"), Id: 1},
				Offset: uint(400),
			},
			wantProbe:    "true",
			wantPage:     "true and (`dateTime` > ? or (`dateTime` = ? and `id` > ?))",
			wantPageArgs: []any{utc("2024-03-05T10:30:00Z"), utc("2024-03-05T10:30:00Z"), uint(1)},
			wantOrder:    "`dateTime` asc, `id` asc",
			wantLimit:    200,
			wantOffset:   0,
			wantCount:    false,
		},
		{
			name: "new: every filter at once, on a cursor page",
			options: CallsSearchOptions{
				Systems:    []uint{1, 2},
				Talkgroups: []CallsSearchTalkgroup{{System: 1, Talkgroup: 100}, {System: 2, Talkgroup: 200}},
				Groups:     []string{"Ems"},
				Tags:       []string{"Dispatch"},
				DateStart:  date("2024-03-05T00:00:00Z"),
				DateStop:   date("2024-03-05T23:59:59Z"),
				After:      &CallsSearchCursor{DateTime: date("2024-03-05T12:00:00Z"), Id: 900},
				Sort:       float64(-1),
				Limit:      uint(50),
			},
			access: &Access{Systems: []any{map[string]any{"id": 1, "talkgroups": []any{100, 101}}, map[string]any{"id": 2, "talkgroups": "*"}}},
			wantProbe: "((`system` = 1 and `talkgroup` in (100, 101)) or `system` = 2)" +
				" and (`system` in (1, 2))" +
				" and ((`system` = 1 and `talkgroup` in (100)) or (`system` = 2 and `talkgroup` in (200)))" +
				" and ((`system` = 3 and `talkgroup` in (300, 301)))" +
				" and ((`system` = 4 and `talkgroup` in (400)))",
			wantWhere: "((`system` = 1 and `talkgroup` in (100, 101)) or `system` = 2)" +
				" and (`system` in (1, 2))" +
				" and ((`system` = 1 and `talkgroup` in (100)) or (`system` = 2 and `talkgroup` in (200)))" +
				" and ((`system` = 3 and `talkgroup` in (300, 301)))" +
				" and ((`system` = 4 and `talkgroup` in (400)))" +
				" and (`dateTime` between ? and ?)",
			wantWhereArgs: []any{utc("2024-03-05T00:00:00Z"), utc("2024-03-05T23:59:59Z")},
			wantPage: "((`system` = 1 and `talkgroup` in (100, 101)) or `system` = 2)" +
				" and (`system` in (1, 2))" +
				" and ((`system` = 1 and `talkgroup` in (100)) or (`system` = 2 and `talkgroup` in (200)))" +
				" and ((`system` = 3 and `talkgroup` in (300, 301)))" +
				" and ((`system` = 4 and `talkgroup` in (400)))" +
				" and (`dateTime` between ? and ?)" +
				" and (`dateTime` < ? or (`dateTime` = ? and `id` < ?))",
			wantPageArgs: []any{
				utc("2024-03-05T00:00:00Z"), utc("2024-03-05T23:59:59Z"),
				utc("2024-03-05T12:00:00Z"), utc("2024-03-05T12:00:00Z"), uint(900),
			},
			wantOrder: "`dateTime` desc, `id` desc",
			wantLimit: 50,
			wantCount: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := searchTestClient(db)
			client.Access = tc.access

			wantWhere := tc.wantWhere
			if wantWhere == "" {
				wantWhere = tc.wantProbe
			}
			wantPage := tc.wantPage
			if wantPage == "" {
				wantPage = wantWhere
			}
			wantPageArgs := tc.wantPageArgs
			if wantPageArgs == nil {
				wantPageArgs = tc.wantWhereArgs
			}

			options := tc.options
			plan := buildCallsSearchPlan(&options, client, db, nil)

			if plan.probeWhere != tc.wantProbe {
				t.Errorf("probeWhere\n got: %s\nwant: %s", plan.probeWhere, tc.wantProbe)
			}
			if plan.where != wantWhere {
				t.Errorf("where\n got: %s\nwant: %s", plan.where, wantWhere)
			}
			if plan.pageWhere != wantPage {
				t.Errorf("pageWhere\n got: %s\nwant: %s", plan.pageWhere, wantPage)
			}
			// Compared rendered: what matters is that each placeholder is
			// backed by the right value in the right position.
			if got, want := fmt.Sprintf("%v", plan.whereArgs), fmt.Sprintf("%v", tc.wantWhereArgs); got != want {
				t.Errorf("whereArgs\n got: %s\nwant: %s", got, want)
			}
			if got, want := fmt.Sprintf("%v", plan.pageArgs), fmt.Sprintf("%v", wantPageArgs); got != want {
				t.Errorf("pageArgs\n got: %s\nwant: %s", got, want)
			}
			if len(plan.pageArgs) != strings.Count(plan.pageWhere, "?") {
				t.Errorf("%d args for %d placeholders in %s", len(plan.pageArgs), strings.Count(plan.pageWhere, "?"), plan.pageWhere)
			}
			if plan.order != tc.wantOrder {
				t.Errorf("order = %s, want %s", plan.order, tc.wantOrder)
			}
			if plan.limit != tc.wantLimit {
				t.Errorf("limit = %d, want %d", plan.limit, tc.wantLimit)
			}
			if plan.offset != tc.wantOffset {
				t.Errorf("offset = %d, want %d", plan.offset, tc.wantOffset)
			}
			if plan.withCount != tc.wantCount {
				t.Errorf("withCount = %v, want %v", plan.withCount, tc.wantCount)
			}
		})
	}
}

// TestCallsSearchOptionsFromMap pins the JSON a client may send. The new fields
// are parsed off the same map the old ones are, so a request carrying both
// shapes — which is every request from a client mid-upgrade — is read whole.
func TestCallsSearchOptionsFromMap(t *testing.T) {
	options := CallsSearchOptions{}
	if err := options.fromMap(map[string]any{
		"date":       "2024-03-05T10:30:00Z",
		"dateStart":  "2024-03-05T00:00:00Z",
		"dateStop":   "2024-03-06T23:59:59Z",
		"group":      "Fire",
		"groups":     []any{"Fire", "Ems"},
		"tag":        "Ops",
		"tags":       []any{"Ops", "Dispatch"},
		"system":     float64(5),
		"systems":    []any{float64(1), float64(2)},
		"talkgroup":  float64(100),
		"talkgroups": []any{map[string]any{"system": float64(1), "talkgroup": float64(100)}},
		"after":      map[string]any{"dateTime": "2024-03-05T12:00:00Z", "id": float64(42)},
		"cursor":     true,
		"limit":      float64(50),
		"offset":     float64(10),
		"sort":       float64(-1),
		"q":          "  fire  ",
	}); err != nil {
		t.Fatal(err)
	}

	if v, ok := options.Date.(time.Time); !ok || !v.Equal(time.Date(2024, 3, 5, 10, 30, 0, 0, time.UTC)) {
		t.Errorf("date = %v", options.Date)
	}
	if v, ok := options.DateStart.(time.Time); !ok || !v.Equal(time.Date(2024, 3, 5, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("dateStart = %v", options.DateStart)
	}
	if v, ok := options.DateStop.(time.Time); !ok || !v.Equal(time.Date(2024, 3, 6, 23, 59, 59, 0, time.UTC)) {
		t.Errorf("dateStop = %v", options.DateStop)
	}
	if options.Group != "Fire" || options.Tag != "Ops" {
		t.Errorf("group/tag = %v/%v", options.Group, options.Tag)
	}
	if v, ok := options.Groups.([]string); !ok || len(v) != 2 || v[0] != "Fire" || v[1] != "Ems" {
		t.Errorf("groups = %v", options.Groups)
	}
	if v, ok := options.Tags.([]string); !ok || len(v) != 2 || v[0] != "Ops" || v[1] != "Dispatch" {
		t.Errorf("tags = %v", options.Tags)
	}
	if options.System != uint(5) || options.Talkgroup != uint(100) {
		t.Errorf("system/talkgroup = %v/%v", options.System, options.Talkgroup)
	}
	if v, ok := options.Systems.([]uint); !ok || len(v) != 2 || v[0] != 1 || v[1] != 2 {
		t.Errorf("systems = %v", options.Systems)
	}
	if v, ok := options.Talkgroups.([]CallsSearchTalkgroup); !ok || len(v) != 1 || v[0] != (CallsSearchTalkgroup{System: 1, Talkgroup: 100}) {
		t.Errorf("talkgroups = %v", options.Talkgroups)
	}
	if v, ok := options.After.(*CallsSearchCursor); !ok || v.Id != 42 || !v.DateTime.Equal(time.Date(2024, 3, 5, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("after = %v", options.After)
	}
	if options.Cursor != true {
		t.Errorf("cursor = %v", options.Cursor)
	}
	if options.Limit != uint(50) || options.Offset != uint(10) || options.Sort != float64(-1) || options.Q != "fire" {
		t.Errorf("limit/offset/sort/q = %v/%v/%v/%v", options.Limit, options.Offset, options.Sort, options.Q)
	}
}

// A half cursor is no cursor. Paging from a position that does not identify a
// row is exactly how a walk skips or repeats, so a malformed `after` is
// dropped rather than half-applied.
func TestCallsSearchOptionsRejectPartialCursor(t *testing.T) {
	for _, after := range []map[string]any{
		{"dateTime": "2024-03-05T12:00:00Z"},
		{"id": float64(42)},
		{"dateTime": "not a date", "id": float64(42)},
		{"dateTime": "2024-03-05T12:00:00Z", "id": "42"},
		{"dateTime": "2024-03-05T12:00:00Z", "id": float64(-1)},
	} {
		options := CallsSearchOptions{}
		options.fromMap(map[string]any{"after": after})
		if options.After != nil {
			t.Errorf("after %v was accepted as %v", after, options.After)
		}
	}
}

// The absent case: a request in the old shape must leave every new field unset,
// so nothing new reaches the where clause for a client that never learned about
// them.
func TestCallsSearchOptionsOldShapeLeavesNewFieldsUnset(t *testing.T) {
	options := CallsSearchOptions{}
	if err := options.fromMap(map[string]any{
		"date":      "2024-03-05T10:30:00Z",
		"group":     "Fire",
		"tag":       "Ops",
		"system":    float64(5),
		"talkgroup": float64(100),
		"limit":     float64(200),
		"offset":    float64(0),
		"sort":      float64(1),
		"q":         "fire",
	}); err != nil {
		t.Fatal(err)
	}

	if options.After != nil || options.Cursor != nil || options.DateStart != nil || options.DateStop != nil ||
		options.Groups != nil || options.Tags != nil || options.Systems != nil || options.Talkgroups != nil {
		t.Errorf("an old-shape request set a new field: %+v", options)
	}
}

// insertSearchTestCall writes one call at a given time on a given talkgroup and
// returns the id the backend assigned, which is what a cursor is built from.
func insertSearchTestCall(t *testing.T, db *Database, when time.Time, system uint, talkgroup uint) uint {
	t.Helper()

	calls := NewCalls()
	call := &Call{
		Audio:       []byte{0},
		AudioName:   "test.m4a",
		AudioType:   "audio/mp4",
		DateTime:    when,
		Frequencies: []map[string]any{},
		Frequency:   154000000,
		Patches:     []uint{},
		Sources:     []map[string]any{},
		System:      system,
		Talkgroup:   talkgroup,
	}

	id, err := calls.WriteCall(call, db)
	if err != nil {
		t.Fatal(err)
	}

	return id
}

// TestCallsSearchCursorWalk is the reason the order clause carries id.
//
// Several of these calls share a dateTime to the precision the backend stores,
// which is not a corner case: a busy system produces them constantly, and a
// patch fans one transmission out over several talkgroups at the same instant.
// Ordered by dateTime alone, the backend is free to return those rows in a
// different order on the next page, and a cursor walk then skips some and
// repeats others. Walked in both directions, every row must appear exactly once.
func TestCallsSearchCursorWalk(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	base := time.Date(2024, 3, 5, 12, 0, 0, 0, time.UTC)

	// Three timestamps, three calls each: every page boundary in the walk below
	// lands inside a group of identical dateTimes.
	inserted := []uint{}
	for group := 0; group < 3; group++ {
		for n := 0; n < 3; n++ {
			inserted = append(inserted, insertSearchTestCall(t, db, base.Add(time.Duration(group)*time.Minute), 1, uint(100+n)))
		}
	}

	client := searchTestClient(db)
	calls := NewCalls()

	for _, direction := range []struct {
		name string
		sort float64
	}{{"ascending", 1}, {"descending", -1}} {
		t.Run(direction.name, func(t *testing.T) {
			var (
				after *CallsSearchCursor
				seen  []uint
			)

			// Two per page against nine rows: four boundaries, all of them
			// inside a tie.
			for page := 0; page < len(inserted)+2; page++ {
				options := &CallsSearchOptions{Limit: uint(2), Sort: direction.sort, Cursor: true}
				if after != nil {
					options.After = after
				}

				results, err := calls.Search(options, client)
				if err != nil {
					t.Fatal(err)
				}

				// Cursor mode must not have paid for a count(*).
				if results.Count != 0 {
					t.Errorf("page %d reported count %d — the count query is still running in cursor mode", page, results.Count)
				}

				// The date bounds still come back: they feed the date picker,
				// and dropping them with the count would blind it.
				if results.DateStart.IsZero() || results.DateStop.IsZero() {
					t.Errorf("page %d lost the date bounds: %v..%v", page, results.DateStart, results.DateStop)
				}

				if len(results.Results) == 0 {
					break
				}

				for _, result := range results.Results {
					seen = append(seen, result.Id)
				}

				last := results.Results[len(results.Results)-1]
				after = &CallsSearchCursor{DateTime: last.DateTime, Id: last.Id}
			}

			if len(seen) != len(inserted) {
				t.Fatalf("walked %d rows, want %d: %v", len(seen), len(inserted), seen)
			}

			counted := map[uint]int{}
			for _, id := range seen {
				counted[id]++
			}
			for _, id := range inserted {
				if counted[id] != 1 {
					t.Errorf("call %d returned %d times over the walk", id, counted[id])
				}
			}

			// The walk must also be monotonic in the sort direction, or the
			// page predicate is agreeing with an order the backend isn't using.
			for i := 1; i < len(seen); i++ {
				if direction.sort > 0 && seen[i] <= seen[i-1] {
					t.Errorf("ascending walk went backwards at %d: %v", i, seen)
					break
				}
				if direction.sort < 0 && seen[i] >= seen[i-1] {
					t.Errorf("descending walk went forwards at %d: %v", i, seen)
					break
				}
			}
		})
	}
}

// Offset paging keeps its count, because the clients that draw numbered pages
// still need it. This is the half of the count change that must not regress.
func TestCallsSearchOffsetStillCounts(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	base := time.Date(2024, 3, 5, 12, 0, 0, 0, time.UTC)
	for n := 0; n < 5; n++ {
		insertSearchTestCall(t, db, base.Add(time.Duration(n)*time.Minute), 1, 100)
	}

	client := searchTestClient(db)
	calls := NewCalls()

	results, err := calls.Search(&CallsSearchOptions{Limit: uint(2), Offset: uint(2)}, client)
	if err != nil {
		t.Fatal(err)
	}

	if results.Count != 5 {
		t.Errorf("count = %d, want 5", results.Count)
	}
	if len(results.Results) != 2 {
		t.Errorf("returned %d rows, want 2", len(results.Results))
	}
}

// The count cache key had to grow the bound args, which is exactly the kind of
// change that silently orphans the entry WarmSearchMeta writes at startup: the
// warm-up still runs, the first search still answers, and the scan it exists to
// avoid is paid on every cold search forever. Proven by warming, then adding a
// row behind the cache's back — the count must still read as the warmed one.
func TestCallsSearchUsesWarmedCount(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	base := time.Date(2024, 3, 5, 12, 0, 0, 0, time.UTC)
	for n := 0; n < 3; n++ {
		insertSearchTestCall(t, db, base.Add(time.Duration(n)*time.Minute), 1, 100)
	}

	calls := NewCalls()
	calls.WarmSearchMeta(db)

	// Straight to SQL: WriteCall would invalidate the cache, which is the whole
	// thing being observed here.
	if _, err := db.Exec(
		"insert into `rdioScannerCalls` (`dateTime`, `system`, `talkgroup`, `source`, `audio`,"+
			" `audioName`, `audioType`, `frequencies`, `frequency`, `patches`, `sources`)"+
			" values (?, 1, 100, 1, ?, 'test.m4a', 'audio/mp4', '[]', 154000000, '[]', '[]')",
		base.Add(time.Hour), []byte{0},
	); err != nil {
		t.Fatal(err)
	}

	results, err := calls.Search(&CallsSearchOptions{}, searchTestClient(db))
	if err != nil {
		t.Fatal(err)
	}

	if results.Count != 3 {
		t.Errorf("count = %d, want the warmed 3 — the warmed entry is keyed differently from what Search looks up", results.Count)
	}
}

// The filters have to hold against real rows, not just render. One row is
// deliberately on the same talkgroup number as another system's, which is the
// mistake a bare talkgroup list would make.
func TestCallsSearchFiltersAgainstRows(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	base := time.Date(2024, 3, 5, 12, 0, 0, 0, time.UTC)

	wanted := insertSearchTestCall(t, db, base, 1, 100)
	sameTalkgroupOtherSystem := insertSearchTestCall(t, db, base.Add(time.Minute), 2, 100)
	otherSystem := insertSearchTestCall(t, db, base.Add(2*time.Minute), 2, 200)
	outOfWindow := insertSearchTestCall(t, db, base.Add(48*time.Hour), 1, 100)

	client := searchTestClient(db)
	calls := NewCalls()

	ids := func(results *CallsSearchResults) []uint {
		out := []uint{}
		for _, result := range results.Results {
			out = append(out, result.Id)
		}
		return out
	}

	equal := func(got []uint, want ...uint) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}

	t.Run("talkgroup pairs are system-scoped", func(t *testing.T) {
		results, err := calls.Search(&CallsSearchOptions{
			Talkgroups: []CallsSearchTalkgroup{{System: 1, Talkgroup: 100}},
			DateStart:  base,
			DateStop:   base.Add(time.Hour),
		}, client)
		if err != nil {
			t.Fatal(err)
		}
		if got := ids(results); !equal(got, wanted) {
			t.Errorf("got %v, want [%d] — %d is talkgroup 100 on another system", got, wanted, sameTalkgroupOtherSystem)
		}
	})

	t.Run("systems", func(t *testing.T) {
		results, err := calls.Search(&CallsSearchOptions{Systems: []uint{2}}, client)
		if err != nil {
			t.Fatal(err)
		}
		if got := ids(results); !equal(got, sameTalkgroupOtherSystem, otherSystem) {
			t.Errorf("got %v, want [%d %d]", got, sameTalkgroupOtherSystem, otherSystem)
		}
	})

	t.Run("date window is inclusive at both ends", func(t *testing.T) {
		results, err := calls.Search(&CallsSearchOptions{
			DateStart: base,
			DateStop:  base.Add(2 * time.Minute),
		}, client)
		if err != nil {
			t.Fatal(err)
		}
		if got := ids(results); !equal(got, wanted, sameTalkgroupOtherSystem, otherSystem) {
			t.Errorf("got %v, want the three calls inside the window (%d is two days later)", got, outOfWindow)
		}
	})

	t.Run("date bounds still span everything the filter matches", func(t *testing.T) {
		results, err := calls.Search(&CallsSearchOptions{
			DateStart: base,
			DateStop:  base.Add(time.Minute),
		}, client)
		if err != nil {
			t.Fatal(err)
		}
		// The window picked one minute; the picker bounds must still reach the
		// call two days out, or the client cannot navigate to it.
		if !results.DateStop.After(base.Add(24 * time.Hour)) {
			t.Errorf("dateStop = %v — the picked window collapsed the date-picker bounds", results.DateStop)
		}
	})
}
