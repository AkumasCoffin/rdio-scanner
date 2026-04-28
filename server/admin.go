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
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/bcrypt"
)

type Admin struct {
	Attempts         AdminLoginAttempts
	AttemptsMax      uint
	AttemptsMaxDelay time.Duration
	Broadcast        chan *[]byte
	Conns            map[*websocket.Conn]bool
	Controller       *Controller
	Register         chan *websocket.Conn
	Tokens           []string
	Unregister       chan *websocket.Conn
	mutex            sync.Mutex
	running          bool
}

type AdminLoginAttempt struct {
	Count uint
	Date  time.Time
}

type AdminLoginAttempts map[string]*AdminLoginAttempt

func NewAdmin(controller *Controller) *Admin {
	return &Admin{
		Attempts:         AdminLoginAttempts{},
		AttemptsMax:      uint(3),
		AttemptsMaxDelay: time.Duration(time.Duration.Minutes(10)),
		Broadcast:        make(chan *[]byte),
		Conns:            make(map[*websocket.Conn]bool),
		Controller:       controller,
		Register:         make(chan *websocket.Conn),
		Tokens:           []string{},
		Unregister:       make(chan *websocket.Conn),
		mutex:            sync.Mutex{},
	}
}

func (admin *Admin) BroadcastConfig() {
	if b, err := json.Marshal(admin.GetConfig()); err == nil {
		admin.Broadcast <- &b
	}
}

func (admin *Admin) ChangePassword(currentPassword any, newPassword string) error {
	var (
		err  error
		hash []byte
	)

	if len(newPassword) == 0 {
		return errors.New("newPassword is empty")
	}

	switch v := currentPassword.(type) {
	case string:
		if err = bcrypt.CompareHashAndPassword([]byte(admin.Controller.Options.adminPassword), []byte(v)); err != nil {
			return err
		}
	}

	if hash, err = bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost); err != nil {
		return err
	}

	admin.Controller.Options.adminPassword = string(hash)
	admin.Controller.Options.adminPasswordNeedChange = newPassword == defaults.adminPassword

	if err := admin.Controller.Options.Write(admin.Controller.Database); err != nil {
		return err
	}

	if err := admin.Controller.Options.Read(admin.Controller.Database); err != nil {
		return err
	}

	admin.Controller.Logs.LogEvent(LogLevelWarn, "admin password changed.")

	return nil
}

func (admin *Admin) ConfigHandler(w http.ResponseWriter, r *http.Request) {
	if strings.EqualFold(r.Header.Get("upgrade"), "websocket") {
		upgrader := websocket.Upgrader{}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}

		admin.Register <- conn

		go func() {
			conn.SetReadDeadline(time.Time{})

			for {
				_, b, err := conn.ReadMessage()
				if err != nil {
					break
				}

				if !admin.ValidateToken(string(b)) {
					break
				}
			}

			admin.Unregister <- conn

			conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(1000, ""))
		}()

	} else {
		logError := func(err error) {
			admin.Controller.Logs.LogEvent(LogLevelError, fmt.Sprintf("admin.confighandler.put: %s", err.Error()))
		}

		t := admin.GetAuthorization(r)
		if !admin.ValidateToken(t) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		switch r.Method {
		case http.MethodGet:
			admin.SendConfig(w)

		case http.MethodPut:
			m := map[string]any{}
			err := json.NewDecoder(r.Body).Decode(&m)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}

			admin.mutex.Lock()
			defer admin.mutex.Unlock()

			admin.Controller.Dirwatches.Stop()

			sectionErrs := map[string]string{}
			track := func(section string, err error) {
				if err != nil {
					logError(fmt.Errorf("%s: %v", section, err))
					sectionErrs[section] = err.Error()
				}
			}

			// Run all the per-section writes inside one transaction so Postgres
			// commits once at the end instead of fsync'ing every row. On large
			// configs this takes the save from ~30s down to ~1s.
			txErr := admin.Controller.Database.WithTx(func(tx *Database) error {
				if v, ok := m["access"].([]any); ok {
					admin.Controller.Accesses.FromMap(v)
					if err := admin.Controller.Accesses.Write(tx); err != nil {
						track("access", err)
					}
				}

				if v, ok := m["apiKeys"].([]any); ok {
					admin.Controller.Apikeys.FromMap(v)
					if err := admin.Controller.Apikeys.Write(tx); err != nil {
						track("apiKeys", err)
					}
				}

				if v, ok := m["dirWatch"].([]any); ok {
					admin.Controller.Dirwatches.FromMap(v)
					if err := admin.Controller.Dirwatches.Write(tx); err != nil {
						track("dirWatch", err)
					}
				}

				if v, ok := m["downstreams"].([]any); ok {
					admin.Controller.Downstreams.FromMap(v)
					if err := admin.Controller.Downstreams.Write(tx); err != nil {
						track("downstreams", err)
					}
				}

				if v, ok := m["groups"].([]any); ok {
					admin.Controller.Groups.FromMap(v)
					if err := admin.Controller.Groups.Write(tx); err != nil {
						track("groups", err)
					}
				}

				if v, ok := m["options"].(map[string]any); ok {
					admin.Controller.Options.FromMap(v)
					if err := admin.Controller.Options.Write(tx); err != nil {
						track("options", err)
					}
				}

				if v, ok := m["systems"].([]any); ok {
					admin.Controller.Systems.FromMap(v)
					if err := admin.Controller.Systems.Write(tx); err != nil {
						track("systems", err)
					}
				}

				if v, ok := m["tags"].([]any); ok {
					admin.Controller.Tags.FromMap(v)
					if err := admin.Controller.Tags.Write(tx); err != nil {
						track("tags", err)
					}
				}

				// If any section errored, roll the whole tx back — partial
				// commits are what made "configs save wrong" previously.
				if len(sectionErrs) > 0 {
					return fmt.Errorf("section errors: %v", sectionErrs)
				}
				return nil
			})
			if txErr != nil && len(sectionErrs) == 0 {
				sectionErrs["_tx"] = txErr.Error()
				logError(txErr)
			}

			// Reload in-memory caches from the persisted state, outside the tx.
			db := admin.Controller.Database
			if _, ok := m["access"]; ok {
				if err := admin.Controller.Accesses.Read(db); err != nil {
					track("access", err)
				}
			}
			if _, ok := m["apiKeys"]; ok {
				if err := admin.Controller.Apikeys.Read(db); err != nil {
					track("apiKeys", err)
				}
			}
			if _, ok := m["dirWatch"]; ok {
				if err := admin.Controller.Dirwatches.Read(db); err != nil {
					track("dirWatch", err)
				}
			}
			if _, ok := m["downstreams"]; ok {
				if err := admin.Controller.Downstreams.Read(db); err != nil {
					track("downstreams", err)
				}
			}
			if _, ok := m["groups"]; ok {
				if err := admin.Controller.Groups.Read(db); err != nil {
					track("groups", err)
				}
			}
			if _, ok := m["systems"]; ok {
				if err := admin.Controller.Systems.Read(db); err != nil {
					track("systems", err)
				}
			}
			if _, ok := m["tags"]; ok {
				if err := admin.Controller.Tags.Read(db); err != nil {
					track("tags", err)
				}
			}

			admin.Controller.EmitConfig()
			admin.Controller.Dirwatches.Start(admin.Controller)

			if len(sectionErrs) > 0 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				b, _ := json.Marshal(map[string]any{"errors": sectionErrs})
				w.Write(b)
				admin.Controller.Logs.LogEvent(LogLevelError, fmt.Sprintf("configuration save had errors: %v", sectionErrs))
				return
			}

			admin.SendConfig(w)

			admin.Controller.Logs.LogEvent(LogLevelWarn, "configuration changed")

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

func (admin *Admin) GetAuthorization(r *http.Request) string {
	return r.Header.Get("Authorization")
}

func (admin *Admin) GetConfig() map[string]any {
	systems := []map[string]any{}
	for _, system := range admin.Controller.Systems.List {
		systems = append(systems, map[string]any{
			"_id":                 system.RowId,
			"autoPopulate":        system.AutoPopulate,
			"blacklists":          system.Blacklists,
			"id":                  system.Id,
			"label":               system.Label,
			"led":                 system.Led,
			"order":               system.Order,
			"talkgroups":          system.Talkgroups.List,
			"transcribe":          system.Transcribe,
			"transcriptionPrompt": system.TranscriptionPrompt,
			"units":               system.Units.List,
		})
	}

	return map[string]any{
		"access":      admin.Controller.Accesses.List,
		"apiKeys":     admin.Controller.Apikeys.List,
		"dirWatch":    admin.Controller.Dirwatches.List,
		"downstreams": admin.Controller.Downstreams.List,
		"groups":      admin.Controller.Groups.List,
		"options":     admin.Controller.Options,
		"systems":     systems,
		"tags":        admin.Controller.Tags.List,
	}
}

func (admin *Admin) LogsHandler(w http.ResponseWriter, r *http.Request) {
	t := admin.GetAuthorization(r)
	if !admin.ValidateToken(t) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	switch r.Method {
	case http.MethodPost:
		m := map[string]any{}
		err := json.NewDecoder(r.Body).Decode(&m)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		logOptions := NewLogSearchOptions().FromMap(m)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		r, err := admin.Controller.Logs.Search(logOptions, admin.Controller.Database)
		if err != nil {
			admin.Controller.Logs.LogEvent(LogLevelError, err.Error())
			w.WriteHeader(http.StatusExpectationFailed)
			return
		}

		b, err := json.Marshal(r)
		if err != nil {
			admin.Controller.Logs.LogEvent(LogLevelError, err.Error())
			w.WriteHeader(http.StatusExpectationFailed)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write(b)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (admin *Admin) LoginHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		m := map[string]any{}

		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		remoteAddr := GetRemoteAddr(r)

		attempt := admin.Attempts[remoteAddr]

		if attempt == nil {
			admin.Attempts[remoteAddr] = &AdminLoginAttempt{
				Count: 1,
				Date:  time.Now(),
			}
			attempt = admin.Attempts[remoteAddr]
		} else {
			attempt.Count++
			attempt.Date = time.Now()
		}

		if attempt.Count > admin.AttemptsMax || time.Since(attempt.Date) < admin.AttemptsMaxDelay {
			if attempt.Count == admin.AttemptsMax+1 {
				admin.Controller.Logs.LogEvent(LogLevelWarn, fmt.Sprintf("too many login attempts for ip=\"%v\"", remoteAddr))
			}

			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		ok := false

		switch v := m["password"].(type) {
		case string:
			if len(v) > 0 {
				if err := bcrypt.CompareHashAndPassword([]byte(admin.Controller.Options.adminPassword), []byte(v)); err == nil {
					ok = true
				}
			}
		}

		if !ok {
			admin.Controller.Logs.LogEvent(LogLevelWarn, fmt.Sprintf("invalid login attempt for ip %v", remoteAddr))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		id, err := uuid.NewRandom()

		if err != nil {
			w.WriteHeader(http.StatusExpectationFailed)
			return
		}

		token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{ID: id.String()})
		sToken, err := token.SignedString([]byte(admin.Controller.Options.secret))

		if err != nil {
			w.WriteHeader(http.StatusExpectationFailed)
			return
		}

		if len(admin.Tokens) < 5 {
			admin.Tokens = append(admin.Tokens, sToken)
		} else {
			admin.Tokens = append(admin.Tokens[1:], sToken)
		}

		b, err := json.Marshal(map[string]any{
			"passwordNeedChange": true,
			"token":              sToken,
		})
		if err != nil {
			w.WriteHeader(http.StatusExpectationFailed)
			return
		}

		for k, v := range admin.Attempts {
			if time.Since(v.Date) > admin.AttemptsMaxDelay {
				delete(admin.Attempts, k)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write(b)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (admin *Admin) LogoutHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		t := admin.GetAuthorization(r)
		if !admin.ValidateToken(t) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		for k, v := range admin.Tokens {
			if v == t {
				admin.Tokens = append(admin.Tokens[:k], admin.Tokens[k+1:]...)
			}
		}
		w.WriteHeader(http.StatusOK)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (admin *Admin) PasswordHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var (
			b               []byte
			currentPassword any
			newPassword     string
		)

		logError := func(err error) {
			admin.Controller.Logs.LogEvent(LogLevelError, fmt.Sprintf("admin.passwordhandler.post: %s", err.Error()))
		}

		t := admin.GetAuthorization(r)
		if !admin.ValidateToken(t) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		m := map[string]any{}
		err := json.NewDecoder(r.Body).Decode(&m)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		switch v := m["currentPassword"].(type) {
		case string:
			currentPassword = v
		}

		switch v := m["newPassword"].(type) {
		case string:
			newPassword = v
		default:
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		if err = admin.ChangePassword(currentPassword, newPassword); err != nil {
			logError(errors.New("unable to change admin password, current password is invalid"))
			w.WriteHeader(http.StatusExpectationFailed)
			return
		}

		if b, err = json.Marshal(map[string]any{"passwordNeedChange": admin.Controller.Options.adminPasswordNeedChange}); err == nil {
			w.Header().Set("Content-Type", "application/json")
			w.Write(b)
		} else {
			w.WriteHeader(http.StatusExpectationFailed)
		}

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (admin *Admin) SendConfig(w http.ResponseWriter) {
	var m map[string]any
	_, docker := os.LookupEnv("DOCKER")
	if docker {
		m = map[string]any{
			"config":             admin.GetConfig(),
			"docker":             docker,
			"passwordNeedChange": admin.Controller.Options.adminPasswordNeedChange,
		}
	} else {
		m = map[string]any{
			"config":             admin.GetConfig(),
			"passwordNeedChange": admin.Controller.Options.adminPasswordNeedChange,
		}
	}
	if b, err := json.Marshal(m); err == nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	} else {
		w.WriteHeader(http.StatusExpectationFailed)
	}
}

func (admin *Admin) Start() error {
	if admin.running {
		return errors.New("admin already running")
	} else {
		admin.running = true
	}

	go func() {
		for {
			select {
			case data, ok := <-admin.Broadcast:
				if !ok {
					return
				}

				for conn := range admin.Conns {
					err := conn.WriteMessage(websocket.TextMessage, *data)
					if err != nil {
						admin.Unregister <- conn
					}
				}

			case conn := <-admin.Register:
				admin.Conns[conn] = true

			case conn := <-admin.Unregister:
				if _, ok := admin.Conns[conn]; ok {
					delete(admin.Conns, conn)
					conn.Close()
				}
			}
		}
	}()

	return nil
}

func (admin *Admin) UserAddHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		logError := func(err error) {
			admin.Controller.Logs.LogEvent(LogLevelError, fmt.Sprintf("admin.useraddhandler.post: %s", err.Error()))
		}

		t := admin.GetAuthorization(r)
		if !admin.ValidateToken(t) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		m := map[string]any{}
		err := json.NewDecoder(r.Body).Decode(&m)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		admin.Controller.Accesses.Add(NewAccess().FromMap(m))

		if err := admin.Controller.Accesses.Write(admin.Controller.Database); err == nil {
			if err := admin.Controller.Accesses.Read(admin.Controller.Database); err == nil {
				admin.BroadcastConfig()
				w.WriteHeader(http.StatusOK)
			} else {
				logError(err)
				w.WriteHeader(http.StatusExpectationFailed)
			}
		} else {
			logError(err)
			w.WriteHeader(http.StatusExpectationFailed)
		}

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (admin *Admin) UserRemoveHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		logError := func(err error) {
			admin.Controller.Logs.LogEvent(LogLevelError, fmt.Sprintf("admin.userremovehandler.post: %s", err.Error()))
		}

		t := admin.GetAuthorization(r)
		if !admin.ValidateToken(t) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		m := map[string]any{}
		err := json.NewDecoder(r.Body).Decode(&m)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		if _, ok := admin.Controller.Accesses.Remove(NewAccess().FromMap(m)); ok {
			if err := admin.Controller.Accesses.Write(admin.Controller.Database); err == nil {
				if err := admin.Controller.Accesses.Read(admin.Controller.Database); err == nil {
					admin.BroadcastConfig()
					w.WriteHeader(http.StatusOK)
				} else {
					logError(err)
					w.WriteHeader(http.StatusExpectationFailed)
				}
			} else {
				logError(err)
				w.WriteHeader(http.StatusExpectationFailed)
			}
		}

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (admin *Admin) TranscribeHandler(w http.ResponseWriter, r *http.Request) {
	t := admin.GetAuthorization(r)
	if !admin.ValidateToken(t) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		Id         uint   `json:"id"`
		Transcript string `json:"transcript"`
		Manual     bool   `json:"manual"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if body.Id == 0 {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"missing id"}`))
		return
	}

	if body.Manual {
		if err := admin.Controller.Calls.UpdateTranscript(body.Id, body.Transcript, admin.Controller.Database); err != nil {
			admin.Controller.Logs.LogEvent(LogLevelError, fmt.Sprintf("admin.transcribe manual: %v", err))
			w.WriteHeader(http.StatusExpectationFailed)
			return
		}
		b, _ := json.Marshal(map[string]any{"id": body.Id, "transcript": body.Transcript})
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
		return
	}

	if !admin.Controller.Transcriber.Enabled() {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"transcription is not configured"}`))
		return
	}

	call, err := admin.Controller.Calls.GetCall(body.Id, admin.Controller.Database)
	if err != nil || call == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	text, err := admin.Controller.Transcriber.Transcribe(call)
	if err != nil {
		admin.Controller.Logs.LogEvent(LogLevelWarn, fmt.Sprintf("admin.transcribe call %v: %v", body.Id, err))
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(fmt.Sprintf(`{"error":%q}`, err.Error())))
		return
	}

	if err = admin.Controller.Calls.UpdateTranscript(body.Id, text, admin.Controller.Database); err != nil {
		admin.Controller.Logs.LogEvent(LogLevelError, fmt.Sprintf("admin.transcribe persist %v: %v", body.Id, err))
		w.WriteHeader(http.StatusExpectationFailed)
		return
	}

	b, _ := json.Marshal(map[string]any{"id": body.Id, "transcript": text})
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

func (admin *Admin) ValidateToken(sToken string) bool {
	found := false
	for _, t := range admin.Tokens {
		if t == sToken {
			found = true
			break
		}
	}
	if !found {
		return false
	}

	token, err := jwt.Parse(sToken, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}

		return []byte(admin.Controller.Options.secret), nil
	})
	if err != nil {
		return false
	}

	return token.Valid
}
