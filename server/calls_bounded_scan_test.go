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
	"strings"
	"testing"
	"time"
)

// A hand-built extension, shaped the way the transcripts plugin registers
// itself. Nothing exercised a real plugin predicate in these tests before —
// buildCallsSearchPlan was handed nil throughout.
func transcriptExtensionFor(t *testing.T, db *Database) []pluginResolvedSearch {
	t.Helper()

	if _, err := db.Exec("create table if not exists `plugin_transcripts_calls` (`callId` integer not null, `transcript` text, primary key (`callId`))"); err != nil {
		t.Fatalf("cannot create the plugin table: %v", err)
	}

	return []pluginResolvedSearch{{
		table:       "plugin_transcripts_calls",
		key:         "callId",
		text:        "transcript",
		resultField: "transcript",
	}}
}

// The plan keeps the expensive predicates apart from the cheap ones, because
// the bounded page form needs to put the scan budget between them.
func TestSearchPlanSplitsPluginPredicates(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	extensions := transcriptExtensionFor(t, db)
	client := searchTestClient(db)

	t.Run("presence filter lands in pluginWhere", func(t *testing.T) {
		options := CallsSearchOptions{LacksField: "transcript", Cursor: true}
		plan := buildCallsSearchPlan(&options, client, db, extensions)

		if plan.pluginWhere == "" {
			t.Fatal("a presence filter produced no pluginWhere")
		}
		if strings.Contains(plan.innerWhere, "plugin_transcripts_calls") {
			t.Fatalf("the plugin predicate leaked into innerWhere: %s", plan.innerWhere)
		}
		if !strings.Contains(plan.pageWhere, plan.pluginWhere) {
			t.Fatal("the flat pageWhere lost the plugin predicate")
		}
		if !strings.Contains(plan.where, plan.pluginWhere) {
			t.Fatal("the count where lost the plugin predicate")
		}
	})

	t.Run("lacks means no row at all", func(t *testing.T) {
		options := CallsSearchOptions{LacksField: "transcript"}
		plan := buildCallsSearchPlan(&options, client, db, extensions)

		if strings.Contains(plan.pluginWhere, "<>") || strings.Contains(plan.pluginWhere, "is not null") {
			t.Fatalf("lacks still tests the text, so an empty marker row would count as missing: %s", plan.pluginWhere)
		}
		if !strings.HasPrefix(plan.pluginWhere, "not (") {
			t.Fatalf("lacks is not a negation: %s", plan.pluginWhere)
		}
	})

	t.Run("has still requires non-empty text", func(t *testing.T) {
		options := CallsSearchOptions{HasField: "transcript"}
		plan := buildCallsSearchPlan(&options, client, db, extensions)

		if !strings.Contains(plan.pluginWhere, "<> ''") {
			t.Fatalf("has stopped testing for text, so an empty marker row would count as present: %s", plan.pluginWhere)
		}
	})

	t.Run("a filter over nothing stays cheap", func(t *testing.T) {
		// No extensions: the presence filter collapses to a constant, which
		// must not trigger the bounded walk — that would page through the
		// archive budget by budget to discover nothing, forever.
		options := CallsSearchOptions{HasField: "transcript", Cursor: true}
		plan := buildCallsSearchPlan(&options, client, db, nil)

		if plan.pluginWhere != "" {
			t.Fatalf("a constant predicate was marked expensive: %s", plan.pluginWhere)
		}
		if !strings.Contains(plan.where, "1 = 0") {
			t.Fatalf("the honest nothing-matches constant went missing: %s", plan.where)
		}
	})

	t.Run("free-text lands in pluginWhere", func(t *testing.T) {
		options := CallsSearchOptions{Q: "fire", Cursor: true}
		plan := buildCallsSearchPlan(&options, client, db, extensions)

		if !strings.Contains(plan.pluginWhere, "like") {
			t.Fatalf("the free-text predicate is not in pluginWhere: %q", plan.pluginWhere)
		}
	})
}

// The walk a bounded search takes when matches are rarer than the budget:
// every page is honest about being partial, the boundary cursor continues
// with no gap and no overlap, and the walk terminates.
func TestCallsSearchBoundedWalk(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	extensions := transcriptExtensionFor(t, db)

	// Shrink the window so 60 calls overflow it several times over.
	restore := callsSearchScanBudget
	callsSearchScanBudget = 25
	t.Cleanup(func() { callsSearchScanBudget = restore })

	// 60 calls a minute apart; every 10th lacks a transcript, so each
	// 25-call window holds two or three matches — never a full page of 5.
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	wanted := []uint{}
	for i := 0; i < 60; i++ {
		id := insertSearchTestCall(t, db, base.Add(time.Duration(i)*time.Minute), 1, 100)
		if i%10 == 0 {
			wanted = append(wanted, id)
			continue
		}
		if _, err := db.Exec("insert into `plugin_transcripts_calls` (`callId`, `transcript`) values (?, ?)", id, fmt.Sprintf("text %d", id)); err != nil {
			t.Fatalf("cannot store a transcript: %v", err)
		}
	}

	client := searchTestClient(db)
	calls := NewCalls()
	collected := []uint{}
	var after *CallsSearchCursor
	pages := 0
	shortPagesWithMore := 0

	for {
		pages++
		if pages > 20 {
			t.Fatal("the walk did not terminate")
		}

		options := &CallsSearchOptions{
			Sort:       float64(-1),
			Limit:      uint(5),
			Cursor:     true,
			LacksField: "transcript",
		}
		if after != nil {
			options.After = after
		}

		results, err := calls.searchWithExtensions(options, client, extensions)
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}

		for _, result := range results.Results {
			collected = append(collected, result.Id)
		}

		if results.More {
			if results.NextAfter == nil {
				t.Fatalf("page %d says more without saying where", pages)
			}
			if uint(len(results.Results)) < options.Limit.(uint) {
				shortPagesWithMore++
			}
			after = results.NextAfter
			continue
		}

		if results.NextAfter != nil {
			t.Fatalf("page %d has a boundary cursor without more", pages)
		}

		break
	}

	if pages < 2 {
		t.Fatalf("the budget never bound: %d page(s) for 60 calls under a 25-call window", pages)
	}
	if shortPagesWithMore == 0 {
		t.Fatal("no page was both short and continuable, which is the case this exists for")
	}

	// Every wanted call exactly once, newest first — no gaps, no repeats.
	if len(collected) != len(wanted) {
		t.Fatalf("collected %d calls, want %d: %v", len(collected), len(wanted), collected)
	}

	seen := map[uint]bool{}
	for _, id := range collected {
		if seen[id] {
			t.Fatalf("call %d returned twice", id)
		}
		seen[id] = true
	}
	for _, id := range wanted {
		if !seen[id] {
			t.Fatalf("call %d was skipped", id)
		}
	}

	for i := 1; i < len(collected); i++ {
		if collected[i] >= collected[i-1] {
			t.Fatalf("order broke at %d: %v", i, collected)
		}
	}
}

// A dense filter — matches everywhere — fills its page in one request and
// carries no continuation fields, exactly as the flat form did.
func TestCallsSearchBoundedDenseIsUnchanged(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	extensions := transcriptExtensionFor(t, db)

	restore := callsSearchScanBudget
	callsSearchScanBudget = 25
	t.Cleanup(func() { callsSearchScanBudget = restore })

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 30; i++ {
		id := insertSearchTestCall(t, db, base.Add(time.Duration(i)*time.Minute), 1, 100)
		if _, err := db.Exec("insert into `plugin_transcripts_calls` (`callId`, `transcript`) values (?, ?)", id, "text"); err != nil {
			t.Fatalf("cannot store a transcript: %v", err)
		}
	}

	options := &CallsSearchOptions{Sort: float64(-1), Limit: uint(5), Cursor: true, HasField: "transcript"}

	results, err := NewCalls().searchWithExtensions(options, searchTestClient(db), extensions)
	if err != nil {
		t.Fatal(err)
	}

	if len(results.Results) != 5 {
		t.Fatalf("a dense filter returned %d rows, want a full page of 5", len(results.Results))
	}
	if results.More || results.NextAfter != nil {
		t.Fatal("a full page carries continuation fields it does not need")
	}
}

// An empty marker row is in neither list: not "with" (no text) and not
// "without" (it was attempted, and the answer was that the audio held
// nothing).
func TestPresenceFiltersAgainstMarkerRows(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	extensions := transcriptExtensionFor(t, db)
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

	withText := insertSearchTestCall(t, db, base, 1, 100)
	marked := insertSearchTestCall(t, db, base.Add(time.Minute), 1, 100)
	missing := insertSearchTestCall(t, db, base.Add(2*time.Minute), 1, 100)

	for id, text := range map[uint]string{withText: "real words", marked: ""} {
		if _, err := db.Exec("insert into `plugin_transcripts_calls` (`callId`, `transcript`) values (?, ?)", id, text); err != nil {
			t.Fatalf("cannot store: %v", err)
		}
	}

	client := searchTestClient(db)
	calls := NewCalls()

	page := func(options *CallsSearchOptions) []uint {
		results, err := calls.searchWithExtensions(options, client, extensions)
		if err != nil {
			t.Fatal(err)
		}
		out := []uint{}
		for _, r := range results.Results {
			out = append(out, r.Id)
		}
		return out
	}

	has := page(&CallsSearchOptions{Sort: float64(-1), Limit: uint(10), Cursor: true, HasField: "transcript"})
	if len(has) != 1 || has[0] != withText {
		t.Fatalf("with transcript = %v, want only %d", has, withText)
	}

	lacks := page(&CallsSearchOptions{Sort: float64(-1), Limit: uint(10), Cursor: true, LacksField: "transcript"})
	if len(lacks) != 1 || lacks[0] != missing {
		t.Fatalf("without transcript = %v, want only %d — the marker call must be in neither list", lacks, missing)
	}
}
