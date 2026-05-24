// Copyright (C) 2019-2026 Chrystian Huot <chrystian.huot@saubeo.solutions>
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

// Delayer defers emitting calls to live WebSocket listeners by a configurable
// per-talkgroup or per-system delay (seconds). Talkgroup delay takes
// precedence over system delay; either being zero falls through to immediate
// emit. The schedule survives restarts via the rdioScannerDelayed table.
//
// Downstream forwarding is NOT affected — IngestCall fires
// EmitCallToDownstreams synchronously before handing the call to the Delayer,
// so downstream instances receive calls as soon as they're ingested. The
// downstream is responsible for its own listener-delay policy.
type Delayer struct {
	controller *Controller
	mutex      sync.Mutex
	timers     map[uint]*time.Timer
}

func NewDelayer(controller *Controller) *Delayer {
	return &Delayer{
		controller: controller,
		timers:     make(map[uint]*time.Timer),
	}
}

func (delayer *Delayer) Delay(call *Call) {
	delay := delayer.getDelay(call)

	if delay == 0 {
		delayer.controller.EmitCallToClients(call)
		return
	}

	logError := func(err error) {
		delayer.controller.Logs.LogEvent(LogLevelError, fmt.Sprintf("delayer.delay: %s", err.Error()))
	}

	callId, ok := callIdAsUint(call.Id)
	if !ok {
		logError(fmt.Errorf("call id is not uint: %T", call.Id))
		return
	}

	timestamp := delayer.getTimestamp(call)
	remaining := time.Until(timestamp)
	if remaining <= 0 {
		delayer.controller.EmitCallToClients(call)
		return
	}

	call.Delayed = true

	if err := delayer.push(callId, timestamp); err != nil {
		logError(err)
		// Fall back to in-memory only — still emit after the delay so the
		// call isn't lost, just not crash-safe.
	}

	timer := time.AfterFunc(remaining, func() {
		if err := delayer.pop(callId); err != nil {
			logError(err)
		}

		delayer.mutex.Lock()
		delete(delayer.timers, callId)
		delayer.mutex.Unlock()

		delayer.controller.EmitCallToClients(call)
	})

	delayer.mutex.Lock()
	delayer.timers[callId] = timer
	delayer.mutex.Unlock()
}

// Start restores any pending delays from rdioScannerDelayed. Calls whose
// emit moment has already passed are emitted immediately; the rest are
// re-armed via Delay.
func (delayer *Delayer) Start() error {
	formatError := func(err error, query string) error {
		return fmt.Errorf("delayer.start: %v while doing %s", err, query)
	}

	rows, err := delayer.controller.Database.Query("select `callId`, `timestamp` from `rdioScannerDelayed`")
	if err != nil {
		return formatError(err, "select rdioScannerDelayed")
	}

	pending := map[uint]int64{}
	for rows.Next() {
		var (
			callId    uint
			timestamp int64
		)
		if err = rows.Scan(&callId, &timestamp); err != nil {
			break
		}
		pending[callId] = timestamp
	}
	rows.Close()
	if err != nil {
		return formatError(err, "scan rdioScannerDelayed")
	}

	if len(pending) == 0 {
		return nil
	}

	// Clear the table — we'll re-insert the still-future entries via Delay.
	if _, err = delayer.controller.Database.Exec("delete from `rdioScannerDelayed`"); err != nil {
		return formatError(err, "delete rdioScannerDelayed")
	}

	now := time.Now()
	for callId, ts := range pending {
		call, gerr := delayer.controller.Calls.GetCall(callId, delayer.controller.Database)
		if gerr != nil {
			continue
		}
		call.Delayed = true

		if time.UnixMilli(ts).Before(now) {
			delayer.controller.EmitCallToClients(call)
		} else {
			delayer.Delay(call)
		}
	}

	return nil
}

func (delayer *Delayer) getDelay(call *Call) uint {
	if system, ok := delayer.controller.Systems.GetSystem(call.System); ok {
		if talkgroup, ok := system.Talkgroups.GetTalkgroup(call.Talkgroup); ok && talkgroup.Delay > 0 {
			return talkgroup.Delay
		}
		if system.Delay > 0 {
			return system.Delay
		}
	}
	return 0
}

func (delayer *Delayer) getTimestamp(call *Call) time.Time {
	return call.DateTime.Add(time.Duration(delayer.getDelay(call)) * time.Second)
}

func (delayer *Delayer) push(callId uint, timestamp time.Time) error {
	delayer.mutex.Lock()
	defer delayer.mutex.Unlock()

	if _, err := delayer.controller.Database.Exec(
		"insert into `rdioScannerDelayed` (`callId`, `timestamp`) values (?, ?)",
		callId, timestamp.UnixMilli(),
	); err != nil {
		return fmt.Errorf("delayer.push: %v", err)
	}
	return nil
}

func (delayer *Delayer) pop(callId uint) error {
	delayer.mutex.Lock()
	defer delayer.mutex.Unlock()

	if _, err := delayer.controller.Database.Exec(
		"delete from `rdioScannerDelayed` where `callId` = ?", callId,
	); err != nil {
		return fmt.Errorf("delayer.pop: %v", err)
	}
	return nil
}

// callIdAsUint normalizes the Call.Id any field into a uint. The runtime
// type is always uint after IngestCall stores the value returned by
// Calls.WriteCall, but other paths (test fixtures, public_api) may stuff a
// different numeric kind in there, so handle the common cases gracefully.
func callIdAsUint(id any) (uint, bool) {
	switch v := id.(type) {
	case uint:
		return v, true
	case int:
		if v >= 0 {
			return uint(v), true
		}
	case int64:
		if v >= 0 {
			return uint(v), true
		}
	case uint64:
		return uint(v), true
	case float64:
		if v >= 0 {
			return uint(v), true
		}
	}
	return 0, false
}

