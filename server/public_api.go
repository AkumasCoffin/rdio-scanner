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
	"strconv"
	"strings"
	"time"
)

type PublicApi struct {
	Controller *Controller
}

func NewPublicApi(controller *Controller) *PublicApi {
	return &PublicApi{Controller: controller}
}

// CallsRouter dispatches /api/v1/calls and /api/v1/calls/<id>{,/transcript,/audio}.
func (p *PublicApi) CallsRouter(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	key, ok := p.extractApiKey(r)
	if !ok {
		p.writeError(w, http.StatusUnauthorized, "missing api key")
		return
	}

	apikey, ok := p.Controller.Apikeys.GetApikey(key)
	if !ok {
		p.writeError(w, http.StatusUnauthorized, "invalid api key")
		return
	}

	const prefix = "/api/v1/calls"
	path := strings.TrimPrefix(r.URL.Path, prefix)

	switch {
	case path == "" || path == "/":
		p.listCalls(w, r, apikey)
		return
	}

	// /<id> or /<id>/transcript or /<id>/audio
	rest := strings.TrimPrefix(path, "/")
	parts := strings.SplitN(rest, "/", 2)
	idVal, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil || idVal == 0 {
		p.writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	id := uint(idVal)

	action := ""
	if len(parts) == 2 {
		action = strings.Trim(parts[1], "/")
	}

	switch action {
	case "":
		p.getCall(w, id, apikey)
	case "transcript":
		p.getTranscript(w, id, apikey)
	case "audio":
		p.getAudio(w, id, apikey)
	default:
		p.writeError(w, http.StatusNotFound, "unknown endpoint")
	}
}

func (p *PublicApi) extractApiKey(r *http.Request) (string, bool) {
	if v := r.Header.Get("X-API-Key"); v != "" {
		return strings.TrimSpace(v), true
	}
	if v := r.Header.Get("Authorization"); v != "" {
		v = strings.TrimSpace(v)
		if strings.HasPrefix(strings.ToLower(v), "bearer ") {
			return strings.TrimSpace(v[7:]), true
		}
		return v, true
	}
	if v := r.URL.Query().Get("key"); v != "" {
		return strings.TrimSpace(v), true
	}
	return "", false
}

func (p *PublicApi) writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	b, _ := json.Marshal(map[string]any{"error": msg})
	w.Write(b)
}

// listCalls builds the WHERE clause from (apikey scope, query params) and
// returns {count, results: [{id,dateTime,system,talkgroup,hasTranscript,transcript?}]}.
func (p *PublicApi) listCalls(w http.ResponseWriter, r *http.Request, apikey *Apikey) {
	q := r.URL.Query()

	where := p.apikeyWhere(apikey)

	if s := q.Get("system"); s != "" {
		if n, err := strconv.ParseUint(s, 10, 64); err == nil {
			where = append(where, fmt.Sprintf("`system` = %d", n))
		}
	}
	if s := q.Get("talkgroup"); s != "" {
		if n, err := strconv.ParseUint(s, 10, 64); err == nil {
			where = append(where, fmt.Sprintf("`talkgroup` = %d", n))
		}
	}

	df := p.Controller.Database.DateTimeFormat
	parseTime := func(v string) (time.Time, bool) {
		for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05Z", "2006-01-02 15:04:05", "2006-01-02"} {
			if t, err := time.Parse(layout, v); err == nil {
				return t, true
			}
		}
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return time.Unix(n, 0).UTC(), true
		}
		return time.Time{}, false
	}
	if s := q.Get("from"); s != "" {
		if t, ok := parseTime(s); ok {
			where = append(where, fmt.Sprintf("`dateTime` >= '%s'", t.Format(df)))
		}
	}
	if s := q.Get("to"); s != "" {
		if t, ok := parseTime(s); ok {
			where = append(where, fmt.Sprintf("`dateTime` <= '%s'", t.Format(df)))
		}
	}
	// Free-text search runs over whatever columns plugins registered as
	// searchable. With none installed the server stores no searchable text on a
	// call, so this correctly matches nothing rather than ignoring the filter
	// and returning everything.
	searchExtensions := p.Controller.PluginSearchExtensions()
	if s := strings.TrimSpace(q.Get("q")); s != "" {
		esc := strings.ReplaceAll(s, "'", "''")

		predicates := []string{}
		for _, extension := range searchExtensions {
			predicates = append(predicates, fmt.Sprintf(
				"exists (select 1 from `%s` where `%s`.`%s` = `rdioScannerCalls`.`id` and `%s`.`%s` like '%%%s%%')",
				extension.table, extension.table, extension.key, extension.table, extension.text, esc,
			))
		}

		if len(predicates) > 0 {
			where = append(where, "("+strings.Join(predicates, " or ")+")")
		} else {
			where = append(where, "1 = 0")
		}
	}

	limit := uint(100)
	if s := q.Get("limit"); s != "" {
		if n, err := strconv.ParseUint(s, 10, 64); err == nil {
			if n > 500 {
				n = 500
			}
			if n > 0 {
				limit = uint(n)
			}
		}
	}
	offset := uint(0)
	if s := q.Get("offset"); s != "" {
		if n, err := strconv.ParseUint(s, 10, 64); err == nil {
			offset = uint(n)
		}
	}

	order := "desc"
	if strings.EqualFold(q.Get("sort"), "asc") {
		order = "asc"
	}

	includeTranscript := q.Get("includeTranscript") == "1" || strings.EqualFold(q.Get("includeTranscript"), "true")

	whereSQL := "true"
	if len(where) > 0 {
		whereSQL = strings.Join(where, " and ")
	}

	db := p.Controller.Database

	var count uint
	countQuery := fmt.Sprintf("select count(*) from `rdioScannerCalls` where %s", whereSQL)
	if err := db.QueryRow(countQuery).Scan(&count); err != nil && err != sql.ErrNoRows {
		p.writeError(w, http.StatusInternalServerError, fmt.Sprintf("count: %v", err))
		return
	}

	listQuery := fmt.Sprintf(
		"select `id`, `dateTime`, `system`, `talkgroup` from `rdioScannerCalls` where %s order by `dateTime` %s limit %d offset %d",
		whereSQL, order, limit, offset,
	)
	rows, err := db.Query(listQuery)
	if err != nil && err != sql.ErrNoRows {
		p.writeError(w, http.StatusInternalServerError, fmt.Sprintf("query: %v", err))
		return
	}

	results := []map[string]any{}
	ids := []uint{}

	for rows.Next() {
		var (
			id        uint
			dateTime  any
			system    uint
			talkgroup uint
		)

		if err = rows.Scan(&id, &dateTime, &system, &talkgroup); err != nil {
			break
		}

		item := map[string]any{}

		if t, err := db.ParseDateTime(dateTime); err == nil {
			item["dateTime"] = t.UTC().Format(time.RFC3339)
		}
		item["id"] = id
		item["system"] = system
		item["talkgroup"] = talkgroup
		ids = append(ids, id)
		results = append(results, item)
	}
	rows.Close()

	if err != nil {
		p.writeError(w, http.StatusInternalServerError, fmt.Sprintf("scan: %v", err))
		return
	}

	// Plugin-contributed fields, batched one query per extension for the whole
	// page. includeTranscript is honoured as a general "include plugin text"
	// switch so the parameter keeps working; presence flags are always sent.
	if len(searchExtensions) > 0 && len(results) > 0 {
		p.attachPluginFields(searchExtensions, ids, results, includeTranscript)
	}

	resp := map[string]any{
		"count":   count,
		"limit":   limit,
		"offset":  offset,
		"results": results,
	}
	w.Header().Set("Content-Type", "application/json")
	b, _ := json.Marshal(resp)
	w.Write(b)
}

// attachPluginFields fills plugin-contributed text onto a page of results.
//
// Also emits a has<Field> flag, which is what lets a caller ask whether a call
// has a transcript without paying to transfer every transcript on the page —
// the behaviour the includeTranscript parameter has always had.
func (p *PublicApi) attachPluginFields(
	extensions []pluginResolvedSearch,
	ids []uint,
	results []map[string]any,
	includeText bool,
) {
	if len(ids) == 0 {
		return
	}

	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprintf("%d", id)
	}
	idList := strings.Join(parts, ", ")

	byId := map[uint]map[string]any{}
	for _, item := range results {
		if id, ok := item["id"].(uint); ok {
			byId[id] = item
		}
	}

	for _, extension := range extensions {
		flag := "has" + strings.ToUpper(extension.resultField[:1]) + extension.resultField[1:]

		for _, item := range results {
			item[flag] = false
			if includeText {
				item[extension.resultField] = nil
			}
		}

		query := fmt.Sprintf(
			"select `%s`, `%s` from `%s` where `%s` in (%s)",
			extension.key, extension.text, extension.table, extension.key, idList,
		)

		rows, err := p.Controller.Database.Query(query)
		if err != nil {
			continue
		}

		for rows.Next() {
			var (
				callId uint
				value  sql.NullString
			)

			if err := rows.Scan(&callId, &value); err != nil {
				break
			}
			if !value.Valid || value.String == "" {
				continue
			}

			if item, ok := byId[callId]; ok {
				item[flag] = true
				if includeText {
					item[extension.resultField] = value.String
				}
			}
		}

		rows.Close()
	}
}

// apikeyWhere translates an apikey's system scope into a WHERE fragment list.
func (p *PublicApi) apikeyWhere(apikey *Apikey) []string {
	switch v := apikey.Systems.(type) {
	case string:
		if v == "*" {
			return nil
		}
		return []string{"false"}
	case []any:
		clauses := []string{}
		for _, scope := range v {
			m, ok := scope.(map[string]any)
			if !ok {
				continue
			}
			sysId, ok := m["id"].(float64)
			if !ok {
				continue
			}
			switch tg := m["talkgroups"].(type) {
			case string:
				if tg == "*" {
					clauses = append(clauses, fmt.Sprintf("(`system` = %d)", uint(sysId)))
				}
			case []any:
				ids := []string{}
				for _, f := range tg {
					if n, ok := f.(float64); ok {
						ids = append(ids, fmt.Sprintf("%d", uint(n)))
					}
				}
				if len(ids) > 0 {
					clauses = append(clauses, fmt.Sprintf("(`system` = %d and `talkgroup` in (%s))", uint(sysId), strings.Join(ids, ",")))
				}
			}
		}
		if len(clauses) == 0 {
			return []string{"false"}
		}
		return []string{"(" + strings.Join(clauses, " or ") + ")"}
	}
	return []string{"false"}
}

func (p *PublicApi) loadCallForAccess(id uint) (*Call, error) {
	return p.Controller.Calls.GetCall(id, p.Controller.Database)
}

func (p *PublicApi) getCall(w http.ResponseWriter, id uint, apikey *Apikey) {
	call, err := p.loadCallForAccess(id)
	if err != nil || call == nil || call.Id == nil {
		p.writeError(w, http.StatusNotFound, "not found")
		return
	}
	if !apikey.HasAccess(call) {
		p.writeError(w, http.StatusForbidden, "forbidden")
		return
	}

	out := map[string]any{
		"id":        id,
		"dateTime":  call.DateTime.UTC().Format(time.RFC3339),
		"system":    call.System,
		"talkgroup": call.Talkgroup,
		"audioName": call.AudioName,
		"audioType": call.AudioType,
	}
	if call.Frequency != nil {
		out["frequency"] = call.Frequency
	}
	if call.Source != nil {
		out["source"] = call.Source
	}
	// Plugin-contributed fields, so the public API exposes the same call shape
	// the websocket does rather than a quietly reduced one.
	p.Controller.ApplyPluginFields(call)
	for key, value := range call.pluginFields {
		if _, taken := out[key]; !taken {
			out[key] = value
		}
	}

	out["audioUrl"] = fmt.Sprintf("/api/v1/calls/%d/audio", id)

	w.Header().Set("Content-Type", "application/json")
	b, _ := json.Marshal(out)
	w.Write(b)
}

// getTranscript keeps /api/v1/calls/<id>/transcript answering, now by reading
// whichever plugin registered a field of that name. The endpoint predates
// plugins and is part of the published API, so it resolves through the generic
// mechanism rather than disappearing when transcription moved out of the
// server.
func (p *PublicApi) getTranscript(w http.ResponseWriter, id uint, apikey *Apikey) {
	call, err := p.loadCallForAccess(id)
	if err != nil || call == nil || call.Id == nil {
		p.writeError(w, http.StatusNotFound, "not found")
		return
	}

	if !apikey.HasAccess(call) {
		p.writeError(w, http.StatusForbidden, "forbidden")
		return
	}

	p.Controller.ApplyPluginFields(call)

	text := ""
	if value, ok := call.pluginFields["transcript"].(string); ok {
		text = value
	}

	w.Header().Set("Content-Type", "application/json")
	b, _ := json.Marshal(map[string]any{
		"id":         id,
		"system":     call.System,
		"talkgroup":  call.Talkgroup,
		"transcript": text,
	})
	w.Write(b)
}

func (p *PublicApi) getAudio(w http.ResponseWriter, id uint, apikey *Apikey) {
	call, err := p.loadCallForAccess(id)
	if err != nil || call == nil || call.Id == nil {
		p.writeError(w, http.StatusNotFound, "not found")
		return
	}
	if !apikey.HasAccess(call) {
		p.writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	if len(call.Audio) == 0 {
		p.writeError(w, http.StatusNotFound, "no audio")
		return
	}

	contentType := ""
	if t, ok := call.AudioType.(string); ok && t != "" {
		contentType = t
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	fn := ""
	if n, ok := call.AudioName.(string); ok && n != "" {
		fn = n
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(call.Audio)))
	if fn != "" {
		w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=\"%s\"", fn))
	}
	w.Write(call.Audio)
}
