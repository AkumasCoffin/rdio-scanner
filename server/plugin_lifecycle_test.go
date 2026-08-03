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

// Re-reading the registry happens after every install and every uninstall. It
// used to rebuild the list from database rows alone, dropping the runtime
// pointer, the Running flag and the resolved directory for every plugin —
// including the ones the operator was not touching.
//
// The consequences all came from one unrelated install: every plugin's frontend
// started 404ing (the asset handler requires Running), lifecycle events stopped
// being delivered, Plugins.Stop had no runtime left to stop at shutdown, and
// purge — which only refuses while Running — would drop the tables of a plugin
// still executing.
func TestReadKeepsRunningPluginsAttached(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	config := &Config{BaseDir: t.TempDir()}

	plugins := NewPlugins()

	// A plugin already in the registry, and running.
	if err := plugins.Write(db, &Plugin{PluginId: "alpha", Name: "Alpha", Version: "1.0.0", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := plugins.Read(db, config); err != nil {
		t.Fatal(err)
	}

	alpha, ok := plugins.Get("alpha")
	if !ok {
		t.Fatal("the plugin was not read back")
	}

	marker := &PluginRuntime{}
	alpha.runtime = marker
	alpha.Running = true
	alpha.dir = "/somewhere/alpha"

	// Now an unrelated plugin is installed, which re-reads the registry.
	if err := plugins.Write(db, &Plugin{PluginId: "beta", Name: "Beta", Version: "1.0.0", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := plugins.Read(db, config); err != nil {
		t.Fatal(err)
	}

	alpha, ok = plugins.Get("alpha")
	if !ok {
		t.Fatal("the running plugin disappeared from the registry")
	}

	if alpha.runtime != marker {
		t.Error("installing another plugin orphaned this plugin's runtime; nothing can stop it now")
	}
	if !alpha.Running {
		t.Error("installing another plugin marked this one not running; its frontend would start 404ing and its events would stop")
	}
	if alpha.dir != "/somewhere/alpha" {
		t.Errorf("the resolved directory was lost: %q", alpha.dir)
	}
}

// Uninstall removed the files and the row but never stopped the plugin, so it
// carried on running with its code deleted — and, once the registry was
// re-read, with nothing holding a reference to stop it.
func TestStopOneDeregistersAndClears(t *testing.T) {
	controller := &Controller{Logs: &Logs{}}
	controller.PluginDispatch = NewPluginDispatch(controller)
	controller.PluginRpc = NewPluginRpc(controller)

	plugins := NewPlugins()
	plugins.List = []*Plugin{{PluginId: "alpha", Running: true}}

	// A handler belonging to the plugin, of the kind uninstall used to leave
	// behind dispatching into deleted code.
	controller.PluginDispatch.Register(&pluginHandler{
		pluginId: "alpha",
		verb:     verbFilter,
		callable: &funcCallable{fn: func(args ...any) (any, error) { return nil, nil }},
	}, PointCallStore)

	if !controller.PluginDispatch.Active(PointCallStore) {
		t.Fatal("the handler did not register")
	}

	plugins.StopOne(controller, "alpha")

	if controller.PluginDispatch.Active(PointCallStore) {
		t.Error("the plugin's handlers still dispatch after it was stopped")
	}

	alpha, _ := plugins.Get("alpha")
	if alpha.Running {
		t.Error("the plugin still reports as running")
	}
	if alpha.runtime != nil {
		t.Error("the runtime pointer was left behind")
	}

	// Stopping something that is not there is not an error.
	if plugins.StopOne(controller, "nosuch") {
		t.Error("stopping an unknown plugin reported that it stopped something")
	}
}

// The per-code connection limit counted listeners by pointer identity, and any
// plugin scoping an access hands every client its own clone — so every client
// compared unequal, the count was always 1, and the limit never applied.
func TestAccessCountSurvivesClonedAccess(t *testing.T) {
	original := &Access{Id: uint(7), Code: "shared", Ident: "team"}

	clients := NewClients()

	// Three listeners on one access code, each holding a clone the way a
	// scope plugin leaves them.
	var first *Client
	for i := 0; i < 3; i++ {
		client := &Client{Access: cloneAccess(original)}
		if i == 0 {
			first = client
		}
		clients.Map[client] = true
	}

	if count := clients.AccessCount(first); count != 3 {
		t.Fatalf("counted %d listeners on one access code, expected 3 — the connection limit would never apply", count)
	}

	// A different code is counted separately.
	other := &Client{Access: &Access{Id: uint(8), Code: "other"}}
	clients.Map[other] = true

	if count := clients.AccessCount(other); count != 1 {
		t.Fatalf("a different access code counted %d, expected 1", count)
	}
	if count := clients.AccessCount(first); count != 3 {
		t.Fatalf("adding an unrelated listener changed the count to %d", count)
	}
}

func TestAccessIdentityPrefersTheRowThenTheCode(t *testing.T) {
	if _, ok := accessIdentity(nil); ok {
		t.Error("a nil access produced an identity")
	}
	if _, ok := accessIdentity(&Access{}); ok {
		t.Error("an access with neither a row nor a code produced an identity")
	}

	byRow, ok := accessIdentity(&Access{Id: uint(4), Code: "abc"})
	if !ok {
		t.Fatal("an access with a row produced no identity")
	}

	// A plugin-supplied access that was never in the database still counts,
	// keyed on what the listener actually presented.
	byCode, ok := accessIdentity(&Access{Code: "abc"})
	if !ok {
		t.Fatal("an access with only a code produced no identity")
	}
	if byRow == byCode {
		t.Error("a stored access and a plugin-supplied one collided on the same identity")
	}
}


// Notify called EmitTo once per registered handler, and a runtime's EmitTo runs
// every handler that runtime registered for the point — so K observers on one
// point produced K² invocations. Three rdio.on('call.stored') handlers meant
// each body ran three times per call, and one that inserted a row inserted
// three. Registering several handlers for a point is entirely ordinary and
// nothing warned against it.
func TestDeliveryIsPerRuntimeNotPerHandler(t *testing.T) {
	alpha := &PluginRuntime{}
	beta := &PluginRuntime{}

	// One plugin with three handlers must be delivered to once, not three
	// times — three deliveries would be nine invocations.
	three := []*PluginRuntime{alpha, alpha, alpha}
	if got := distinctRuntimes(len(three), func(i int) *PluginRuntime { return three[i] }); len(got) != 1 {
		t.Fatalf("three handlers in one plugin produced %d deliveries; each runs all three, so that is %d invocations instead of 3",
			len(got), len(got)*3)
	}

	// Two plugins are separate event loops and each gets its own delivery.
	mixed := []*PluginRuntime{alpha, beta, alpha, beta}
	got := distinctRuntimes(len(mixed), func(i int) *PluginRuntime { return mixed[i] })
	if len(got) != 2 {
		t.Fatalf("two plugins produced %d deliveries, expected 2", len(got))
	}
	// Order follows registration order, which is what makes dispatch
	// deterministic and reportable.
	if got[0] != alpha || got[1] != beta {
		t.Error("delivery order did not follow registration order")
	}

	if len(distinctRuntimes(0, func(int) *PluginRuntime { return nil })) != 0 {
		t.Error("no handlers produced a delivery")
	}
}

// The admin password was put into the payload every observer and filter
// received, not just the provider that needs it — so three lines of JavaScript
// in any installed plugin were enough to exfiltrate it, for a capability none
// of them have. A provider genuinely needs it: verifying a credential against
// an external directory cannot be done with a hash.
func TestAdminPasswordReachesOnlyAProvider(t *testing.T) {
	controller := &Controller{Logs: &Logs{}}
	dispatch := NewPluginDispatch(controller)

	// Observer and filter are handed the same map, so checking the filter
	// covers both. An observer needs a live runtime to dispatch into and this
	// test has no use for one.
	var providerSaw, filterSaw map[string]any

	dispatch.Register(&pluginHandler{
		pluginId: "auth", verb: verbProvide,
		callable: &funcCallable{fn: func(args ...any) (any, error) {
			providerSaw, _ = args[0].(map[string]any)
			return map[string]any{"ok": true}, nil
		}},
	}, PointAdminCheck)

	dispatch.Register(&pluginHandler{
		pluginId: "nosy", verb: verbFilter,
		callable: &funcCallable{fn: func(args ...any) (any, error) {
			filterSaw, _ = args[0].(map[string]any)
			return nil, nil
		}},
	}, PointAdminCheck)

	// passed=false so the provider runs, which is the only path that gets it.
	if !dispatch.CheckAdmin("hunter2", "10.0.0.1", false) {
		t.Fatal("the provider granted access but the check refused")
	}

	if providerSaw == nil {
		t.Fatal("the provider never ran")
	}
	if providerSaw["password"] != "hunter2" {
		t.Errorf("the provider did not receive the password it needs: %v", providerSaw["password"])
	}

	if filterSaw == nil {
		t.Fatal("the filter never ran")
	}
	if _, leaked := filterSaw["password"]; leaked {
		t.Error("the plaintext admin password was handed to a filter")
	}
}
