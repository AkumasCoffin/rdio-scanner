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

type Stats struct {
	Controller *Controller

	mu       sync.Mutex
	cached   *StatsResponse
	cachedAt time.Time
}

type StatsOverview struct {
	TotalCalls       uint    `json:"totalCalls"`
	TodayCalls       uint    `json:"todayCalls"`
	WeekCalls        uint    `json:"weekCalls"`
	MonthCalls       uint    `json:"monthCalls"`
	ActiveSystems    uint    `json:"activeSystems"`
	ActiveTalkgroups uint    `json:"activeTalkgroups"`
	AvgCallsPerDay   float64 `json:"avgCallsPerDay"`
	PeakHour         uint    `json:"peakHour"`
}

type StatsCallsByHour struct {
	Hour  uint `json:"hour"`
	Count uint `json:"count"`
}

type StatsCallsByDay struct {
	Date  string `json:"date"`
	Count uint   `json:"count"`
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

type StatsResponse struct {
	Overview           StatsOverview            `json:"overview"`
	CallsByHour        []StatsCallsByHour       `json:"callsByHour"`
	CallsByDay         []StatsCallsByDay        `json:"callsByDay"`
	TopTalkgroups      []StatsTopTalkgroup      `json:"topTalkgroups"`
	TopSystems         []StatsTopSystem         `json:"topSystems"`
	TopUnits           []StatsTopUnit           `json:"topUnits"`
	RecentActivity     []StatsCallsByHour       `json:"recentActivity"`
	LastHourTalkgroups []StatsLastHourTalkgroup `json:"lastHourTalkgroups"`
}

func NewStats(controller *Controller) *Stats {
	return &Stats{
		Controller: controller,
	}
}

func (stats *Stats) GetOverview(db *Database) (*StatsOverview, error) {
	overview := &StatsOverview{}
	// Calls are stored with UTC dateTime (see parsers.go), so every filter
	// threshold has to be expressed in UTC for the column comparison.
	// We compute calendar boundaries in server local time though — "today"
	// in the UI is the user's calendar day, not the UTC day. On a
	// correctly-configured self-hosted box (server TZ == user TZ) those
	// align. If the server runs in UTC and the user is in EST, set the
	// server timezone (timedatectl) so this matches expectations.
	nowLocal := time.Now()
	now := nowLocal.UTC()
	df := db.DateTimeFormat

	if err := db.QueryRow("select count(*) from `rdioScannerCalls`").Scan(&overview.TotalCalls); err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("stats.overview.total: %v", err)
	}

	// Today = since local midnight, expressed in UTC for the WHERE clause.
	todayStart := time.Date(
		nowLocal.Year(), nowLocal.Month(), nowLocal.Day(),
		0, 0, 0, 0, time.Local,
	).UTC()
	if err := db.QueryRow(
		"select count(*) from `rdioScannerCalls` where `dateTime` >= ?",
		todayStart.Format(df),
	).Scan(&overview.TodayCalls); err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("stats.overview.today: %v", err)
	}

	weekAgo := now.AddDate(0, 0, -7)
	if err := db.QueryRow(
		"select count(*) from `rdioScannerCalls` where `dateTime` >= ?",
		weekAgo.Format(df),
	).Scan(&overview.WeekCalls); err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("stats.overview.week: %v", err)
	}

	monthAgo := now.AddDate(0, 0, -30)
	if err := db.QueryRow(
		"select count(*) from `rdioScannerCalls` where `dateTime` >= ?",
		monthAgo.Format(df),
	).Scan(&overview.MonthCalls); err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("stats.overview.month: %v", err)
	}

	dayAgo := now.AddDate(0, 0, -1)
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

	overview.AvgCallsPerDay = float64(overview.MonthCalls) / 30.0

	// Peak hour: scan the last 7 days and tally in Go so the answer is
	// consistent with GetCallsByHour (same data source, same aggregation
	// logic) instead of two parallel SQL implementations that could
	// disagree on the edge cases.
	peakRows, err := db.Query(
		"select `dateTime` from `rdioScannerCalls` where `dateTime` >= ?",
		weekAgo.Format(df),
	)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("stats.overview.peakHour: %v", err)
	}
	if peakRows != nil {
		hourCounts := [24]uint{}
		for peakRows.Next() {
			var raw any
			if err := peakRows.Scan(&raw); err != nil {
				continue
			}
			t, err := db.ParseDateTime(raw)
			if err != nil {
				continue
			}
			// Local hour so "Peak Hour: 7 AM" reads as 7 AM in the
			// user's calendar, not 7 AM UTC.
			h := t.Local().Hour()
			if h >= 0 && h < 24 {
				hourCounts[h]++
			}
		}
		peakRows.Close()
		var best uint
		for h, c := range hourCounts {
			if c > best {
				best = c
				overview.PeakHour = uint(h)
			}
		}
	}

	return overview, nil
}

// GetCallsByHour returns counts bucketed by hour-of-day over the last 7 days.
//
// Aggregates in Go (not SQL) so we don't depend on per-DB date-function
// dialects (extract / strftime / hour) — one less place where a typed
// scan can silently drop rows.
func (stats *Stats) GetCallsByHour(db *Database) ([]StatsCallsByHour, error) {
	result := make([]StatsCallsByHour, 24)
	for i := 0; i < 24; i++ {
		result[i] = StatsCallsByHour{Hour: uint(i)}
	}

	since := time.Now().UTC().AddDate(0, 0, -7)
	rows, err := db.Query(
		"select `dateTime` from `rdioScannerCalls` where `dateTime` >= ?",
		since.Format(db.DateTimeFormat),
	)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("stats.callsByHour: %v", err)
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
		// Bucket by LOCAL hour so the chart axis matches the user's
		// calendar (a 7 AM bar means 7 AM local, not 7 AM UTC).
		h := t.Local().Hour()
		if h >= 0 && h < 24 {
			result[h].Count++
		}
	}
	return result, nil
}

// GetCallsByDay returns counts bucketed by date for the last `days` days.
//
// Aggregates in Go so the date key format (YYYY-MM-DD) is consistent
// across DB backends — to_char / strftime / date_format all behave
// slightly differently with respect to padding and timezone, and any
// one of them returning text the scan can't decode dropped the row
// silently in the previous SQL-aggregated version.
func (stats *Stats) GetCallsByDay(db *Database, days int) ([]StatsCallsByDay, error) {
	since := time.Now().UTC().AddDate(0, 0, -days)
	rows, err := db.Query(
		"select `dateTime` from `rdioScannerCalls` where `dateTime` >= ?",
		since.Format(db.DateTimeFormat),
	)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("stats.callsByDay: %v", err)
	}
	defer rows.Close()

	counts := map[string]uint{}
	for rows.Next() {
		var raw any
		if err := rows.Scan(&raw); err != nil {
			continue
		}
		t, err := db.ParseDateTime(raw)
		if err != nil {
			continue
		}
		// Bucket by LOCAL date so calls from late evening don't get
		// counted on "tomorrow" because their UTC timestamp has
		// already rolled past midnight. Server's TZ env determines
		// what "local" means — set timedatectl on the host if the
		// chart's day boundaries look shifted.
		counts[t.Local().Format("2006-01-02")]++
	}

	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	result := make([]StatsCallsByDay, 0, len(keys))
	for _, k := range keys {
		result = append(result, StatsCallsByDay{Date: k, Count: counts[k]})
	}
	return result, nil
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

// GetTopSystems: top N systems by call count over the last 7 days.
func (stats *Stats) GetTopSystems(db *Database, limit int) ([]StatsTopSystem, error) {
	result := []StatsTopSystem{}
	since := time.Now().UTC().AddDate(0, 0, -7)

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
			item.LastCall = t.Format(db.DateTimeFormat)
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
			UnitId:   e.unit,
			Count:    e.count,
			LastCall: e.last.Format(db.DateTimeFormat),
		}
		_, item.UnitLabel = stats.lookupSystemAndUnit(systemId, e.unit)
		if item.UnitLabel == "" {
			item.UnitLabel = fmt.Sprintf("%d", e.unit)
		}
		result = append(result, item)
	}
	return result, nil
}

// GetRecentActivity: calls-per-hour across the last 24 hours.
//
// Aggregates in Go for the same robustness reasons as GetCallsByHour —
// avoids per-DB hour-extraction syntax and silent type-mismatch drops.
// Bucketing is in LOCAL hours so the trailing 24-hour line traces the
// user's day, not UTC's.
func (stats *Stats) GetRecentActivity(db *Database) ([]StatsCallsByHour, error) {
	nowLocal := time.Now()
	result := make([]StatsCallsByHour, 24)
	for i := 23; i >= 0; i-- {
		result[23-i] = StatsCallsByHour{Hour: uint((nowLocal.Hour() - i + 24) % 24), Count: 0}
	}

	since := nowLocal.UTC().Add(-24 * time.Hour)
	rows, err := db.Query(
		"select `dateTime` from `rdioScannerCalls` where `dateTime` >= ?",
		since.Format(db.DateTimeFormat),
	)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("stats.recentActivity: %v", err)
	}
	defer rows.Close()

	counts := map[uint]uint{}
	for rows.Next() {
		var raw any
		if err := rows.Scan(&raw); err != nil {
			continue
		}
		t, err := db.ParseDateTime(raw)
		if err != nil {
			continue
		}
		counts[uint(t.Local().Hour())]++
	}
	for i := range result {
		if v, ok := counts[result[i].Hour]; ok {
			result[i].Count = v
		}
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
		if v, err := stats.GetCallsByHour(db); err != nil {
			logErr(err)
		} else {
			resp.CallsByHour = v
		}
	})
	run(func() {
		if v, err := stats.GetCallsByDay(db, 30); err != nil {
			logErr(err)
		} else {
			resp.CallsByDay = v
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
		if v, err := stats.GetTopSystems(db, 10); err != nil {
			logErr(err)
		} else {
			resp.TopSystems = v
		}
	})
	run(func() {
		if v, err := stats.GetTopUnits(db, 10); err != nil {
			logErr(err)
		} else {
			resp.TopUnits = v
		}
	})
	run(func() {
		if v, err := stats.GetRecentActivity(db); err != nil {
			logErr(err)
		} else {
			resp.RecentActivity = v
		}
	})
	run(func() {
		if v, err := stats.GetLastHourTalkgroups(db); err != nil {
			logErr(err)
		} else {
			resp.LastHourTalkgroups = v
		}
	})

	wg.Wait()

	sort.SliceStable(resp.CallsByDay, func(i, j int) bool {
		return resp.CallsByDay[i].Date < resp.CallsByDay[j].Date
	})

	return resp
}

func (stats *Stats) cachedBuild(db *Database) *StatsResponse {
	stats.mu.Lock()
	if stats.cached != nil && time.Since(stats.cachedAt) < statsCacheTTL {
		cached := stats.cached
		stats.mu.Unlock()
		return cached
	}
	stats.mu.Unlock()

	resp := stats.build(db)

	stats.mu.Lock()
	stats.cached = resp
	stats.cachedAt = time.Now()
	stats.mu.Unlock()

	return resp
}

func (stats *Stats) Handler(w http.ResponseWriter, r *http.Request) {
	t := stats.Controller.Admin.GetAuthorization(r)
	if !stats.Controller.Admin.ValidateToken(t) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	stats.handleStatsRequest(w, r)
}

func (stats *Stats) PublicHandler(w http.ResponseWriter, r *http.Request) {
	stats.handleStatsRequest(w, r)
}

func (stats *Stats) handleStatsRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	resp := stats.cachedBuild(stats.Controller.Database)

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
