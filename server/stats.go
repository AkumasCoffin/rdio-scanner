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
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"
)

const statsCacheTTL = 2 * time.Minute

// statsSlowBuildThreshold — past this, a build is worth a log line. It is not
// an error: the dashboard keeps serving the previous snapshot either way.
const statsSlowBuildThreshold = 3 * time.Second

// statsHourBucketRange — how far back hour-grain buckets cover. 30 days
// is the longest period any client chart needs; everything narrower
// (week, 24-hour view, today) is derived client-side by filtering the
// buckets. 30 * 24 = 720 bucket entries ≈ 28 KB JSON, ~5 KB gzipped.
const statsHourBucketRange = 30 * 24 * time.Hour

// statsFineBucketRange — how far back the 10-minute call buckets cover.
// Only the short filter ranges (≤ 48 h) render at this grain; anything
// longer uses the 30-day hourly buckets.
const statsFineBucketRange = 48 * time.Hour

// statsMicroBucket* — 5-minute grain over the last 6 hours, for the 1-hour
// filter. Tallied from the scan GetCallFineBuckets already runs, so it adds
// no query; 72 buckets is a negligible payload.
const statsMicroBucketRange = 6 * time.Hour
const statsMicroBucketInterval = 5 * time.Minute

type Stats struct {
	Controller *Controller

	mu       sync.Mutex
	cached   *StatsResponse
	cachedAt time.Time
	// building guards the background refresh so a burst of viewers triggers
	// one rebuild, not one each.
	building bool
}

// statsRangeSince maps a filter key from the dashboard to the start of its
// window. An unknown key (or "all") means no lower bound — the epoch keeps
// the query shape identical so the dateTime index is still used.
func statsRangeSince(rng string) time.Time {
	now := time.Now().UTC()

	switch rng {
	case "1h":
		return now.Add(-time.Hour)
	case "6h":
		return now.Add(-6 * time.Hour)
	case "12h":
		return now.Add(-12 * time.Hour)
	case "24h":
		return now.Add(-24 * time.Hour)
	case "48h":
		return now.Add(-48 * time.Hour)
	case "1w":
		return now.AddDate(0, 0, -7)
	case "1m":
		return now.AddDate(0, 0, -30)
	default:
		return time.Unix(0, 0).UTC()
	}
}

type StatsOverview struct {
	// Total across the whole table — no time window, TZ-independent.
	TotalCalls uint `json:"totalCalls"`
	// "Active" counts use a rolling 24-hour window from now (UTC) so
	// they're TZ-independent. Client computes its own local-day counts
	// from HourBuckets.
	ActiveSystems    uint `json:"activeSystems"`
	ActiveTalkgroups uint `json:"activeTalkgroups"`
}

// StatsHourBucket — calls in [StartUtc, StartUtc + 1h).
//
// All time-series charts on the client (Calls/Hour-of-day, Calls/Day,
// Recent 24h, Peak Hour, Today total) are derived from these by binning
// in the browser's local timezone. The server never bucketed by local
// hour or day; it just emits raw UTC-anchored counts.
type StatsHourBucket struct {
	StartUtc string `json:"startUtc"`
	Count    uint   `json:"count"`
}

type StatsTopTalkgroup struct {
	SystemId       uint   `json:"systemId"`
	SystemLabel    string `json:"systemLabel"`
	TalkgroupId    uint   `json:"talkgroupId"`
	TalkgroupLabel string `json:"talkgroupLabel"`
	TalkgroupName  string `json:"talkgroupName"`
	Count          uint   `json:"count"`
}

type StatsTopSystem struct {
	SystemId    uint   `json:"systemId"`
	SystemLabel string `json:"systemLabel"`
	Count       uint   `json:"count"`
}

// StatsTopCategory — one slice of the "Top ..." ranking chart. Which lens
// the ranking uses (system, group, or tag) follows the SortByGroups /
// SortByTags options, mirroring how the main UI organizes talkgroups.
type StatsTopCategory struct {
	Label string `json:"label"`
	Count uint   `json:"count"`
}

type StatsTopUnit struct {
	SystemId    uint   `json:"systemId"`
	SystemLabel string `json:"systemLabel"`
	UnitId      uint   `json:"unitId"`
	UnitLabel   string `json:"unitLabel"`
	Count       uint   `json:"count"`
}

type StatsLastHourTalkgroup struct {
	SystemId       uint   `json:"systemId"`
	SystemLabel    string `json:"systemLabel"`
	TalkgroupId    uint   `json:"talkgroupId"`
	TalkgroupLabel string `json:"talkgroupLabel"`
	TalkgroupName  string `json:"talkgroupName"`
	Count          uint   `json:"count"`
	LastCall       string `json:"lastCall"`
}

type StatsTalkgroupUnit struct {
	UnitId    uint   `json:"unitId"`
	UnitLabel string `json:"unitLabel"`
	Count     uint   `json:"count"`
	LastCall  string `json:"lastCall"`
}

// StatsListenerBucket — listener samples aggregated over [StartUtc,
// StartUtc + 10m). Unlike StatsHourBucket the series is NOT pre-seeded with
// zeros: an absent slot means no samples were taken (server down), a present
// bucket with Avg == 0 means the server was up with nobody listening. The
// client builds the dense axis and renders absent slots as gaps.
type StatsListenerBucket struct {
	StartUtc string  `json:"startUtc"`
	Avg      float64 `json:"avg"`
	Peak     uint    `json:"peak"`
}

type StatsResponse struct {
	Overview           StatsOverview            `json:"overview"`
	HourBuckets        []StatsHourBucket        `json:"hourBuckets"`
	// CallFineBuckets — dense 10-minute call counts for the last 48 hours,
	// for the short filter ranges. Zeros are pre-seeded like HourBuckets: a
	// zero genuinely means no calls.
	CallFineBuckets    []StatsHourBucket        `json:"callFineBuckets,omitempty"`
	// CallMicroBuckets — dense 5-minute counts for the last 6 hours, for the
	// by-time chart on the 1-hour range.
	CallMicroBuckets   []StatsHourBucket        `json:"callMicroBuckets,omitempty"`
	TopTalkgroups      []StatsTopTalkgroup      `json:"topTalkgroups"`
	TopSystems         []StatsTopSystem         `json:"topSystems"`
	// TopCategories is what the "Top ..." chart renders: by group when
	// SortByGroups is on, by tag when SortByTags is on, else by system.
	// TopSystems stays for compatibility.
	TopCategories     []StatsTopCategory `json:"topCategories,omitempty"`
	TopCategoriesKind string             `json:"topCategoriesKind,omitempty"`
	TopUnits           []StatsTopUnit           `json:"topUnits"`
	LastHourTalkgroups []StatsLastHourTalkgroup `json:"lastHourTalkgroups"`
	// ListenerBuckets is admin-only unless Options.ShowListenerStats is on;
	// the public handler strips it from a shallow copy of the cached
	// response. omitempty also hides it on a fresh install with no samples.
	ListenerBuckets []StatsListenerBucket `json:"listenerBuckets,omitempty"`
	// Configured inventory, counted from the in-memory config rather than
	// the database — no query cost, and "configured" is a different fact
	// than the activity-based counts in Overview.
	ConfiguredSystems    uint `json:"configuredSystems"`
	ConfiguredTalkgroups uint `json:"configuredTalkgroups"`
	ConfiguredUnits      uint `json:"configuredUnits"`
}

func NewStats(controller *Controller) *Stats {
	return &Stats{
		Controller: controller,
	}
}

// GetOverview returns the TZ-independent overview counts: all-time total
// and active-systems / active-talkgroups over a rolling 24-hour window.
//
// All other overview numbers the previous version returned (today,
// week, month, avg/day, peak hour) are derived client-side from
// HourBuckets, so they bin in the viewer's local calendar and the
// wire format stays purely UTC.
func (stats *Stats) GetOverview(db *Database) (*StatsOverview, error) {
	overview := &StatsOverview{}
	df := db.DateTimeFormat

	if err := db.QueryRow("select count(*) from `rdioScannerCalls`").Scan(&overview.TotalCalls); err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("stats.overview.total: %v", err)
	}

	// 24-hour rolling window from now (UTC) for activity counts. This is
	// "active in the last 24 h", not "active today", so it's
	// TZ-independent on purpose.
	dayAgo := time.Now().UTC().AddDate(0, 0, -1)
	if err := db.QueryRow(
		"select count(distinct `system`) from `rdioScannerCalls` where `dateTime` >= ?",
		dayAgo.Format(df),
	).Scan(&overview.ActiveSystems); err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("stats.overview.activeSystems: %v", err)
	}

	if err := db.QueryRow(
		"select count(distinct `talkgroup`) from `rdioScannerCalls` where `dateTime` >= ?",
		dayAgo.Format(df),
	).Scan(&overview.ActiveTalkgroups); err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("stats.overview.activeTalkgroups: %v", err)
	}

	return overview, nil
}

// statsBucketExpr renders SQL that truncates `dateTime` down to a bucket of
// the given size in minutes, formatted as 'YYYY-MM-DD HH:MM:SS'.
//
// It subtracts (minute % size) minutes from the minute-truncated value, which
// gives the hour start when size is 60 and works for any divisor of 60. No
// epoch or timezone conversion is involved: every backend stores these
// columns already in UTC, so arithmetic on the stored value stays in UTC.
//
// Returns "" for backends where grouping in SQL is not worth it — see
// statsBucketCounts.
func statsBucketExpr(dbType string, minutes int) string {
	switch dbType {
	case DbTypePostgres:
		return fmt.Sprintf(
			`to_char(date_trunc('minute', "dateTime") - ((extract(minute from "dateTime")::int %% %d) * interval '1 minute'), 'YYYY-MM-DD HH24:MI:00')`,
			minutes,
		)

	case DbTypeMariadb, DbTypeMysql:
		return fmt.Sprintf(
			"date_format(date_sub(`dateTime`, interval (minute(`dateTime`) %%%% %d) minute), '%%Y-%%m-%%d %%H:%%i:00')",
			minutes,
		)

	default:
		// SQLite keeps these columns as text, so any date function parses a
		// string per row — measured slower than streaming the rows and
		// bucketing in Go, which is what the caller falls back to.
		return ""
	}
}

// statsBucketCounts asks the database for one row per bucket instead of one
// row per call. Returns ok == false when the caller should bucket the rows
// itself: either the backend is one where that is faster (SQLite), or the
// grouped query failed and falling back beats failing.
//
// On a server-backed database this is the difference between transferring
// every call in the window across the network and transferring a few hundred
// counts.
func statsBucketCounts(db *Database, since time.Time, minutes int) (map[time.Time]uint, bool) {
	expr := statsBucketExpr(db.Config.DbType, minutes)
	if expr == "" {
		return nil, false
	}

	rows, err := db.Query(
		"select "+expr+" as bucket, count(*) from `rdioScannerCalls` where `dateTime` >= ? group by bucket",
		since.Format(db.DateTimeFormat),
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return map[time.Time]uint{}, true
		}
		return nil, false
	}
	defer rows.Close()

	// Sanity bounds. A backend that formatted the bucket in a session
	// timezone rather than UTC would still return plausible-looking rows,
	// just shifted by hours — silently wrong data on a chart nobody can
	// cross-check. A shifted bucket lands outside the window that was asked
	// for, so bounds-checking catches it where a row count never would.
	slack := time.Duration(minutes) * time.Minute
	lower := since.Add(-slack)
	upper := time.Now().UTC().Add(slack)

	tally := map[time.Time]uint{}
	for rows.Next() {
		var bucket sql.NullString
		var count uint
		if err := rows.Scan(&bucket, &count); err != nil {
			return nil, false
		}
		if !bucket.Valid {
			continue
		}
		t, err := time.ParseInLocation("2006-01-02 15:04:05", bucket.String, time.UTC)
		if err != nil {
			return nil, false
		}
		if t.Before(lower) || t.After(upper) {
			return nil, false
		}
		tally[t] = count
	}

	if rows.Err() != nil {
		return nil, false
	}

	return tally, true
}

// GetHourBuckets returns hour-grain UTC counts for the last 30 days.
//
// 720 buckets max, each `{startUtc, count}`. The client derives every
// time-series chart (Calls per Hour-of-day, Calls per Day, Recent 24 h,
// Peak Hour, Today total) from these by binning in the browser's
// timezone. Server is intentionally TZ-blind — the wire format is
// pure UTC.
//
// Implementation: scan dateTime for the period, round each to the hour
// it falls in (UTC), tally. Pre-seeds zero counts for every hour in the
// window so the client gets a stable axis without gaps.
func (stats *Stats) GetHourBuckets(db *Database) ([]StatsHourBucket, error) {
	now := time.Now().UTC().Truncate(time.Hour)
	since := now.Add(-statsHourBucketRange).Truncate(time.Hour)

	tally, ok := statsBucketCounts(db, since, 60)
	if !ok {
		tally = map[time.Time]uint{}

		rows, err := db.Query(
			"select `dateTime` from `rdioScannerCalls` where `dateTime` >= ?",
			since.Format(db.DateTimeFormat),
		)
		if err != nil && err != sql.ErrNoRows {
			return nil, fmt.Errorf("stats.hourBuckets: %v", err)
		}
		defer rows.Close()

		for rows.Next() {
			var raw any
			if err := rows.Scan(&raw); err != nil {
				continue
			}
			t, err := db.ParseDateTime(raw)
			if err != nil {
				continue
			}
			tally[t.UTC().Truncate(time.Hour)]++
		}
	}

	hours := int(statsHourBucketRange / time.Hour)
	result := make([]StatsHourBucket, 0, hours)
	for i := 0; i < hours; i++ {
		t := since.Add(time.Duration(i) * time.Hour)
		result = append(result, StatsHourBucket{
			StartUtc: t.Format(time.RFC3339),
			Count:    tally[t],
		})
	}
	return result, nil
}

// GetCallFineBuckets returns dense 10-minute call counts for the last 48
// hours — the same shape and scan as GetHourBuckets, just a finer grain
// over a shorter window so the 1h–48h filter ranges have real curves
// instead of one or two hourly points. The seed loop runs one slot past
// the window so the current in-progress slot is included.
// It also returns a 5-minute series over the last 6 hours, tallied in the
// same pass: the 1-hour filter only covers six 10-minute slots, which is too
// coarse to show a shape. Sharing the scan means the finer series costs no
// extra query.
func (stats *Stats) GetCallFineBuckets(db *Database) ([]StatsHourBucket, []StatsHourBucket, error) {
	now := time.Now().UTC().Truncate(listenerBucketInterval)
	since := now.Add(-statsFineBucketRange)

	microSince := time.Now().UTC().Truncate(statsMicroBucketInterval).Add(-statsMicroBucketRange)

	// Two grouped queries over small windows, or one row scan — see
	// statsBucketCounts. Both series come from the same scan in the fallback,
	// so the finer one still costs nothing extra there.
	tally, ok := statsBucketCounts(db, since, int(listenerBucketInterval/time.Minute))
	microTally, microOk := map[time.Time]uint{}, false

	if ok {
		microTally, microOk = statsBucketCounts(db, microSince, int(statsMicroBucketInterval/time.Minute))
	}

	if !ok || !microOk {
		tally = map[time.Time]uint{}
		microTally = map[time.Time]uint{}

		rows, err := db.Query(
			"select `dateTime` from `rdioScannerCalls` where `dateTime` >= ?",
			since.Format(db.DateTimeFormat),
		)
		if err != nil && err != sql.ErrNoRows {
			return nil, nil, fmt.Errorf("stats.callFineBuckets: %v", err)
		}
		defer rows.Close()

		for rows.Next() {
			var raw any
			if err := rows.Scan(&raw); err != nil {
				continue
			}
			t, err := db.ParseDateTime(raw)
			if err != nil {
				continue
			}
			utc := t.UTC()
			tally[utc.Truncate(listenerBucketInterval)]++

			if !utc.Before(microSince) {
				microTally[utc.Truncate(statsMicroBucketInterval)]++
			}
		}
	}

	n := int(statsFineBucketRange / listenerBucketInterval)
	result := make([]StatsHourBucket, 0, n+1)
	for i := 0; i <= n; i++ {
		t := since.Add(time.Duration(i) * listenerBucketInterval)
		result = append(result, StatsHourBucket{
			StartUtc: t.Format(time.RFC3339),
			Count:    tally[t],
		})
	}

	micro := int(statsMicroBucketRange / statsMicroBucketInterval)
	microResult := make([]StatsHourBucket, 0, micro+1)
	for i := 0; i <= micro; i++ {
		t := microSince.Add(time.Duration(i) * statsMicroBucketInterval)
		microResult = append(microResult, StatsHourBucket{
			StartUtc: t.Format(time.RFC3339),
			Count:    microTally[t],
		})
	}

	return result, microResult, nil
}

// GetListenerBuckets returns 10-minute-grain listener averages/peaks over
// everything the table holds, aggregated in Go from the minute-grain
// samples so the SQL stays backend-agnostic. Retention is bounded by the
// PruneDays option (the prune keeps the table to that window; the client's
// "All" range renders whatever ships), so no extra time filter is applied
// here. Default 7-day retention is ~10 080 rows in, ~1 008 buckets out.
// Same UTC wire format as GetHourBuckets, but sparse — see
// StatsListenerBucket.
func (stats *Stats) GetListenerBuckets(db *Database) ([]StatsListenerBucket, error) {
	samples, err := stats.Controller.Listeners.GetSamples(db, time.Unix(0, 0))
	if err != nil {
		return nil, fmt.Errorf("stats.listenerBuckets: %v", err)
	}

	return aggregateListenerSamples(samples), nil
}

// GetTopTalkgroups: top N talkgroups by call count over the last 7 days.
func (stats *Stats) GetTopTalkgroups(db *Database, limit int) ([]StatsTopTalkgroup, error) {
	result := []StatsTopTalkgroup{}
	since := time.Now().UTC().AddDate(0, 0, -7)

	q := fmt.Sprintf(
		"select `system`, `talkgroup`, count(*) as c from `rdioScannerCalls` where `dateTime` >= ? group by `system`, `talkgroup` order by c desc limit %d",
		limit,
	)
	rows, err := db.Query(q, since.Format(db.DateTimeFormat))
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("stats.topTalkgroups: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var item StatsTopTalkgroup
		if err := rows.Scan(&item.SystemId, &item.TalkgroupId, &item.Count); err != nil {
			continue
		}
		stats.annotateTalkgroup(&item)
		result = append(result, item)
	}
	return result, nil
}

func (stats *Stats) annotateTalkgroup(item *StatsTopTalkgroup) {
	for _, sys := range stats.Controller.Systems.List {
		if sys.Id == item.SystemId {
			item.SystemLabel = sys.Label
			for _, tg := range sys.Talkgroups.List {
				if tg.Id == item.TalkgroupId {
					item.TalkgroupLabel = tg.Label
					item.TalkgroupName = tg.Name
					break
				}
			}
			break
		}
	}
	if item.SystemLabel == "" {
		item.SystemLabel = fmt.Sprintf("System %d", item.SystemId)
	}
	if item.TalkgroupLabel == "" {
		item.TalkgroupLabel = fmt.Sprintf("TG %d", item.TalkgroupId)
	}
}

// GetTopSystems: top N systems by call count since the given time. The
// window comes from the dashboard's range filter, so the ranking matches the
// period the rest of the dashboard is showing.
func (stats *Stats) GetTopSystems(db *Database, limit int, since time.Time) ([]StatsTopSystem, error) {
	result := []StatsTopSystem{}

	q := fmt.Sprintf(
		"select `system`, count(*) as c from `rdioScannerCalls` where `dateTime` >= ? group by `system` order by c desc limit %d",
		limit,
	)
	rows, err := db.Query(q, since.Format(db.DateTimeFormat))
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("stats.topSystems: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var item StatsTopSystem
		if err := rows.Scan(&item.SystemId, &item.Count); err != nil {
			continue
		}
		for _, sys := range stats.Controller.Systems.List {
			if sys.Id == item.SystemId {
				item.SystemLabel = sys.Label
				break
			}
		}
		if item.SystemLabel == "" {
			item.SystemLabel = fmt.Sprintf("System %d", item.SystemId)
		}
		result = append(result, item)
	}
	return result, nil
}

// topCategoriesKind picks the "Top ..." lens from the sort options: groups
// when SortByGroups is on, tags when SortByTags is on, systems otherwise —
// the same lens the main UI sorts talkgroups by.
func (stats *Stats) topCategoriesKind() string {
	byGroups, byTags := stats.Controller.Options.GetSortLens()
	if byGroups {
		return "groups"
	}
	if byTags {
		return "tags"
	}
	return "systems"
}

// getTopCategoriesGrouped returns the given window ranked by group or tag:
// tally per (system, talkgroup) in SQL, then resolve each pair to its label
// via the systems config; talkgroups without a resolvable group or tag land
// in "Other". The systems lens never comes here — build() derives it from
// the TopSystems aggregation it already ran.
func (stats *Stats) getTopCategoriesGrouped(db *Database, limit int, kind string, since time.Time) ([]StatsTopCategory, error) {
	rows, err := db.Query(
		"select `system`, `talkgroup`, count(*) as c from `rdioScannerCalls` where `dateTime` >= ? group by `system`, `talkgroup`",
		since.Format(db.DateTimeFormat),
	)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("stats.topCategories: %v", err)
	}
	defer rows.Close()

	tally := map[string]uint{}
	for rows.Next() {
		var (
			systemId    uint
			talkgroupId uint
			count       uint
		)
		if err := rows.Scan(&systemId, &talkgroupId, &count); err != nil {
			continue
		}
		tally[stats.categoryLabel(kind, systemId, talkgroupId)] += count
	}

	result := make([]StatsTopCategory, 0, len(tally))
	for label, count := range tally {
		result = append(result, StatsTopCategory{Label: label, Count: count})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Count != result[j].Count {
			return result[i].Count > result[j].Count
		}
		return result[i].Label < result[j].Label
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

// categoryLabel resolves a (system, talkgroup) pair to its group or tag
// label through the mutex-taking lookup helpers — the admin config save
// repopulates these lists in place, so walking Systems.List lock-free here
// would race it.
func (stats *Stats) categoryLabel(kind string, systemId uint, talkgroupId uint) string {
	if sys, ok := stats.Controller.Systems.GetSystem(systemId); ok {
		if tg, ok := sys.Talkgroups.GetTalkgroup(talkgroupId); ok {
			if kind == "groups" {
				if g, ok := stats.Controller.Groups.GetGroup(tg.GroupId); ok && g.Label != "" {
					return g.Label
				}
			} else {
				if t, ok := stats.Controller.Tags.GetTag(tg.TagId); ok && t.Label != "" {
					return t.Label
				}
			}
		}
	}
	return "Other"
}

// extractUnitsFromSources pulls unit IDs out of the per-call `sources` JSON
// column ("[{pos,src,tag?}, ...]"). Some recorders (DSD FME with custom
// metadata masks, multi-keying trunked recorders) only populate the JSON
// array and leave the scalar `source` column at 0 — so any stats query
// that filters on `source > 0` silently misses those calls. Deduped
// because the same unit can appear at multiple positions in a long call.
func extractUnitsFromSources(raw any) []uint {
	var b []byte
	switch v := raw.(type) {
	case []byte:
		b = v
	case string:
		b = []byte(v)
	default:
		return nil
	}
	if len(b) == 0 {
		return nil
	}
	var arr []map[string]any
	if err := json.Unmarshal(b, &arr); err != nil {
		return nil
	}
	seen := map[uint]bool{}
	out := []uint{}
	for _, s := range arr {
		v, ok := s["src"]
		if !ok {
			continue
		}
		var u uint
		switch n := v.(type) {
		case float64:
			if n > 0 {
				u = uint(n)
			}
		case json.Number:
			if i, err := n.Int64(); err == nil && i > 0 {
				u = uint(i)
			}
		}
		if u > 0 && !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	return out
}

// annotateUnitLabels fills SystemLabel/UnitLabel from the in-memory systems
// catalog. Pulled out so GetTopUnits and GetTalkgroupUnits can share it.
func (stats *Stats) lookupSystemAndUnit(systemId, unitId uint) (sysLabel, unitLabel string) {
	for _, sys := range stats.Controller.Systems.List {
		if sys.Id == systemId {
			sysLabel = sys.Label
			for _, unit := range sys.Units.List {
				if unit.Id == unitId {
					unitLabel = unit.Label
					break
				}
			}
			break
		}
	}
	return
}

// GetTopUnits: top N units by call count over the last 7 days.
//
// Aggregates in Go (not SQL) so we can count units that only appear in
// the per-call sources JSON array, not just the scalar source column.
// See extractUnitsFromSources for the rationale.
func (stats *Stats) GetTopUnits(db *Database, limit int) ([]StatsTopUnit, error) {
	result := []StatsTopUnit{}
	since := time.Now().UTC().AddDate(0, 0, -7)

	rows, err := db.Query(
		"select `system`, `source`, `sources` from `rdioScannerCalls` where `dateTime` >= ?",
		since.Format(db.DateTimeFormat),
	)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("stats.topUnits: %v", err)
	}
	defer rows.Close()

	type key struct{ sys, unit uint }
	counts := map[key]uint{}

	for rows.Next() {
		var sysId uint
		var src sql.NullInt64
		var sourcesRaw any
		if err := rows.Scan(&sysId, &src, &sourcesRaw); err != nil {
			continue
		}

		units := map[uint]bool{}
		if src.Valid && src.Int64 > 0 {
			units[uint(src.Int64)] = true
		}
		for _, u := range extractUnitsFromSources(sourcesRaw) {
			units[u] = true
		}
		for u := range units {
			counts[key{sysId, u}]++
		}
	}

	// Materialize, sort by count desc, trim to limit.
	type entry struct {
		sys, unit, count uint
	}
	all := make([]entry, 0, len(counts))
	for k, c := range counts {
		all = append(all, entry{k.sys, k.unit, c})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].count > all[j].count })
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}

	for _, e := range all {
		item := StatsTopUnit{SystemId: e.sys, UnitId: e.unit, Count: e.count}
		item.SystemLabel, item.UnitLabel = stats.lookupSystemAndUnit(e.sys, e.unit)
		if item.SystemLabel == "" {
			item.SystemLabel = fmt.Sprintf("System %d", e.sys)
		}
		if item.UnitLabel == "" {
			item.UnitLabel = fmt.Sprintf("Unit %d", e.unit)
		}
		result = append(result, item)
	}
	return result, nil
}

// GetLastHourTalkgroups: top 20 talkgroups active in the last hour with last
// call timestamp.
func (stats *Stats) GetLastHourTalkgroups(db *Database) ([]StatsLastHourTalkgroup, error) {
	result := []StatsLastHourTalkgroup{}
	since := time.Now().UTC().Add(-time.Hour)

	q := "select `system`, `talkgroup`, count(*) as c, max(`dateTime`) as last from `rdioScannerCalls` where `dateTime` >= ? group by `system`, `talkgroup` order by last desc limit 20"
	rows, err := db.Query(q, since.Format(db.DateTimeFormat))
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("stats.lastHourTalkgroups: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var item StatsLastHourTalkgroup
		var last any
		if err := rows.Scan(&item.SystemId, &item.TalkgroupId, &item.Count, &last); err != nil {
			continue
		}
		if t, err := db.ParseDateTime(last); err == nil {
			// RFC3339 carries the timezone offset ("Z" for UTC) so the
			// browser parses it as an absolute instant. With
			// db.DateTimeFormat ("2006-01-02 15:04:05") there's no TZ
			// marker, so JS treats the string as local time and the
			// resulting "X hours ago" reads as UTC-offset hours off
			// instead of the truth (the bug reported as "10 hours ago
			// for a call that just happened" on a UTC+10 client).
			item.LastCall = t.UTC().Format(time.RFC3339)
		}
		// Reuse the talkgroup annotation helper via the same-shaped item.
		proxy := StatsTopTalkgroup{SystemId: item.SystemId, TalkgroupId: item.TalkgroupId}
		stats.annotateTalkgroup(&proxy)
		item.SystemLabel = proxy.SystemLabel
		item.TalkgroupLabel = proxy.TalkgroupLabel
		item.TalkgroupName = proxy.TalkgroupName
		result = append(result, item)
	}
	return result, nil
}

// GetTalkgroupUnits: top 50 units active in a specific (system,talkgroup) in
// the last hour.
//
// Aggregates in Go so the response includes units that only show up in the
// per-call `sources` JSON array, not just the scalar `source` column. This
// is the path that broke for DSD FME with custom metadata masks: their
// recordings populate the sources array but leave the scalar at 0, so the
// previous `where source > 0` SQL filter dropped them silently.
func (stats *Stats) GetTalkgroupUnits(db *Database, systemId, talkgroupId uint) ([]StatsTalkgroupUnit, error) {
	result := []StatsTalkgroupUnit{}
	since := time.Now().UTC().Add(-time.Hour)

	rows, err := db.Query(
		"select `source`, `sources`, `dateTime` from `rdioScannerCalls` where `system` = ? and `talkgroup` = ? and `dateTime` >= ?",
		systemId, talkgroupId, since.Format(db.DateTimeFormat),
	)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("stats.talkgroupUnits: %v", err)
	}
	defer rows.Close()

	type agg struct {
		count uint
		last  time.Time
	}
	tally := map[uint]*agg{}

	for rows.Next() {
		var src sql.NullInt64
		var sourcesRaw any
		var dateTime any
		if err := rows.Scan(&src, &sourcesRaw, &dateTime); err != nil {
			continue
		}
		t, err := db.ParseDateTime(dateTime)
		if err != nil {
			continue
		}

		units := map[uint]bool{}
		if src.Valid && src.Int64 > 0 {
			units[uint(src.Int64)] = true
		}
		for _, u := range extractUnitsFromSources(sourcesRaw) {
			units[u] = true
		}

		for u := range units {
			a, ok := tally[u]
			if !ok {
				a = &agg{}
				tally[u] = a
			}
			a.count++
			if t.After(a.last) {
				a.last = t
			}
		}
	}

	type entry struct {
		unit  uint
		count uint
		last  time.Time
	}
	all := make([]entry, 0, len(tally))
	for u, a := range tally {
		all = append(all, entry{u, a.count, a.last})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].last.After(all[j].last) })
	if len(all) > 50 {
		all = all[:50]
	}

	for _, e := range all {
		item := StatsTalkgroupUnit{
			UnitId: e.unit,
			Count:  e.count,
			// RFC3339 so the browser parses it as an absolute instant
			// rather than wall-clock-in-local; see GetLastHourTalkgroups
			// for the rationale.
			LastCall: e.last.UTC().Format(time.RFC3339),
		}
		_, item.UnitLabel = stats.lookupSystemAndUnit(systemId, e.unit)
		if item.UnitLabel == "" {
			item.UnitLabel = fmt.Sprintf("%d", e.unit)
		}
		result = append(result, item)
	}
	return result, nil
}

// Build runs every stats query and assembles the response. Callers should
// prefer cachedBuild which serves this behind a short TTL cache.
//
// The eight sub-queries run in parallel against the DB pool — one slow query
// no longer blocks the others, so wall time is close to max(query) instead
// of sum. That keeps a cold-cache load well under the Cloudflare 100 s edge
// timeout on big tables (~300 k rows).
func (stats *Stats) build(db *Database) *StatsResponse {
	resp := &StatsResponse{}

	// Log every sub-query failure so a single misbehaving panel doesn't
	// silently take the whole stats page down — without this, a Postgres
	// permission error or schema drift just shows up as an empty chart
	// with no breadcrumb in the logs.
	logErr := func(err error) {
		stats.Controller.Logs.LogEvent(LogLevelError, fmt.Sprintf("stats.handler: %v", err))
	}

	var wg sync.WaitGroup
	run := func(fn func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn()
		}()
	}

	run(func() {
		if v, err := stats.GetOverview(db); err != nil {
			logErr(err)
		} else {
			resp.Overview = *v
		}
	})
	run(func() {
		if v, err := stats.GetHourBuckets(db); err != nil {
			logErr(err)
		} else {
			resp.HourBuckets = v
		}
	})
	run(func() {
		if v, err := stats.GetTopTalkgroups(db, 10); err != nil {
			logErr(err)
		} else {
			resp.TopTalkgroups = v
		}
	})
	run(func() {
		if v, err := stats.GetTopSystems(db, 10, statsRangeSince("1w")); err != nil {
			logErr(err)
		} else {
			resp.TopSystems = v
		}
	})
	// The lens is decided once per build; the systems case is filled in
	// after the wait from the TopSystems aggregation instead of re-running
	// the identical query.
	topKind := stats.topCategoriesKind()
	if topKind != "systems" {
		run(func() {
			if v, err := stats.getTopCategoriesGrouped(db, 10, topKind, statsRangeSince("1w")); err != nil {
				logErr(err)
			} else {
				resp.TopCategories = v
			}
		})
	}
	run(func() {
		if v, err := stats.GetTopUnits(db, 10); err != nil {
			logErr(err)
		} else {
			resp.TopUnits = v
		}
	})
	run(func() {
		if v, err := stats.GetLastHourTalkgroups(db); err != nil {
			logErr(err)
		} else {
			resp.LastHourTalkgroups = v
		}
	})
	run(func() {
		if v, micro, err := stats.GetCallFineBuckets(db); err != nil {
			logErr(err)
		} else {
			resp.CallFineBuckets = v
			resp.CallMicroBuckets = micro
		}
	})
	run(func() {
		if v, err := stats.GetListenerBuckets(db); err != nil {
			logErr(err)
		} else {
			resp.ListenerBuckets = v
		}
	})

	wg.Wait()

	resp.TopCategoriesKind = topKind
	if topKind == "systems" {
		categories := make([]StatsTopCategory, 0, len(resp.TopSystems))
		for _, s := range resp.TopSystems {
			categories = append(categories, StatsTopCategory{Label: s.SystemLabel, Count: s.Count})
		}
		resp.TopCategories = categories
	}

	return resp
}

// cachedBuild returns a stats response, building + caching for 2 minutes.
// Single shared cache — the response is TZ-independent (everything time-
// bucketed is UTC) so all viewers can share it.
//
// A stale snapshot is served immediately and refreshed in the background.
// Rebuild cost grows with the calls table, and making a request wait for one
// is what turns a slow database into a gateway timeout: the viewer waits,
// their connection is held, and concurrent viewers each queue another build.
// Serving data up to a TTL stale costs nothing anyone can perceive on a
// dashboard; blocking costs the whole page. Only the very first build (before
// any snapshot exists) waits, and startup pre-warms that.
func (stats *Stats) cachedBuild(db *Database) *StatsResponse {
	stats.mu.Lock()
	cached, cachedAt, building := stats.cached, stats.cachedAt, stats.building

	if cached != nil {
		stale := time.Since(cachedAt) >= statsCacheTTL

		if stale && !building {
			// Single-flight: one refresh at a time however many viewers ask.
			stats.building = true
			go stats.rebuild(db)
		}

		stats.mu.Unlock()

		return cached
	}
	stats.mu.Unlock()

	// Nothing to serve yet — this one has to wait.
	return stats.rebuild(db)
}

// rebuild runs a build and replaces the snapshot. Always clears the
// single-flight flag, including on panic, so one bad build can't wedge the
// cache into never refreshing again.
func (stats *Stats) rebuild(db *Database) *StatsResponse {
	started := time.Now()

	defer func() {
		stats.mu.Lock()
		stats.building = false
		stats.mu.Unlock()
	}()

	resp := stats.build(db)
	elapsed := time.Since(started)

	stats.mu.Lock()
	stats.cached = resp
	stats.cachedAt = time.Now()
	stats.mu.Unlock()

	// Logged so a slow database is visible as a number rather than as a
	// mysteriously stale dashboard.
	if elapsed > statsSlowBuildThreshold {
		stats.Controller.Logs.LogEvent(
			LogLevelWarn,
			fmt.Sprintf("stats build took %s; the dashboard is serving cached data while it refreshes", elapsed.Round(time.Millisecond)),
		)
	}

	return resp
}

func (stats *Stats) Handler(w http.ResponseWriter, r *http.Request) {
	t := stats.Controller.Admin.GetAuthorization(r)
	if !stats.Controller.Admin.ValidateToken(t) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	stats.handleStatsRequest(w, r, true)
}

func (stats *Stats) PublicHandler(w http.ResponseWriter, r *http.Request) {
	// Gated per request rather than baked into the cache, so flipping the
	// option takes effect immediately instead of after the cache TTL.
	stats.handleStatsRequest(w, r, stats.Controller.Options.GetShowListenerStats())
}

// configuredInventory counts what is configured, as opposed to what has been
// heard — the activity counts in Overview come from the calls table.
//
// Talkgroups and Units carry their own mutexes and the call-ingest path
// appends to them under auto-populate, so each is locked in turn rather than
// read behind the Systems lock alone. Systems first, then the child, matching
// the order Systems.Read uses.
func (stats *Stats) configuredInventory() (systems uint, talkgroups uint, units uint) {
	// A controller without a systems collection is only ever a partially
	// built one in a test; report zeroes rather than panicking a request.
	if stats.Controller == nil || stats.Controller.Systems == nil {
		return 0, 0, 0
	}

	// TryLock, not Lock: a config save holds this mutex for the whole write
	// of every system, talkgroup and unit. Waiting on it made a stats request
	// hang for the length of a save, and those requests hold a connection
	// while they wait. Reporting nothing for one poll is the better trade.
	if !stats.Controller.Systems.mutex.TryLock() {
		return 0, 0, 0
	}
	defer stats.Controller.Systems.mutex.Unlock()

	for _, system := range stats.Controller.Systems.List {
		if system == nil {
			continue
		}

		systems++

		if system.Talkgroups != nil && system.Talkgroups.mutex.TryLock() {
			talkgroups += uint(len(system.Talkgroups.List))
			system.Talkgroups.mutex.Unlock()
		}

		if system.Units != nil && system.Units.mutex.TryLock() {
			units += uint(len(system.Units.List))
			system.Units.mutex.Unlock()
		}
	}

	return systems, talkgroups, units
}

func (stats *Stats) handleStatsRequest(w http.ResponseWriter, r *http.Request, includeListeners bool) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	cached := stats.cachedBuild(stats.Controller.Database)

	// Shallow copy — the cached response is shared by every caller, so
	// per-request shaping in place would leak between them.
	shaped := *cached

	// Counted per request rather than inside build(): it costs nothing (no
	// query, just walking the in-memory config), and folding it into the
	// cached build left the tiles showing pre-save numbers for the rest of
	// the cache TTL, which reads as a save that didn't take.
	shaped.ConfiguredSystems, shaped.ConfiguredTalkgroups, shaped.ConfiguredUnits = stats.configuredInventory()

	if !includeListeners {
		shaped.ListenerBuckets = nil
	}

	resp := &shaped

	w.Header().Set("Content-Type", "application/json")
	if b, err := json.Marshal(resp); err == nil {
		w.Write(b)
	} else {
		w.WriteHeader(http.StatusInternalServerError)
	}
}

func (stats *Stats) TalkgroupUnitsHandler(w http.ResponseWriter, r *http.Request) {
	t := stats.Controller.Admin.GetAuthorization(r)
	if !stats.Controller.Admin.ValidateToken(t) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	stats.handleTalkgroupUnitsRequest(w, r)
}

func (stats *Stats) PublicTalkgroupUnitsHandler(w http.ResponseWriter, r *http.Request) {
	stats.handleTalkgroupUnitsRequest(w, r)
}

func (stats *Stats) handleTalkgroupUnitsRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	query := r.URL.Query()
	systemId := query.Get("system")
	talkgroupId := query.Get("talkgroup")
	if systemId == "" || talkgroupId == "" {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Missing system or talkgroup parameter"))
		return
	}

	var sysId, tgId uint
	if _, err := fmt.Sscanf(systemId, "%d", &sysId); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Invalid system ID"))
		return
	}
	if _, err := fmt.Sscanf(talkgroupId, "%d", &tgId); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Invalid talkgroup ID"))
		return
	}

	units, err := stats.GetTalkgroupUnits(stats.Controller.Database, sysId, tgId)
	if err != nil {
		stats.Controller.Logs.LogEvent(LogLevelError, fmt.Sprintf("stats.talkgroupUnits: %v", err))
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if b, err := json.Marshal(units); err == nil {
		w.Write(b)
	} else {
		w.WriteHeader(http.StatusInternalServerError)
	}
}
