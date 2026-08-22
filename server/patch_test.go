// Copyright (C) 2019-2024 Chrystian Huot <chrystian.huot@saubeo.solutions>
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
	"reflect"
	"testing"
	"time"
)

func patchSystem() *System {
	talkgroups := NewTalkgroups()
	talkgroups.List = []*Talkgroup{
		{Id: 100, Label: "Dispatch", Name: "City Dispatch", Order: 1},
		{Id: 200, Label: "Fireground", Name: "City Fireground", Order: 2},
		{Id: 300, Label: "Tac", Name: "City Tac", Order: 3},
	}

	return &System{Id: 1, Label: "City", Talkgroups: talkgroups, Units: NewUnits()}
}

// The primary belongs in the member list whether or not it was listed, and a
// member named twice is still one member.
func TestPatchNormalize(t *testing.T) {
	for _, tc := range []struct {
		name        string
		patch       Patch
		wantPrimary uint
		wantMembers []uint
		wantUsable  bool
	}{
		{
			name:        "primary missing from members is added at the front",
			patch:       Patch{SystemId: 1, TalkgroupId: 100, Talkgroups: []uint{200, 300}},
			wantPrimary: 100,
			wantMembers: []uint{100, 200, 300},
			wantUsable:  true,
		},
		{
			name:        "repeats and zeroes are dropped",
			patch:       Patch{SystemId: 1, TalkgroupId: 200, Talkgroups: []uint{200, 100, 200, 0, 100}},
			wantPrimary: 200,
			wantMembers: []uint{200, 100},
			wantUsable:  true,
		},
		{
			name:        "an unset primary takes the first member",
			patch:       Patch{SystemId: 1, Talkgroups: []uint{300, 100}},
			wantPrimary: 300,
			wantMembers: []uint{300, 100},
			wantUsable:  true,
		},
		{
			name:        "one talkgroup is not a patch",
			patch:       Patch{SystemId: 1, TalkgroupId: 100, Talkgroups: []uint{100}},
			wantPrimary: 100,
			wantMembers: []uint{100},
			wantUsable:  false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			patch := tc.patch
			patch.normalize()

			if patch.TalkgroupId != tc.wantPrimary {
				t.Errorf("primary is %v, want %v", patch.TalkgroupId, tc.wantPrimary)
			}

			if !reflect.DeepEqual(patch.Talkgroups, tc.wantMembers) {
				t.Errorf("members are %v, want %v", patch.Talkgroups, tc.wantMembers)
			}

			if patch.usable() != tc.wantUsable {
				t.Errorf("usable is %v, want %v", patch.usable(), tc.wantUsable)
			}
		})
	}
}

func TestPatchesGetPatch(t *testing.T) {
	patches := NewPatches()
	patches.FromMap([]any{
		map[string]any{
			"_id": 1, "label": "City fire", "systemId": 1,
			"talkgroupId": 100, "talkgroups": []any{100, 200},
		},
		map[string]any{
			"_id": 2, "label": "Switched off", "systemId": 1, "disabled": true,
			"talkgroupId": 300, "talkgroups": []any{300, 400},
		},
	})

	for _, tc := range []struct {
		name      string
		system    uint
		talkgroup uint
		wantFound bool
		wantLabel string
	}{
		{name: "the primary", system: 1, talkgroup: 100, wantFound: true, wantLabel: "City fire"},
		{name: "another member", system: 1, talkgroup: 200, wantFound: true, wantLabel: "City fire"},
		{name: "a talkgroup in no patch", system: 1, talkgroup: 999},
		{name: "a disabled patch", system: 1, talkgroup: 300},
		{name: "the same talkgroup on another system", system: 2, talkgroup: 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			patch, ok := patches.GetPatch(tc.system, tc.talkgroup)

			if ok != tc.wantFound {
				t.Fatalf("found is %v, want %v", ok, tc.wantFound)
			}

			if ok && patch.Label != tc.wantLabel {
				t.Errorf("matched patch %q, want %q", patch.Label, tc.wantLabel)
			}
		})
	}
}

func TestApplyPatchRefilesOntoThePrimary(t *testing.T) {
	patch := &Patch{SystemId: 1, TalkgroupId: 100, Talkgroups: []uint{100, 200, 300}}
	call := &Call{System: 1, Talkgroup: 200}

	primary, ok := applyPatch(patch, call, patchSystem())
	if !ok {
		t.Fatal("applyPatch refused a patch whose primary is in the system")
	}

	if primary.Label != "Dispatch" {
		t.Errorf("primary is %q, want %q", primary.Label, "Dispatch")
	}

	if call.Talkgroup != 100 {
		t.Errorf("call filed under talkgroup %v, want 100", call.Talkgroup)
	}

	// Only the talkgroup this copy came in on: the patch's other members are
	// not claimed until a copy really arrives on them.
	if got, want := call.Patches, []uint{200}; !reflect.DeepEqual(got, want) {
		t.Errorf("patches are %v, want %v", got, want)
	}
}

// A copy that arrived on the primary was not received anywhere else, so it has
// nothing to report — otherwise every call on a patched talkgroup would claim
// to be patched with itself.
func TestApplyPatchOnThePrimaryRecordsNothing(t *testing.T) {
	patch := &Patch{SystemId: 1, TalkgroupId: 100, Talkgroups: []uint{100, 200, 300}}
	call := &Call{System: 1, Talkgroup: 100}

	if _, ok := applyPatch(patch, call, patchSystem()); !ok {
		t.Fatal("applyPatch refused a copy arriving on the primary")
	}

	if got := mergePatches(call.Patches, nil); len(got) != 0 {
		t.Errorf("patches are %v, want none", got)
	}
}

// A patch pointing at a talkgroup the system does not have would file calls
// where no client can select them.
func TestApplyPatchRefusesAMissingPrimary(t *testing.T) {
	patch := &Patch{SystemId: 1, TalkgroupId: 999, Talkgroups: []uint{999, 200}}
	call := &Call{System: 1, Talkgroup: 200}

	if _, ok := applyPatch(patch, call, patchSystem()); ok {
		t.Fatal("applyPatch accepted a primary that is not in the system")
	}

	if call.Talkgroup != 200 {
		t.Errorf("call was refiled to %v despite the refusal, want 200", call.Talkgroup)
	}
}

// A recorder-reported patch and a configured one describe the same call, so
// neither should erase the other.
func TestMergePatchesKeepsBothSets(t *testing.T) {
	for _, tc := range []struct {
		name     string
		existing any
		members  []uint
		want     []uint
	}{
		{name: "nothing reported", existing: nil, members: []uint{100, 200}, want: []uint{100, 200}},
		{name: "uint slice", existing: []uint{500}, members: []uint{100, 200}, want: []uint{500, 100, 200}},
		{name: "decoded json", existing: []any{float64(500)}, members: []uint{100}, want: []uint{500, 100}},
		{name: "overlap counted once", existing: []uint{100}, members: []uint{100, 200}, want: []uint{100, 200}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mergePatches(tc.existing, tc.members); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("merged to %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPatchesReadWriteRoundTrip(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	patches := NewPatches()
	patches.List = []*Patch{{
		Label:       "City fire",
		Order:       1,
		SystemId:    1,
		TalkgroupId: 100,
		Talkgroups:  []uint{100, 200, 300},
	}}

	if err := patches.Write(db); err != nil {
		t.Fatalf("write: %v", err)
	}

	read := NewPatches()
	if err := read.Read(db); err != nil {
		t.Fatalf("read: %v", err)
	}

	if len(read.List) != 1 {
		t.Fatalf("read %v patches, want 1", len(read.List))
	}

	got := read.List[0]

	if got.Label != "City fire" || got.SystemId != 1 || got.TalkgroupId != 100 {
		t.Errorf("read back %+v, want label=City fire systemId=1 talkgroupId=100", got)
	}

	if want := []uint{100, 200, 300}; !reflect.DeepEqual(got.Talkgroups, want) {
		t.Errorf("members are %v, want %v", got.Talkgroups, want)
	}

	// Dropping the patch from the list is how the admin deletes it.
	patches.List = []*Patch{}

	if err := patches.Write(db); err != nil {
		t.Fatalf("write after delete: %v", err)
	}

	if err := read.Read(db); err != nil {
		t.Fatalf("read after delete: %v", err)
	}

	if len(read.List) != 0 {
		t.Errorf("read %v patches after deleting them, want 0", len(read.List))
	}
}

// The collapse itself: refiling every copy onto the primary is what makes the
// later arrivals findable, and the match is the exact timestamp — the copies
// of a patched transmission are one recording fanned out by the recorder, so
// they share it, and a transmission even moments later is its own call.
func TestPatchedCopiesCollapseOnTheExactTimestamp(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	calls := NewCalls()
	system := patchSystem()
	patch := &Patch{SystemId: 1, TalkgroupId: 100, Talkgroups: []uint{100, 200, 300}}

	at := time.Date(2026, 8, 22, 19, 30, 0, 0, time.UTC)

	first := &Call{System: 1, Talkgroup: 200, DateTime: at, Audio: []byte{0x01}, AudioName: "first.wav"}

	if _, ok := applyPatch(patch, first, system); !ok {
		t.Fatal("applyPatch refused the first copy")
	}

	if _, found := calls.GetPatchDuplicateId(first, db); found {
		t.Fatal("the first copy of a patched call matched a call that is not there")
	}

	id, err := calls.WriteCall(first, db)
	if err != nil {
		t.Fatalf("write first: %v", err)
	}

	// The same transmission, arriving on a different member with the same
	// timestamp.
	second := &Call{System: 1, Talkgroup: 300, DateTime: at, Audio: []byte{0x02}, AudioName: "second.wav"}

	if _, ok := applyPatch(patch, second, system); !ok {
		t.Fatal("applyPatch refused the second copy")
	}

	if found, ok := calls.GetPatchDuplicateId(second, db); !ok || found != id {
		t.Errorf("the patched copy resolved to (%v, %v), want the stored call %v", found, ok, id)
	}

	// Moments later is not the same transmission.
	later := &Call{System: 1, Talkgroup: 300, DateTime: at.Add(120 * time.Millisecond), Audio: []byte{0x03}, AudioName: "later.wav"}

	if _, ok := applyPatch(patch, later, system); !ok {
		t.Fatal("applyPatch refused the later call")
	}

	if _, found := calls.GetPatchDuplicateId(later, db); found {
		t.Error("a transmission 120ms later was collapsed into the earlier one")
	}
}

// The dropped copies are what tell you which channels carried the
// transmission, so dropping them must not lose that.
func TestPatchedSiblingsJoinTheStoredCall(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	calls := NewCalls()
	system := patchSystem()
	patch := &Patch{SystemId: 1, TalkgroupId: 100, Talkgroups: []uint{100, 200, 300}}

	at := time.Date(2026, 8, 22, 19, 30, 0, 0, time.UTC)

	// First copy, on Fireground.
	first := &Call{System: 1, Talkgroup: 200, DateTime: at, Audio: []byte{0x01}, AudioName: "first.wav"}

	applyPatch(patch, first, system)

	id, err := calls.WriteCall(first, db)
	if err != nil {
		t.Fatalf("write first: %v", err)
	}

	// Second copy, on Tac, same timestamp — dropped, but its talkgroup is kept.
	second := &Call{System: 1, Talkgroup: 300, DateTime: at}
	arrivedOn := second.Talkgroup

	applyPatch(patch, second, system)

	found, ok := calls.GetPatchDuplicateId(second, db)
	if !ok {
		t.Fatal("the second copy was not recognised as a duplicate")
	}

	if found != id {
		t.Fatalf("duplicate resolved to call %v, want %v", found, id)
	}

	if err := calls.AddPatch(found, arrivedOn, db); err != nil {
		t.Fatalf("add patch: %v", err)
	}

	stored, err := calls.GetCall(id, db)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}

	got := mergePatches(stored.Patches, nil)

	if want := []uint{200, 300}; !reflect.DeepEqual(got, want) {
		t.Errorf("stored call reports %v, want %v — both channels carried it", got, want)
	}

	// A third copy on a talkgroup already recorded must not double it up.
	if err := calls.AddPatch(id, 300, db); err != nil {
		t.Fatalf("add patch again: %v", err)
	}

	stored, _ = calls.GetCall(id, db)

	if got := mergePatches(stored.Patches, nil); len(got) != 2 {
		t.Errorf("stored call reports %v after a repeat, want two talkgroups", got)
	}
}

// Recorders sometimes announce a patch that only covers the talkgroup the
// call is already on. That is not a patch, and it must not read as one.
func TestNormalizeReportedPatches(t *testing.T) {
	for _, tc := range []struct {
		name     string
		reported any
		own      uint
		want     []uint
	}{
		{name: "own talkgroup alone becomes no patch", reported: []uint{10128}, own: 10128, want: []uint{}},
		{name: "own talkgroup drops out of a real patch", reported: []uint{30003, 30013, 10075}, own: 30003, want: []uint{30013, 10075}},
		{name: "a list without the own talkgroup is untouched", reported: []uint{30013, 10075}, own: 30003, want: []uint{30013, 10075}},
		{name: "nothing reported stays nothing", reported: nil, own: 30003, want: []uint{}},
		{name: "decoded json shape", reported: []any{float64(10128), float64(10123)}, own: 10128, want: []uint{10123}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeReportedPatches(tc.reported, tc.own); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("normalized to %v, want %v", got, tc.want)
			}
		})
	}
}
