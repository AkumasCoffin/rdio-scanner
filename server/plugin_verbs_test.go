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

// Registering was checked against the point's name only, so a verb the point
// never invokes was accepted and then silently never fired. The reference
// promises the opposite: "registering against a point that does not exist is an
// error, rather than a handler that silently never runs."
func TestPointsRefuseVerbsTheyNeverInvoke(t *testing.T) {
	refused := []struct {
		point string
		verb  pluginVerb
		why   string
	}{
		{PointCallConvert, verbOn, "conversion is replaced wholesale; there is nothing to observe"},
		{PointCallConvert, verbFilter, "conversion is override-only"},
		{PointCallAudio, verbOn, "supplying audio is provide-only"},
		{PointCallAudio, verbFilter, "supplying audio is provide-only"},
		{PointCallStored, verbFilter, "the call is already written; nothing can be changed"},
		{PointCallEmitted, verbFilter, "reporting who received it is after the fact"},
		{PointStartup, verbProvide, "lifecycle points only observe"},
		{PointTick, verbFilter, "lifecycle points only observe"},
		{PointClientConnect, verbOverride, "lifecycle points only observe"},
		{PointCallEmit, verbOverride, "delivery cannot be replaced, only filtered"},
		{PointAccessScope, verbProvide, "scope narrows an access that already exists"},
	}

	for _, c := range refused {
		if pointAcceptsVerb(c.point, c.verb) {
			t.Errorf("%s accepts %s but never invokes it — %s", c.point, c.verb, c.why)
		}
	}

	accepted := []struct {
		point string
		verb  pluginVerb
	}{
		{PointCallConvert, verbOverride},
		{PointCallAudio, verbProvide},
		{PointCallReceive, verbOn},
		{PointCallReceive, verbFilter},
		{PointCallStore, verbFilter},
		{PointCallStored, verbOn},
		{PointCallEmit, verbOn},
		{PointCallEmit, verbFilter},
		{PointAccessCheck, verbProvide},
		{PointAccessCheck, verbFilter},
		{PointAccessCheck, verbOn},
		{PointApikeyCheck, verbProvide},
		{PointAdminCheck, verbProvide},
		{PointStartup, verbOn},
		{PointConfigSave, verbFilter},
	}

	for _, c := range accepted {
		if !pointAcceptsVerb(c.point, c.verb) {
			t.Errorf("%s refuses %s, which core actually invokes on it", c.point, c.verb)
		}
	}
}

// A point a plugin defined for other plugins accepts anything — core has no
// idea what its author meant by it.
func TestPluginDefinedPointsAcceptAnyVerb(t *testing.T) {
	for _, verb := range []pluginVerb{verbOn, verbFilter, verbOverride, verbProvide} {
		if !pointAcceptsVerb("someplugin.thing", verb) {
			t.Errorf("a plugin-defined point refused %s", verb)
		}
	}
}

// The names list is derived from the declarations, so the two cannot drift
// apart the way the schema and its reader did.
func TestEveryPointDeclaresAtLeastOneVerb(t *testing.T) {
	if len(pluginPoints) != len(pluginPointDefs) {
		t.Fatalf("%d names for %d declarations", len(pluginPoints), len(pluginPointDefs))
	}

	seen := map[string]bool{}

	for _, def := range pluginPointDefs {
		if len(def.verbs) == 0 {
			t.Errorf("%s declares no verbs, so nothing may register against it", def.name)
		}
		if seen[def.name] {
			t.Errorf("%s is declared twice", def.name)
		}
		seen[def.name] = true

		if len(pointVerbNames(def.name)) != len(def.verbs) {
			t.Errorf("%s reports a different verb count than it declares", def.name)
		}
	}
}
