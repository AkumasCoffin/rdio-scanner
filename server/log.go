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
	"log"
	"math"
	"strings"
	"sync"
	"time"
)

const (
	LogLevelInfo  = "info"
	LogLevelWarn  = "warn"
	LogLevelError = "error"
)

type Log struct {
	Id       any       `json:"_id"`
	DateTime time.Time `json:"dateTime"`
	Level    string    `json:"level"`
	Message  string    `json:"message"`
}

type Logs struct {
	database *Database
	mutex    sync.Mutex
	daemon   *Daemon
}

func NewLogs() *Logs {
	return &Logs{
		mutex: sync.Mutex{},
	}
}

func (logs *Logs) LogEvent(level string, message string) error {
	logs.mutex.Lock()
	defer logs.mutex.Unlock()

	if logs.daemon != nil {
		switch level {
		case LogLevelError:
			logs.daemon.Logger.Error(message)
		case LogLevelWarn:
			logs.daemon.Logger.Warning(message)
		case LogLevelInfo:
			logs.daemon.Logger.Info(message)
		}

	} else {
		log.Println(message)
	}

	if logs.database != nil {
		l := Log{
			DateTime: time.Now().UTC(),
			Level:    level,
			Message:  message,
		}

		if _, err := logs.database.Exec("insert into `rdioScannerLogs` (`dateTime`, `level`, `message`) values (?, ?, ?)", l.DateTime, l.Level, l.Message); err != nil {
			return fmt.Errorf("logs.logevent: %v", err)
		}
	}

	return nil
}

func (logs *Logs) Prune(db *Database, pruneDays uint) error {
	logs.mutex.Lock()
	defer logs.mutex.Unlock()

	date := time.Now().Add(-24 * time.Hour * time.Duration(pruneDays)).Format(db.DateTimeFormat)
	_, err := db.Exec("delete from `rdioScannerLogs` where `dateTime` < ?", date)

	return err
}

// PruneToCount enforces a hard cap on the number of log rows, keeping only the
// newest maxRows by _id (autoincrement, so chronological). This bounds the
// table regardless of the time-based retention or the log generation rate — a
// burst (e.g. a bot flood faster than the day-based prune can react) can't grow
// the table without limit. maxRows == 0 disables the cap.
//
// The threshold (_id of the oldest row we keep) is found via OFFSET on the
// primary-key index, then everything older is deleted in one statement. The
// nested derived table (`tmp`) is required so MySQL doesn't reject deleting
// from a table referenced in its own subquery (error 1093); Postgres/SQLite
// accept it too.
func (logs *Logs) PruneToCount(db *Database, maxRows uint) error {
	if maxRows == 0 {
		return nil
	}

	logs.mutex.Lock()
	defer logs.mutex.Unlock()

	query := fmt.Sprintf("delete from `rdioScannerLogs` where `_id` < (select `x` from (select `_id` as `x` from `rdioScannerLogs` order by `_id` desc limit 1 offset %d) as `tmp`)", maxRows-1)
	_, err := db.Exec(query)

	return err
}

// logCountCap bounds the admin logs count(*) so the query stays fast on very
// large or bloated tables. When the real row count exceeds this, the paginator
// reports the cap; recent logs still page correctly and filters narrow further.
const logCountCap = 100000

// logCategoryPatterns maps an admin-facing log category to the set of SQL
// LIKE patterns whose union defines that category. Patterns are matched
// against the message column; a category matches if ANY of its patterns do.
// Keep these in sync with the LogEvent message strings they target.
var logCategoryPatterns = map[string][]string{
	// Listeners joining / leaving the live feed.
	"connections": {"new listener%", "listener disconnected%"},
	// Access denials and login attempts (listener access codes + admin login).
	"access": {"invalid access code%", "locked access%", "expired access%", "too many concurrent%", "invalid login%", "too many login%"},
	// Transcription lifecycle: transcribed/received/applied/deferred/skipped/failed.
	"transcription": {"transcrib%", "transcript%"},
	// Share-link / call-fetch requests (CAL websocket command).
	"sharelink": {"CAL request%"},
	// Configuration and admin account changes.
	"config": {"configuration%", "admin password%", "admin.%"},
	// Server lifecycle and background jobs.
	"lifecycle": {"server started%", "database pruning%", "listeners count%", "delayer%", "dirwatch%", "scheduler%"},
}

func (logs *Logs) Search(searchOptions *LogsSearchOptions, db *Database) (*LogsSearchResults, error) {
	const (
		ascOrder  = "asc"
		descOrder = "desc"
	)

	var (
		args     []any
		dateTime any
		err      error
		id       sql.NullFloat64
		limit    uint
		offset   uint
		order    string
		query    string
		rows     *sql.Rows
		where    string = "true"
	)

	// NOTE: Search intentionally does NOT take logs.mutex. It is read-only,
	// and the mutex is held by LogEvent for the duration of every log write.
	// When the server is busy writing logs (e.g. a burst of share-link CAL
	// requests), taking the mutex here starved the admin query until it hit
	// the gateway timeout (HTTP 524). The DB connection pool already provides
	// the concurrency safety the read needs.

	formatError := func(err error) error {
		return fmt.Errorf("logs.search: %v", err)
	}

	logResults := &LogsSearchResults{
		Options: searchOptions,
		Logs:    []Log{},
	}

	switch v := searchOptions.Level.(type) {
	case string:
		where += " and `level` = ?"
		args = append(args, v)
	}

	switch v := searchOptions.Category.(type) {
	case string:
		if patterns, ok := logCategoryPatterns[v]; ok && len(patterns) > 0 {
			likes := make([]string, 0, len(patterns))
			for _, p := range patterns {
				likes = append(likes, "`message` like ?")
				args = append(args, p)
			}
			where += " and (" + strings.Join(likes, " or ") + ")"
		}
	}

	switch v := searchOptions.Search.(type) {
	case string:
		if s := strings.TrimSpace(v); s != "" {
			where += " and `message` like ?"
			args = append(args, "%"+s+"%")
		}
	}

	switch v := searchOptions.Sort.(type) {
	case int:
		if v < 0 {
			order = descOrder
		} else {
			order = ascOrder
		}
	default:
		order = ascOrder
	}

	switch v := searchOptions.Date.(type) {
	case time.Time:
		var (
			df    string = db.DateTimeFormat
			start time.Time
			stop  time.Time
		)

		if order == ascOrder {
			start = time.Date(v.Year(), v.Month(), v.Day(), v.Hour(), v.Minute(), 0, 0, time.UTC)
			stop = start.Add(time.Hour*24 - time.Millisecond)

		} else {
			start = time.Date(v.Year(), v.Month(), v.Day(), v.Hour(), v.Minute(), 0, 0, time.UTC).Add(time.Hour*-24 - time.Hour*time.Duration(v.Hour())).Add(time.Minute * time.Duration(-v.Minute()))
			stop = start.Add(time.Hour*24 - time.Millisecond - time.Hour*time.Duration(v.Hour())).Add(time.Minute * time.Duration(-v.Minute()))
		}

		where += " and (`dateTime` between ? and ?)"
		args = append(args, start.Format(df), stop.Format(df))
	}

	switch v := searchOptions.Limit.(type) {
	case uint:
		limit = uint(math.Min(float64(500), float64(v)))
	default:
		limit = 200
	}

	switch v := searchOptions.Offset.(type) {
	case uint:
		offset = v
	}

	// Date-picker bounds reflect the WHOLE log table, not the current filter —
	// you want to be able to jump to any date in the history regardless of the
	// active level/category/search. Computing them unfiltered also keeps them
	// cheap: a single min/max the index answers instantly, instead of two
	// filtered ordered scans that crawl on a large table (a filtered probe for
	// a sparse category was a big contributor to the /api/admin/logs hangs).
	var dtMin, dtMax any
	query = "select min(`dateTime`), max(`dateTime`) from `rdioScannerLogs`"
	if err = db.QueryRow(query).Scan(&dtMin, &dtMax); err != nil && err != sql.ErrNoRows {
		return nil, formatError(fmt.Errorf("%v, %v", err, query))
	}

	if t, err := db.ParseDateTime(dtMin); err == nil {
		logResults.DateStart = t
	}

	if t, err := db.ParseDateTime(dtMax); err == nil {
		logResults.DateStop = t
	}

	// Count. An exact `count(*)` over the whole table is a full scan that, on a
	// busy/bloated Postgres logs table (constant inserts + hourly bulk prune
	// leaves many dead tuples), runs long enough to trip the reverse-proxy
	// timeout (the 502/524 on /api/admin/logs).
	//
	// `where == "true"` means no filter was applied (the unfiltered initial
	// page load — the case that was timing out). On Postgres we take the
	// planner's instant row estimate there instead of scanning. For filtered
	// queries (which narrow the set) and other databases, count inside a
	// LIMITed subquery so the work is capped at logCountCap rows.
	logResults.Count = 0
	if where == "true" && db.Config.DbType == DbTypePostgres {
		var est sql.NullFloat64
		query = "select reltuples from pg_class where relname = 'rdioScannerLogs'"
		if err = db.QueryRow(query).Scan(&est); err != nil && err != sql.ErrNoRows {
			return nil, formatError(fmt.Errorf("%v, %v", err, query))
		}
		if est.Valid && est.Float64 > 0 {
			logResults.Count = uint(est.Float64)
		}
	}

	// Fallback / filtered path (also covers Postgres before its first ANALYZE,
	// where reltuples is -1/0 and the estimate above is left at zero).
	if logResults.Count == 0 {
		query = fmt.Sprintf("select count(*) from (select 1 from `rdioScannerLogs` where %v limit %v) as sub", where, logCountCap)
		if err = db.QueryRow(query, args...).Scan(&logResults.Count); err != nil && err != sql.ErrNoRows {
			return nil, formatError(fmt.Errorf("%v, %v", err, query))
		}
	}

	query = fmt.Sprintf("select `_id`, `dateTime`, `level`, `message` from `rdioScannerLogs` where %v order by `dateTime` %v limit %v offset %v", where, order, limit, offset)
	if rows, err = db.Query(query, args...); err != nil {
		return nil, formatError(fmt.Errorf("%v, %v", err, query))
	}
	defer rows.Close()

	for rows.Next() {
		log := Log{}

		if err = rows.Scan(&id, &dateTime, &log.Level, &log.Message); err != nil {
			break
		}

		if id.Valid && id.Float64 > 0 {
			log.Id = uint(id.Float64)
		}

		if t, err := db.ParseDateTime(dateTime); err == nil {
			log.DateTime = t
		} else {
			continue
		}

		logResults.Logs = append(logResults.Logs, log)
	}

	if err != nil {
		return nil, formatError(err)
	}

	return logResults, nil
}

func (logs *Logs) setDaemon(d *Daemon) {
	logs.daemon = d
}

func (logs *Logs) setDatabase(d *Database) {
	logs.database = d
}

type LogsSearchOptions struct {
	Category any `json:"category,omitempty"`
	Date     any `json:"date,omitempty"`
	Level    any `json:"level,omitempty"`
	Limit    any `json:"limit,omitempty"`
	Offset   any `json:"offset,omitempty"`
	Search   any `json:"search,omitempty"`
	Sort     any `json:"sort,omitempty"`
}

func NewLogSearchOptions() *LogsSearchOptions {
	return &LogsSearchOptions{}
}

func (searchOptions *LogsSearchOptions) FromMap(m map[string]any) *LogsSearchOptions {
	switch v := m["category"].(type) {
	case string:
		searchOptions.Category = v
	}

	switch v := m["search"].(type) {
	case string:
		searchOptions.Search = v
	}

	switch v := m["date"].(type) {
	case string:
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			searchOptions.Date = t
		}
	}

	switch v := m["level"].(type) {
	case string:
		searchOptions.Level = v
	}

	switch v := m["limit"].(type) {
	case float64:
		searchOptions.Limit = uint(v)
	}

	switch v := m["offset"].(type) {
	case float64:
		searchOptions.Offset = uint(v)
	}

	switch v := m["sort"].(type) {
	case float64:
		searchOptions.Sort = int(v)
	}

	return searchOptions
}

type LogsSearchResults struct {
	Count     uint               `json:"count"`
	DateStart time.Time          `json:"dateStart"`
	DateStop  time.Time          `json:"dateStop"`
	Options   *LogsSearchOptions `json:"options"`
	Logs      []Log              `json:"logs"`
}
