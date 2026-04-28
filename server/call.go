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
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
)

type Call struct {
	Id             any       `json:"id"`
	Audio          []byte    `json:"audio"`
	AudioName      any       `json:"audioName"`
	AudioType      any       `json:"audioType"`
	DateTime       time.Time `json:"dateTime"`
	Frequencies    any       `json:"frequencies"`
	Frequency      any       `json:"frequency"`
	Patches        any       `json:"patches"`
	Source         any       `json:"source"`
	Sources        any       `json:"sources"`
	System         uint      `json:"system"`
	Talkgroup      uint      `json:"talkgroup"`
	Transcript     any       `json:"transcript,omitempty"`
	systemLabel    any
	talkgroupGroup any
	talkgroupLabel any
	talkgroupName  any
	talkgroupTag   any
	units          any
	apiKeyIdent    string
}

func NewCall() *Call {
	return &Call{
		Frequencies: []map[string]any{},
		Patches:     []uint{},
		Sources:     []map[string]any{},
	}
}

func (call *Call) IsValid() (ok bool, err error) {
	ok = true

	if len(call.Audio) <= 44 {
		ok = false
		err = errors.New("no audio")
	}

	if call.DateTime.Unix() == 0 {
		ok = false
		err = errors.New("no datetime")
	}

	if call.System < 1 {
		ok = false
		err = errors.New("no system")
	}

	if call.Talkgroup < 1 {
		ok = false
		err = errors.New("no talkgroup")
	}

	return ok, err
}

func (call *Call) MarshalJSON() ([]byte, error) {
	audio := fmt.Sprintf("%v", call.Audio)
	audio = strings.ReplaceAll(audio, " ", ",")

	out := map[string]any{
		"id": call.Id,
		"audio": map[string]any{
			"data": json.RawMessage(audio),
			"type": "Buffer",
		},
		"audioName":   call.AudioName,
		"audioType":   call.AudioType,
		"dateTime":    call.DateTime.Format(time.RFC3339),
		"frequencies": call.Frequencies,
		"frequency":   call.Frequency,
		"patches":     call.Patches,
		"source":      call.Source,
		"sources":     call.Sources,
		"system":      call.System,
		"talkgroup":   call.Talkgroup,
	}

	if call.Transcript != nil {
		out["transcript"] = call.Transcript
	}

	return json.Marshal(out)
}

func (call *Call) ToJson() (string, error) {
	if b, err := json.Marshal(call); err == nil {
		return string(b), nil
	} else {
		return "", fmt.Errorf("call.tojson: %v", err)
	}
}

type Calls struct {
	mutex     sync.Mutex
	metaMutex sync.Mutex
	metaCache map[string]*callsSearchMeta
}

type callsSearchMeta struct {
	dateStart time.Time
	dateStop  time.Time
	count     uint
	expires   time.Time
}

const callsSearchMetaTTL = 15 * time.Second

func NewCalls() *Calls {
	return &Calls{
		mutex:     sync.Mutex{},
		metaMutex: sync.Mutex{},
		metaCache: make(map[string]*callsSearchMeta),
	}
}

func (calls *Calls) InvalidateSearchMeta() {
	calls.metaMutex.Lock()
	calls.metaCache = make(map[string]*callsSearchMeta)
	calls.metaMutex.Unlock()
}

func (calls *Calls) getSearchMeta(key string) (*callsSearchMeta, bool) {
	calls.metaMutex.Lock()
	defer calls.metaMutex.Unlock()
	m, ok := calls.metaCache[key]
	if !ok || time.Now().After(m.expires) {
		return nil, false
	}
	return m, true
}

func (calls *Calls) putSearchMeta(key string, m *callsSearchMeta) {
	calls.metaMutex.Lock()
	defer calls.metaMutex.Unlock()
	calls.metaCache[key] = m
}

func (calls *Calls) CheckDuplicate(call *Call, msTimeFrame uint, db *Database) bool {
	var count uint

	// Read-only — rely on the database driver's own concurrency guard.
	d := time.Duration(msTimeFrame) * time.Millisecond
	from := call.DateTime.Add(-d)
	to := call.DateTime.Add(d)

	query := fmt.Sprintf("select count(*) from `rdioScannerCalls` where (`dateTime` between '%v' and '%v') and `system` = %v and `talkgroup` = %v", from, to, call.System, call.Talkgroup)
	if err := db.QueryRow(query).Scan(&count); err != nil {
		return false
	}

	return count > 0
}

func (calls *Calls) GetCall(id uint, db *Database) (*Call, error) {
	var (
		audioName   sql.NullString
		audioType   sql.NullString
		dateTime    any
		frequency   sql.NullFloat64
		source      sql.NullFloat64
		frequencies string
		patches     string
		sources     string
		t           time.Time
		transcript  sql.NullString
	)

	// No mutex here: database/sql is already goroutine-safe, and holding a
	// global lock around the read (which includes the audio blob transfer,
	// 50–200 KB) means click-to-play has to queue behind every in-flight
	// WriteCall from the ingest path. Removing the lock drops that queue.
	call := Call{Id: id}

	query := fmt.Sprintf("select `audio`, `audioName`, `audioType`, `dateTime`, `frequencies`, `frequency`, `patches`, `source`, `sources`, `system`, `talkgroup`, `transcript` from `rdioScannerCalls` where `id` = %v", id)
	err := db.QueryRow(query).Scan(&call.Audio, &audioName, &audioType, &dateTime, &frequencies, &frequency, &patches, &source, &sources, &call.System, &call.Talkgroup, &transcript)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("getcall: %v, %v", err, query)
	}

	if audioName.Valid {
		call.AudioName = audioName.String
	}

	if audioType.Valid {
		call.AudioType = audioType.String
	}

	if transcript.Valid {
		call.Transcript = transcript.String
	}

	if frequency.Valid && frequency.Float64 > 0 {
		call.Frequency = uint(frequency.Float64)
	}

	if t, err = db.ParseDateTime(dateTime); err == nil {
		call.DateTime = t
	} else {
		call.DateTime = time.Time{}
	}

	if len(frequencies) > 0 {
		if err = json.Unmarshal([]byte(frequencies), &call.Frequencies); err != nil {
			call.Frequencies = []any{}
		}
	}

	if len(patches) > 0 {
		if err = json.Unmarshal([]byte(patches), &call.Patches); err != nil {
			call.Patches = []any{}
		}
	}

	if source.Valid && source.Float64 > 0 {
		call.Source = uint(source.Float64)
	}

	if len(sources) > 0 {
		if err = json.Unmarshal([]byte(sources), &call.Sources); err != nil {
			call.Sources = []any{}
		}
	}

	return &call, nil
}

func (calls *Calls) GetTranscript(id uint, db *Database) (system uint, talkgroup uint, transcript string, err error) {
	var t sql.NullString

	err = db.QueryRow("select `system`, `talkgroup`, `transcript` from `rdioScannerCalls` where `id` = ?", id).Scan(&system, &talkgroup, &t)
	if err == sql.ErrNoRows {
		err = nil
		return
	}
	if err != nil {
		return
	}
	if t.Valid {
		transcript = t.String
	}
	return
}

func (calls *Calls) UpdateTranscript(id uint, transcript string, db *Database) error {
	_, err := db.Exec("update `rdioScannerCalls` set `transcript` = ? where `id` = ?", transcript, id)
	return err
}

func (calls *Calls) Prune(db *Database, pruneDays uint) error {
	date := time.Now().Add(-24 * time.Hour * time.Duration(pruneDays)).Format(db.DateTimeFormat)
	_, err := db.Exec("delete from `rdioScannerCalls` where `dateTime` < ?", date)

	return err
}

func (calls *Calls) Search(searchOptions *CallsSearchOptions, client *Client) (*CallsSearchResults, error) {
	const (
		ascOrder  = "asc"
		descOrder = "desc"
	)

	var (
		dateTime any
		err      error
		id       sql.NullFloat64
		limit    uint
		offset   uint
		order    string
		query    string
		rows     *sql.Rows
		t        time.Time
		where    string = "true"
	)

	// Read-only aggregate; no need to serialize behind ingests.
	db := client.Controller.Database

	formatError := func(err error) error {
		return fmt.Errorf("calls.search: %v", err)
	}

	searchResults := &CallsSearchResults{
		Options: searchOptions,
		Results: []CallsSearchResult{},
	}

	if client.Access != nil {
		switch v := client.Access.Systems.(type) {
		case []any:
			a := []string{}
			for _, scope := range v {
				var c string
				switch v := scope.(type) {
				case map[string]any:
					switch v["talkgroups"].(type) {
					case []any:
						b := strings.ReplaceAll(fmt.Sprintf("%v", v["talkgroups"]), " ", ", ")
						b = strings.ReplaceAll(b, "[", "(")
						b = strings.ReplaceAll(b, "]", ")")
						c = fmt.Sprintf("(`system` = %v and `talkgroup` in %v)", v["id"], b)
					case string:
						if v["talkgroups"] == "*" {
							c = fmt.Sprintf("`system` = %v", v["id"])
						}
					}
				}
				if len(c) > 0 {
					a = append(a, c)
				}
			}
			where = fmt.Sprintf("(%s)", strings.Join(a, " or "))
		}
	}

	switch v := searchOptions.System.(type) {
	case uint:
		a := []string{
			fmt.Sprintf("`system` = %v", v),
		}
		switch v := searchOptions.Talkgroup.(type) {
		case uint:
			if searchOptions.searchPatchedTalkgroups {
				a = append(a, fmt.Sprintf("`talkgroup` = %v or patches = '%v' or patches like '[%v,%%' or patches like '%%,%v,%%' or patches like '%%,%v]'", v, v, v, v, v))
			} else {
				a = append(a, fmt.Sprintf("`talkgroup` = %v", v))
			}
		}
		where += fmt.Sprintf(" and (%s)", strings.Join(a, " and "))
	}

	switch v := searchOptions.Group.(type) {
	case string:
		a := []string{}
		for id, m := range client.GroupsMap[v] {
			b := strings.ReplaceAll(fmt.Sprintf("%v", m), " ", ", ")
			b = strings.ReplaceAll(b, "[", "(")
			b = strings.ReplaceAll(b, "]", ")")
			a = append(a, fmt.Sprintf("(`system` = %v and `talkgroup` in %v)", id, b))
		}
		if len(a) > 0 {
			where += fmt.Sprintf(" and (%s)", strings.Join(a, " or "))
		}
	}

	switch v := searchOptions.Tag.(type) {
	case string:
		a := []string{}
		for id, m := range client.TagsMap[v] {
			b := strings.ReplaceAll(fmt.Sprintf("%v", m), " ", ", ")
			b = strings.ReplaceAll(b, "[", "(")
			b = strings.ReplaceAll(b, "]", ")")
			a = append(a, fmt.Sprintf("(`system` = %v and `talkgroup` in %v)", id, b))
		}
		if len(a) > 0 {
			where += fmt.Sprintf(" and (%s)", strings.Join(a, " or "))
		}
	}

	if q, ok := searchOptions.Q.(string); ok && q != "" {
		esc := strings.ReplaceAll(q, "'", "''")
		op := "like"
		if db.Config.DbType == DbTypePostgres {
			op = "ilike"
		}
		where += fmt.Sprintf(" and `transcript` %s '%%%s%%'", op, esc)
	}

	rangeKey := "range:" + where
	if cached, ok := calls.getSearchMeta(rangeKey); ok {
		searchResults.DateStart = cached.dateStart
		searchResults.DateStop = cached.dateStop
	} else {
		query = fmt.Sprintf("select `dateTime` from `rdioScannerCalls` where %v order by `dateTime` asc limit 1", where)
		if err = db.QueryRow(query).Scan(&dateTime); err != nil && err != sql.ErrNoRows {
			return nil, formatError(fmt.Errorf("%v, %v", err, query))
		}

		if t, err = db.ParseDateTime(dateTime); err == nil {
			searchResults.DateStart = t
		}

		query = fmt.Sprintf("select `dateTime` from `rdioScannerCalls` where %v order by `dateTime` desc limit 1", where)
		if err = db.QueryRow(query).Scan(&dateTime); err != nil && err != sql.ErrNoRows {
			return nil, formatError(fmt.Errorf("%v, %v", err, query))
		}

		if t, err = db.ParseDateTime(dateTime); err == nil {
			searchResults.DateStop = t
		} else {
			searchResults.DateStop = time.Now()
		}

		calls.putSearchMeta(rangeKey, &callsSearchMeta{
			dateStart: searchResults.DateStart,
			dateStop:  searchResults.DateStop,
			expires:   time.Now().Add(callsSearchMetaTTL),
		})
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
			df    string = client.Controller.Database.DateTimeFormat
			start time.Time
			stop  time.Time
		)

		if order == ascOrder {
			start = time.Date(v.Year(), v.Month(), v.Day(), v.Hour(), v.Minute(), 0, 0, time.UTC)
			stop = start.Add(time.Hour*24 - time.Millisecond)

		} else {
			start = time.Date(v.Year(), v.Month(), v.Day(), v.Hour(), v.Minute(), 0, 0, time.UTC).Add(time.Hour*-24 + time.Millisecond)
			stop = time.Date(v.Year(), v.Month(), v.Day(), v.Hour(), v.Minute(), 0, 0, time.UTC)
		}

		where += fmt.Sprintf(" and (`dateTime` between '%v' and '%v')", start.Format(df), stop.Format(df))
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

	countKey := "count:" + where
	if cached, ok := calls.getSearchMeta(countKey); ok {
		searchResults.Count = cached.count
	} else {
		query = fmt.Sprintf("select count(*) from `rdioScannerCalls` where %v", where)
		if err = db.QueryRow(query).Scan(&searchResults.Count); err != nil && err != sql.ErrNoRows {
			return nil, formatError(fmt.Errorf("%v, %v", err, query))
		}
		calls.putSearchMeta(countKey, &callsSearchMeta{
			count:   searchResults.Count,
			expires: time.Now().Add(callsSearchMetaTTL),
		})
	}

	query = fmt.Sprintf("select `id`, `dateTime`, `system`, `talkgroup`, `transcript` from `rdioScannerCalls` where %v order by `dateTime` %v limit %v offset %v", where, order, limit, offset)
	if rows, err = db.Query(query); err != nil && err != sql.ErrNoRows {
		return nil, formatError(fmt.Errorf("%v, %v", err, query))
	}

	for rows.Next() {
		searchResult := CallsSearchResult{}
		var transcript sql.NullString
		if err = rows.Scan(&id, &dateTime, &searchResult.System, &searchResult.Talkgroup, &transcript); err != nil {
			break
		}

		if id.Valid && id.Float64 > 0 {
			searchResult.Id = uint(id.Float64)
		}

		if t, err = db.ParseDateTime(dateTime); err == nil {
			searchResult.DateTime = t

		} else {
			continue
		}

		if transcript.Valid && transcript.String != "" {
			searchResult.Transcript = transcript.String
			searchResult.HasTranscript = true
		}

		searchResults.Results = append(searchResults.Results, searchResult)
	}

	rows.Close()

	if err != nil {
		return nil, formatError(err)
	}

	return searchResults, err
}

func (calls *Calls) WriteCall(call *Call, db *Database) (uint, error) {
	var (
		b           []byte
		err         error
		frequencies string
		id          int64
		patches     string
		res         sql.Result
		sources     string
	)

	calls.mutex.Lock()
	defer calls.mutex.Unlock()

	formatError := func(err error) error {
		return fmt.Errorf("call.write: %s", err.Error())
	}

	switch v := call.Frequencies.(type) {
	case []map[string]any:
		if b, err = json.Marshal(v); err == nil {
			frequencies = string(b)
		} else {
			return 0, formatError(err)
		}
	}

	switch v := call.Patches.(type) {
	case []uint:
		if b, err = json.Marshal(v); err == nil {
			patches = string(b)
		} else {
			return 0, formatError(err)
		}
	}

	switch v := call.Sources.(type) {
	case []map[string]any:
		if b, err = json.Marshal(v); err == nil {
			sources = string(b)
		} else {
			return 0, formatError(err)
		}
	}

	if db.Config.DbType == DbTypePostgres {
		err = db.QueryRow("insert into `rdioScannerCalls` (`audio`, `audioName`, `audioType`, `dateTime`, `frequencies`, `frequency`, `patches`, `source`, `sources`, `system`, `talkgroup`) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) returning `id`",
			call.Audio, call.AudioName, call.AudioType, call.DateTime, frequencies, call.Frequency, patches, call.Source, sources, call.System, call.Talkgroup).Scan(&id)
		if err != nil {
			return 0, formatError(err)
		}
		return uint(id), nil
	}

	if res, err = db.Exec("insert into `rdioScannerCalls` (`id`, `audio`, `audioName`, `audioType`, `dateTime`, `frequencies`, `frequency`, `patches`, `source`, `sources`, `system`, `talkgroup`) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", call.Id, call.Audio, call.AudioName, call.AudioType, call.DateTime, frequencies, call.Frequency, patches, call.Source, sources, call.System, call.Talkgroup); err != nil {
		return 0, formatError(err)
	}

	if id, err = res.LastInsertId(); err == nil {
		return uint(id), nil
	} else {
		return 0, formatError(err)
	}
}

// WarmSearchMeta populates the unscoped metadata cache with dateStart,
// dateStop, and count(*) so the first search request doesn't pay the
// cold-start penalty. Safe to call in a goroutine.
func (calls *Calls) WarmSearchMeta(db *Database) {
	const where = "true"
	var (
		dateTime any
		t        time.Time
	)

	startQuery := fmt.Sprintf("select `dateTime` from `rdioScannerCalls` where %s order by `dateTime` asc limit 1", where)
	var start time.Time
	if err := db.QueryRow(startQuery).Scan(&dateTime); err == nil {
		if t, err = db.ParseDateTime(dateTime); err == nil {
			start = t
		}
	}

	stopQuery := fmt.Sprintf("select `dateTime` from `rdioScannerCalls` where %s order by `dateTime` desc limit 1", where)
	var stop time.Time
	if err := db.QueryRow(stopQuery).Scan(&dateTime); err == nil {
		if t, err = db.ParseDateTime(dateTime); err == nil {
			stop = t
		}
	}
	if stop.IsZero() {
		stop = time.Now()
	}

	calls.putSearchMeta("range:"+where, &callsSearchMeta{
		dateStart: start,
		dateStop:  stop,
		expires:   time.Now().Add(callsSearchMetaTTL),
	})

	var count uint
	countQuery := fmt.Sprintf("select count(*) from `rdioScannerCalls` where %s", where)
	if err := db.QueryRow(countQuery).Scan(&count); err == nil {
		calls.putSearchMeta("count:"+where, &callsSearchMeta{
			count:   count,
			expires: time.Now().Add(callsSearchMetaTTL),
		})
	}
}

type CallsSearchOptions struct {
	Date                    any `json:"date,omitempty"`
	Group                   any `json:"group,omitempty"`
	Limit                   any `json:"limit,omitempty"`
	Offset                  any `json:"offset,omitempty"`
	Q                       any `json:"q,omitempty"`
	Sort                    any `json:"sort,omitempty"`
	System                  any `json:"system,omitempty"`
	Tag                     any `json:"tag,omitempty"`
	Talkgroup               any `json:"talkgroup,omitempty"`
	searchPatchedTalkgroups bool
}

func (searchOptions *CallsSearchOptions) fromMap(m map[string]any) error {
	switch v := m["date"].(type) {
	case string:
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			searchOptions.Date = t
		}
	}

	switch v := m["group"].(type) {
	case string:
		searchOptions.Group = v
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

	switch v := m["system"].(type) {
	case float64:
		searchOptions.System = uint(v)
	}

	switch v := m["tag"].(type) {
	case string:
		searchOptions.Tag = v
	}

	switch v := m["talkgroup"].(type) {
	case float64:
		searchOptions.Talkgroup = uint(v)
	}

	switch v := m["q"].(type) {
	case string:
		s := strings.TrimSpace(v)
		if s != "" {
			searchOptions.Q = s
		}
	}

	return nil
}

type CallsSearchResult struct {
	Id            uint      `json:"id"`
	DateTime      time.Time `json:"dateTime"`
	System        uint      `json:"system"`
	Talkgroup     uint      `json:"talkgroup"`
	HasTranscript bool      `json:"hasTranscript,omitempty"`
	Transcript    string    `json:"transcript,omitempty"`
}

type CallsSearchResults struct {
	Count     uint                `json:"count"`
	DateStart time.Time           `json:"dateStart"`
	DateStop  time.Time           `json:"dateStop"`
	Options   *CallsSearchOptions `json:"options"`
	Results   []CallsSearchResult `json:"results"`
}
