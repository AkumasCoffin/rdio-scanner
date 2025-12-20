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
	"sync"
	"time"
)

type Stats struct {
	Controller *Controller
	mutex      sync.Mutex
}

type StatsOverview struct {
	TotalCalls      uint    `json:"totalCalls"`
	TodayCalls      uint    `json:"todayCalls"`
	WeekCalls       uint    `json:"weekCalls"`
	MonthCalls      uint    `json:"monthCalls"`
	ActiveSystems   uint    `json:"activeSystems"`
	ActiveTalkgroups uint   `json:"activeTalkgroups"`
	AvgCallsPerDay  float64 `json:"avgCallsPerDay"`
	PeakHour        uint    `json:"peakHour"`
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
	Overview            StatsOverview            `json:"overview"`
	CallsByHour         []StatsCallsByHour       `json:"callsByHour"`
	CallsByDay          []StatsCallsByDay        `json:"callsByDay"`
	TopTalkgroups       []StatsTopTalkgroup      `json:"topTalkgroups"`
	TopSystems          []StatsTopSystem         `json:"topSystems"`
	TopUnits            []StatsTopUnit           `json:"topUnits"`
	RecentActivity      []StatsCallsByHour       `json:"recentActivity"`
	LastHourTalkgroups  []StatsLastHourTalkgroup `json:"lastHourTalkgroups"`
}

func NewStats(controller *Controller) *Stats {
	return &Stats{
		Controller: controller,
		mutex:      sync.Mutex{},
	}
}

func (stats *Stats) GetOverview(db *Database) (*StatsOverview, error) {
	stats.mutex.Lock()
	defer stats.mutex.Unlock()

	overview := &StatsOverview{}
	now := time.Now()

	// Total calls
	err := db.Sql.QueryRow("SELECT COUNT(*) FROM `rdioScannerCalls`").Scan(&overview.TotalCalls)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("stats.overview.totalCalls: %v", err)
	}

	// Today's calls - using datetime comparison for accuracy
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	todayEnd := todayStart.AddDate(0, 0, 1)
	err = db.Sql.QueryRow(
		"SELECT COUNT(*) FROM `rdioScannerCalls` WHERE `dateTime` >= ? AND `dateTime` < ?",
		todayStart.Format(db.DateTimeFormat), todayEnd.Format(db.DateTimeFormat),
	).Scan(&overview.TodayCalls)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("stats.overview.todayCalls: %v", err)
	}

	// This week's calls (last 7 days)
	weekAgo := now.AddDate(0, 0, -7)
	err = db.Sql.QueryRow(
		"SELECT COUNT(*) FROM `rdioScannerCalls` WHERE `dateTime` >= ?",
		weekAgo.Format(db.DateTimeFormat),
	).Scan(&overview.WeekCalls)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("stats.overview.weekCalls: %v", err)
	}

	// This month's calls (last 30 days)
	monthAgo := now.AddDate(0, 0, -30)
	err = db.Sql.QueryRow(
		"SELECT COUNT(*) FROM `rdioScannerCalls` WHERE `dateTime` >= ?",
		monthAgo.Format(db.DateTimeFormat),
	).Scan(&overview.MonthCalls)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("stats.overview.monthCalls: %v", err)
	}

	// Active systems (systems with calls in last 24 hours)
	dayAgo := now.AddDate(0, 0, -1)
	err = db.Sql.QueryRow(
		"SELECT COUNT(DISTINCT `system`) FROM `rdioScannerCalls` WHERE `dateTime` >= ?",
		dayAgo.Format(db.DateTimeFormat),
	).Scan(&overview.ActiveSystems)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("stats.overview.activeSystems: %v", err)
	}

	// Active talkgroups (talkgroups with calls in last 24 hours)
	err = db.Sql.QueryRow(
		"SELECT COUNT(DISTINCT `talkgroup`) FROM `rdioScannerCalls` WHERE `dateTime` >= ?",
		dayAgo.Format(db.DateTimeFormat),
	).Scan(&overview.ActiveTalkgroups)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("stats.overview.activeTalkgroups: %v", err)
	}

	// Average calls per day (last 30 days)
	overview.AvgCallsPerDay = float64(overview.MonthCalls) / 30.0

	// Peak hour (most active hour in last 7 days) - use substr to extract hour
	rows, err := db.Sql.Query(`
		SELECT CAST(substr(dateTime, 12, 2) AS INTEGER) as hour, COUNT(*) as cnt 
		FROM rdioScannerCalls 
		WHERE dateTime >= ? 
		GROUP BY hour 
		ORDER BY cnt DESC 
		LIMIT 1
	`, weekAgo.Format(db.DateTimeFormat))
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("stats.overview.peakHour: %v", err)
	}
	defer rows.Close()
	
	if rows.Next() {
		var cnt uint
		rows.Scan(&overview.PeakHour, &cnt)
	}

	return overview, nil
}

func (stats *Stats) GetCallsByHour(db *Database) ([]StatsCallsByHour, error) {
	stats.mutex.Lock()
	defer stats.mutex.Unlock()

	result := make([]StatsCallsByHour, 24)
	for i := 0; i < 24; i++ {
		result[i] = StatsCallsByHour{Hour: uint(i), Count: 0}
	}

	weekAgo := time.Now().AddDate(0, 0, -7)

	// Get recent calls (15k calls/day max * 7 days * 1.5 safety = ~157k calls)
	rows, err := db.Sql.Query(`
		SELECT dateTime
		FROM rdioScannerCalls 
		ORDER BY dateTime DESC
		LIMIT 200000
	`)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("stats.callsByHour: %v", err)
	}
	defer rows.Close()

	hourCounts := make(map[uint]uint)
	for rows.Next() {
		var dateTimeStr string
		if err := rows.Scan(&dateTimeStr); err == nil {
			// Parse the datetime with timezone
			if callTime, err := db.ParseDateTime(dateTimeStr); err == nil {
				// Check if call is within last week
				if callTime.After(weekAgo) {
					// Convert to local time and get hour
					localTime := callTime.Local()
					hour := uint(localTime.Hour())
					hourCounts[hour]++
				} else {
					// Since ordered by dateTime DESC, once we hit older calls we can stop
					if len(hourCounts) > 0 {
						break
					}
				}
			}
		}
	}

	// Populate result array
	for hour, count := range hourCounts {
		if hour < 24 {
			result[hour].Count = count
		}
	}

	return result, nil
}

func (stats *Stats) GetCallsByDay(db *Database, days int) ([]StatsCallsByDay, error) {
	stats.mutex.Lock()
	defer stats.mutex.Unlock()

	result := []StatsCallsByDay{}

	now := time.Now()
	daysAgo := now.AddDate(0, 0, -days)

	// Get recent calls (15k calls/day max * 30 days * 1.5 safety = ~675k calls)
	rows, err := db.Sql.Query(`
		SELECT dateTime
		FROM rdioScannerCalls 
		ORDER BY dateTime DESC
		LIMIT 700000
	`)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("stats.callsByDay: %v", err)
	}
	defer rows.Close()

	// Count calls per day
	dayCounts := make(map[string]uint)

	for rows.Next() {
		var dateTimeStr string
		if err := rows.Scan(&dateTimeStr); err == nil {
			// Parse the datetime with timezone
			if callTime, err := db.ParseDateTime(dateTimeStr); err == nil {
				// Check if call is within the requested time window
				if callTime.After(daysAgo) && callTime.Before(now.Add(24*time.Hour)) {
					// Format date as YYYY-MM-DD in local time
					localTime := callTime.Local()
					dateKey := localTime.Format("2006-01-02")
					dayCounts[dateKey]++
				}
			}
		}
	}

	// Convert map to slice and sort by date
	for dateKey, count := range dayCounts {
		result = append(result, StatsCallsByDay{
			Date:  dateKey,
			Count: count,
		})
	}

	// Sort by date ascending
	for i := 0; i < len(result); i++ {
		for j := i + 1; j < len(result); j++ {
			if result[i].Date > result[j].Date {
				result[i], result[j] = result[j], result[i]
			}
		}
	}

	return result, nil
}

func (stats *Stats) GetTopTalkgroups(db *Database, limit int) ([]StatsTopTalkgroup, error) {
	stats.mutex.Lock()
	defer stats.mutex.Unlock()

	result := []StatsTopTalkgroup{}

	weekAgo := time.Now().AddDate(0, 0, -7)

	// Get recent calls (15k calls/day max * 7 days * 1.5 safety = ~157k calls)
	rows, err := db.Sql.Query(`
		SELECT system, talkgroup, dateTime
		FROM rdioScannerCalls 
		ORDER BY dateTime DESC
		LIMIT 200000
	`)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("stats.topTalkgroups: %v", err)
	}
	defer rows.Close()

	// Count calls per talkgroup
	type tgKey struct {
		system    uint
		talkgroup uint
	}
	tgCounts := make(map[tgKey]uint)

	for rows.Next() {
		var systemId, talkgroupId uint
		var dateTimeStr string
		if err := rows.Scan(&systemId, &talkgroupId, &dateTimeStr); err == nil {
			if callTime, err := db.ParseDateTime(dateTimeStr); err == nil {
				if callTime.After(weekAgo) {
					key := tgKey{systemId, talkgroupId}
					tgCounts[key]++
				} else {
					if len(tgCounts) > 0 {
						break
					}
				}
			}
		}
	}

	// Convert map to slice and sort by count
	type tgResult struct {
		key   tgKey
		count uint
	}
	var tgResults []tgResult
	for key, count := range tgCounts {
		tgResults = append(tgResults, tgResult{key, count})
	}

	// Sort by count descending
	for i := 0; i < len(tgResults); i++ {
		for j := i + 1; j < len(tgResults); j++ {
			if tgResults[j].count > tgResults[i].count {
				tgResults[i], tgResults[j] = tgResults[j], tgResults[i]
			}
		}
	}

	// Take top N
	if len(tgResults) > limit {
		tgResults = tgResults[:limit]
	}

	// Build response with labels
	for _, tgr := range tgResults {
		var item StatsTopTalkgroup
		item.SystemId = tgr.key.system
		item.TalkgroupId = tgr.key.talkgroup
		item.Count = tgr.count

		// Get labels from controller
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
		result = append(result, item)
	}

	return result, nil
}

func (stats *Stats) GetTopSystems(db *Database, limit int) ([]StatsTopSystem, error) {
	stats.mutex.Lock()
	defer stats.mutex.Unlock()

	result := []StatsTopSystem{}

	weekAgo := time.Now().AddDate(0, 0, -7)

	// Get recent calls (15k calls/day max * 7 days * 1.5 safety = ~157k calls)
	rows, err := db.Sql.Query(`
		SELECT system, dateTime
		FROM rdioScannerCalls 
		ORDER BY dateTime DESC
		LIMIT 200000
	`)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("stats.topSystems: %v", err)
	}
	defer rows.Close()

	// Count calls per system
	sysCounts := make(map[uint]uint)

	for rows.Next() {
		var systemId uint
		var dateTimeStr string
		if err := rows.Scan(&systemId, &dateTimeStr); err == nil {
			if callTime, err := db.ParseDateTime(dateTimeStr); err == nil {
				if callTime.After(weekAgo) {
					sysCounts[systemId]++
				} else {
					if len(sysCounts) > 0 {
						break
					}
				}
			}
		}
	}

	// Convert map to slice and sort by count
	type sysResult struct {
		systemId uint
		count    uint
	}
	var sysResults []sysResult
	for systemId, count := range sysCounts {
		sysResults = append(sysResults, sysResult{systemId, count})
	}

	// Sort by count descending
	for i := 0; i < len(sysResults); i++ {
		for j := i + 1; j < len(sysResults); j++ {
			if sysResults[j].count > sysResults[i].count {
				sysResults[i], sysResults[j] = sysResults[j], sysResults[i]
			}
		}
	}

	// Take top N
	if len(sysResults) > limit {
		sysResults = sysResults[:limit]
	}

	// Build response with labels
	for _, sr := range sysResults {
		var item StatsTopSystem
		item.SystemId = sr.systemId
		item.Count = sr.count

		// Get label from controller
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

func (stats *Stats) GetTopUnits(db *Database, limit int) ([]StatsTopUnit, error) {
	stats.mutex.Lock()
	defer stats.mutex.Unlock()

	result := []StatsTopUnit{}

	weekAgo := time.Now().AddDate(0, 0, -7)

	// Get recent calls (15k calls/day max * 7 days * 1.5 safety = ~157k calls)
	rows, err := db.Sql.Query(`
		SELECT system, source, dateTime
		FROM rdioScannerCalls 
		WHERE source IS NOT NULL AND source > 0
		ORDER BY dateTime DESC
		LIMIT 200000
	`)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("stats.topUnits: %v", err)
	}
	defer rows.Close()

	// Count calls per unit
	type unitKey struct {
		system uint
		unit   uint
	}
	unitCounts := make(map[unitKey]uint)

	for rows.Next() {
		var systemId, unitId uint
		var dateTimeStr string
		if err := rows.Scan(&systemId, &unitId, &dateTimeStr); err == nil {
			if callTime, err := db.ParseDateTime(dateTimeStr); err == nil {
				if callTime.After(weekAgo) {
					key := unitKey{systemId, unitId}
					unitCounts[key]++
				} else {
					if len(unitCounts) > 0 {
						break
					}
				}
			}
		}
	}

	// Convert map to slice and sort by count
	type unitResult struct {
		key   unitKey
		count uint
	}
	var unitResults []unitResult
	for key, count := range unitCounts {
		unitResults = append(unitResults, unitResult{key, count})
	}

	// Sort by count descending
	for i := 0; i < len(unitResults); i++ {
		for j := i + 1; j < len(unitResults); j++ {
			if unitResults[j].count > unitResults[i].count {
				unitResults[i], unitResults[j] = unitResults[j], unitResults[i]
			}
		}
	}

	// Take top N
	if len(unitResults) > limit {
		unitResults = unitResults[:limit]
	}

	// Build response with labels
	for _, ur := range unitResults {
		var item StatsTopUnit
		item.SystemId = ur.key.system
		item.UnitId = ur.key.unit
		item.Count = ur.count

		// Get labels from controller
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

func (stats *Stats) GetLastHourTalkgroups(db *Database) ([]StatsLastHourTalkgroup, error) {
	stats.mutex.Lock()
	defer stats.mutex.Unlock()

	result := []StatsLastHourTalkgroup{}

	now := time.Now()
	hourAgo := now.Add(-1 * time.Hour)

	// Get recent calls (15k calls/day / 24 = ~625/hour max, use 2000 for safety)
	rows, err := db.Sql.Query(`
		SELECT system, talkgroup, dateTime
		FROM rdioScannerCalls 
		ORDER BY dateTime DESC
		LIMIT 2000
	`)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("stats.lastHourTalkgroups: %v", err)
	}
	defer rows.Close()

	// Count calls per talkgroup in the last hour
	type tgKey struct {
		system    uint
		talkgroup uint
	}
	tgCounts := make(map[tgKey]uint)
	tgLastCall := make(map[tgKey]time.Time)
	foundInLastHour := 0

	for rows.Next() {
		var systemId, talkgroupId uint
		var dateTimeStr string
		if err := rows.Scan(&systemId, &talkgroupId, &dateTimeStr); err == nil {
			// Parse the datetime with timezone
			if callTime, err := db.ParseDateTime(dateTimeStr); err == nil {
				// Check if call is within last hour using Unix timestamps for accuracy
				callUnix := callTime.Unix()
				hourAgoUnix := hourAgo.Unix()
				
				if callUnix >= hourAgoUnix {
					key := tgKey{systemId, talkgroupId}
					tgCounts[key]++
					if tgLastCall[key].IsZero() || callTime.After(tgLastCall[key]) {
						tgLastCall[key] = callTime
					}
					foundInLastHour++
				} else {
					// Since ordered by dateTime DESC, once we hit older calls we can stop
					if foundInLastHour > 0 {
						break
					}
				}
			}
		}
	}

	// Convert map to slice and sort by last call time
	type tgResult struct {
		key      tgKey
		count    uint
		lastCall time.Time
	}
	var tgResults []tgResult
	for key, count := range tgCounts {
		tgResults = append(tgResults, tgResult{key, count, tgLastCall[key]})
	}

	// Sort by last call time (most recent first)
	for i := 0; i < len(tgResults); i++ {
		for j := i + 1; j < len(tgResults); j++ {
			if tgResults[j].lastCall.After(tgResults[i].lastCall) {
				tgResults[i], tgResults[j] = tgResults[j], tgResults[i]
			}
		}
	}

	// Take top 20
	limit := 20
	if len(tgResults) < limit {
		limit = len(tgResults)
	}
	tgResults = tgResults[:limit]

	// Build response with labels
	for _, tgr := range tgResults {
		var item StatsLastHourTalkgroup
		item.SystemId = tgr.key.system
		item.TalkgroupId = tgr.key.talkgroup
		item.Count = tgr.count
		item.LastCall = tgr.lastCall.Format(db.DateTimeFormat)

		// Get labels from controller
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
		result = append(result, item)
	}

	return result, nil
}

func (stats *Stats) GetTalkgroupUnits(db *Database, systemId uint, talkgroupId uint) ([]StatsTalkgroupUnit, error) {
	stats.mutex.Lock()
	defer stats.mutex.Unlock()

	result := []StatsTalkgroupUnit{}

	hourAgo := time.Now().Add(-1 * time.Hour)

	// Get all calls for this system/talkgroup and parse them in Go
	rows, err := db.Sql.Query(`
		SELECT source, dateTime
		FROM rdioScannerCalls 
		WHERE system = ? AND talkgroup = ? AND source IS NOT NULL AND source > 0
		ORDER BY dateTime DESC
		LIMIT 500
	`, systemId, talkgroupId)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("stats.talkgroupUnits: %v", err)
	}
	defer rows.Close()

	// Count calls per unit in the last hour and track most recent call
	unitCounts := make(map[uint]uint)
	unitLastCall := make(map[uint]time.Time)
	foundInLastHour := 0

	for rows.Next() {
		var unitId uint
		var dateTimeStr string
		if err := rows.Scan(&unitId, &dateTimeStr); err == nil {
			// Parse the datetime with timezone
			if callTime, err := db.ParseDateTime(dateTimeStr); err == nil {
				// Check if call is within last hour
				if callTime.After(hourAgo) {
					unitCounts[unitId]++
					if unitLastCall[unitId].IsZero() || callTime.After(unitLastCall[unitId]) {
						unitLastCall[unitId] = callTime
					}
					foundInLastHour++
				} else {
					// Stop once we hit older calls
					if foundInLastHour > 0 {
						break
					}
				}
			}
		}
	}

	// Convert map to slice
	type unitResult struct {
		unitId   uint
		count    uint
		lastCall time.Time
	}
	var unitResults []unitResult
	for unitId, count := range unitCounts {
		unitResults = append(unitResults, unitResult{unitId, count, unitLastCall[unitId]})
	}

	// Sort by most recent call time (most recent first)
	for i := 0; i < len(unitResults); i++ {
		for j := i + 1; j < len(unitResults); j++ {
			if unitResults[j].lastCall.After(unitResults[i].lastCall) {
				unitResults[i], unitResults[j] = unitResults[j], unitResults[i]
			}
		}
	}

	// Take top 50
	limit := 50
	if len(unitResults) < limit {
		limit = len(unitResults)
	}
	unitResults = unitResults[:limit]

	// Build response with labels
	for _, ur := range unitResults {
		var item StatsTalkgroupUnit
		item.UnitId = ur.unitId
		item.Count = ur.count
		item.LastCall = ur.lastCall.Format(db.DateTimeFormat)

		// Get unit label from controller
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

func (stats *Stats) GetRecentActivity(db *Database) ([]StatsCallsByHour, error) {
	stats.mutex.Lock()
	defer stats.mutex.Unlock()

	// Get calls per hour for last 24 hours
	result := make([]StatsCallsByHour, 24)
	now := time.Now()
	
	for i := 23; i >= 0; i-- {
		result[23-i] = StatsCallsByHour{Hour: uint((now.Hour() - i + 24) % 24), Count: 0}
	}

	dayAgo := time.Now().Add(-24 * time.Hour)

	// Get recent calls (15k calls/day max * 1.5 safety = ~22k calls)
	rows, err := db.Sql.Query(`
		SELECT dateTime
		FROM rdioScannerCalls 
		ORDER BY dateTime DESC
		LIMIT 25000
	`)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("stats.recentActivity: %v", err)
	}
	defer rows.Close()

	hourCounts := make(map[uint]uint)
	for rows.Next() {
		var dateTimeStr string
		if err := rows.Scan(&dateTimeStr); err == nil {
			// Parse the datetime with timezone
			if callTime, err := db.ParseDateTime(dateTimeStr); err == nil {
				// Check if call is within last 24 hours
				if callTime.After(dayAgo) {
					// Convert to local time and get hour
					localTime := callTime.Local()
					hour := uint(localTime.Hour())
					hourCounts[hour]++
				} else {
					// Since ordered by dateTime DESC, once we hit older calls we can stop
					if len(hourCounts) > 0 {
						break
					}
				}
			}
		}
	}

	for i := range result {
		if count, ok := hourCounts[result[i].Hour]; ok {
			result[i].Count = count
		}
	}

	return result, nil
}

func (stats *Stats) Handler(w http.ResponseWriter, r *http.Request) {
	// Validate admin token
	t := stats.Controller.Admin.GetAuthorization(r)
	if !stats.Controller.Admin.ValidateToken(t) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	stats.handleStatsRequest(w, r)
}

func (stats *Stats) PublicHandler(w http.ResponseWriter, r *http.Request) {
	// Public endpoint - no authentication required
	stats.handleStatsRequest(w, r)
}

func (stats *Stats) handleStatsRequest(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		db := stats.Controller.Database

		response := StatsResponse{}

		// Get overview stats
		overview, err := stats.GetOverview(db)
		if err != nil {
			stats.Controller.Logs.LogEvent(LogLevelError, fmt.Sprintf("stats.handler: %v", err))
		} else {
			response.Overview = *overview
		}

		// Get calls by hour
		callsByHour, err := stats.GetCallsByHour(db)
		if err != nil {
			stats.Controller.Logs.LogEvent(LogLevelError, fmt.Sprintf("stats.handler: %v", err))
		} else {
			response.CallsByHour = callsByHour
		}

		// Get calls by day (last 30 days)
		callsByDay, err := stats.GetCallsByDay(db, 30)
		if err != nil {
			stats.Controller.Logs.LogEvent(LogLevelError, fmt.Sprintf("stats.handler: %v", err))
		} else {
			response.CallsByDay = callsByDay
		}

		// Get top talkgroups
		topTalkgroups, err := stats.GetTopTalkgroups(db, 10)
		if err != nil {
			stats.Controller.Logs.LogEvent(LogLevelError, fmt.Sprintf("stats.handler: %v", err))
		} else {
			response.TopTalkgroups = topTalkgroups
		}

		// Get top systems
		topSystems, err := stats.GetTopSystems(db, 10)
		if err != nil {
			stats.Controller.Logs.LogEvent(LogLevelError, fmt.Sprintf("stats.handler: %v", err))
		} else {
			response.TopSystems = topSystems
		}

		// Get top units
		topUnits, err := stats.GetTopUnits(db, 10)
		if err != nil {
			stats.Controller.Logs.LogEvent(LogLevelError, fmt.Sprintf("stats.handler: %v", err))
		} else {
			response.TopUnits = topUnits
		}

		// Get recent activity
		recentActivity, err := stats.GetRecentActivity(db)
		if err != nil {
			stats.Controller.Logs.LogEvent(LogLevelError, fmt.Sprintf("stats.handler: %v", err))
		} else {
			response.RecentActivity = recentActivity
		}

		// Get last hour talkgroups
		lastHourTalkgroups, err := stats.GetLastHourTalkgroups(db)
		if err != nil {
			stats.Controller.Logs.LogEvent(LogLevelError, fmt.Sprintf("stats.handler: %v", err))
		} else {
			response.LastHourTalkgroups = lastHourTalkgroups
		}

		// Send response
		w.Header().Set("Content-Type", "application/json")
		if b, err := json.Marshal(response); err == nil {
			w.Write(b)
		} else {
			w.WriteHeader(http.StatusInternalServerError)
		}

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (stats *Stats) TalkgroupUnitsHandler(w http.ResponseWriter, r *http.Request) {
	// Validate admin token
	t := stats.Controller.Admin.GetAuthorization(r)
	if !stats.Controller.Admin.ValidateToken(t) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	stats.handleTalkgroupUnitsRequest(w, r)
}

func (stats *Stats) PublicTalkgroupUnitsHandler(w http.ResponseWriter, r *http.Request) {
	// Public endpoint - no authentication required
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

