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

import "time"

// Every extension point in the server, in one place.
//
// This list is the contract. It is what the generated documentation is built
// from, and it is the checklist the plugin system is complete against — if
// something rdio does is not reachable from here, that is a gap to close rather
// than a boundary to defend.
//
// A point declared here but not yet called from anywhere is worse than no point
// at all: a plugin registers, waits, and nothing ever happens. Anything listed
// must be dispatched somewhere, and the test in plugin_points_test.go enforces
// exactly that.

const (
	// Lifecycle.
	PointStartup          = "startup"
	PointShutdown         = "shutdown"
	PointTick             = "tick"
	PointConfigChanged    = "config.changed"
	PointClientConnect    = "client.connect"
	PointClientDisconnect = "client.disconnect"

	// Ingest, in the order a call meets them.
	PointCallReceive   = "call.receive"
	PointCallAccept    = "call.accept"
	PointCallDuplicate = "call.duplicate"
	PointCallConvert   = "call.convert"
	PointCallStore     = "call.store"
	PointCallStored    = "call.stored"

	// Delivery.
	PointCallDelay      = "call.delay"
	PointCallEmit       = "call.emit"
	PointCallPayload    = "call.payload"
	PointCallEmitted    = "call.emitted"
	PointDownstreamSend = "downstream.send"
	PointClientConfig   = "client.config"

	// Access.
	PointAccessCheck = "access.check"
	PointAccessScope = "access.scope"
	PointApikeyCheck = "apikey.check"
	PointAdminCheck  = "admin.check"

	// Data.
	PointCallSearch = "call.search"
	PointCallPrune  = "call.prune"
	PointCallAudio  = "call.audio"
	PointConfigSave = "config.save"
)

// pluginPoints is every built-in point. Plugins may add their own with
// rdio.definePoint so they can extend each other without core changes.
var pluginPoints = []string{
	PointStartup,
	PointShutdown,
	PointTick,
	PointConfigChanged,
	PointClientConnect,
	PointClientDisconnect,

	PointCallReceive,
	PointCallAccept,
	PointCallDuplicate,
	PointCallConvert,
	PointCallStore,
	PointCallStored,

	PointCallDelay,
	PointCallEmit,
	PointCallPayload,
	PointCallEmitted,
	PointDownstreamSend,
	PointClientConfig,

	PointAccessCheck,
	PointAccessScope,
	PointApikeyCheck,
	PointAdminCheck,

	PointCallSearch,
	PointCallPrune,
	PointCallAudio,
	PointConfigSave,
}

// pointTimeouts overrides the default for points where the default is too
// generous. Anything running per listener or per row needs to be short: the
// cost is paid once for every recipient, not once for the call.
var pointTimeouts = map[string]time.Duration{
	PointCallEmit:     250 * time.Millisecond,
	PointCallPayload:  250 * time.Millisecond,
	PointClientConfig: 250 * time.Millisecond,
	PointAccessScope:  250 * time.Millisecond,
	PointCallSearch:   2 * time.Second,

	// The ingest points block the single goroutine draining the ingest channel,
	// because a veto cannot mean anything otherwise. The decision points get a
	// short leash: they answer a question about a call that is already in hand,
	// so a second is already generous and a slow one throttles every upload.
	// call.convert and call.store keep the default, since both do real work on
	// audio and a plugin re-encoding a long call legitimately needs the time.
	PointCallAccept:    time.Second,
	PointCallDuplicate: time.Second,
	PointCallReceive:   5 * time.Second,
}

func pointTimeout(point string) time.Duration {
	if timeout, ok := pointTimeouts[point]; ok {
		return timeout
	}
	return pluginDispatchTimeout
}
