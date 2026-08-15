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
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAggregateListenerSamplesEmpty(t *testing.T) {
	buckets := aggregateListenerSamples(nil)
	if len(buckets) != 0 {
		t.Fatalf("expected no buckets, got %d", len(buckets))
	}
}

func TestAggregateListenerSamples(t *testing.T) {
	slotA := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC).Unix()
	// 09:15 falls in the 09:10 ten-minute slot, leaving 09:10's
	// predecessor absent.
	sampleC := time.Date(2026, 8, 15, 9, 15, 0, 0, time.UTC).Unix()

	// Two slots of samples with an absent slot between them is impossible
	// here (09:00 → 09:10 are adjacent), so use the gap semantics check:
	// nothing between the two produced slots may appear as a zero bucket.
	samples := []listenerSample{
		{Timestamp: slotA, Count: 0},
		{Timestamp: slotA + 60, Count: 5},
		{Timestamp: slotA + 120, Count: 4},
		{Timestamp: sampleC, Count: 2},
	}

	buckets := aggregateListenerSamples(samples)

	if len(buckets) != 2 {
		t.Fatalf("expected 2 buckets, got %d", len(buckets))
	}

	if buckets[0].StartUtc != "2026-08-15T09:00:00Z" {
		t.Errorf("bucket 0 startUtc = %s", buckets[0].StartUtc)
	}
	if buckets[0].Avg != 3 {
		t.Errorf("bucket 0 avg = %v, expected 3", buckets[0].Avg)
	}
	if buckets[0].Peak != 5 {
		t.Errorf("bucket 0 peak = %v, expected 5", buckets[0].Peak)
	}

	if buckets[1].StartUtc != "2026-08-15T09:10:00Z" {
		t.Errorf("bucket 1 startUtc = %s", buckets[1].StartUtc)
	}
	if buckets[1].Avg != 2 {
		t.Errorf("bucket 1 avg = %v, expected 2", buckets[1].Avg)
	}
	if buckets[1].Peak != 2 {
		t.Errorf("bucket 1 peak = %v, expected 2", buckets[1].Peak)
	}
}

func TestAggregateListenerSamplesGap(t *testing.T) {
	slotA := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC).Unix()
	slotC := time.Date(2026, 8, 15, 9, 40, 0, 0, time.UTC).Unix()

	// Samples 40 minutes apart: the slots between them must stay absent,
	// not appear as zero buckets.
	buckets := aggregateListenerSamples([]listenerSample{
		{Timestamp: slotA, Count: 1},
		{Timestamp: slotC, Count: 3},
	})

	if len(buckets) != 2 {
		t.Fatalf("expected 2 buckets, got %d", len(buckets))
	}
	if buckets[0].StartUtc != "2026-08-15T09:00:00Z" || buckets[1].StartUtc != "2026-08-15T09:40:00Z" {
		t.Errorf("unexpected slots: %s, %s", buckets[0].StartUtc, buckets[1].StartUtc)
	}
}

func TestAggregateListenerSamplesRounding(t *testing.T) {
	slot := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC).Unix()

	buckets := aggregateListenerSamples([]listenerSample{
		{Timestamp: slot, Count: 1},
		{Timestamp: slot + 60, Count: 1},
		{Timestamp: slot + 120, Count: 2},
	})

	if len(buckets) != 1 {
		t.Fatalf("expected 1 bucket, got %d", len(buckets))
	}
	// 4/3 rounded to two decimals.
	if buckets[0].Avg != 1.33 {
		t.Errorf("avg = %v, expected 1.33", buckets[0].Avg)
	}
}

// TestStatsHandlerListenerGating verifies that the public shaping strips
// listenerBuckets from the JSON without mutating the shared cached response —
// stripping in place would hide the buckets from admins too until the next
// cache rebuild.
func TestStatsHandlerListenerGating(t *testing.T) {
	stats := &Stats{
		Controller: &Controller{},
	}

	cached := &StatsResponse{
		ListenerBuckets: []StatsListenerBucket{
			{StartUtc: "2026-08-15T09:00:00Z", Avg: 3, Peak: 5},
		},
	}
	stats.cached = cached
	stats.cachedAt = time.Now()

	get := func(includeListeners bool) string {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/api/stats", nil)
		stats.handleStatsRequest(w, r, includeListeners)
		return w.Body.String()
	}

	public := get(false)
	if strings.Contains(public, "listenerBuckets") {
		t.Errorf("gated response still contains listenerBuckets: %s", public)
	}

	if cached.ListenerBuckets == nil {
		t.Fatal("gating mutated the shared cached response")
	}

	admin := get(true)
	var resp StatsResponse
	if err := json.Unmarshal([]byte(admin), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.ListenerBuckets) != 1 || resp.ListenerBuckets[0].Peak != 5 {
		t.Errorf("ungated response lost listenerBuckets: %s", admin)
	}
}

// TestListenersTableSqlBackends is a smoke check that every backend declares
// the table; column parity across backends is enforced by the generic
// migration parity test, which picks listenersTableSql up by name.
func TestListenersTableSqlBackends(t *testing.T) {
	for _, dbType := range []string{DbTypeSqlite, DbTypePostgres, DbTypeMysql, DbTypeMariadb} {
		queries := listenersTableSql(dbType)
		if len(queries) != 1 {
			t.Errorf("%s: expected 1 statement, got %d", dbType, len(queries))
			continue
		}
		if !strings.Contains(queries[0], "rdioScannerListeners") {
			t.Errorf("%s: unexpected statement %s", dbType, queries[0])
		}
		if !strings.Contains(queries[0], "if not exists") {
			t.Errorf("%s: statement is not re-runnable: %s", dbType, queries[0])
		}
	}
}
