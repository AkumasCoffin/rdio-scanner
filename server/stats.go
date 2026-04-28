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

// hourExpr returns the SQL expression that extracts the hour-of-day (0..23)
// from the dateTime column for the active DB type. Result is an integer.
func (stats *Stats) hourExpr() string {
	switch stats.Controller.Database.Config.DbType {
	case DbTypePostgres:
		return `extract(hour from "dateTime")::integer`
	case DbTypeSqlite:
		return `cast(strftime('%H', ` + "`dateTime`" + `) as integer)`
	default:
		return "hour(`dateTime`)"
	}
}

// dayExpr returns the SQL expression that extracts the YYYY-MM-DD date string.
func (stats *Stats) dayExpr() string {
	switch stats.Controller.Database.Config.DbType {
	case DbTypePostgres:
		return `to_char("dateTime", 'YYYY-MM-DD')`
	case DbTypeSqlite:
		return "strftime('%Y-%m-%d', `dateTime`)"
	default:
		return "date_format(`dateTime`, '%Y-%m-%d')"
	}
}

func (stats *Stats) GetOverview(db *Database) (*StatsOverview, error) {
	overview := &StatsOverview{}
	// Calls are stored with UTC dateTime (see parsers.go), so every filter
	// threshold has to be expressed in UTC too. Using local-time here breaks
	// the "today" counter on servers running in a non-UTC timezone — the
	// formatted string lands in the future relative to stored UTC values
	// and the query returns 0 matches.
	now := time.Now().UTC()
	df := db.DateTimeFormat

	if err := db.QueryRow("select count(*) from `rdioScannerCalls`").Scan(&overview.TotalCalls); err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("stats.overview.total: %v", err)
	}

	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
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

	peakQ := fmt.Sprintf(
		"select %s as h, count(*) as c from `rdioScannerCalls` where `dateTime` >= ? group by h order by c desc limit 1",
		stats.hourExpr(),
	)
	var h sql.NullInt64
	var c sql.NullInt64
	if err := db.QueryRow(peakQ, weekAgo.Format(df)).Scan(&h, &c); err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("stats.overview.peakHour: %v", err)
	}
	if h.Valid {
		overview.PeakHour = uint(h.Int64)
	}

	return overview, nil
}

// GetCallsByHour returns counts bucketed by hour-of-day over the last 7 days.
func (stats *Stats) GetCallsByHour(db *Database) ([]StatsCallsByHour, error) {
	result := make([]StatsCallsByHour, 24)
	for i := 0; i < 24; i++ {
		result[i] = StatsCallsByHour{Hour: uint(i)}
	}

	since := time.Now().UTC().AddDate(0, 0, -7)
	q := fmt.Sprintf(
		"select %s as h, count(*) from `rdioScannerCalls` where `dateTime` >= ? group by h",
		stats.hourExpr(),
	)
	rows, err := db.Query(q, since.Format(db.DateTimeFormat))
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("stats.callsByHour: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var h, c sql.NullInt64
		if err := rows.Scan(&h, &c); err != nil {
			continue
		}
		if h.Valid && h.Int64 >= 0 && h.Int64 < 24 && c.Valid {
			result[h.Int64].Count = uint(c.Int64)
		}
	}
	return result, nil
}

// GetCallsByDay returns counts bucketed by date for the last `days` days.
func (stats *Stats) GetCallsByDay(db *Database, days int) ([]StatsCallsByDay, error) {
	result := []StatsCallsByDay{}
	since := time.Now().UTC().AddDate(0, 0, -days)

	q := fmt.Sprintf(
		"select %s as d, count(*) from `rdioScannerCalls` where `dateTime` >= ? group by d order by d asc",
		stats.dayExpr(),
	)
	rows, err := db.Query(q, since.Format(db.DateTimeFormat))
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("stats.callsByDay: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var d sql.NullString
		var c sql.NullInt64
		if err := rows.Scan(&d, &c); err != nil {
			continue
		}
		if d.Valid && c.Valid {
			result = append(result, StatsCallsByDay{Date: d.String, Count: uint(c.Int64)})
		}
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

// GetTopUnits: top N units by call count over the last 7 days.
func (stats *Stats) GetTopUnits(db *Database, limit int) ([]StatsTopUnit, error) {
	result := []StatsTopUnit{}
	since := time.Now().UTC().AddDate(0, 0, -7)

	q := fmt.Sprintf(
		"select `system`, `source`, count(*) as c from `rdioScannerCalls` where `dateTime` >= ? and `source` is not null and `source` > 0 group by `system`, `source` order by c desc limit %d",
		limit,
	)
	rows, err := db.Query(q, since.Format(db.DateTimeFormat))
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("stats.topUnits: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var item StatsTopUnit
		if err := rows.Scan(&item.SystemId, &item.UnitId, &item.Count); err != nil {
			continue
		}
		for _, sys := range stats.Controller.Systems.List {
			if sys.Id == item.SystemId {
				item.SystemLabel = sys.Label
				for _, unit := range sys.Units.List {
					if unit.Id == item.UnitId {
						item.UnitLabel = unit.Label
						break
					}
				}
				break
			}
		}
		if item.SystemLabel == "" {
			item.SystemLabel = fmt.Sprintf("System %d", item.SystemId)
		}
		if item.UnitLabel == "" {
			item.UnitLabel = fmt.Sprintf("Unit %d", item.UnitId)
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
func (stats *Stats) GetTalkgroupUnits(db *Database, systemId, talkgroupId uint) ([]StatsTalkgroupUnit, error) {
	result := []StatsTalkgroupUnit{}
	since := time.Now().UTC().Add(-time.Hour)

	q := "select `source`, count(*) as c, max(`dateTime`) as last from `rdioScannerCalls` where `system` = ? and `talkgroup` = ? and `source` is not null and `source` > 0 and `dateTime` >= ? group by `source` order by last desc limit 50"
	rows, err := db.Query(q, systemId, talkgroupId, since.Format(db.DateTimeFormat))
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("stats.talkgroupUnits: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var item StatsTalkgroupUnit
		var last any
		if err := rows.Scan(&item.UnitId, &item.Count, &last); err != nil {
			continue
		}
		if t, err := db.ParseDateTime(last); err == nil {
			item.LastCall = t.Format(db.DateTimeFormat)
		}
		for _, sys := range stats.Controller.Systems.List {
			if sys.Id == systemId {
				for _, unit := range sys.Units.List {
					if unit.Id == item.UnitId {
						item.UnitLabel = unit.Label
						break
					}
				}
				break
			}
		}
		if item.UnitLabel == "" {
			item.UnitLabel = fmt.Sprintf("%d", item.UnitId)
		}
		result = append(result, item)
	}
	return result, nil
}

// GetRecentActivity: calls-per-hour across the last 24 hours.
func (stats *Stats) GetRecentActivity(db *Database) ([]StatsCallsByHour, error) {
	now := time.Now().UTC()
	result := make([]StatsCallsByHour, 24)
	for i := 23; i >= 0; i-- {
		result[23-i] = StatsCallsByHour{Hour: uint((now.Hour() - i + 24) % 24), Count: 0}
	}

	since := now.Add(-24 * time.Hour)
	q := fmt.Sprintf(
		"select %s as h, count(*) from `rdioScannerCalls` where `dateTime` >= ? group by h",
		stats.hourExpr(),
	)
	rows, err := db.Query(q, since.Format(db.DateTimeFormat))
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("stats.recentActivity: %v", err)
	}
	defer rows.Close()

	counts := map[uint]uint{}
	for rows.Next() {
		var h, c sql.NullInt64
		if err := rows.Scan(&h, &c); err != nil {
			continue
		}
		if h.Valid && c.Valid {
			counts[uint(h.Int64)] = uint(c.Int64)
		}
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
