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
	"sort"
	"strings"
	"sync"
	"time"
)

// What plugins cost, measured.
//
// Nothing timed dispatch, and the only signal an operator ever received was a
// log line when a handler errored or hard-timed-out. A plugin that reliably
// took 240ms per listener — one comfortably destroying a large site — was
// completely silent, and the symptom it produced was "uploads stopped working"
// with nothing connecting the two.
//
// Counters are cheap enough to keep unconditionally: an atomic-free mutex per
// record, taken once per dispatch, against a map that is written once per
// plugin per point and read only by the admin panel.

// pluginPointStat is one plugin's record at one extension point.
type pluginPointStat struct {
	PluginId string `json:"pluginId"`
	Point    string `json:"point"`
	Verb     string `json:"verb"`

	Calls    int64 `json:"calls"`
	Failures int64 `json:"failures"`
	Timeouts int64 `json:"timeouts"`
	Vetoes   int64 `json:"vetoes"`
	// Skipped counts handlers passed over because the call had no time left.
	// Distinct from a failure: nothing went wrong, there was simply nothing to
	// spend.
	Skipped int64 `json:"skipped"`

	TotalMs float64 `json:"totalMs"`
	MaxMs   float64 `json:"maxMs"`
	// AverageMs is what an operator actually reads. Derived on the way out
	// rather than stored, so it cannot disagree with the two it comes from.
	AverageMs float64 `json:"averageMs"`

	LastAt time.Time `json:"lastAt,omitempty"`
}

type pluginMetrics struct {
	mutex sync.RWMutex
	stats map[string]*pluginPointStat
}

func newPluginMetrics() *pluginMetrics {
	return &pluginMetrics{stats: map[string]*pluginPointStat{}}
}

// Every entry point tolerates a nil receiver. Measuring is a side concern:
// a dispatch that was constructed without it should still dispatch, and a test
// exercising the chain should not have to know this file exists.
func (metrics *pluginMetrics) record(pluginId string, point string, verb pluginVerb) *pluginPointStat {
	if metrics == nil {
		return nil
	}

	key := pluginId + "\x00" + point + "\x00" + verb.String()

	metrics.mutex.RLock()
	stat := metrics.stats[key]
	metrics.mutex.RUnlock()

	if stat != nil {
		return stat
	}

	metrics.mutex.Lock()
	defer metrics.mutex.Unlock()

	if stat = metrics.stats[key]; stat == nil {
		stat = &pluginPointStat{PluginId: pluginId, Point: point, Verb: verb.String()}
		metrics.stats[key] = stat
	}

	return stat
}

// observe records one completed dispatch.
func (metrics *pluginMetrics) observe(pluginId string, point string, verb pluginVerb, elapsed time.Duration, err error) {
	stat := metrics.record(pluginId, point, verb)
	if stat == nil {
		return
	}

	ms := float64(elapsed.Microseconds()) / 1000

	metrics.mutex.Lock()
	defer metrics.mutex.Unlock()

	stat.Calls++
	stat.TotalMs += ms
	if ms > stat.MaxMs {
		stat.MaxMs = ms
	}
	stat.LastAt = time.Now().UTC()

	if err != nil {
		stat.Failures++
		// A timeout is worth separating from a throw: one is a plugin that is
		// wrong, the other is a plugin that is too slow, and the fix differs.
		if isPluginTimeout(err) {
			stat.Timeouts++
		}
	}
}

func (metrics *pluginMetrics) observeVeto(pluginId string, point string) {
	stat := metrics.record(pluginId, point, verbFilter)
	if stat == nil {
		return
	}

	metrics.mutex.Lock()
	defer metrics.mutex.Unlock()

	stat.Vetoes++
}

func (metrics *pluginMetrics) observeSkipped(pluginId string, point string, verb pluginVerb) {
	stat := metrics.record(pluginId, point, verb)
	if stat == nil {
		return
	}

	metrics.mutex.Lock()
	defer metrics.mutex.Unlock()

	stat.Skipped++
}

// Snapshot returns a copy, ordered so the most expensive plugin is first —
// which is the question anyone opening this page is asking.
func (metrics *pluginMetrics) Snapshot() []pluginPointStat {
	if metrics == nil {
		return nil
	}

	metrics.mutex.RLock()
	defer metrics.mutex.RUnlock()

	out := make([]pluginPointStat, 0, len(metrics.stats))

	for _, stat := range metrics.stats {
		copied := *stat
		if copied.Calls > 0 {
			copied.AverageMs = copied.TotalMs / float64(copied.Calls)
		}
		out = append(out, copied)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].TotalMs != out[j].TotalMs {
			return out[i].TotalMs > out[j].TotalMs
		}
		return out[i].PluginId < out[j].PluginId
	})

	return out
}

// ForPlugin totals one plugin across every point it touches, for the summary
// line on its card.
func (metrics *pluginMetrics) ForPlugin(pluginId string) pluginPointStat {
	if metrics == nil {
		return pluginPointStat{PluginId: pluginId}
	}

	total := pluginPointStat{PluginId: pluginId}

	metrics.mutex.RLock()
	defer metrics.mutex.RUnlock()

	for _, stat := range metrics.stats {
		if stat.PluginId != pluginId {
			continue
		}

		total.Calls += stat.Calls
		total.Failures += stat.Failures
		total.Timeouts += stat.Timeouts
		total.Vetoes += stat.Vetoes
		total.Skipped += stat.Skipped
		total.TotalMs += stat.TotalMs

		if stat.MaxMs > total.MaxMs {
			total.MaxMs = stat.MaxMs
		}
		if stat.LastAt.After(total.LastAt) {
			total.LastAt = stat.LastAt
		}
	}

	if total.Calls > 0 {
		total.AverageMs = total.TotalMs / float64(total.Calls)
	}

	return total
}

// Forget drops a plugin's records, so an uninstalled plugin does not linger in
// the panel claiming time nothing is spending.
func (metrics *pluginMetrics) Forget(pluginId string) {
	if metrics == nil {
		return
	}

	metrics.mutex.Lock()
	defer metrics.mutex.Unlock()

	for key, stat := range metrics.stats {
		if stat.PluginId == pluginId {
			delete(metrics.stats, key)
		}
	}
}

// isPluginTimeout separates "too slow" from "wrong", because the two want
// different responses from whoever is reading the panel.
func isPluginTimeout(err error) bool {
	if err == nil {
		return false
	}

	text := err.Error()

	return strings.Contains(text, "timed out") || strings.Contains(text, "time limit")
}
