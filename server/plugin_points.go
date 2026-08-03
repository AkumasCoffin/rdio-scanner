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
	PointStartup = "startup"
	// PointPluginsReady fires once, after every plugin has started.
	//
	// startup fires per plugin as it loads, so a plugin asking what else is
	// running from there sees only the ones that happened to load first — and
	// load order is not something a plugin should depend on. Found exactly that
	// way: a plugin logged "transcripts present: false" at startup and then
	// called into transcripts successfully a moment later.
	PointPluginsReady = "plugins.ready"
	PointShutdown     = "shutdown"
	PointTick             = "tick"
	PointConfigChanged    = "config.changed"
	PointClientConnect    = "client.connect"
	PointClientDisconnect = "client.disconnect"

	// Ingest, in the order a call meets them.
	PointCallReceive = "call.receive"
	// PointCallSystem and PointCallTalkgroup sit where a call's system and
	// talkgroup are resolved.
	//
	// They exist for the plugin that is itself an ingest source. Such a plugin
	// knows what it is feeding in — it read the name off a control channel or a
	// CAD feed — but had no way to say so: it could create the call and then
	// only watch rdio label it "System 3" and "Talkgroup 10329". Provide
	// supplies a definition rdio does not have; filter amends one it does.
	PointCallSystem    = "call.system"
	PointCallTalkgroup = "call.talkgroup"
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

// pluginPointDef declares a point and the verbs core actually invokes on it.
//
// The verbs are the load-bearing part. Registering was checked against the
// point's name only, so a verb the point never invokes was accepted without
// complaint and then never fired: rdio.filter('call.convert') and
// rdio.on('call.audio') both registered successfully and did nothing at all.
// That is precisely the failure the point list exists to prevent — the check
// was simply one level too coarse.
type pluginPointDef struct {
	name  string
	verbs []pluginVerb
	// group is the heading this point appears under in the reference. It lives
	// here rather than in a list the documentation keeps separately, because
	// that list was hand-maintained and plugins.ready was missing from it — the
	// one point the getting-started guide tells every author to use was absent
	// from the reference entirely, and the test meant to catch that passed by
	// accident because the name happened to appear inside another point's note.
	group string
}

var (
	verbsObserve       = []pluginVerb{verbOn}
	verbsObserveFilter = []pluginVerb{verbOn, verbFilter}
	verbsOverride      = []pluginVerb{verbOverride}
	verbsProvide       = []pluginVerb{verbProvide}
	verbsAuth          = []pluginVerb{verbOn, verbFilter, verbProvide}
)

// pluginPointDefs is every built-in point. Plugins may add their own with
// rdio.definePoint so they can extend each other without core changes.
var pluginPointDefs = []pluginPointDef{
	// Lifecycle. Delivered through the runtime's own handler list, which only
	// ever holds observers.
	{PointStartup, verbsObserve, "Lifecycle"},
	{PointPluginsReady, verbsObserve, "Lifecycle"},
	{PointShutdown, verbsObserve, "Lifecycle"},
	{PointTick, verbsObserve, "Lifecycle"},
	{PointConfigChanged, verbsObserve, "Lifecycle"},
	{PointClientConnect, verbsObserve, "Lifecycle"},
	{PointClientDisconnect, verbsObserve, "Lifecycle"},

	// Ingest.
	{PointCallReceive, verbsObserveFilter, "Ingest"},
	// Same shape as the access points, and for the same reason: provide fills
	// a gap rdio cannot fill itself, filter narrows what it already decided.
	{PointCallSystem, verbsAuth, "Ingest"},
	{PointCallTalkgroup, verbsAuth, "Ingest"},
	{PointCallAccept, verbsObserveFilter, "Ingest"},
	{PointCallDuplicate, verbsObserveFilter, "Ingest"},
	// Replacing conversion is all-or-nothing, so there is nothing to observe
	// or amend — a plugin either owns the encoder or it does not.
	{PointCallConvert, verbsOverride, "Ingest"},
	{PointCallStore, verbsObserveFilter, "Ingest"},
	{PointCallStored, verbsObserve, "Ingest"},

	// Delivery.
	{PointCallDelay, verbsObserveFilter, "Delivery"},
	{PointCallEmit, verbsObserveFilter, "Delivery"},
	{PointCallPayload, verbsObserveFilter, "Delivery"},
	{PointCallEmitted, verbsObserve, "Delivery"},
	{PointDownstreamSend, verbsObserveFilter, "Delivery"},
	{PointClientConfig, verbsObserveFilter, "Delivery"},

	// Access. Provide supplies a decision core could not make; filter then
	// narrows or refuses one it did.
	{PointAccessCheck, verbsAuth, "Access"},
	{PointAccessScope, verbsObserveFilter, "Access"},
	{PointApikeyCheck, verbsAuth, "Access"},
	{PointAdminCheck, verbsAuth, "Access"},

	// Data.
	{PointCallSearch, verbsObserveFilter, "Data"},
	{PointCallPrune, verbsObserveFilter, "Data"},
	// Supplying audio core does not have is the whole purpose; there is no
	// original value to observe or filter.
	{PointCallAudio, verbsProvide, "Data"},
	{PointConfigSave, verbsObserveFilter, "Data"},
}

// pluginPoints is the names, derived so the two can never disagree.
var pluginPoints = func() []string {
	names := make([]string, 0, len(pluginPointDefs))
	for _, def := range pluginPointDefs {
		names = append(names, def.name)
	}
	return names
}()

// pointVerbs answers what a point accepts. A point defined by a plugin is
// absent, and accepts anything — core cannot know what its author intended.
var pointVerbs = func() map[string][]pluginVerb {
	verbs := make(map[string][]pluginVerb, len(pluginPointDefs))
	for _, def := range pluginPointDefs {
		verbs[def.name] = def.verbs
	}
	return verbs
}()

// pointAcceptsVerb reports whether registering this verb against this point
// would ever fire.
func pointAcceptsVerb(point string, verb pluginVerb) bool {
	allowed, known := pointVerbs[point]
	if !known {
		return true
	}

	for _, candidate := range allowed {
		if candidate == verb {
			return true
		}
	}

	return false
}

// pluginPointGroups is the reference's headings, in order, each with the points
// that belong to it — derived from the declarations, so a point added without a
// group is a compile error rather than a silent omission.
func pluginPointGroups() []struct {
	Title  string
	Points []string
} {
	order := []string{"Lifecycle", "Ingest", "Delivery", "Access", "Data"}

	byGroup := map[string][]string{}
	for _, def := range pluginPointDefs {
		byGroup[def.group] = append(byGroup[def.group], def.name)
	}

	out := []struct {
		Title  string
		Points []string
	}{}

	for _, title := range order {
		out = append(out, struct {
			Title  string
			Points []string
		}{Title: title, Points: byGroup[title]})
	}

	return out
}

// pointVerbNames is the accepted verbs in a form fit for an error message or a
// documentation table.
func pointVerbNames(point string) []string {
	allowed, known := pointVerbs[point]
	if !known {
		return nil
	}

	names := make([]string, 0, len(allowed))
	for _, verb := range allowed {
		names = append(names, verb.String())
	}

	return names
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
