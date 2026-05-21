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
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	transcriptSupportUnknown = iota
	transcriptSupportYes
	transcriptSupportNo
)

// transcriptSupportCacheTTL is how long a probed result (yes or no) is kept
// before re-probing. Lets a server that upgrades from the original repo get
// picked up within an hour without a restart.
const transcriptSupportCacheTTL = time.Hour

type Downstream struct {
	Id       any    `json:"_id"`
	Apikey   string `json:"apiKey"`
	Disabled bool   `json:"disabled"`
	Order    any    `json:"order"`
	Systems  any    `json:"systems"`
	Url      string `json:"url"`

	// in-memory only — not persisted, reset on restart
	transcriptMu      sync.Mutex
	transcriptSupport int
	transcriptChecked time.Time
}

func (downstream *Downstream) FromMap(m map[string]any) *Downstream {
	switch v := m["_id"].(type) {
	case float64:
		downstream.Id = uint(v)
	}

	switch v := m["apiKey"].(type) {
	case string:
		downstream.Apikey = v
	}

	switch v := m["disabled"].(type) {
	case bool:
		downstream.Disabled = v
	}

	switch v := m["order"].(type) {
	case float64:
		downstream.Order = uint(v)
	}

	switch v := m["systems"].(type) {
	case []any:
		if b, err := json.Marshal(v); err == nil {
			downstream.Systems = string(b)
		}
	case string:
		downstream.Systems = v
	}

	switch v := m["url"].(type) {
	case string:
		downstream.Url = v
	}

	return downstream
}

func (downstream *Downstream) HasAccess(call *Call) bool {
	if downstream.Disabled {
		return false
	}

	switch v := downstream.Systems.(type) {
	case []any:
		for _, f := range v {
			switch v := f.(type) {
			case map[string]any:
				switch id := v["id"].(type) {
				case float64:
					if id == float64(call.System) {
						switch tg := v["talkgroups"].(type) {
						case string:
							if tg == "*" {
								return true
							}
						case []any:
							for _, f := range tg {
								switch tg := f.(type) {
								case float64:
									if tg == float64(call.Talkgroup) {
										return true
									}
								}
							}
						}
					}
				}
			}
		}

	case string:
		if v == "*" {
			return true
		}

	}

	return false
}

func (downstream *Downstream) Send(call *Call) error {
	var (
		audioName string
		buf       = bytes.Buffer{}
	)

	if downstream.Disabled {
		return nil
	}

	formatError := func(err error) error {
		return fmt.Errorf("downstream.send: %s", err.Error())
	}

	mw := multipart.NewWriter(&buf)

	switch v := call.AudioName.(type) {
	case string:
		audioName = v
	}

	if w, err := mw.CreateFormFile("audio", audioName); err == nil {
		if _, err = w.Write(call.Audio); err != nil {
			return formatError(err)
		}
	} else {
		return formatError(err)
	}

	switch v := call.AudioName.(type) {
	case string:
		if w, err := mw.CreateFormField("audioName"); err == nil {
			if _, err = w.Write([]byte(v)); err != nil {
				return formatError(err)
			}
		} else {
			return formatError(err)
		}
	}

	switch v := call.AudioType.(type) {
	case string:
		if w, err := mw.CreateFormField("audioType"); err == nil {
			if _, err = w.Write([]byte(v)); err != nil {
				return formatError(err)
			}
		} else {
			return formatError(err)
		}
	}

	if w, err := mw.CreateFormField("dateTime"); err == nil {
		if _, err = w.Write([]byte(call.DateTime.UTC().Format(time.RFC3339))); err != nil {
			return formatError(err)
		}
	} else {
		return formatError(err)
	}

	switch v := call.Frequencies.(type) {
	case []map[string]any:
		if w, err := mw.CreateFormField("frequencies"); err == nil {
			if b, err := json.Marshal(v); err == nil {
				if _, err = w.Write(b); err != nil {
					return formatError(err)
				}
			} else {
				return formatError(err)
			}
		} else {
			return formatError(err)
		}
	}

	switch v := call.Frequency.(type) {
	case uint:
		if w, err := mw.CreateFormField("frequency"); err == nil {
			if _, err = w.Write([]byte(fmt.Sprintf("%v", v))); err != nil {
				return formatError(err)
			}
		} else {
			return formatError(err)
		}
	}

	if w, err := mw.CreateFormField("key"); err == nil {
		if _, err = w.Write([]byte(downstream.Apikey)); err != nil {
			return formatError(err)
		}
	} else {
		return formatError(err)
	}

	switch v := call.Patches.(type) {
	case []uint:
		if w, err := mw.CreateFormField("patches"); err == nil {
			if b, err := json.Marshal(v); err == nil {
				if _, err = w.Write(b); err != nil {
					return formatError(err)
				}
			} else {
				return formatError(err)
			}
		} else {
			return formatError(err)
		}
	}

	switch v := call.Source.(type) {
	case uint:
		if w, err := mw.CreateFormField("source"); err == nil {
			if _, err = w.Write([]byte(fmt.Sprintf("%v", v))); err != nil {
				return formatError(err)
			}
		} else {
			return formatError(err)
		}
	}

	switch v := call.Sources.(type) {
	case []map[string]any:
		if w, err := mw.CreateFormField("sources"); err == nil {
			if b, err := json.Marshal(v); err == nil {
				if _, err = w.Write(b); err != nil {
					return formatError(err)
				}
			} else {
				return formatError(err)
			}
		} else {
			return formatError(err)
		}
	}

	if w, err := mw.CreateFormField("system"); err == nil {
		if _, err = w.Write([]byte(fmt.Sprintf("%v", call.System))); err != nil {
			return formatError(err)
		}
	} else {
		return formatError(err)
	}

	switch v := call.systemLabel.(type) {
	case string:
		if w, err := mw.CreateFormField("systemLabel"); err == nil {
			if _, err = w.Write([]byte(v)); err != nil {
				return formatError(err)
			}
		} else {
			return formatError(err)
		}
	}

	if w, err := mw.CreateFormField("talkgroup"); err == nil {
		if _, err = w.Write([]byte(fmt.Sprintf("%v", call.Talkgroup))); err != nil {
			return formatError(err)
		}
	} else {
		return formatError(err)
	}

	switch v := call.talkgroupGroup.(type) {
	case string:
		if w, err := mw.CreateFormField("talkgroupGroup"); err == nil {
			if _, err = w.Write([]byte(v)); err != nil {
				return formatError(err)
			}
		} else {
			return formatError(err)
		}
	}

	switch v := call.talkgroupLabel.(type) {
	case string:
		if w, err := mw.CreateFormField("talkgroupLabel"); err == nil {
			if _, err = w.Write([]byte(v)); err != nil {
				return formatError(err)
			}
		} else {
			return formatError(err)
		}
	}

	switch v := call.talkgroupName.(type) {
	case string:
		if w, err := mw.CreateFormField("talkgroupName"); err == nil {
			if _, err = w.Write([]byte(v)); err != nil {
				return formatError(err)
			}
		} else {
			return formatError(err)
		}
	}

	switch v := call.talkgroupTag.(type) {
	case string:
		if w, err := mw.CreateFormField("talkgroupTag"); err == nil {
			if _, err = w.Write([]byte(v)); err != nil {
				return formatError(err)
			}
		} else {
			return formatError(err)
		}
	}

	if err := mw.Close(); err != nil {
		return formatError(err)
	}

	if u, err := url.Parse(downstream.Url); err == nil {
		u.Path = path.Join(u.Path, "/api/call-upload")

		c := http.Client{Timeout: 30 * time.Second}

		if res, err := c.Post(u.String(), mw.FormDataContentType(), &buf); err == nil {
			if res.StatusCode != http.StatusOK {
				return formatError(fmt.Errorf("bad status: %s", res.Status))
			}

		} else {
			return formatError(err)
		}

	} else {
		return formatError(err)
	}

	return nil
}

// probeTranscriptSupport checks whether the downstream's server supports the
// /api/call-transcript endpoint by hitting /api/capabilities. Results are
// cached for transcriptSupportCacheTTL. The HTTP call is made without holding
// the mutex so callers aren't serialised behind the network round-trip; the
// worst case is several concurrent probes on the very first call after startup,
// all arriving at the same result.
func (downstream *Downstream) probeTranscriptSupport() bool {
	downstream.transcriptMu.Lock()
	if downstream.transcriptSupport != transcriptSupportUnknown &&
		time.Since(downstream.transcriptChecked) < transcriptSupportCacheTTL {
		result := downstream.transcriptSupport == transcriptSupportYes
		downstream.transcriptMu.Unlock()
		return result
	}
	downstream.transcriptMu.Unlock()

	u, err := url.Parse(downstream.Url)
	if err != nil {
		return false
	}
	u.Path = path.Join(u.Path, "/api/capabilities")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(u.String())
	if err != nil {
		// Network error — leave as unknown so we retry next time.
		return false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	support := transcriptSupportNo
	if resp.StatusCode == http.StatusOK {
		var caps struct {
			Features []string `json:"features"`
		}
		if json.Unmarshal(body, &caps) == nil {
			for _, f := range caps.Features {
				if f == "transcript-forward" {
					support = transcriptSupportYes
					break
				}
			}
		}
	}

	downstream.transcriptMu.Lock()
	downstream.transcriptSupport = support
	downstream.transcriptChecked = time.Now()
	downstream.transcriptMu.Unlock()

	return support == transcriptSupportYes
}

// SendTranscript pushes a completed transcript to this downstream if it
// supports the transcript-forward feature. Returns nil for legacy downstreams
// (404 on capabilities probe) so callers don't log spurious warnings.
func (downstream *Downstream) SendTranscript(call *Call) error {
	if downstream.Disabled {
		return nil
	}

	transcript, _ := call.Transcript.(string)
	if transcript == "" {
		return nil
	}

	if !downstream.probeTranscriptSupport() {
		return nil
	}

	u, err := url.Parse(downstream.Url)
	if err != nil {
		return fmt.Errorf("downstream.sendtranscript: %v", err)
	}
	u.Path = path.Join(u.Path, "/api/call-transcript")

	payload := map[string]any{
		"key":        downstream.Apikey,
		"system":     call.System,
		"talkgroup":  call.Talkgroup,
		"dateTime":   call.DateTime.UTC().Format(time.RFC3339),
		"transcript": transcript,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("downstream.sendtranscript: %v", err)
	}

	c := http.Client{Timeout: 30 * time.Second}
	resp, err := c.Post(u.String(), "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("downstream.sendtranscript: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// Call already pruned on the downstream — not an error.
		return nil
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downstream.sendtranscript: bad status %s", resp.Status)
	}

	return nil
}

type Downstreams struct {
	List  []*Downstream
	mutex sync.Mutex
}

func NewDownstreams() *Downstreams {
	return &Downstreams{
		List:  []*Downstream{},
		mutex: sync.Mutex{},
	}
}

func (downstreams *Downstreams) FromMap(f []any) *Downstreams {
	downstreams.mutex.Lock()
	defer downstreams.mutex.Unlock()

	downstreams.List = []*Downstream{}

	for _, r := range f {
		switch m := r.(type) {
		case map[string]any:
			downstream := &Downstream{}
			downstream.FromMap(m)
			downstreams.List = append(downstreams.List, downstream)
		}
	}

	return downstreams
}

func (downstreams *Downstreams) Read(db *Database) error {
	var (
		err     error
		id      sql.NullFloat64
		order   sql.NullFloat64
		rows    *sql.Rows
		systems string
	)

	downstreams.mutex.Lock()
	defer downstreams.mutex.Unlock()

	downstreams.List = []*Downstream{}

	formatError := func(err error) error {
		return fmt.Errorf("downstreams.read: %v", err)
	}

	if rows, err = db.Query("select `_id`, `apiKey`, `disabled`, `order`, `systems`, `url` from `rdioScannerDownstreams`"); err != nil {
		return formatError(err)
	}

	for rows.Next() {
		downstream := &Downstream{}

		if err = rows.Scan(&id, &downstream.Apikey, &downstream.Disabled, &order, &systems, &downstream.Url); err != nil {
			break
		}

		if id.Valid && id.Float64 > 0 {
			downstream.Id = uint(id.Float64)
		}

		if len(downstream.Apikey) == 0 {
			downstream.Apikey = uuid.New().String()
		}

		if order.Valid && order.Float64 > 0 {
			downstream.Order = uint(order.Float64)
		}

		if err = json.Unmarshal([]byte(systems), &downstream.Systems); err != nil {
			downstream.Systems = []any{}
		}

		if len(downstream.Url) == 0 {
			continue
		}

		downstreams.List = append(downstreams.List, downstream)
	}

	rows.Close()

	if err != nil {
		return formatError(err)
	}

	return nil
}

func (downstreams *Downstreams) Send(controller *Controller, call *Call) {
	for _, downstream := range downstreams.List {
		logEvent := func(logLevel string, message string) {
			controller.Logs.LogEvent(logLevel, fmt.Sprintf("downstream: system=%v talkgroup=%v file=%v to %v %v", call.System, call.Talkgroup, call.AudioName, downstream.Url, message))
		}

		if downstream.HasAccess(call) {
			if err := downstream.Send(call); err == nil {
				logEvent(LogLevelInfo, "success")
			} else {
				logEvent(LogLevelError, err.Error())
			}
		}
	}
}

// SendTranscript pushes a completed transcript to all eligible downstreams.
// Called from the transcription goroutine after UpdateTranscript succeeds.
// Skips downstreams that don't support transcript-forward (legacy servers).
func (downstreams *Downstreams) SendTranscript(controller *Controller, call *Call) {
	downstreams.mutex.Lock()
	list := make([]*Downstream, len(downstreams.List))
	copy(list, downstreams.List)
	downstreams.mutex.Unlock()

	for _, downstream := range list {
		if !downstream.HasAccess(call) {
			continue
		}
		logEvent := func(logLevel string, message string) {
			controller.Logs.LogEvent(logLevel, fmt.Sprintf("downstream.transcript: system=%v talkgroup=%v to %v %v", call.System, call.Talkgroup, downstream.Url, message))
		}
		if err := downstream.SendTranscript(call); err != nil {
			logEvent(LogLevelWarn, err.Error())
		} else if downstream.transcriptSupport == transcriptSupportYes {
			logEvent(LogLevelInfo, "success")
		}
	}
}

func (downstreams *Downstreams) Write(db *Database) error {
	var (
		count   uint
		err     error
		rows    *sql.Rows
		rowIds  = []uint{}
		systems any
	)

	downstreams.mutex.Lock()
	defer downstreams.mutex.Unlock()

	formatError := func(err error) error {
		return fmt.Errorf("downstreams.write: %v", err)
	}

	if rows, err = db.Query("select `_id` from `rdioScannerDownstreams`"); err != nil {
		return formatError(err)
	}

	for rows.Next() {
		var rowId uint
		if err = rows.Scan(&rowId); err != nil {
			break
		}
		remove := true
		for _, downstream := range downstreams.List {
			if downstream.Id == nil || downstream.Id == rowId {
				remove = false
				break
			}
		}
		if remove {
			rowIds = append(rowIds, rowId)
		}
	}

	rows.Close()

	if err != nil {
		return formatError(err)
	}

	if len(rowIds) > 0 {
		if b, err := json.Marshal(rowIds); err == nil {
			s := string(b)
			s = strings.ReplaceAll(s, "[", "(")
			s = strings.ReplaceAll(s, "]", ")")
			q := fmt.Sprintf("delete from `rdioScannerDownstreams` where `_id` in %v", s)
			if _, err = db.Exec(q); err != nil {
				return formatError(err)
			}
		}
	}

	for _, downstream := range downstreams.List {
		switch downstream.Systems {
		case "*":
			systems = `"*"`
		default:
			systems = downstream.Systems
		}

		if err = db.QueryRow("select count(*) from `rdioScannerDownstreams` where `_id` = ?", downstream.Id).Scan(&count); err != nil {
			break
		}

		if count == 0 {
			idVal, hasId := downstream.Id.(uint)
			if db.Config.DbType == DbTypePostgres && (!hasId || idVal == 0) {
				_, err = db.Exec("insert into `rdioScannerDownstreams` (`apiKey`, `disabled`, `order`, `systems`, `url`) values (?, ?, ?, ?, ?)", downstream.Apikey, downstream.Disabled, downstream.Order, systems, downstream.Url)
			} else {
				_, err = db.Exec("insert into `rdioScannerDownstreams` (`_id`, `apiKey`, `disabled`, `order`, `systems`, `url`) values (?, ?, ?, ?, ?, ?)", downstream.Id, downstream.Apikey, downstream.Disabled, downstream.Order, systems, downstream.Url)
			}
			if err != nil {
				break
			}

		} else if _, err = db.Exec("update `rdioScannerDownstreams` set `_id` = ?, `apiKey` = ?, `disabled` = ?, `order` = ?, `systems` = ?, `url` = ? where `_id` = ?", downstream.Id, downstream.Apikey, downstream.Disabled, downstream.Order, systems, downstream.Url, downstream.Id); err != nil {
			break
		}
	}

	if err != nil {
		return formatError(err)
	}

	return nil
}
