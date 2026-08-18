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
	// DualLed enables a second LED color per system/talkgroup (#10): the admin
	// editors show a second color selector and the main screen renders a
	// second LED. Off by default so stock-looking setups stay stock-looking.
	DualLed                     bool   `json:"dualLed"`
	DuplicateDetectionTimeFrame uint   `json:"duplicateDetectionTimeFrame"`
	Email                       string `json:"email"`
	KeypadBeeps                 string `json:"keypadBeeps"`
	MaxClients                  uint   `json:"maxClients"`
	PlaybackGoesLive            bool   `json:"playbackGoesLive"`
	PruneDays                   uint   `json:"pruneDays"`
	LogPruneDays                uint   `json:"logPruneDays"`
	LogPruneCount               uint   `json:"logPruneCount"`
	SearchPatchedTalkgroups     bool   `json:"searchPatchedTalkgroups"`
	// ShowListenerStats exposes the listener-count history charts on the
	// public stats endpoint. Off by default — admins always see them via
	// /api/admin/stats regardless.
	ShowListenerStats           bool   `json:"showListenerStats"`
	ShowListenersCount          bool   `json:"showListenersCount"`
	SortByGroups               bool   `json:"sortByGroups"`
	SortByTags                 bool   `json:"sortByTags"`
	SortTalkgroups              bool   `json:"sortTalkgroups"`
	TagsToggle                  bool   `json:"tagsToggle"`
	Time12hFormat               bool   `json:"time12hFormat"`
	UmamiUrl                    string `json:"umamiUrl"`
	UmamiWebsiteId              string `json:"umamiWebsiteId"`
	// UpdateUrl points the admin auto-updater at a GitHub repo
	// (https://github.com/owner/repo). Empty = use DefaultUpdateRepo.
	UpdateUrl                   string `json:"updateUrl"`
	// UpdatePrereleases selects the update channel: false = stable releases
	// only (the default), true = include prereleases. See Read().
	UpdatePrereleases           bool   `json:"updatePrereleases"`
	// PluginRepos is the JSON-encoded list of user-added plugin repositories.
	// Stored as JSON in a single option row rather than its own table because
	// it is a short, wholly-replaced list that only the plugin store reads.
	// The official repository is implicit and never stored here.
	PluginRepos                 string `json:"pluginRepos"`
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

// GetShowListenerStats reads the option under the mutex. The admin save
// rewrites this struct in place (FromMap/Read hold the mutex), so request
// handlers reading it lock-free would race the save.
func (options *Options) GetShowListenerStats() bool {
	options.mutex.Lock()
	defer options.mutex.Unlock()
	return options.ShowListenerStats
}

// GetSortLens returns the SortByGroups/SortByTags pair under the mutex —
// same reasoning as GetShowListenerStats, read from the stats build path.
func (options *Options) GetSortLens() (byGroups bool, byTags bool) {
	options.mutex.Lock()
	defer options.mutex.Unlock()
	return options.SortByGroups, options.SortByTags
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
		if v, ok := jsonUintFrom(m, key); ok {
			*dest = v
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
	setBool("dualLed", &options.DualLed)
	setUint("duplicateDetectionTimeFrame", &options.DuplicateDetectionTimeFrame)
	setStr("email", &options.Email)
	setStr("keypadBeeps", &options.KeypadBeeps)
	setUint("maxClients", &options.MaxClients)
	setBool("playbackGoesLive", &options.PlaybackGoesLive)
	setUint("pruneDays", &options.PruneDays)
	setUint("logPruneDays", &options.LogPruneDays)
	setUint("logPruneCount", &options.LogPruneCount)
	setBool("searchPatchedTalkgroups", &options.SearchPatchedTalkgroups)
	setBool("showListenerStats", &options.ShowListenerStats)
	setBool("showListenersCount", &options.ShowListenersCount)
	setBool("sortByGroups", &options.SortByGroups)
	setBool("sortByTags", &options.SortByTags)
	if options.SortByGroups && options.SortByTags {
		options.SortByGroups = false
	}
	setBool("sortTalkgroups", &options.SortTalkgroups)
	setBool("tagsToggle", &options.TagsToggle)
	setBool("time12hFormat", &options.Time12hFormat)
	setStr("umamiUrl", &options.UmamiUrl)
	setStr("umamiWebsiteId", &options.UmamiWebsiteId)
	setStr("updateUrl", &options.UpdateUrl)
	setBool("updatePrereleases", &options.UpdatePrereleases)
	setStr("pluginRepos", &options.PluginRepos)

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
		{"dualLed", options.DualLed},
		{"duplicateDetectionTimeFrame", options.DuplicateDetectionTimeFrame},
		{"email", options.Email},
		{"keypadBeeps", options.KeypadBeeps},
		{"maxClients", options.MaxClients},
		{"playbackGoesLive", options.PlaybackGoesLive},
		{"pruneDays", options.PruneDays},
		{"logPruneDays", options.LogPruneDays},
		{"logPruneCount", options.LogPruneCount},
		{"searchPatchedTalkgroups", options.SearchPatchedTalkgroups},
		{"showListenerStats", options.ShowListenerStats},
		{"showListenersCount", options.ShowListenersCount},
		{"sortByGroups", options.SortByGroups},
		{"sortByTags", options.SortByTags},
		{"sortTalkgroups", options.SortTalkgroups},
		{"tagsToggle", options.TagsToggle},
		{"time12hFormat", options.Time12hFormat},
		{"umamiUrl", options.UmamiUrl},
		{"umamiWebsiteId", options.UmamiWebsiteId},
		{"updateUrl", options.UpdateUrl},
		{"updatePrereleases", options.UpdatePrereleases},
		{"pluginRepos", options.PluginRepos},
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
	options.LogPruneDays = defaults.options.logPruneDays
	options.LogPruneCount = defaults.options.logPruneCount
	options.SearchPatchedTalkgroups = defaults.options.searchPatchedTalkgroups
	options.ShowListenerStats = defaults.options.showListenerStats
	options.ShowListenersCount = defaults.options.showListenersCount
	options.SortByGroups = defaults.options.sortByGroups
	options.SortByTags = defaults.options.sortByTags
	options.SortTalkgroups = defaults.options.sortTalkgroups
	options.TagsToggle = defaults.options.tagsToggle
	options.UpdatePrereleases = false

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
		applyBool("dualLed", &options.DualLed)
		applyUint("duplicateDetectionTimeFrame", &options.DuplicateDetectionTimeFrame)
		applyStr("email", &options.Email)
		applyStr("keypadBeeps", &options.KeypadBeeps)
		applyUint("maxClients", &options.MaxClients)
		applyBool("playbackGoesLive", &options.PlaybackGoesLive)
		applyUint("pruneDays", &options.PruneDays)
		applyUint("logPruneDays", &options.LogPruneDays)
		applyUint("logPruneCount", &options.LogPruneCount)
		applyBool("searchPatchedTalkgroups", &options.SearchPatchedTalkgroups)
		applyBool("showListenerStats", &options.ShowListenerStats)
		applyBool("showListenersCount", &options.ShowListenersCount)
		applyBool("sortByGroups", &options.SortByGroups)
		applyBool("sortByTags", &options.SortByTags)
		if options.SortByGroups && options.SortByTags {
			options.SortByGroups = false
		}
		applyBool("sortTalkgroups", &options.SortTalkgroups)
		applyBool("tagsToggle", &options.TagsToggle)
		applyBool("time12hFormat", &options.Time12hFormat)
		applyStr("umamiUrl", &options.UmamiUrl)
		applyStr("umamiWebsiteId", &options.UmamiWebsiteId)
		applyStr("updateUrl", &options.UpdateUrl)
		applyBool("updatePrereleases", &options.UpdatePrereleases)
		applyStr("pluginRepos", &options.PluginRepos)
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
			// MySQL and MariaDB report changed rows rather than matched rows by
			// default. Updating a key to the value it already has therefore
			// returns zero, even though the row exists. Confirm absence before
			// inserting or every options save tries to duplicate adminPassword.
			var exists int
			err := db.QueryRow(
				"select 1 from `rdioScannerConfigs` where `key` = ?", key,
			).Scan(&exists)
			if err == nil {
				return nil
			}
			if err != sql.ErrNoRows {
				return err
			}

			// Through ExecInsert because this is where a skewed Postgres
			// sequence surfaces: an option that already has a row is only ever
			// updated, so the sequence is never touched, and the first save
			// after a new option is added is the one that fails. It then fails
			// on every save afterwards, because the row it collided over was
			// never written.
			return db.ExecInsert(
				"rdioScannerConfigs", "_id",
				"insert into `rdioScannerConfigs` (`key`, `val`) values (?, ?)", key, raw,
			)
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

