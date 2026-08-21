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

	if got, want := call.Patches, []uint{100, 200, 300}; !reflect.DeepEqual(got, want) {
		t.Errorf("patches are %v, want %v", got, want)
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
// second arrival a duplicate in the plainest sense, which is the whole reason
// the patch is applied before the duplicate check rather than after it.
func TestPatchedCopiesCollapseUnderTheDuplicateCheck(t *testing.T) {
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

	if calls.CheckDuplicate(first, 500, db) {
		t.Fatal("the first copy of a patched call was called a duplicate")
	}

	if _, err := calls.WriteCall(first, db); err != nil {
		t.Fatalf("write first: %v", err)
	}

	// The same transmission, arriving on a different member 120 ms later.
	second := &Call{System: 1, Talkgroup: 300, DateTime: at.Add(120 * time.Millisecond), Audio: []byte{0x02}, AudioName: "second.wav"}

	if _, ok := applyPatch(patch, second, system); !ok {
		t.Fatal("applyPatch refused the second copy")
	}

	if !calls.CheckDuplicate(second, 500, db) {
		t.Error("a patched copy on another talkgroup was not seen as a duplicate — both would be stored")
	}

	// A genuinely later transmission on the patch is still its own call.
	later := &Call{System: 1, Talkgroup: 300, DateTime: at.Add(30 * time.Second), Audio: []byte{0x03}, AudioName: "later.wav"}

	if _, ok := applyPatch(patch, later, system); !ok {
		t.Fatal("applyPatch refused the later call")
	}

	if calls.CheckDuplicate(later, 500, db) {
		t.Error("a transmission 30s later was collapsed into the earlier one")
	}
}
