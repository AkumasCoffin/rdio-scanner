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

import "testing"

// scopeFixture builds one system whose talkgroups sit in an order that is
// neither by id nor by label, so a sort of either kind is visible.
func scopeFixture() (*Systems, *Groups, *Tags, *Client) {
	talkgroups := NewTalkgroups()
	talkgroups.List = []*Talkgroup{
		{Id: 30, Label: "Alpha", Order: 1, GroupId: 1, TagId: 1},
		{Id: 10, Label: "Charlie", Order: 2, GroupId: 1, TagId: 1},
		{Id: 20, Label: "Bravo", Order: 3, GroupId: 1, TagId: 1},
	}

	systems := NewSystems()
	systems.List = []*System{{
		Id:         1,
		Label:      "Test system",
		Order:      1,
		Talkgroups: talkgroups,
		Units:      NewUnits(),
	}}

	groups := NewGroups()
	groups.List = []*Group{{Id: uint(1), Label: "Group"}}

	tags := NewTags()
	tags.List = []*Tag{{Id: uint(1), Label: "Tag"}}

	return systems, groups, tags, &Client{}
}

// scopedTalkgroups pulls the talkgroup list out of the one system the fixture
// builds, in the order the client is handed it.
func scopedTalkgroups(t *testing.T, scoped SystemsMap) TalkgroupsMap {
	t.Helper()

	if len(scoped) != 1 {
		t.Fatalf("scoped systems has %v entries, want 1", len(scoped))
	}

	talkgroups, ok := scoped[0]["talkgroups"].(TalkgroupsMap)
	if !ok {
		t.Fatalf("talkgroups missing from the scoped system: %v", scoped[0])
	}

	return talkgroups
}

// The scoped view is built per client. Building one must not reach back into
// the configuration every other client — and the admin panel — is reading, or
// a single listener with the sort option on silently rewrites everybody's
// talkgroup order, and the next save in the admin writes that to the database.
func TestGetScopedSystemsLeavesTheConfigAlone(t *testing.T) {
	systems, groups, tags, client := scopeFixture()

	systems.GetScopedSystems(client, groups, tags, true)

	wantIds := []uint{30, 10, 20}
	wantOrders := []uint{1, 2, 3}

	for i, talkgroup := range systems.List[0].Talkgroups.List {
		if talkgroup.Id != wantIds[i] {
			t.Errorf("shared config talkgroup %v is id %v, want %v — the scoped copy reordered it",
				i, talkgroup.Id, wantIds[i])
		}

		if talkgroup.Order != wantOrders[i] {
			t.Errorf("shared config talkgroup %v has order %v, want %v — the scoped copy rewrote it",
				i, talkgroup.Order, wantOrders[i])
		}
	}
}

// The admin describes the option as sorting talkgroups by their id.
func TestGetScopedSystemsSortsByTalkgroupId(t *testing.T) {
	systems, groups, tags, client := scopeFixture()

	talkgroups := scopedTalkgroups(t, systems.GetScopedSystems(client, groups, tags, true))

	wantIds := []uint{10, 20, 30}

	for i, talkgroup := range talkgroups {
		if id, _ := talkgroup["id"].(uint); id != wantIds[i] {
			t.Errorf("scoped talkgroup %v is id %v, want %v", i, id, wantIds[i])
		}

		if order, _ := talkgroup["order"].(uint); order != uint(i+1) {
			t.Errorf("scoped talkgroup %v has order %v, want %v", i, order, i+1)
		}
	}
}

// With the option off, the configured order is what the client is told.
func TestGetScopedSystemsKeepsConfiguredOrder(t *testing.T) {
	systems, groups, tags, client := scopeFixture()

	talkgroups := scopedTalkgroups(t, systems.GetScopedSystems(client, groups, tags, false))

	wantIds := []uint{30, 10, 20}

	for i, talkgroup := range talkgroups {
		if id, _ := talkgroup["id"].(uint); id != wantIds[i] {
			t.Errorf("scoped talkgroup %v is id %v, want %v", i, id, wantIds[i])
		}

		if order, _ := talkgroup["order"].(uint); order != uint(i+1) {
			t.Errorf("scoped talkgroup %v has order %v, want %v", i, order, i+1)
		}
	}
}
