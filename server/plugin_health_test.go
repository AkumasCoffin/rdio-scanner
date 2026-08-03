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
	"testing"
	"time"
)

// The rule that matters most: a slow call happens. A long recording, a cold
// cache, a database checkpoint. None of those should cost an operator a working
// plugin.
func TestOneSlowCallNeverDisablesAPlugin(t *testing.T) {
	health := newPluginHealth()

	if verdict := health.strike("p"); verdict != pluginVerdictFine {
		t.Fatalf("a single overrun produced verdict %v", verdict)
	}

	// Everything short of the threshold still only warns. One strike has
	// already landed above, so this stops one short of it.
	var sawWarning bool
	for i := 1; i < pluginStrikesBeforeDisable-1; i++ {
		switch health.strike("p") {
		case pluginVerdictDisable:
			t.Fatalf("disabled after %d strikes, before the threshold of %d", i+1, pluginStrikesBeforeDisable)
		case pluginVerdictWarn:
			sawWarning = true
		}
	}

	if !sawWarning {
		t.Error("nothing was said before the plugin was on the brink of being disabled")
	}
}

// Sustained misbehaviour is a different thing, and on a large site nobody may
// be watching.
func TestSustainedOverrunningDisables(t *testing.T) {
	health := newPluginHealth()

	disabled := false
	for i := 0; i < pluginStrikesBeforeDisable+5; i++ {
		if health.strike("p") == pluginVerdictDisable {
			disabled = true
			break
		}
	}

	if !disabled {
		t.Fatal("a plugin overrunning without pause was never acted on")
	}

	// The decision is made once, not on every strike after it.
	for i := 0; i < 10; i++ {
		if health.strike("p") == pluginVerdictDisable {
			t.Fatal("the disable decision repeated")
		}
	}
}

// Strikes age out, so a plugin that stumbles once in a while never accumulates
// its way to being disabled.
func TestStrikesExpire(t *testing.T) {
	health := newPluginHealth()

	// Land nearly enough strikes, then age them past the window.
	for i := 0; i < pluginStrikesBeforeDisable-1; i++ {
		health.strike("p")
	}

	health.mutex.Lock()
	stale := time.Now().Add(-2 * pluginStrikeWindow)
	for i := range health.strikes["p"] {
		health.strikes["p"][i] = stale
	}
	health.mutex.Unlock()

	if verdict := health.strike("p"); verdict == pluginVerdictDisable {
		t.Fatal("strikes from outside the window still counted; a plugin misbehaving once an hour would eventually be disabled")
	}

	health.mutex.Lock()
	remaining := len(health.strikes["p"])
	health.mutex.Unlock()

	if remaining != 1 {
		t.Fatalf("%d strikes survived the window, expected only the new one", remaining)
	}
}

// Re-enabling is an operator saying they have looked at it, so the record goes
// with it — otherwise the plugin is one strike from being disabled again and
// the operator has no way to know.
func TestForgettingResetsAPlugin(t *testing.T) {
	health := newPluginHealth()

	for i := 0; i < pluginStrikesBeforeDisable+1; i++ {
		health.strike("p")
	}

	health.forget("p")

	if verdict := health.strike("p"); verdict != pluginVerdictFine {
		t.Fatalf("a plugin the operator re-enabled started at verdict %v", verdict)
	}
}

// Plugins are judged separately. One misbehaving must not spend another's
// budget of goodwill.
func TestPluginsAreJudgedSeparately(t *testing.T) {
	health := newPluginHealth()

	for i := 0; i < pluginStrikesBeforeDisable+1; i++ {
		health.strike("noisy")
	}

	if verdict := health.strike("quiet"); verdict != pluginVerdictFine {
		t.Fatalf("an unrelated plugin was judged %v", verdict)
	}
}

func TestHealthToleratesBeingAbsent(t *testing.T) {
	var health *pluginHealth

	if verdict := health.strike("p"); verdict != pluginVerdictFine {
		t.Error("a nil health produced a verdict")
	}
	health.forget("p")
}
