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
	"errors"
	"testing"
	"time"
)

// funcCallable is a host-side handler, so these tests measure the chain rather
// than goja.
type funcCallable struct {
	fn func(args ...any) (any, error)
}

func (c *funcCallable) call(args ...any) (any, error) { return c.fn(args...) }

var errTestHandlerFailed = errors.New("handler failed")

func newTestDispatch() *PluginDispatch {
	dispatch := &PluginDispatch{points: map[string]bool{PointCallStore: true}}
	dispatch.registry.Store(&dispatchRegistry{handlers: map[string][]*pluginHandler{}})
	return dispatch
}

func (dispatch *PluginDispatch) addTestFilter(pluginId string, point string, fn func(args ...any) (any, error)) {
	dispatch.Register(&pluginHandler{
		pluginId: pluginId,
		verb:     verbFilter,
		callable: &funcCallable{fn: fn},
	}, point)
}

// Two filters on one point is the case nothing tested, and the case the chain
// got wrong in both directions.
//
// A filter returns only the fields it wants changed — naming one field changes
// one field. Assigning that partial result straight over the chain value meant
// the second handler received an object holding nothing but the first
// handler's output, and its own return then discarded the first handler's work.
// With a single filter installed it looked perfectly correct.
func TestTwoFiltersOnOnePointBothTakeEffect(t *testing.T) {
	dispatch := newTestDispatch()

	var secondSaw map[string]any

	dispatch.addTestFilter("first", PointCallStore, func(args ...any) (any, error) {
		return map[string]any{"talkgroup": 999}, nil
	})

	dispatch.addTestFilter("second", PointCallStore, func(args ...any) (any, error) {
		secondSaw, _ = args[0].(map[string]any)
		return map[string]any{"meta": map[string]string{"tagged": "yes"}}, nil
	})

	value := map[string]any{
		"system":    uint(3),
		"talkgroup": uint(101),
		"audioName": "call.m4a",
	}

	filtered, keep := dispatch.Filter(PointCallStore, value, time.Second)
	if !keep {
		t.Fatal("the chain vetoed a call neither filter dropped")
	}

	// The second handler must see a whole call, not the first one's fragment.
	if secondSaw == nil {
		t.Fatal("the second filter never ran")
	}
	if secondSaw["system"] != uint(3) {
		t.Errorf("the second filter saw system %v; the rest of the call was lost on the way", secondSaw["system"])
	}
	if secondSaw["audioName"] != "call.m4a" {
		t.Errorf("the second filter saw audioName %v", secondSaw["audioName"])
	}
	if secondSaw["talkgroup"] != 999 {
		t.Errorf("the second filter saw talkgroup %v, so it could not build on the first filter's change", secondSaw["talkgroup"])
	}

	result, ok := filtered.(map[string]any)
	if !ok {
		t.Fatalf("the chain produced %T", filtered)
	}

	// Both changes survive to the end.
	if result["talkgroup"] != 999 {
		t.Errorf("the first filter's change was discarded: talkgroup is %v", result["talkgroup"])
	}
	if meta, _ := result["meta"].(map[string]string); meta["tagged"] != "yes" {
		t.Errorf("the second filter's change was discarded: meta is %v", result["meta"])
	}
	// Only what handlers named comes back — the caller writes these onto the
	// live object, and echoing untouched fields costs precision on the ones
	// that round-trip through a string.
	if _, echoed := result["audioName"]; echoed {
		t.Error("a field no handler touched was returned as a change")
	}
}

// A filter returning nothing is stating no opinion, and must not blank the call
// for the handler after it.
func TestAFilterReturningNothingLeavesTheValueIntact(t *testing.T) {
	dispatch := newTestDispatch()

	var lastSaw map[string]any

	dispatch.addTestFilter("quiet", PointCallStore, func(args ...any) (any, error) {
		return nil, nil
	})
	dispatch.addTestFilter("watcher", PointCallStore, func(args ...any) (any, error) {
		lastSaw, _ = args[0].(map[string]any)
		return nil, nil
	})

	value := map[string]any{"system": uint(3), "talkgroup": uint(101)}

	filtered, keep := dispatch.Filter(PointCallStore, value, time.Second)
	if !keep {
		t.Fatal("a chain of no-opinion filters vetoed the call")
	}
	if lastSaw["system"] != uint(3) || lastSaw["talkgroup"] != uint(101) {
		t.Fatalf("a no-opinion filter changed what the next one saw: %v", lastSaw)
	}
	if result, _ := filtered.(map[string]any); result["talkgroup"] != uint(101) {
		t.Fatalf("the value came out changed: %v", filtered)
	}
}

// A handler that fails is treated as having done nothing — it must not take the
// previous handler's changes down with it.
func TestAFailingFilterDoesNotDiscardEarlierChanges(t *testing.T) {
	dispatch := newTestDispatch()
	dispatch.controller = &Controller{Logs: &Logs{}}

	dispatch.addTestFilter("first", PointCallStore, func(args ...any) (any, error) {
		return map[string]any{"talkgroup": 999}, nil
	})
	dispatch.addTestFilter("broken", PointCallStore, func(args ...any) (any, error) {
		return nil, errTestHandlerFailed
	})

	filtered, keep := dispatch.Filter(PointCallStore, map[string]any{"system": uint(3)}, time.Second)
	if !keep {
		t.Fatal("a failing filter vetoed the call, which a failure must never do")
	}

	result, _ := filtered.(map[string]any)
	if result["talkgroup"] != 999 {
		t.Errorf("a later failure discarded an earlier filter's change: %v", result)
	}
	if _, echoed := result["system"]; echoed {
		t.Error("a field no handler touched was returned as a change")
	}
}

// A veto stops the chain and reports the value as it stood, not the veto object.
func TestAVetoStopsTheChain(t *testing.T) {
	dispatch := newTestDispatch()
	dispatch.controller = &Controller{Logs: &Logs{}}

	ran := false

	dispatch.addTestFilter("dropper", PointCallStore, func(args ...any) (any, error) {
		return map[string]any{"drop": true}, nil
	})
	dispatch.addTestFilter("after", PointCallStore, func(args ...any) (any, error) {
		ran = true
		return nil, nil
	})

	if _, keep := dispatch.Filter(PointCallStore, map[string]any{"system": uint(3)}, time.Second); keep {
		t.Fatal("a filter returning drop did not veto")
	}
	if ran {
		t.Error("a handler after the veto still ran")
	}
}

// The overlay must not write into the map it was handed, which may still be
// referenced by the handler that produced it.
func TestOverlayDoesNotMutateItsInputs(t *testing.T) {
	base := map[string]any{"system": uint(3), "talkgroup": uint(101)}
	fields := map[string]any{"talkgroup": uint(999)}

	merged := overlayPluginFields(base, fields)

	if base["talkgroup"] != uint(101) {
		t.Error("the overlay wrote into the value it was given")
	}
	if len(fields) != 1 {
		t.Error("the overlay wrote into the handler's returned map")
	}
	if merged["talkgroup"] != uint(999) || merged["system"] != uint(3) {
		t.Errorf("the overlay produced %v", merged)
	}
}
