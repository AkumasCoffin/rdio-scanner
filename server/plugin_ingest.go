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

// The ingest path, where a plugin stops being an observer.
//
// Everything before this phase let a plugin watch a call go by. These points let
// it change one or refuse it, which is the difference between a notification
// system and an extension system.
//
// The cost is that these dispatches are synchronous on the single goroutine
// draining the ingest channel: while a filter runs, no other call is being
// written. That is the only way a veto can mean anything — an asynchronous hook
// cannot stop something that has already happened. The channel buffers 8192
// calls, every handler is bounded by a timeout, and a point with nothing
// registered costs one atomic load and a map lookup, so an install with no
// plugins pays nothing at all.

// pluginCallFields is every field a filter may write back. Deliberately a
// whitelist rather than "everything in the map": `id` is assigned by the
// database, and letting a plugin set it would produce a call that silently
// overwrites another one.
var pluginCallFields = map[string]bool{
	"audio":       true,
	"audioName":   true,
	"audioType":   true,
	"dateTime":    true,
	"frequencies": true,
	"frequency":   true,
	"meta":        true,
	"patches":     true,
	"source":      true,
	"sources":     true,
	"system":      true,
	"talkgroup":   true,
}

// pointCarriesAudio names the points where the call's audio travels with it.
//
// Declared once rather than passed at each call site so the cost is visible in
// one place: including audio copies the whole blob into the runtime, typically
// 50-200 KB per call. The points that need it are the ones where a plugin acts
// on the sound itself — detecting silence, replacing the encoding. The decision
// points work on metadata and would be paying for nothing.
var pointCarriesAudio = map[string]bool{
	PointCallReceive: true,
	PointCallConvert: true,
	PointCallStore:   true,
}

// FilterCall runs one ingest point against a call: observers first, then the
// filter chain, then the result written back onto the call itself.
//
// Reports whether the call should continue through ingest. False means a plugin
// vetoed it, and the caller must stop.
//
// Building the argument is skipped entirely when nothing is registered, so an
// install with no plugins never copies an audio blob it has no use for.
func (dispatch *PluginDispatch) FilterCall(point string, call *Call) bool {
	if !dispatch.Active(point) {
		return true
	}

	value := pluginCallValue(call, pointCarriesAudio[point])

	dispatch.Notify(point, value)

	// One allowance for the whole of this call's journey through ingest. The
	// individual point timeouts are generous on purpose — re-encoding a long
	// call legitimately takes time — but they are per point and per handler, so
	// their sum is what an upload actually waits for, and every other upload
	// waits behind that.
	if call.pluginBudget == nil {
		call.pluginBudget = newPluginBudget(pluginIngestCallBudget)
	}

	filtered, keep := dispatch.FilterWithin(call.pluginBudget, point, value, pointTimeout(point))
	if !keep {
		return false
	}

	applyPluginCallValue(call, filtered)

	return true
}

// applyPluginCallValue writes a filter's returned map back onto the call.
//
// Anything unrecognised, or of a type that cannot be made sense of, is left
// alone rather than zeroed. A plugin that returns a partial object is saying
// "change these fields", not "clear the rest" — the opposite reading would make
// every filter responsible for echoing back the entire call correctly, and the
// first one to forget a field would quietly corrupt it.
func applyPluginCallValue(call *Call, value any) {
	fields, ok := value.(map[string]any)
	if !ok {
		return
	}

	for name, raw := range fields {
		if !pluginCallFields[name] {
			continue
		}

		switch name {
		case "audio":
			// Empty is not a valid replacement. A plugin returning null or a
			// zero-length buffer has almost certainly hit an error path it did
			// not handle, and honouring it would store a silent call rather than
			// leaving the original audio intact.
			if audio, err := pluginBytes(raw); err == nil && len(audio) > 0 {
				call.Audio = audio
			}

		case "audioName":
			if text, ok := raw.(string); ok {
				call.AudioName = text
			}

		case "audioType":
			if text, ok := raw.(string); ok {
				call.AudioType = text
			}

		case "dateTime":
			// Handed out as RFC3339 UTC, so that is what comes back. A plugin
			// that echoes the value unchanged must not shift the timestamp.
			if text, ok := raw.(string); ok {
				if parsed, err := time.Parse(time.RFC3339, text); err == nil {
					call.DateTime = parsed
				}
			} else if when, ok := raw.(time.Time); ok {
				call.DateTime = when
			}

		case "system":
			if id, ok := pluginUint(raw); ok && id > 0 {
				call.System = id
			}

		case "talkgroup":
			if id, ok := pluginUint(raw); ok && id > 0 {
				call.Talkgroup = id
			}

		case "meta":
			call.meta = pluginStringMap(raw)

		case "frequency":
			call.Frequency = raw

		case "frequencies":
			call.Frequencies = raw

		case "patches":
			call.Patches = raw

		case "source":
			call.Source = raw

		case "sources":
			call.Sources = raw
		}
	}
}

// pluginUint converts whatever JavaScript produced into an unsigned integer.
// Numbers arrive as int64 or float64 depending on how they were computed, so
// both have to be handled or a plugin's arithmetic changes the type underneath
// it and the value is silently dropped.
func pluginUint(raw any) (uint, bool) {
	return jsonUint(raw)
}

// pluginStringMap normalises the metadata map, stringifying values so a plugin
// storing a number does not end up with a map the database layer refuses.
func pluginStringMap(raw any) map[string]string {
	source, ok := raw.(map[string]any)
	if !ok {
		if already, ok := raw.(map[string]string); ok {
			return already
		}
		return nil
	}

	out := make(map[string]string, len(source))
	for key, value := range source {
		switch v := value.(type) {
		case string:
			out[key] = v
		case nil:
			continue
		default:
			out[key] = fmt.Sprintf("%v", v)
		}
	}

	return out
}

// KeepDuplicate asks whether a call core has identified as a duplicate should be
// let through anyway.
//
// This is the one decision that reads inverted from the others. Core has already
// decided to reject, so a filter returning nothing must leave that rejection
// standing — the plugin has to say `keep: true` explicitly to overrule it.
// Silence means "I have no opinion", never "let it in".
func (dispatch *PluginDispatch) KeepDuplicate(call *Call) bool {
	if !dispatch.Active(PointCallDuplicate) {
		return false
	}

	value := pluginCallValue(call, false)

	dispatch.Notify(PointCallDuplicate, value)

	filtered, ok := dispatch.Filter(PointCallDuplicate, value, pointTimeout(PointCallDuplicate))
	if !ok {
		return false
	}

	fields, isMap := filtered.(map[string]any)
	if !isMap {
		return false
	}

	keep, _ := fields["keep"].(bool)
	if keep {
		applyPluginCallValue(call, filtered)
	}

	return keep
}

// ConvertCall gives a plugin the chance to replace audio conversion outright.
//
// Reports whether a plugin handled it. When one does and fails, the error is
// returned rather than swallowed: override is the verb where the plugin owns the
// outcome, because there is no original behaviour left to fall back to.
func (dispatch *PluginDispatch) ConvertCall(call *Call) (bool, error) {
	if !dispatch.Active(PointCallConvert) {
		return false, nil
	}

	result, handled, err := dispatch.Override(
		PointCallConvert,
		pluginCallValue(call, true),
		pointTimeout(PointCallConvert),
	)

	if !handled {
		return false, nil
	}

	if err != nil {
		return true, err
	}

	applyPluginCallValue(call, result)

	return true, nil
}
