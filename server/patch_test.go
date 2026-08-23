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

// A call is never refiled by configuration alone: it stays on the talkgroup
// that received it, and only records that receipt. The configured homes only
// matter later, as promotion targets when a copy really lands on them.
func TestApplyPatchKeepsTheArrivalTalkgroup(t *testing.T) {
	patch := &Patch{SystemId: 1, TalkgroupId: 100, Talkgroups: []uint{100, 200, 300}}
	call := &Call{System: 1, Talkgroup: 200}

	if !applyPatch(patch, call) {
		t.Fatal("applyPatch refused a member copy")
	}

	if call.Talkgroup != 200 {
		t.Errorf("call filed under talkgroup %v, want the arrival talkgroup 200", call.Talkgroup)
	}

	if got, want := call.Patches, []uint{200}; !reflect.DeepEqual(got, want) {
		t.Errorf("patches are %v, want %v", got, want)
	}
}

// A copy arriving on a home talkgroup records that receipt too — displays
// skip the call's own talkgroup, so it still reads as unpatched, and the
// receipt is what the promotion ladder needs to be truthful.
func TestApplyPatchOnTheHomeRecordsTheReceipt(t *testing.T) {
	patch := &Patch{SystemId: 1, TalkgroupId: 100, Talkgroups: []uint{100, 200, 300}}
	call := &Call{System: 1, Talkgroup: 100}

	if !applyPatch(patch, call) {
		t.Fatal("applyPatch refused a copy arriving on the home")
	}

	if call.Talkgroup != 100 {
		t.Errorf("call filed under %v, want 100", call.Talkgroup)
	}

	if got, want := mergePatches(call.Patches, nil), []uint{100}; !reflect.DeepEqual(got, want) {
		t.Errorf("patches are %v, want %v", got, want)
	}
}

// The promotion ladder is the member list's own order: highest-listed wins,
// an outsider scores nothing.
func TestPatchHomeRank(t *testing.T) {
	patch := &Patch{SystemId: 1, Talkgroups: []uint{100, 200, 300}}
	patch.normalize()

	if a, b, c := patch.homeRank(100), patch.homeRank(200), patch.homeRank(300); !(a > b && b > c && c > 0) {
		t.Errorf("ranks are %v/%v/%v, want strictly descending and positive", a, b, c)
	}

	if got := patch.homeRank(999); got != 0 {
		t.Errorf("an outsider ranks %v, want 0", got)
	}

	if got := patch.homeRank(0); got != 0 {
		t.Errorf("talkgroup zero ranks %v, want 0", got)
	}

	// The legacy fields fold to the front: primary above secondary above the
	// rest, which is exactly the ladder they used to be.
	legacy := &Patch{SystemId: 1, TalkgroupId: 200, PrimaryTalkgroupId: 100, Talkgroups: []uint{300, 200, 100}}
	legacy.normalize()

	if got, want := legacy.Talkgroups, []uint{100, 200, 300}; !reflect.DeepEqual(got, want) {
		t.Errorf("legacy fold produced %v, want %v", got, want)
	}

	if legacy.homeRank(100) <= legacy.homeRank(200) {
		t.Error("the legacy primary does not outrank the legacy secondary after the fold")
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
	patch := &Patch{SystemId: 1, TalkgroupId: 100, Talkgroups: []uint{100, 200, 300}}

	at := time.Date(2026, 8, 22, 19, 30, 0, 0, time.UTC)

	first := &Call{System: 1, Talkgroup: 200, DateTime: at, Audio: []byte{0x01}, AudioName: "first.wav"}

	if !applyPatch(patch, first) {
		t.Fatal("applyPatch refused the first copy")
	}

	if _, _, found := calls.GetPatchDuplicate(first, patch.Talkgroups, db); found {
		t.Fatal("the first copy of a patched call matched a call that is not there")
	}

	id, err := calls.WriteCall(first, db)
	if err != nil {
		t.Fatalf("write first: %v", err)
	}

	// The same transmission, arriving on a different member with the same
	// timestamp.
	second := &Call{System: 1, Talkgroup: 300, DateTime: at, Audio: []byte{0x02}, AudioName: "second.wav"}

	if !applyPatch(patch, second) {
		t.Fatal("applyPatch refused the second copy")
	}

	if found, _, ok := calls.GetPatchDuplicate(second, patch.Talkgroups, db); !ok || found != id {
		t.Errorf("the patched copy resolved to (%v, %v), want the stored call %v", found, ok, id)
	}

	// Moments later is not the same transmission.
	later := &Call{System: 1, Talkgroup: 300, DateTime: at.Add(120 * time.Millisecond), Audio: []byte{0x03}, AudioName: "later.wav"}

	if !applyPatch(patch, later) {
		t.Fatal("applyPatch refused the later call")
	}

	if _, _, found := calls.GetPatchDuplicate(later, patch.Talkgroups, db); found {
		t.Error("a transmission 120ms later was collapsed into the earlier one")
	}
}

// The dropped copies are what tell you which channels carried the
// transmission, so dropping them must not lose that.
func TestPatchedSiblingsJoinTheStoredCall(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	calls := NewCalls()
	patch := &Patch{SystemId: 1, TalkgroupId: 100, Talkgroups: []uint{100, 200, 300}}

	at := time.Date(2026, 8, 22, 19, 30, 0, 0, time.UTC)

	// First copy, on Fireground.
	first := &Call{System: 1, Talkgroup: 200, DateTime: at, Audio: []byte{0x01}, AudioName: "first.wav"}

	applyPatch(patch, first)

	id, err := calls.WriteCall(first, db)
	if err != nil {
		t.Fatalf("write first: %v", err)
	}

	// Second copy, on Tac, same timestamp — dropped, but its talkgroup is kept.
	second := &Call{System: 1, Talkgroup: 300, DateTime: at}
	arrivedOn := second.Talkgroup

	applyPatch(patch, second)

	found, _, ok := calls.GetPatchDuplicate(second, patch.Talkgroups, db)
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

	// The stored call stayed where it arrived: Fireground received the first
	// copy, so Fireground is its home.
	if stored.Talkgroup != 200 {
		t.Errorf("stored call filed under %v, want the first receiving talkgroup 200", stored.Talkgroup)
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

// The full ladder: a transmission heard on a plain member stays there; a copy
// on the secondary moves the call up; a copy on the primary moves it to the
// top — and later copies still find it wherever it currently sits.
func TestPatchPromotionLadder(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	calls := NewCalls()
	patch := &Patch{SystemId: 1, TalkgroupId: 200, PrimaryTalkgroupId: 100, Talkgroups: []uint{100, 200, 300}}
	patch.normalize()

	// Mirrors the ingest path's promotion decision.
	collapse := func(t *testing.T, copyCall *Call, id uint) {
		t.Helper()

		arrivedOn := copyCall.Talkgroup
		applyPatch(patch, copyCall)

		found, storedOn, ok := calls.GetPatchDuplicate(copyCall, patch.Talkgroups, db)
		if !ok || found != id {
			t.Fatalf("copy on %v resolved to (%v, %v), want %v", arrivedOn, found, ok, id)
		}

		if err := calls.AddPatch(id, arrivedOn, db); err != nil {
			t.Fatal(err)
		}

		if patch.homeRank(arrivedOn) > patch.homeRank(storedOn) {
			if err := calls.PromoteCall(id, arrivedOn, db); err != nil {
				t.Fatal(err)
			}
		}
	}

	filedUnder := func(t *testing.T, id uint) uint {
		t.Helper()

		stored, err := calls.GetCall(id, db)
		if err != nil {
			t.Fatal(err)
		}

		return stored.Talkgroup
	}

	at := time.Date(2026, 8, 22, 20, 0, 0, 0, time.UTC)

	// First copy on plain member Tac (300): stays there.
	first := &Call{System: 1, Talkgroup: 300, DateTime: at, Audio: []byte{1}, AudioName: "a.wav"}
	applyPatch(patch, first)

	if first.Talkgroup != 300 {
		t.Fatalf("first copy filed under %v, want its own talkgroup 300", first.Talkgroup)
	}

	id, err := calls.WriteCall(first, db)
	if err != nil {
		t.Fatal(err)
	}

	// A copy on the secondary (200): the call moves up to it.
	collapse(t, &Call{System: 1, Talkgroup: 200, DateTime: at}, id)

	if got := filedUnder(t, id); got != 200 {
		t.Fatalf("after the secondary receipt the call sits on %v, want 200", got)
	}

	// A copy on the primary (100): the call moves to the top.
	collapse(t, &Call{System: 1, Talkgroup: 100, DateTime: at}, id)

	if got := filedUnder(t, id); got != 100 {
		t.Fatalf("after the primary receipt the call sits on %v, want 100", got)
	}

	// Another member copy afterwards: found in the new home, no demotion.
	collapse(t, &Call{System: 1, Talkgroup: 300, DateTime: at}, id)

	if got := filedUnder(t, id); got != 100 {
		t.Errorf("a later member copy demoted the call to %v", got)
	}

	stored, err := calls.GetCall(id, db)
	if err != nil {
		t.Fatal(err)
	}

	// Every receiving talkgroup recorded, once each.
	got := mergePatches(stored.Patches, nil)
	if want := []uint{300, 200, 100}; !reflect.DeepEqual(got, want) {
		t.Errorf("received list is %v, want %v", got, want)
	}
}

// A transmission that never reached the primary never files there, and one
// that reached neither configured home stays on the first member that heard
// it.
func TestPatchHomesNeverClaimedWithoutReceipt(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	calls := NewCalls()
	patch := &Patch{SystemId: 1, TalkgroupId: 200, PrimaryTalkgroupId: 100, Talkgroups: []uint{100, 200, 300}}
	patch.normalize()

	at := time.Date(2026, 8, 22, 20, 5, 0, 0, time.UTC)

	// Heard only on the plain member: stays there, no home claimed.
	first := &Call{System: 1, Talkgroup: 300, DateTime: at, Audio: []byte{1}, AudioName: "a.wav"}
	applyPatch(patch, first)

	id, err := calls.WriteCall(first, db)
	if err != nil {
		t.Fatal(err)
	}

	stored, err := calls.GetCall(id, db)
	if err != nil {
		t.Fatal(err)
	}

	if stored.Talkgroup != 300 {
		t.Errorf("call filed under %v, want 300 — neither home received it", stored.Talkgroup)
	}
}

// The live feed sends a patched call the moment its first copy lands, which is
// before the copies on the other talkgroups exist. Whatever is holding that
// call between then and the siblings arriving is holding a value that says the
// transmission was heard on one talkgroup when the record says several — the
// reason a patch showed in search but never on the LCD.
func TestRefreshPatchStatePicksUpWhatTheSiblingsAdded(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	calls := NewCalls()
	patch := &Patch{SystemId: 1, TalkgroupId: 100, Talkgroups: []uint{100, 200, 300}}
	patch.normalize()

	at := time.Date(2026, 8, 23, 12, 32, 3, 0, time.UTC)

	first := &Call{System: 1, Talkgroup: 300, DateTime: at, Audio: []byte{1}, AudioName: "a.wav"}
	applyPatch(patch, first)

	id, err := calls.WriteCall(first, db)
	if err != nil {
		t.Fatal(err)
	}

	first.Id = id

	// This is the value the live feed was handed: its own talkgroup and
	// nothing else, which every display reads as unpatched.
	if got := mergePatches(first.Patches, nil); len(got) != 1 || got[0] != 300 {
		t.Fatalf("the emitted call named %v, want just the talkgroup it arrived on", got)
	}

	// The siblings land: one joins, one joins and outranks the home.
	if err := calls.AddPatch(id, 200, db); err != nil {
		t.Fatal(err)
	}

	if err := calls.AddPatch(id, 100, db); err != nil {
		t.Fatal(err)
	}

	if err := calls.PromoteCall(id, 100, db); err != nil {
		t.Fatal(err)
	}

	if err := calls.RefreshPatchState(first, db); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if got, want := mergePatches(first.Patches, nil), []uint{300, 200, 100}; !reflect.DeepEqual(got, want) {
		t.Errorf("refreshed call names %v, want %v — every talkgroup that carried it", got, want)
	}

	if first.Talkgroup != 100 {
		t.Errorf("refreshed call filed under %v, want 100 — it was promoted", first.Talkgroup)
	}
}

// The correction has to reach the listener who was sent the call, and that is
// not the same set as the listeners who would be sent it now: a patched call
// is promoted onto the highest-ranked talkgroup that received a copy, which
// may be one the recipient does not hold.
func TestPatchUpdateReachesAListenerWhoDoesNotHoldTheNewTalkgroup(t *testing.T) {
	clients := NewClients()

	listener := &Client{Access: &Access{}, Livefeed: NewLivefeed(), Send: make(chan *Message, 8)}
	listener.Livefeed.Matrix[1] = map[uint]bool{300: true}
	clients.Add(listener)

	// Promoted to 100, which this listener does not hold.
	clients.EmitPatchUpdate(&Call{Id: uint(7), System: 1, Talkgroup: 100, Patches: []uint{300, 100}}, false)

	select {
	case message := <-listener.Send:
		if message.Command != MessageCommandPatch {
			t.Fatalf("listener got command %v, want %v", message.Command, MessageCommandPatch)
		}

		payload, ok := message.Payload.(map[string]any)
		if !ok {
			t.Fatalf("payload is %T, want a map", message.Payload)
		}

		if payload["id"] != uint(7) {
			t.Errorf("payload names call %v, want 7", payload["id"])
		}

		if got, want := payload["patches"], []uint{300, 100}; !reflect.DeepEqual(got, want) {
			t.Errorf("payload carries %v, want %v", got, want)
		}

		if payload["talkgroup"] != uint(100) {
			t.Errorf("payload files the call under %v, want 100", payload["talkgroup"])
		}

		// A correction is not a call: sending audio would make listeners play
		// the transmission a second time.
		if _, carries := payload["audio"]; carries {
			t.Error("the correction carries audio")
		}

	default:
		t.Fatal("the listener holding the call got no correction")
	}
}
