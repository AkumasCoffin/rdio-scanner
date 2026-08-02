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

import "testing"

// TestSearchOptionsComeBackAsUint is the type check that decides whether a
// narrowed search is actually narrowed.
//
// Search reads every option with a type switch on uint, while a JavaScript
// literal arrives as int64. Writing the raw value back would leave the option
// ignored and the search running unconstrained — indistinguishable, from the
// outside, from a plugin that chose not to act.
func TestSearchOptionsComeBackAsUint(t *testing.T) {
	for name, value := range map[string]any{
		"int64":   int64(25),
		"float64": float64(25),
	} {
		options := &CallsSearchOptions{}

		applyPluginSearchOptions(options, map[string]any{
			"limit":     value,
			"system":    value,
			"talkgroup": value,
		})

		for field, got := range map[string]any{
			"limit":     options.Limit,
			"system":    options.System,
			"talkgroup": options.Talkgroup,
		} {
			if got != uint(25) {
				t.Errorf("%s %s came back as %T(%v), which Search ignores", name, field, got, got)
			}
		}
	}
}

// TestSearchOptionsIgnorePartialResults keeps the contract every other filter
// has: naming one field changes one field.
func TestSearchOptionsIgnorePartialResults(t *testing.T) {
	options := &CallsSearchOptions{
		Limit:     uint(50),
		System:    uint(1),
		Talkgroup: uint(101),
		Q:         "fire",
	}

	applyPluginSearchOptions(options, map[string]any{"limit": int64(10)})

	if options.Limit != uint(10) {
		t.Errorf("limit was not applied: %v", options.Limit)
	}
	if options.System != uint(1) || options.Talkgroup != uint(101) {
		t.Errorf("an unnamed field moved: system %v talkgroup %v", options.System, options.Talkgroup)
	}
	if options.Q != "fire" {
		t.Errorf("the query text changed to %q", options.Q)
	}
}

// TestSearchOptionsRefuseZeroLimit stops a plugin's arithmetic mistake turning
// into a search that returns nothing with no explanation.
func TestSearchOptionsRefuseZeroLimit(t *testing.T) {
	options := &CallsSearchOptions{Limit: uint(50)}

	applyPluginSearchOptions(options, map[string]any{"limit": int64(0)})

	if options.Limit != uint(50) {
		t.Errorf("a zero limit was accepted: %v", options.Limit)
	}
}

// TestProvidedAudioAcceptsBothShapes covers a provider answering with the bytes
// alone and with an object that also corrects the type, which is what cold
// storage that re-encodes has to do.
func TestProvidedAudioAcceptsBothShapes(t *testing.T) {
	bare := ingestTestCall()
	bare.Audio = nil

	if !applyPluginCallAudio(bare, []byte{9, 8, 7}) {
		t.Fatal("raw bytes were refused")
	}
	if len(bare.Audio) != 3 {
		t.Errorf("audio is %d bytes", len(bare.Audio))
	}
	if bare.AudioType != "audio/mp4" {
		t.Errorf("the type changed when the provider did not name one: %v", bare.AudioType)
	}

	wrapped := ingestTestCall()
	wrapped.Audio = nil

	if !applyPluginCallAudio(wrapped, map[string]any{
		"audio":     []byte{1, 2},
		"audioType": "audio/mpeg",
		"audioName": "restored.mp3",
	}) {
		t.Fatal("an object answer was refused")
	}
	if wrapped.AudioType != "audio/mpeg" || wrapped.AudioName != "restored.mp3" {
		t.Errorf("the corrected type was not applied: %v %v", wrapped.AudioType, wrapped.AudioName)
	}
}

// TestFailedProviderDoesNotSilenceTheCall is the one that matters for
// retention. A provider that hit an error and returned nothing must leave the
// call as it was, not overwrite it with emptiness — otherwise a transient
// failure in cold storage would be indistinguishable from a call that never had
// audio.
func TestFailedProviderDoesNotSilenceTheCall(t *testing.T) {
	for name, answer := range map[string]any{
		"nil":          nil,
		"empty bytes":  []byte{},
		"empty object": map[string]any{},
		"wrong type":   42,
		"null audio":   map[string]any{"audio": nil, "audioType": "audio/mpeg"},
	} {
		call := ingestTestCall()

		if applyPluginCallAudio(call, answer) {
			t.Errorf("%s was reported as a successful restore", name)
		}

		if len(call.Audio) != 4 {
			t.Errorf("%s wiped the audio: %d bytes left", name, len(call.Audio))
		}

		if call.AudioType != "audio/mp4" {
			t.Errorf("%s changed the type to %v despite supplying no audio", name, call.AudioType)
		}
	}
}

// TestAudioIsOnlyAskedForWhenMissing guards the condition that keeps this point
// free for everyone not using cold storage.
func TestAudioIsOnlyAskedForWhenMissing(t *testing.T) {
	dispatch := &PluginDispatch{points: map[string]bool{}}
	dispatch.registry.Store(&dispatchRegistry{handlers: map[string][]*pluginHandler{}})

	call := ingestTestCall()
	original := len(call.Audio)

	// A call that still has its audio must not reach a provider at all. With no
	// handlers registered this also exercises the no-plugin fast path, which is
	// what every ordinary install runs.
	dispatch.ProvideCallAudio(call)

	if len(call.Audio) != original {
		t.Errorf("audio changed from %d to %d bytes", original, len(call.Audio))
	}
}
