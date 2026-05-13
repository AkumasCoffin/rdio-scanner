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

// statsHourBucketRange — how far back hour-grain buckets cover. 30 days
// is the longest period any client chart needs; everything narrower
// (week, 24-hour view, today) is derived client-side by filtering the
// buckets. 30 * 24 = 720 bucket entries ≈ 28 KB JSON, ~5 KB gzipped.
const statsHourBucketRange = 30 * 24 * time.Hour

type Stats struct {
	Controller *Controller

	mu       sync.Mutex
	cached   *StatsResponse
	cachedAt time.Time
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
	HourBuckets        []StatsHourBucket        `json:"hourBuckets"`
	TopTalkgroups      []StatsTopTalkgroup      `json:"topTalkgroups"`
	TopSystems         []StatsTopSystem         `json:"topSystems"`
	TopUnits           []StatsTopUnit           `json:"topUnits"`
	LastHourTalkgroups []StatsLastHourTalkgroup `json:"lastHourTalkgroups"`
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

	tally := map[time.Time]uint{}
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
		if v, err := stats.GetLastHourTalkgroups(db); err != nil {
			logErr(err)
		} else {
			resp.LastHourTalkgroups = v
		}
	})

	wg.Wait()

	return resp
}

// cachedBuild returns a stats response, building + caching for 2 minutes.
// Single shared cache — the response is TZ-independent (everything time-
// bucketed is UTC) so all viewers can share it.
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
