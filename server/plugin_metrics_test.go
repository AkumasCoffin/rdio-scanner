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
	"errors"
	"testing"
	"time"
)

// A plugin that is merely slow — never failing, never timing out — was
// completely invisible, and it is the one that takes a large site down. What an
// operator saw instead was uploads backing up, with nothing connecting the two.
func TestMetricsRecordWhatAPluginCosts(t *testing.T) {
	metrics := newPluginMetrics()

	metrics.observe("slowplugin", PointCallEmit, verbFilter, 40*time.Millisecond, nil)
	metrics.observe("slowplugin", PointCallEmit, verbFilter, 60*time.Millisecond, nil)

	total := metrics.ForPlugin("slowplugin")

	if total.Calls != 2 {
		t.Fatalf("recorded %d calls, expected 2", total.Calls)
	}
	if total.TotalMs != 100 {
		t.Fatalf("total was %v ms, expected 100", total.TotalMs)
	}
	if total.AverageMs != 50 {
		t.Fatalf("average was %v ms, expected 50", total.AverageMs)
	}
	// The longest single run is the number that says whether a plugin belongs
	// on a hot path at all; an average hides it.
	if total.MaxMs != 60 {
		t.Fatalf("longest run was %v ms, expected 60", total.MaxMs)
	}
	if total.LastAt.IsZero() {
		t.Error("nothing recorded when the plugin last ran")
	}
}

// Failing, timing out, and being skipped are three different things and want
// three different responses. Collapsing them would leave an operator unable to
// tell a plugin that is wrong from one that is slow.
func TestMetricsSeparateFailureFromSlownessFromSkipping(t *testing.T) {
	metrics := newPluginMetrics()

	metrics.observe("p", PointCallStore, verbFilter, time.Millisecond, errors.New("TypeError: x is not a function"))
	metrics.observe("p", PointCallStore, verbFilter, time.Second, errors.New("plugin p timed out at call.store"))
	metrics.observeSkipped("p", PointCallStore, verbFilter)
	metrics.observeVeto("p", PointCallStore)

	total := metrics.ForPlugin("p")

	if total.Failures != 2 {
		t.Errorf("failures %d, expected 2 — a timeout is also a failure", total.Failures)
	}
	if total.Timeouts != 1 {
		t.Errorf("timeouts %d, expected 1 — only one of them was too slow", total.Timeouts)
	}
	if total.Skipped != 1 {
		t.Errorf("skipped %d, expected 1", total.Skipped)
	}
	if total.Vetoes != 1 {
		t.Errorf("vetoes %d, expected 1", total.Vetoes)
	}
	// A skip is not a call: nothing ran.
	if total.Calls != 2 {
		t.Errorf("calls %d, expected 2 — a skipped handler never ran", total.Calls)
	}
}

// The snapshot answers "what is costing me the most", so it has to be ordered
// by that and split by point — "which plugin" and "doing what" are different
// questions.
func TestMetricsSnapshotOrdering(t *testing.T) {
	metrics := newPluginMetrics()

	metrics.observe("cheap", PointCallStored, verbOn, time.Millisecond, nil)
	metrics.observe("dear", PointCallEmit, verbFilter, 500*time.Millisecond, nil)
	metrics.observe("dear", PointCallStore, verbFilter, 10*time.Millisecond, nil)

	snapshot := metrics.Snapshot()

	if len(snapshot) != 3 {
		t.Fatalf("%d records, expected one per plugin per point per verb", len(snapshot))
	}
	if snapshot[0].PluginId != "dear" || snapshot[0].Point != PointCallEmit {
		t.Fatalf("most expensive first failed: %v at %v", snapshot[0].PluginId, snapshot[0].Point)
	}
	if snapshot[0].Verb != "filter" {
		t.Errorf("the verb was not recorded: %q", snapshot[0].Verb)
	}
}

// An uninstalled plugin must not linger in the panel claiming time nothing is
// spending.
func TestMetricsForgetAPlugin(t *testing.T) {
	metrics := newPluginMetrics()

	metrics.observe("gone", PointCallStore, verbFilter, time.Millisecond, nil)
	metrics.observe("staying", PointCallStore, verbFilter, time.Millisecond, nil)

	metrics.Forget("gone")

	if total := metrics.ForPlugin("gone"); total.Calls != 0 {
		t.Errorf("an uninstalled plugin still reports %d calls", total.Calls)
	}
	if total := metrics.ForPlugin("staying"); total.Calls != 1 {
		t.Errorf("forgetting one plugin dropped another's records")
	}
}

// Measuring is a side concern. A dispatch built without it should still
// dispatch, and a test exercising the chain should not have to know this file
// exists.
func TestMetricsToleratesBeingAbsent(t *testing.T) {
	var metrics *pluginMetrics

	metrics.observe("p", PointCallStore, verbFilter, time.Millisecond, nil)
	metrics.observeVeto("p", PointCallStore)
	metrics.observeSkipped("p", PointCallStore, verbFilter)
	metrics.Forget("p")

	if snapshot := metrics.Snapshot(); snapshot != nil {
		t.Error("a nil metrics produced a snapshot")
	}
	if total := metrics.ForPlugin("p"); total.Calls != 0 {
		t.Error("a nil metrics produced counts")
	}
}
