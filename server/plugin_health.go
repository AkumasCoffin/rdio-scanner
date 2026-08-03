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
	"sync"
	"time"
)

// When the server stops trusting a plugin.
//
// The server can now measure what plugins cost, which raises the question of
// what it should do about one that is ruining it. Nothing is the wrong answer
// on a large site, where the operator may not be watching and the symptom —
// uploads backing up, listeners falling behind — points nowhere near the cause.
//
// So: escalation, never a hair trigger. A plugin that overruns is warned about,
// visibly, several times. One that keeps overrunning across a sustained window
// is disabled with the reason written where the admin panel already shows it.
// A single slow call must never disable a working plugin, because a slow call
// happens — a long recording, a cold cache, a database checkpoint.

const (
	// pluginStrikeWindow is how long a plugin has to redeem itself. Strikes
	// older than this are forgotten, so a plugin that misbehaves once a day
	// never accumulates its way to being disabled.
	pluginStrikeWindow = 10 * time.Minute

	// pluginStrikesBeforeWarning is how many overruns are absorbed silently.
	// Not zero: the first timeout on a busy server is usually the server, not
	// the plugin.
	pluginStrikesBeforeWarning = 5

	// pluginStrikesBeforeDisable is sustained misbehaviour rather than a bad
	// moment. At the hot points this is reached in seconds by a plugin that is
	// genuinely broken, and never by one that is merely slow now and then.
	pluginStrikesBeforeDisable = 50
)

// pluginHealth tracks recent misbehaviour per plugin.
type pluginHealth struct {
	mutex   sync.Mutex
	strikes map[string][]time.Time
	warned  map[string]bool
	// disabled remembers who has already been acted on, so the decision is
	// made once rather than every time another strike lands.
	disabled map[string]bool
}

func newPluginHealth() *pluginHealth {
	return &pluginHealth{
		strikes:  map[string][]time.Time{},
		warned:   map[string]bool{},
		disabled: map[string]bool{},
	}
}

// pluginVerdict is what the server has decided about a plugin, if anything.
type pluginVerdict int

const (
	pluginVerdictFine pluginVerdict = iota
	pluginVerdictWarn
	pluginVerdictDisable
)

// strike records one overrun and reports what should follow.
func (health *pluginHealth) strike(pluginId string) pluginVerdict {
	if health == nil {
		return pluginVerdictFine
	}

	now := time.Now()

	health.mutex.Lock()
	defer health.mutex.Unlock()

	if health.disabled[pluginId] {
		return pluginVerdictFine
	}

	// Only recent strikes count. A plugin that overruns once an hour is not
	// the problem this exists for, and letting those accumulate would
	// eventually disable something that was working.
	kept := health.strikes[pluginId][:0]
	for _, at := range health.strikes[pluginId] {
		if now.Sub(at) < pluginStrikeWindow {
			kept = append(kept, at)
		}
	}

	kept = append(kept, now)
	health.strikes[pluginId] = kept

	switch {
	case len(kept) >= pluginStrikesBeforeDisable:
		health.disabled[pluginId] = true
		return pluginVerdictDisable

	case len(kept) >= pluginStrikesBeforeWarning && !health.warned[pluginId]:
		health.warned[pluginId] = true
		return pluginVerdictWarn
	}

	return pluginVerdictFine
}

// forget clears a plugin's record, so re-enabling one an operator has looked at
// starts it from a clean slate rather than one strike from being disabled
// again.
func (health *pluginHealth) forget(pluginId string) {
	if health == nil {
		return
	}

	health.mutex.Lock()
	defer health.mutex.Unlock()

	delete(health.strikes, pluginId)
	delete(health.warned, pluginId)
	delete(health.disabled, pluginId)
}

// noteMisbehaviour is the dispatch-side entry point: a handler timed out or ran
// out of budget, and this decides whether that is worth doing anything about.
func (dispatch *PluginDispatch) noteMisbehaviour(pluginId string, point string, reason string) {
	switch dispatch.health.strike(pluginId) {
	case pluginVerdictWarn:
		dispatch.controller.Logs.LogEvent(LogLevelWarn, fmt.Sprintf(
			"plugin %s keeps overrunning at %s (%s). It is still running. Work this slow belongs on its own worker rather than in a handler the server waits for.",
			pluginId, point, reason,
		))

	case pluginVerdictDisable:
		dispatch.disableMisbehavingPlugin(pluginId, point, reason)
	}
}

// disableMisbehavingPlugin stops a plugin and says why, in the place the admin
// panel already looks.
func (dispatch *PluginDispatch) disableMisbehavingPlugin(pluginId string, point string, reason string) {
	controller := dispatch.controller
	if controller == nil || controller.Plugins == nil {
		return
	}

	explanation := fmt.Sprintf(
		"disabled automatically: overran its time budget at %s more than %d times in %s (%s)",
		point, pluginStrikesBeforeDisable, pluginStrikeWindow, reason,
	)

	controller.Logs.LogEvent(LogLevelError, fmt.Sprintf("plugin %s %s", pluginId, explanation))

	// Stopped first, then recorded. A plugin left running with its row saying
	// disabled is the worst of both, and stopping is the part that actually
	// protects the server.
	controller.Plugins.StopOne(controller, pluginId)

	if plugin, ok := controller.Plugins.Get(pluginId); ok {
		plugin.Error = explanation
	}

	if controller.Database != nil {
		if err := controller.Plugins.SetEnabled(controller.Database, pluginId, false); err != nil {
			controller.Logs.LogEvent(LogLevelError, fmt.Sprintf(
				"plugin %s was stopped but could not be recorded as disabled, so it will start again on the next restart: %v",
				pluginId, err,
			))
		}
	}
}
