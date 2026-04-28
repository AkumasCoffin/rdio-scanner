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
	"strings"
	"sync"

	"golang.org/x/crypto/bcrypt"
)

type Options struct {
	AfsSystems                  string `json:"afsSystems"`
	AudioConversion             uint   `json:"audioConversion"`
	AutoPopulate                bool   `json:"autoPopulate"`
	Branding                    string `json:"branding"`
	DimmerDelay                 uint   `json:"dimmerDelay"`
	DisableDuplicateDetection   bool   `json:"disableDuplicateDetection"`
	DuplicateDetectionTimeFrame uint   `json:"duplicateDetectionTimeFrame"`
	Email                       string `json:"email"`
	KeypadBeeps                 string `json:"keypadBeeps"`
	MaxClients                  uint   `json:"maxClients"`
	PlaybackGoesLive            bool   `json:"playbackGoesLive"`
	PruneDays                   uint   `json:"pruneDays"`
	SearchPatchedTalkgroups     bool   `json:"searchPatchedTalkgroups"`
	ShowListenersCount          bool   `json:"showListenersCount"`
	SortTalkgroups              bool   `json:"sortTalkgroups"`
	TagsToggle                  bool   `json:"tagsToggle"`
	Time12hFormat               bool   `json:"time12hFormat"`
	TranscriptionEnabled        bool   `json:"transcriptionEnabled"`
	TranscriptionProvider       string `json:"transcriptionProvider"`
	TranscriptionBaseUrl        string `json:"transcriptionBaseUrl"`
	TranscriptionApiKey         string `json:"transcriptionApiKey"`
	TranscriptionModel          string `json:"transcriptionModel"`
	TranscriptionLanguage       string `json:"transcriptionLanguage"`
	TranscriptionPrompt         string `json:"transcriptionPrompt"`
	TranscriptionMaxPerMinute   uint   `json:"transcriptionMaxPerMinute"`
	TranscriptionMinAudioBytes  uint   `json:"transcriptionMinAudioBytes"`
	WaitForTranscript           bool   `json:"waitForTranscript"`
	ShowRetranscribeButton      bool   `json:"showRetranscribeButton"`
	UmamiUrl                    string `json:"umamiUrl"`
	UmamiWebsiteId              string `json:"umamiWebsiteId"`
	adminPassword               string
	adminPasswordNeedChange     bool
	mutex                       sync.Mutex
	secret                      string
}

const (
	AUDIO_CONVERSION_DISABLED          = 0
	AUDIO_CONVERSION_ENABLED           = 1
	AUDIO_CONVERSION_ENABLED_NORM      = 2
	AUDIO_CONVERSION_ENABLED_LOUD_NORM = 3

	optionRowPrefix = "option."
)

func NewOptions() *Options {
	return &Options{
		mutex: sync.Mutex{},
	}
}

// FromMap overlays any fields present in m onto the current options.
// Missing fields are intentionally left alone so a partial payload from
// the admin UI cannot accidentally reset unrelated settings.
func (options *Options) FromMap(m map[string]any) *Options {
	options.mutex.Lock()
	defer options.mutex.Unlock()

	setStr := func(key string, dest *string) {
		if v, ok := m[key].(string); ok {
			*dest = v
		}
	}
	setUint := func(key string, dest *uint) {
		if v, ok := m[key].(float64); ok {
			*dest = uint(v)
		}
	}
	setBool := func(key string, dest *bool) {
		if v, ok := m[key].(bool); ok {
			*dest = v
		}
	}

	setStr("afsSystems", &options.AfsSystems)
	setUint("audioConversion", &options.AudioConversion)
	setBool("autoPopulate", &options.AutoPopulate)
	setStr("branding", &options.Branding)
	setUint("dimmerDelay", &options.DimmerDelay)

	// legacy shorthand used by older clients
	if v, ok := m["disableAudioConversion"].(bool); ok {
		if v {
			options.AudioConversion = 2
		} else {
			options.AudioConversion = 0
		}
	}

	setBool("disableDuplicateDetection", &options.DisableDuplicateDetection)
	setUint("duplicateDetectionTimeFrame", &options.DuplicateDetectionTimeFrame)
	setStr("email", &options.Email)
	setStr("keypadBeeps", &options.KeypadBeeps)
	setUint("maxClients", &options.MaxClients)
	setBool("playbackGoesLive", &options.PlaybackGoesLive)
	setUint("pruneDays", &options.PruneDays)
	setBool("searchPatchedTalkgroups", &options.SearchPatchedTalkgroups)
	setBool("showListenersCount", &options.ShowListenersCount)
	setBool("sortTalkgroups", &options.SortTalkgroups)
	setBool("tagsToggle", &options.TagsToggle)
	setBool("time12hFormat", &options.Time12hFormat)
	setBool("transcriptionEnabled", &options.TranscriptionEnabled)
	setStr("transcriptionProvider", &options.TranscriptionProvider)
	setStr("transcriptionBaseUrl", &options.TranscriptionBaseUrl)
	setStr("transcriptionApiKey", &options.TranscriptionApiKey)
	setStr("transcriptionModel", &options.TranscriptionModel)
	setStr("transcriptionLanguage", &options.TranscriptionLanguage)
	setStr("transcriptionPrompt", &options.TranscriptionPrompt)
	setUint("transcriptionMaxPerMinute", &options.TranscriptionMaxPerMinute)
	setUint("transcriptionMinAudioBytes", &options.TranscriptionMinAudioBytes)
	setBool("waitForTranscript", &options.WaitForTranscript)
	setBool("showRetranscribeButton", &options.ShowRetranscribeButton)
	setStr("umamiUrl", &options.UmamiUrl)
	setStr("umamiWebsiteId", &options.UmamiWebsiteId)

	return options
}

// optionKeyValuePairs enumerates the per-row option entries that are
// read/written individually in rdioScannerConfigs. Used by both Read and
// Write so the set stays in sync.
func (options *Options) optionKeyValuePairs() []struct {
	Key string
	Val any
} {
	return []struct {
		Key string
		Val any
	}{
		{"afsSystems", options.AfsSystems},
		{"audioConversion", options.AudioConversion},
		{"autoPopulate", options.AutoPopulate},
		{"branding", options.Branding},
		{"dimmerDelay", options.DimmerDelay},
		{"disableDuplicateDetection", options.DisableDuplicateDetection},
		{"duplicateDetectionTimeFrame", options.DuplicateDetectionTimeFrame},
		{"email", options.Email},
		{"keypadBeeps", options.KeypadBeeps},
		{"maxClients", options.MaxClients},
		{"playbackGoesLive", options.PlaybackGoesLive},
		{"pruneDays", options.PruneDays},
		{"searchPatchedTalkgroups", options.SearchPatchedTalkgroups},
		{"showListenersCount", options.ShowListenersCount},
		{"sortTalkgroups", options.SortTalkgroups},
		{"tagsToggle", options.TagsToggle},
		{"time12hFormat", options.Time12hFormat},
		{"transcriptionEnabled", options.TranscriptionEnabled},
		{"transcriptionProvider", options.TranscriptionProvider},
		{"transcriptionBaseUrl", options.TranscriptionBaseUrl},
		{"transcriptionApiKey", options.TranscriptionApiKey},
		{"transcriptionModel", options.TranscriptionModel},
		{"transcriptionLanguage", options.TranscriptionLanguage},
		{"transcriptionPrompt", options.TranscriptionPrompt},
		{"transcriptionMaxPerMinute", options.TranscriptionMaxPerMinute},
		{"transcriptionMinAudioBytes", options.TranscriptionMinAudioBytes},
		{"waitForTranscript", options.WaitForTranscript},
		{"showRetranscribeButton", options.ShowRetranscribeButton},
		{"umamiUrl", options.UmamiUrl},
		{"umamiWebsiteId", options.UmamiWebsiteId},
	}
}

func (options *Options) Read(db *Database) error {
	var (
		defaultPassword []byte
		err             error
		s               string
	)

	options.mutex.Lock()
	defer options.mutex.Unlock()

	defaultPassword, _ = bcrypt.GenerateFromPassword([]byte(defaults.adminPassword), bcrypt.DefaultCost)

	options.adminPassword = string(defaultPassword)
	options.adminPasswordNeedChange = defaults.adminPasswordNeedChange
	options.AudioConversion = defaults.options.audioConversion
	options.AutoPopulate = defaults.options.autoPopulate
	options.DimmerDelay = defaults.options.dimmerDelay
	options.DisableDuplicateDetection = defaults.options.disableDuplicateDetection
	options.DuplicateDetectionTimeFrame = defaults.options.duplicateDetectionTimeFrame
	options.KeypadBeeps = defaults.options.keypadBeeps
	options.MaxClients = defaults.options.maxClients
	options.PlaybackGoesLive = defaults.options.playbackGoesLive
	options.PruneDays = defaults.options.pruneDays
	options.SearchPatchedTalkgroups = defaults.options.searchPatchedTalkgroups
	options.ShowListenersCount = defaults.options.showListenersCount
	options.SortTalkgroups = defaults.options.sortTalkgroups
	options.TagsToggle = defaults.options.tagsToggle
	options.TranscriptionEnabled = defaults.options.transcriptionEnabled
	options.TranscriptionProvider = defaults.options.transcriptionProvider
	options.TranscriptionBaseUrl = defaults.options.transcriptionBaseUrl
	options.TranscriptionModel = defaults.options.transcriptionModel
	options.TranscriptionLanguage = defaults.options.transcriptionLanguage
	options.TranscriptionPrompt = defaults.options.transcriptionPrompt

	err = db.QueryRow("select `val` from `rdioScannerConfigs` where `key` = 'adminPassword'").Scan(&s)
	if err == nil {
		if err = json.Unmarshal([]byte(s), &s); err == nil {
			options.adminPassword = s
		}
	}

	err = db.QueryRow("select `val` from `rdioScannerConfigs` where `key` = 'adminPasswordNeedChange'").Scan(&s)
	if err == nil {
		var b bool
		if err = json.Unmarshal([]byte(s), &b); err == nil {
			options.adminPasswordNeedChange = b
		}
	}

	// Load each option from its own row. Missing rows keep the defaults set above.
	rows, err := db.Query("select `key`, `val` from `rdioScannerConfigs` where `key` like 'option.%'")
	if err == nil {
		stored := map[string]string{}
		for rows.Next() {
			var k, v string
			if err := rows.Scan(&k, &v); err == nil {
				stored[strings.TrimPrefix(k, optionRowPrefix)] = v
			}
		}
		rows.Close()

		applyStr := func(key string, dest *string) {
			if raw, ok := stored[key]; ok {
				var x string
				if json.Unmarshal([]byte(raw), &x) == nil {
					*dest = x
				}
			}
		}
		applyUint := func(key string, dest *uint) {
			if raw, ok := stored[key]; ok {
				var x float64
				if json.Unmarshal([]byte(raw), &x) == nil {
					*dest = uint(x)
				}
			}
		}
		applyBool := func(key string, dest *bool) {
			if raw, ok := stored[key]; ok {
				var x bool
				if json.Unmarshal([]byte(raw), &x) == nil {
					*dest = x
				}
			}
		}

		applyStr("afsSystems", &options.AfsSystems)
		applyUint("audioConversion", &options.AudioConversion)
		applyBool("autoPopulate", &options.AutoPopulate)
		applyStr("branding", &options.Branding)
		applyUint("dimmerDelay", &options.DimmerDelay)
		applyBool("disableDuplicateDetection", &options.DisableDuplicateDetection)
		applyUint("duplicateDetectionTimeFrame", &options.DuplicateDetectionTimeFrame)
		applyStr("email", &options.Email)
		applyStr("keypadBeeps", &options.KeypadBeeps)
		applyUint("maxClients", &options.MaxClients)
		applyBool("playbackGoesLive", &options.PlaybackGoesLive)
		applyUint("pruneDays", &options.PruneDays)
		applyBool("searchPatchedTalkgroups", &options.SearchPatchedTalkgroups)
		applyBool("showListenersCount", &options.ShowListenersCount)
		applyBool("sortTalkgroups", &options.SortTalkgroups)
		applyBool("tagsToggle", &options.TagsToggle)
		applyBool("time12hFormat", &options.Time12hFormat)
		applyBool("transcriptionEnabled", &options.TranscriptionEnabled)
		applyStr("transcriptionProvider", &options.TranscriptionProvider)
		applyStr("transcriptionBaseUrl", &options.TranscriptionBaseUrl)
		applyStr("transcriptionApiKey", &options.TranscriptionApiKey)
		applyStr("transcriptionModel", &options.TranscriptionModel)
		applyStr("transcriptionLanguage", &options.TranscriptionLanguage)
		applyStr("transcriptionPrompt", &options.TranscriptionPrompt)
		applyUint("transcriptionMaxPerMinute", &options.TranscriptionMaxPerMinute)
		applyUint("transcriptionMinAudioBytes", &options.TranscriptionMinAudioBytes)
		applyBool("waitForTranscript", &options.WaitForTranscript)
		applyBool("showRetranscribeButton", &options.ShowRetranscribeButton)
		applyStr("umamiUrl", &options.UmamiUrl)
		applyStr("umamiWebsiteId", &options.UmamiWebsiteId)
	}

	err = db.QueryRow("select `val` from `rdioScannerConfigs` where `key` = 'secret'").Scan(&s)
	if err == nil {
		if err = json.Unmarshal([]byte(s), &s); err == nil {
			options.secret = s
		}
	}

	return nil
}

func (options *Options) Write(db *Database) error {
	options.mutex.Lock()
	defer options.mutex.Unlock()

	formatError := func(err error) error {
		return fmt.Errorf("options.write: %v", err)
	}

	upsert := func(key string, raw string) error {
		res, err := db.Exec("update `rdioScannerConfigs` set `val` = ? where `key` = ?", raw, key)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err == nil && n == 0 {
			if _, err := db.Exec("insert into `rdioScannerConfigs` (`key`, `val`) values (?, ?)", key, raw); err != nil {
				return err
			}
		}
		return nil
	}

	b, err := json.Marshal(options.adminPassword)
	if err != nil {
		return formatError(err)
	}
	if err := upsert("adminPassword", string(b)); err != nil {
		return formatError(err)
	}

	b, err = json.Marshal(options.adminPasswordNeedChange)
	if err != nil {
		return formatError(err)
	}
	if err := upsert("adminPasswordNeedChange", string(b)); err != nil {
		return formatError(err)
	}

	for _, entry := range options.optionKeyValuePairs() {
		b, err := json.Marshal(entry.Val)
		if err != nil {
			return formatError(fmt.Errorf("%s: %v", entry.Key, err))
		}
		if err := upsert(optionRowPrefix+entry.Key, string(b)); err != nil {
			return formatError(fmt.Errorf("%s: %v", entry.Key, err))
		}
	}

	// Sanity: clear any legacy combined blob so there's a single source of truth.
	_, _ = db.Exec("delete from `rdioScannerConfigs` where `key` = 'options'")

	return nil
}

