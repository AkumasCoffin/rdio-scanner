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
	"testing"
)

// Check for updates has to actually check. It passes fresh all the way down
// for this reason, but the store's own listing cache ignored the flag and kept
// answering from an entry up to ten minutes old — so pressing the button right
// after publishing a version reported "latest" against the previous one, with
// no way to tell that from the button being broken.
func TestFreshSkipsTheListingCache(t *testing.T) {
	store := NewPluginStore(nil)

	fetches := 0
	fetch := func() (any, error) {
		fetches++
		return fmt.Sprintf("listing %d", fetches), nil
	}

	if _, err := store.cached("k", false, fetch); err != nil {
		t.Fatal(err)
	}

	// Cached: no second fetch.
	if _, err := store.cached("k", false, fetch); err != nil {
		t.Fatal(err)
	}
	if fetches != 1 {
		t.Fatalf("cached read fetched %d times; want 1", fetches)
	}

	value, err := store.cached("k", true, fetch)
	if err != nil {
		t.Fatal(err)
	}

	if fetches != 2 {
		t.Errorf("fresh read fetched %d times; want 2 — the cache was not skipped", fetches)
	}

	if value != "listing 2" {
		t.Errorf("fresh read returned %q; want the refetched value", value)
	}
}

// Wanting fresh data is not a reason to answer with nothing. GitHub's
// unauthenticated limit is 60 requests an hour, so a refresh that runs into it
// must fall back to what is already known rather than blanking the list.
func TestFreshStillFallsBackToStaleWhenTheFetchFails(t *testing.T) {
	store := NewPluginStore(nil)

	if _, err := store.cached("k", false, func() (any, error) { return "listing", nil }); err != nil {
		t.Fatal(err)
	}

	value, err := store.cached("k", true, func() (any, error) {
		return nil, fmt.Errorf("403 rate limit exceeded")
	})

	if err != nil {
		t.Fatalf("failed outright: %v", err)
	}

	if value != "listing" {
		t.Errorf("returned %q; want the last known listing", value)
	}
}
