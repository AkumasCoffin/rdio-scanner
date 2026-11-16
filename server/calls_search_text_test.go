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

// The search box takes whatever a listener types, and a listener is anyone who
// can open the page. Each term here is text SQL gives a meaning to, and each
// has to come back as what it is: characters to look for.
//
// It runs the real search rather than inspecting the generated SQL, against
// whichever backend the suite is pointed at, because the three disagree about
// which of these are dangerous. A backslash before a quote ends the literal on
// MySQL and MariaDB and nowhere else; a question mark is renumbered into a
// placeholder on Postgres and nowhere else. A check on the text would have to
// know all of that, and the next surprise would not be in it.
func TestSearchTextIsMatchedNotExecuted(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	extensions := transcriptExtensionFor(t, db)
	client := searchTestClient(db)
	calls := NewCalls()

	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	transcripts := []string{
		"engine 4 on scene",
		"is that ok? confirm",
		`path C:\temp\x noted`,
		"battery at 100% now",
		"unit_7 responding",
		"o'brien street",
		`he said \' and left`,
		"unit 7 responding",
		"battery at 1000 now",
	}

	ids := make([]uint, len(transcripts))
	for i, text := range transcripts {
		ids[i] = insertSearchTestCall(t, db, base.Add(time.Duration(i)*time.Minute), 1, 100)
		if _, err := db.Exec("insert into `plugin_transcripts_calls` (`callId`, `transcript`) values (?, ?)", ids[i], text); err != nil {
			t.Fatalf("cannot store transcript %d: %v", i, err)
		}
	}

	search := func(t *testing.T, q string, options CallsSearchOptions) []uint {
		t.Helper()

		options.Q = q
		options.Sort = 1

		results, err := calls.searchWithExtensions(&options, client, extensions)
		if err != nil {
			t.Fatalf("searching for %q failed: %v", q, err)
		}

		out := []uint{}
		for _, result := range results.Results {
			out = append(out, result.Id)
		}

		return out
	}

	for _, tc := range []struct {
		name string
		q    string
		want []int
	}{
		{"plain text", "engine", []int{0}},
		{"a question mark", "ok?", []int{1}},
		{"a backslash", `C:\temp`, []int{2}},
		{"a percent sign is not a wildcard", "100%", []int{3}},
		{"an underscore is not a wildcard", "unit_7", []int{4}},
		{"an apostrophe", "o'brien", []int{5}},
		{"a backslash before a quote", `\'`, []int{6}},
		{"a quote that would close the literal", `\') or 1=1) #`, []int{}},
		{"a quote and a comment", "' or 1=1 --", []int{}},
		{"only a wildcard", "%", []int{3}},
	} {
		want := []uint{}
		for _, i := range tc.want {
			want = append(want, ids[i])
		}

		// Both shapes of the request: the cursor form takes the bounded page
		// query and the offset form takes the flat one plus the count, and
		// the text is spliced into each separately.
		for _, shape := range []struct {
			name    string
			options CallsSearchOptions
		}{
			{"cursor", CallsSearchOptions{Cursor: true}},
			{"offset", CallsSearchOptions{}},
			{"cursor with a date window", CallsSearchOptions{Cursor: true, DateStart: base.Add(-time.Hour), DateStop: base.Add(time.Hour)}},
		} {
			t.Run(tc.name+", "+shape.name, func(t *testing.T) {
				got := search(t, tc.q, shape.options)

				if len(got) != len(want) {
					t.Fatalf("searching for %q returned calls %v, want %v", tc.q, got, want)
				}
				for i := range got {
					if got[i] != want[i] {
						t.Fatalf("searching for %q returned calls %v, want %v", tc.q, got, want)
					}
				}
			})
		}
	}

	t.Run("the count agrees with the page", func(t *testing.T) {
		options := CallsSearchOptions{Q: "ok?", Sort: 1}

		results, err := calls.searchWithExtensions(&options, client, extensions)
		if err != nil {
			t.Fatal(err)
		}
		if results.Count != 1 {
			t.Fatalf("count = %d for a search with one match", results.Count)
		}
	})

	t.Run("two terms do not share a cached count", func(t *testing.T) {
		// Once the term is bound, the where text for every search is the same
		// string. The count cache is keyed on that text plus its arguments, so
		// the term has to be among them or the second search is answered with
		// the first one's number.
		first := CallsSearchOptions{Q: "responding", Sort: 1}
		second := CallsSearchOptions{Q: "engine", Sort: 1}

		a, err := calls.searchWithExtensions(&first, client, extensions)
		if err != nil {
			t.Fatal(err)
		}
		b, err := calls.searchWithExtensions(&second, client, extensions)
		if err != nil {
			t.Fatal(err)
		}

		if a.Count != 2 || b.Count != 1 {
			t.Fatalf("counts = %d and %d, want 2 and 1", a.Count, b.Count)
		}
	})

	t.Run("the term never reaches the statement text", func(t *testing.T) {
		options := CallsSearchOptions{Q: "zzqxj", Cursor: true}
		plan := buildCallsSearchPlan(&options, client, db, extensions)

		if strings.Contains(plan.where, "zzqxj") || strings.Contains(plan.pageWhere, "zzqxj") || strings.Contains(plan.pluginWhere, "zzqxj") {
			t.Fatalf("the search term is part of the SQL text: %s", plan.pageWhere)
		}
	})
}
