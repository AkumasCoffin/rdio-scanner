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
	"database/sql"
	"fmt"
	"math"
	"sort"
	"time"
)

const (
	listenerSampleInterval = time.Minute
	listenerBucketInterval = 10 * time.Minute
	listenerRetention      = 90 * 24 * time.Hour
)

// Listeners persists periodic samples of the connected-listener count so the
// stats charts can show history. Samples are written unconditionally — the
// ShowListenerStats option only gates who gets to *see* the aggregates, so
// history already exists the day an admin turns the toggle on.
type Listeners struct{}

func NewListeners() *Listeners {
	return &Listeners{}
}

type listenerSample struct {
	Timestamp int64
	Count     uint
}

// Sample records one listener-count reading at the current time. Zeros are
// written on purpose: a gap in the series means the server was down, a zero
// means the server was up with nobody listening — the charts render them
// differently.
//
// The insert ignores timestamp collisions rather than failing: the epoch
// second is the primary key, and a crash-loop restart in the same second,
// an NTP step backwards, or two instances sharing one database would
// otherwise turn every colliding tick into a lost sample and a false
// "server down" gap on the chart.
func (listeners *Listeners) Sample(db *Database, count int) error {
	var query string
	switch db.Config.DbType {
	case DbTypeSqlite:
		query = "insert or ignore into `rdioScannerListeners` (`timestamp`, `count`) values (?, ?)"
	case DbTypePostgres:
		query = "insert into `rdioScannerListeners` (`timestamp`, `count`) values (?, ?) on conflict (`timestamp`) do nothing"
	default:
		query = "insert ignore into `rdioScannerListeners` (`timestamp`, `count`) values (?, ?)"
	}

	if _, err := db.Exec(query, time.Now().UTC().Unix(), count); err != nil {
		return fmt.Errorf("listeners.sample: %v", err)
	}
	return nil
}

// GetSamples returns all samples at or after since, unordered.
func (listeners *Listeners) GetSamples(db *Database, since time.Time) ([]listenerSample, error) {
	samples := []listenerSample{}

	rows, err := db.Query(
		"select `timestamp`, `count` from `rdioScannerListeners` where `timestamp` >= ?",
		since.Unix(),
	)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("listeners.getSamples: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var s listenerSample
		if err := rows.Scan(&s.Timestamp, &s.Count); err == nil {
			samples = append(samples, s)
		}
	}

	return samples, nil
}

// Prune deletes samples older than listenerRetention. Unlike calls and logs
// this retention is a constant, not an option — the table grows by one small
// row a minute, so there is nothing worth tuning.
func (listeners *Listeners) Prune(db *Database) error {
	_, err := db.Exec(
		"delete from `rdioScannerListeners` where `timestamp` < ?",
		time.Now().Add(-listenerRetention).UTC().Unix(),
	)
	if err != nil {
		return fmt.Errorf("listeners.prune: %v", err)
	}
	return nil
}

// aggregateListenerSamples buckets raw samples into 10-minute-grain UTC
// averages and peaks, sorted ascending. Slots with no samples are ABSENT
// from the result — the client renders them as gaps (server down), distinct
// from a present bucket whose Avg is zero (server up, nobody listening).
func aggregateListenerSamples(samples []listenerSample) []StatsListenerBucket {
	type slotTally struct {
		sum  uint64
		n    uint64
		peak uint
	}

	bucketSec := int64(listenerBucketInterval / time.Second)

	tally := map[int64]*slotTally{}
	for _, s := range samples {
		slot := s.Timestamp - s.Timestamp%bucketSec
		t := tally[slot]
		if t == nil {
			t = &slotTally{}
			tally[slot] = t
		}
		t.sum += uint64(s.Count)
		t.n++
		if s.Count > t.peak {
			t.peak = s.Count
		}
	}

	slots := make([]int64, 0, len(tally))
	for slot := range tally {
		slots = append(slots, slot)
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i] < slots[j] })

	result := make([]StatsListenerBucket, 0, len(slots))
	for _, slot := range slots {
		t := tally[slot]
		result = append(result, StatsListenerBucket{
			StartUtc: time.Unix(slot, 0).UTC().Format(time.RFC3339),
			Avg:      math.Round(float64(t.sum)/float64(t.n)*100) / 100,
			Peak:     t.peak,
		})
	}
	return result
}
