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

func resolveDispatch(t *testing.T) *PluginDispatch {
	t.Helper()

	controller := &Controller{Logs: &Logs{}}
	return NewPluginDispatch(controller)
}

func (dispatch *PluginDispatch) addTestHandler(pluginId string, verb pluginVerb, point string, fn func(args ...any) (any, error)) {
	dispatch.Register(&pluginHandler{
		pluginId: pluginId,
		verb:     verb,
		callable: &funcCallable{fn: fn},
	}, point)
}

// A plugin that is itself an ingest source knows what it is feeding in. Before
// this it could create the call and then only watch rdio label it "System 3".
func TestAPluginCanNameTheSystemItIngests(t *testing.T) {
	dispatch := resolveDispatch(t)

	dispatch.addTestHandler("source", verbProvide, PointCallSystem, func(args ...any) (any, error) {
		value, _ := args[0].(map[string]any)
		if known, _ := value["known"].(bool); known {
			t.Error("provide ran for a system rdio already has")
		}
		return map[string]any{"label": "Metro Fire"}, nil
	})

	call := &Call{System: 3}

	label, create := dispatch.ResolveSystem(call, false, "")

	if label != "Metro Fire" {
		t.Fatalf("the system was named %q", label)
	}
	// Naming a system rdio does not have implies wanting it, or the plugin's
	// calls would keep arriving for a system that never gets created.
	if !create {
		t.Error("naming an unknown system did not ask for it to be created")
	}
}

// Provide must not fire for a system an operator already configured — that is
// the same rule the access points follow, and for the same reason.
func TestProvideDoesNotRenameAConfiguredSystem(t *testing.T) {
	dispatch := resolveDispatch(t)

	provided := false

	dispatch.addTestHandler("source", verbProvide, PointCallSystem, func(args ...any) (any, error) {
		provided = true
		return map[string]any{"label": "Hijacked"}, nil
	})

	label, create := dispatch.ResolveSystem(&Call{System: 3}, true, "Configured")

	if provided {
		t.Error("provide ran for a system that already exists")
	}
	if label != "Configured" {
		t.Errorf("the configured label became %q", label)
	}
	if create {
		t.Error("an existing system was marked for creation")
	}
}

// A filter may still adjust the label for this call, on a system rdio has.
func TestAFilterMayAdjustAKnownSystem(t *testing.T) {
	dispatch := resolveDispatch(t)

	dispatch.addTestHandler("tweak", verbFilter, PointCallSystem, func(args ...any) (any, error) {
		return map[string]any{"label": "Metro Fire (South)"}, nil
	})

	label, _ := dispatch.ResolveSystem(&Call{System: 3}, true, "Metro Fire")

	if label != "Metro Fire (South)" {
		t.Fatalf("the filter's label was ignored: %q", label)
	}
}

// The talkgroup carries group and tag, because without them rdio files a new
// talkgroup under "Unknown" and "Untagged" — the naming an ingest plugin exists
// to supply.
func TestAPluginCanNameTheTalkgroupItIngests(t *testing.T) {
	dispatch := resolveDispatch(t)

	dispatch.addTestHandler("source", verbProvide, PointCallTalkgroup, func(args ...any) (any, error) {
		return map[string]any{
			"label": "FIRE-1",
			"name":  "Fire Dispatch",
			"group": "Fire",
			"tag":   "Dispatch",
		}, nil
	})

	naming := dispatch.ResolveTalkgroup(&Call{System: 3, Talkgroup: 101}, false, pluginTalkgroupNaming{})

	if naming.Label != "FIRE-1" || naming.Name != "Fire Dispatch" {
		t.Fatalf("label=%q name=%q", naming.Label, naming.Name)
	}
	if naming.Group != "Fire" || naming.Tag != "Dispatch" {
		t.Fatalf("group=%q tag=%q — these decide where it appears in the interface", naming.Group, naming.Tag)
	}
	if !naming.Create {
		t.Error("naming an unknown talkgroup did not ask for it to be created")
	}
}

// Empty means "no opinion", never "clear it" — the rule every other filter
// follows.
func TestEmptyFieldsLeaveTheExistingNamingAlone(t *testing.T) {
	dispatch := resolveDispatch(t)

	dispatch.addTestHandler("quiet", verbFilter, PointCallTalkgroup, func(args ...any) (any, error) {
		return map[string]any{"label": "", "group": ""}, nil
	})

	naming := dispatch.ResolveTalkgroup(&Call{System: 3, Talkgroup: 101}, true, pluginTalkgroupNaming{
		Label: "FIRE-1", Group: "Fire", Tag: "Dispatch",
	})

	if naming.Label != "FIRE-1" || naming.Group != "Fire" {
		t.Fatalf("an empty field cleared existing naming: label=%q group=%q", naming.Label, naming.Group)
	}
}

// With nothing registered the points cost nothing and change nothing, which is
// what every install without an ingest plugin is.
func TestResolutionIsAnUntouchedPassThroughWithNoPlugins(t *testing.T) {
	dispatch := resolveDispatch(t)

	label, create := dispatch.ResolveSystem(&Call{System: 3}, false, "System 3")
	if label != "System 3" || create {
		t.Fatalf("label=%q create=%v", label, create)
	}

	naming := dispatch.ResolveTalkgroup(&Call{System: 3, Talkgroup: 101}, false, pluginTalkgroupNaming{Label: "TG"})
	if naming.Label != "TG" || naming.Create {
		t.Fatalf("naming=%+v", naming)
	}
}
