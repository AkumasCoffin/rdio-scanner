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
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

type Api struct {
	Controller *Controller
}

func NewApi(controller *Controller) *Api {
	return &Api{Controller: controller}
}

func (api *Api) CallUploadHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var (
			call = NewCall()
			key  string
		)

		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			api.exitWithError(w, http.StatusBadRequest, "Invalid content-type")
			return
		}

		if !strings.HasPrefix(mediaType, "multipart/") {
			api.exitWithError(w, http.StatusBadRequest, "Not a multipart content")
			return
		}

		mr := multipart.NewReader(r.Body, params["boundary"])

		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			} else if err != nil {
				api.exitWithError(w, http.StatusExpectationFailed, fmt.Sprintf("multipart: %s\n", err.Error()))
				return
			}

			b, err := io.ReadAll(p)
			if err != nil {
				api.exitWithError(w, http.StatusExpectationFailed, fmt.Sprintf("ioread: %s\n", err.Error()))
				return
			}

			switch p.FormName() {
			case "key":
				key = string(b)
			default:
				ParseMultipartContent(call, p, b)
			}
		}

		if ok, err := call.IsValid(); ok {
			api.HandleCall(key, call, w)
		} else {
			api.exitWithError(w, http.StatusExpectationFailed, fmt.Sprintf("Incomplete call data: %s\n", err.Error()))
		}

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		w.Write([]byte("Unsupported method\n"))
	}
}

func (api *Api) HandleCall(key string, call *Call, w http.ResponseWriter) {
	msg := []byte(fmt.Sprintf("Invalid API key for system %v talkgroup %v.\n", call.System, call.Talkgroup))

	if apikey, ok := api.Controller.Apikeys.GetApikey(key); ok {
		if apikey.HasAccess(call) {
			call.apiKeyIdent = apikey.Ident
			api.Controller.Ingest <- call

		} else {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write(msg)
			return
		}

	} else {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write(msg)
		return
	}

	w.Write([]byte("Call imported successfully.\n"))
}

func (api *Api) TrunkRecorderCallUploadHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var (
			call = NewCall()
			key  string
		)

		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			api.exitWithError(w, http.StatusBadRequest, "Invalid content-type")
			return
		}

		if !strings.HasPrefix(mediaType, "multipart/") {
			api.exitWithError(w, http.StatusBadRequest, "Not a multipart content")
			return
		}

		mr := multipart.NewReader(r.Body, params["boundary"])

		parts := map[*multipart.Part][]byte{}

		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			} else if err != nil {
				api.exitWithError(w, http.StatusExpectationFailed, fmt.Sprintf("multipart: %s", err.Error()))
				return
			}

			b, err := io.ReadAll(p)
			if err != nil {
				api.exitWithError(w, http.StatusExpectationFailed, fmt.Sprintf("ioread: %s", err.Error()))
				return
			}

			switch p.FormName() {
			case "key":
				key = string(b)
			case "meta":
				if err := ParseTrunkRecorderMeta(call, b); err != nil {
					api.exitWithError(w, http.StatusExpectationFailed, "Invalid call data")
					return
				}
			default:
				parts[p] = b
			}
		}

		for p, b := range parts {
			ParseMultipartContent(call, p, b)
		}

		if ok, err := call.IsValid(); ok {
			api.HandleCall(key, call, w)

		} else {
			api.exitWithError(w, http.StatusExpectationFailed, fmt.Sprintf("Incomplete call data: %s\n", err.Error()))
		}

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		w.Write([]byte("Unsupported method\n"))
	}
}

// CapabilitiesHandler advertises optional features this server supports.
// Downstream probers hit this before attempting transcript-forward pushes;
// the original repo returns 404 here, which callers treat as "not supported".
func (api *Api) CapabilitiesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"features":["transcript-forward"]}`))
}

type callTranscriptRequest struct {
	Key        string `json:"key"`
	System     uint   `json:"system"`
	Talkgroup  uint   `json:"talkgroup"`
	DateTime   string `json:"dateTime"`
	Transcript string `json:"transcript"`
}

// CallTranscriptHandler receives a transcript forwarded by an upstream instance
// and stores it against the matching local call record.
func (api *Api) CallTranscriptHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		api.exitWithError(w, http.StatusBadRequest, "cannot read body")
		return
	}

	var req callTranscriptRequest
	if err := json.Unmarshal(body, &req); err != nil {
		api.exitWithError(w, http.StatusBadRequest, "invalid json")
		return
	}

	dt, err := time.Parse(time.RFC3339, req.DateTime)
	if err != nil {
		api.exitWithError(w, http.StatusBadRequest, "invalid dateTime")
		return
	}

	api.Controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf("transcript push received: system=%v talkgroup=%v dateTime=%v", req.System, req.Talkgroup, req.DateTime))

	stub := &Call{System: req.System, Talkgroup: req.Talkgroup}
	apikey, ok := api.Controller.Apikeys.GetApikey(req.Key)
	if !ok || !apikey.HasAccess(stub) {
		api.Controller.Logs.LogEvent(LogLevelWarn, fmt.Sprintf("transcript push auth failed: system=%v talkgroup=%v dateTime=%v", req.System, req.Talkgroup, req.DateTime))
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(fmt.Sprintf("Invalid API key for system %v talkgroup %v.\n", req.System, req.Talkgroup)))
		return
	}

	db := api.Controller.Database
	id, err := api.Controller.Calls.GetIdByKey(req.System, req.Talkgroup, dt, db)
	if err != nil {
		api.exitWithError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if id == 0 {
		api.Controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf("transcript push no matching call: system=%v talkgroup=%v dateTime=%v (already pruned, datetime mismatch, or call never arrived)", req.System, req.Talkgroup, req.DateTime))
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("Call not found.\n"))
		return
	}

	if err := api.Controller.Calls.UpdateTranscript(id, req.Transcript, db); err != nil {
		api.exitWithError(w, http.StatusInternalServerError, err.Error())
		return
	}

	api.Controller.Clients.EmitTranscript(id, req.System, req.Talkgroup, req.Transcript, api.Controller.Accesses.IsRestricted())
	api.Controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf("transcript received: system=%v talkgroup=%v id=%v (%d chars)", req.System, req.Talkgroup, id, len(req.Transcript)))

	w.Write([]byte("Transcript updated successfully.\n"))
}

func (api *Api) exitWithError(w http.ResponseWriter, status int, message string) {
	api.Controller.Logs.LogEvent(LogLevelError, fmt.Sprintf("api: %s", message))

	w.WriteHeader(status)
	w.Write([]byte(fmt.Sprintf("%s\n", message)))
}
