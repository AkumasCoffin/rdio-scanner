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
	"fmt"
	"time"
)

// The read paths — searching, retention, and where a call's audio comes from.
//
// call.audio is the one that changes what is possible rather than what is
// convenient. Until now a call's audio had to be the blob in the calls table,
// which meant retention was a single decision for the whole install: keep
// everything and pay for the disk, or delete it and lose it. A provider here
// lets the row stay and the bytes live somewhere else — S3, a NAS, tape, another
// rdio — and be fetched back when someone actually presses play.

// ProvideCallAudio fills in a call's audio when the database no longer holds it.
//
// Only asked when the blob is empty, so an install storing audio normally never
// reaches a plugin at all. That is also what makes a retention plugin's job
// simple: blank the column, leave the row, and answer here.
func (dispatch *PluginDispatch) ProvideCallAudio(call *Call) {
	if call == nil || len(call.Audio) > 0 {
		return
	}

	if !dispatch.Active(PointCallAudio) {
		return
	}

	value := pluginCallValue(call, false)

	result, ok := dispatch.Provide(PointCallAudio, value, pointTimeout(PointCallAudio))
	if !ok {
		return
	}

	if applyPluginCallAudio(call, result) {
		dispatch.controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf(
			"call %v audio restored by plugin (%d bytes)", call.Id, len(call.Audio),
		))
	}
}

// FilterPrune settles how many days of calls to keep, or whether to prune at all
// this cycle.
//
// Runs once an hour at most. A plugin archiving calls before they are deleted
// wants the observer here and its own sweep on rdio.schedule — the filter is for
// deciding retention, not for doing the archiving, because a prune that waited
// on an upload to cold storage would hold the scheduler for as long as that took.
func (dispatch *PluginDispatch) FilterPrune(days uint) (uint, bool) {
	if !dispatch.Active(PointCallPrune) {
		return days, true
	}

	cutoff := time.Now().Add(-24 * time.Hour * time.Duration(days))

	value := map[string]any{
		"days":   days,
		"before": cutoff.UTC().Format(time.RFC3339),
	}

	dispatch.Notify(PointCallPrune, value)

	filtered, ok := dispatch.Filter(PointCallPrune, value, pointTimeout(PointCallPrune))
	if !ok {
		// A veto skips this cycle rather than disabling retention. The next tick
		// asks again, so a plugin holding calls back while it archives them does
		// not have to remember to turn pruning back on.
		dispatch.controller.Logs.LogEvent(LogLevelInfo, "call pruning skipped this cycle by plugin")
		return days, false
	}

	fields, isMap := filtered.(map[string]any)
	if !isMap {
		return days, true
	}

	if updated, ok := pluginUint(fields["days"]); ok && updated > 0 {
		return updated, true
	}

	return days, true
}

// FilterSearch lets a plugin narrow a search before it runs.
//
// Deliberately the query and not the results. Handing back a page of calls would
// mean marshalling every row into the runtime and filtering it in JavaScript on
// a path a user is waiting on; a plugin that wants to contribute searchable data
// registers it with rdio.search.extend instead, and core does the lookup
// natively in the same SQL. This point is for constraining what may be asked,
// which is one small object per search.
func (dispatch *PluginDispatch) FilterSearch(options *CallsSearchOptions, client *Client) bool {
	if options == nil || !dispatch.Active(PointCallSearch) {
		return true
	}

	// Every option is `any` and unset means nil, so they go across as they are
	// and a plugin sees undefined for anything the client did not ask for.
	value := map[string]any{
		"client":    pluginClientValue(client),
		"date":      options.Date,
		"group":     options.Group,
		"limit":     options.Limit,
		"offset":    options.Offset,
		"q":         options.Q,
		"sort":      options.Sort,
		"system":    options.System,
		"tag":       options.Tag,
		"talkgroup": options.Talkgroup,
	}

	dispatch.Notify(PointCallSearch, value)

	filtered, allowed := dispatch.Filter(PointCallSearch, value, pointTimeout(PointCallSearch))
	if !allowed {
		return false
	}

	if fields, isMap := filtered.(map[string]any); isMap {
		applyPluginSearchOptions(options, fields)
	}

	return true
}

// applyPluginSearchOptions writes a plugin's answer back onto a search.
//
// Everything is stored as uint or string, matching what fromMap produces from
// the client's own request. Search reads these with type switches, so writing
// back the int64 a JavaScript literal produces would leave the option silently
// ignored — the search would run unnarrowed and look like the plugin had simply
// chosen not to act.
func applyPluginSearchOptions(options *CallsSearchOptions, fields map[string]any) {
	if options == nil {
		return
	}

	if limit, ok := pluginUint(fields["limit"]); ok && limit > 0 {
		options.Limit = limit
	}

	if offset, ok := pluginUint(fields["offset"]); ok {
		options.Offset = offset
	}

	if system, ok := pluginUint(fields["system"]); ok {
		options.System = system
	}

	if talkgroup, ok := pluginUint(fields["talkgroup"]); ok {
		options.Talkgroup = talkgroup
	}

	if group, ok := fields["group"].(string); ok && group != "" {
		options.Group = group
	}

	if tag, ok := fields["tag"].(string); ok && tag != "" {
		options.Tag = tag
	}

	if q, ok := fields["q"].(string); ok && q != "" {
		options.Q = q
	}
}

// applyPluginCallAudio writes a provider's answer onto a call.
//
// Accepts either the bytes directly or an object carrying them, because cold
// storage that re-encodes needs to correct the type as well. An empty answer is
// ignored rather than stored: a provider that failed and returned nothing must
// not turn a recoverable call into a silent one.
func applyPluginCallAudio(call *Call, result any) bool {
	if call == nil {
		return false
	}

	switch v := result.(type) {
	case map[string]any:
		audio, err := pluginBytes(v["audio"])
		if err != nil || len(audio) == 0 {
			return false
		}

		call.Audio = audio

		if name, ok := v["audioName"].(string); ok && name != "" {
			call.AudioName = name
		}
		if kind, ok := v["audioType"].(string); ok && kind != "" {
			call.AudioType = kind
		}

		return true

	default:
		audio, err := pluginBytes(result)
		if err != nil || len(audio) == 0 {
			return false
		}

		call.Audio = audio

		return true
	}
}

// FilterConfigSave runs before an admin configuration is written.
//
// Both verbs are useful here and they do different jobs: an observer is how a
// plugin notices that the systems it mirrors have changed, and a filter is how
// it refuses a configuration it cannot work with. Rare enough — a human pressing
// save — that the default timeout is not worth tightening.
func (dispatch *PluginDispatch) FilterConfigSave(config map[string]any) (map[string]any, bool) {
	if !dispatch.Active(PointConfigSave) {
		return config, true
	}

	value := map[string]any{"config": config}

	dispatch.Notify(PointConfigSave, value)

	filtered, allowed := dispatch.Filter(PointConfigSave, value, pointTimeout(PointConfigSave))
	if !allowed {
		dispatch.controller.Logs.LogEvent(LogLevelWarn, "configuration save refused by plugin")
		return config, false
	}

	fields, isMap := filtered.(map[string]any)
	if !isMap {
		return config, true
	}

	updated, isMap := fields["config"].(map[string]any)
	if !isMap {
		return config, true
	}

	return updated, true
}
