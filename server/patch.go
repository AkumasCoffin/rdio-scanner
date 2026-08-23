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
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// Patch is a set of talkgroups on one system that carry the same conversation.
//
// A recorder that knows about a patch reports it per call, in Call.Patches.
// This is the same thing declared up front: when the transmission arrives once
// per member talkgroup, the copies are collapsed into a single call filed under
// the primary, carrying every member as its patches. Everything downstream of
// that — the LCD flag, the livefeed fan-out to listeners holding any member,
// the downstream payload, patched-talkgroup search — reads Call.Patches and so
// cannot tell the two apart, which is the point.
type Patch struct {
	Id       any    `json:"_id"`
	Disabled bool   `json:"disabled"`
	Label    string `json:"label"`
	Order    uint   `json:"order"`
	SystemId uint   `json:"systemId"`

	// Legacy homes from before the ranking was the member order itself.
	// normalize folds them to the front of Talkgroups — primary first, then
	// secondary — and then derives TalkgroupId back from the list so the
	// column stays coherent for anything still reading it.
	TalkgroupId        uint `json:"talkgroupId"`
	PrimaryTalkgroupId uint `json:"primaryTalkgroupId"`

	// Every talkgroup in the patch, in display order — which is the ranking.
	// The surviving call files under the highest-listed talkgroup that
	// actually received a copy, and climbs when a copy arrives on a higher
	// one. No talkgroup is ever claimed without a real receipt.
	Talkgroups []uint `json:"talkgroups"`

	// Delay is how far apart, in seconds, the copies of one transmission may
	// be stamped and still be recognised as copies.
	//
	// Zero — the default, and what patches did before this existed — means
	// the timestamps must match exactly. That holds when one recorder fans a
	// patched transmission out to every member talkgroup, because the copies
	// are the same recording. It does not hold when the members are covered
	// by separate recorders, which start their own clocks: a second between
	// copies is enough for an exact match to see two unrelated calls, store
	// both, and flag neither.
	//
	// Widening this is a trade. Every window is also a window in which two
	// genuinely different transmissions on two member talkgroups get read as
	// one, and the later one is discarded. Keep it just above the spread the
	// recorders actually produce.
	Delay uint `json:"delay"`
}

func (patch *Patch) FromMap(m map[string]any) *Patch {
	if v, ok := jsonUint(m["_id"]); ok {
		patch.Id = v
	}

	switch v := m["disabled"].(type) {
	case bool:
		patch.Disabled = v
	}

	switch v := m["label"].(type) {
	case string:
		patch.Label = v
	}

	if v, ok := jsonUint(m["order"]); ok {
		patch.Order = v
	}

	if v, ok := jsonUint(m["systemId"]); ok {
		patch.SystemId = v
	}

	if v, ok := jsonUint(m["talkgroupId"]); ok {
		patch.TalkgroupId = v
	}

	if v, ok := jsonUint(m["primaryTalkgroupId"]); ok {
		patch.PrimaryTalkgroupId = v
	}

	if v, ok := jsonUint(m["delay"]); ok {
		patch.Delay = v
	}

	patch.Talkgroups = []uint{}

	switch v := m["talkgroups"].(type) {
	case []any:
		for _, f := range v {
			if id, ok := jsonUint(f); ok {
				patch.Talkgroups = append(patch.Talkgroups, id)
			}
		}
	}

	patch.normalize()

	return patch
}

// normalize settles the member list into its canonical ranked form: repeats
// and zeroes dropped, the legacy primary/secondary homes folded to the front
// in that order, and TalkgroupId re-derived from the top of the list so the
// stored column stays coherent. Idempotent, so a patch configured under the
// ranked-list model passes through untouched.
func (patch *Patch) normalize() {
	seen := map[uint]bool{}
	ordered := []uint{}

	add := func(id uint) {
		if id == 0 || seen[id] {
			return
		}

		seen[id] = true
		ordered = append(ordered, id)
	}

	add(patch.PrimaryTalkgroupId)
	add(patch.TalkgroupId)

	for _, id := range patch.Talkgroups {
		add(id)
	}

	patch.Talkgroups = ordered
	patch.PrimaryTalkgroupId = 0
	patch.TalkgroupId = 0

	if len(ordered) > 0 {
		patch.TalkgroupId = ordered[0]
	}
}

// homeRank scores a talkgroup by its position in the ranked member list:
// highest-listed scores highest, an outsider scores nothing. A call only ever
// moves up this ladder, and only onto a talkgroup that really received a copy
// — the ranks decide promotion, never placement by decree.
func (patch *Patch) homeRank(talkgroupId uint) int {
	for i, id := range patch.Talkgroups {
		if id == talkgroupId {
			return len(patch.Talkgroups) - i
		}
	}

	return 0
}

// usable says whether this patch can collapse anything. A patch of one
// talkgroup is just that talkgroup.
func (patch *Patch) usable() bool {
	return !patch.Disabled && patch.SystemId != 0 && patch.TalkgroupId != 0 && len(patch.Talkgroups) > 1
}

type Patches struct {
	List  []*Patch
	mutex sync.Mutex
}

func NewPatches() *Patches {
	return &Patches{
		List:  []*Patch{},
		mutex: sync.Mutex{},
	}
}

func (patches *Patches) FromMap(f []any) *Patches {
	patches.mutex.Lock()
	defer patches.mutex.Unlock()

	patches.List = []*Patch{}

	for _, r := range f {
		switch m := r.(type) {
		case map[string]any:
			patch := &Patch{}
			patch.FromMap(m)
			patches.List = append(patches.List, patch)
		}
	}

	return patches
}

// GetPatch returns the patch a talkgroup belongs to, if any.
//
// The first usable match wins. Overlapping patches are a misconfiguration, and
// picking one deterministically beats collapsing a call twice.
func (patches *Patches) GetPatch(systemId uint, talkgroupId uint) (*Patch, bool) {
	patches.mutex.Lock()
	defer patches.mutex.Unlock()

	for _, patch := range patches.List {
		if patch.SystemId != systemId || !patch.usable() {
			continue
		}

		for _, id := range patch.Talkgroups {
			if id == talkgroupId {
				return patch, true
			}
		}
	}

	return nil, false
}

func (patches *Patches) Read(db *Database) error {
	var (
		err        error
		id         sql.NullFloat64
		order      sql.NullFloat64
		rows       *sql.Rows
		talkgroups string
	)

	patches.mutex.Lock()
	defer patches.mutex.Unlock()

	patches.List = []*Patch{}

	formatError := func(err error) error {
		return fmt.Errorf("patches.read: %v", err)
	}

	if rows, err = db.Query("select `_id`, `disabled`, `label`, `order`, `systemId`, `talkgroupId`, `primaryTalkgroupId`, `talkgroups`, `delay` from `rdioScannerPatches`"); err != nil {
		return formatError(err)
	}

	for rows.Next() {
		patch := &Patch{}

		if err = rows.Scan(&id, &patch.Disabled, &patch.Label, &order, &patch.SystemId, &patch.TalkgroupId, &patch.PrimaryTalkgroupId, &talkgroups, &patch.Delay); err != nil {
			break
		}

		if id.Valid && id.Float64 > 0 {
			patch.Id = uint(id.Float64)
		}

		if order.Valid && order.Float64 > 0 {
			patch.Order = uint(order.Float64)
		}

		if err = json.Unmarshal([]byte(talkgroups), &patch.Talkgroups); err != nil {
			patch.Talkgroups = []uint{}
		}

		patch.normalize()

		patches.List = append(patches.List, patch)
	}

	rows.Close()

	if err != nil {
		return formatError(err)
	}

	return nil
}

func (patches *Patches) Write(db *Database) error {
	var (
		count  uint
		err    error
		rows   *sql.Rows
		rowIds = []uint{}
	)

	patches.mutex.Lock()
	defer patches.mutex.Unlock()

	formatError := func(err error) error {
		return fmt.Errorf("patches.write: %v", err)
	}

	if rows, err = db.Query("select `_id` from `rdioScannerPatches`"); err != nil {
		return formatError(err)
	}

	for rows.Next() {
		var rowId uint

		if err = rows.Scan(&rowId); err != nil {
			break
		}

		remove := true

		for _, patch := range patches.List {
			if patch.Id == nil || patch.Id == rowId {
				remove = false
				break
			}
		}

		if remove {
			rowIds = append(rowIds, rowId)
		}
	}

	rows.Close()

	if err != nil {
		return formatError(err)
	}

	if len(rowIds) > 0 {
		if b, err := json.Marshal(rowIds); err == nil {
			s := string(b)
			s = strings.ReplaceAll(s, "[", "(")
			s = strings.ReplaceAll(s, "]", ")")

			if _, err = db.Exec(fmt.Sprintf("delete from `rdioScannerPatches` where `_id` in %v", s)); err != nil {
				return formatError(err)
			}
		}
	}

	for _, patch := range patches.List {
		patch.normalize()

		var talkgroups string

		if b, err := json.Marshal(patch.Talkgroups); err == nil {
			talkgroups = string(b)
		} else {
			return formatError(err)
		}

		if err = db.QueryRow("select count(*) from `rdioScannerPatches` where `_id` = ?", patch.Id).Scan(&count); err != nil {
			break
		}

		if count == 0 {
			idVal, hasId := patch.Id.(uint)

			if db.Config.DbType == DbTypePostgres && (!hasId || idVal == 0) {
				_, err = db.Exec("insert into `rdioScannerPatches` (`disabled`, `label`, `order`, `systemId`, `talkgroupId`, `primaryTalkgroupId`, `talkgroups`, `delay`) values (?, ?, ?, ?, ?, ?, ?, ?)",
					patch.Disabled, patch.Label, patch.Order, patch.SystemId, patch.TalkgroupId, patch.PrimaryTalkgroupId, talkgroups, patch.Delay)
			} else {
				_, err = db.Exec("insert into `rdioScannerPatches` (`_id`, `disabled`, `label`, `order`, `systemId`, `talkgroupId`, `primaryTalkgroupId`, `talkgroups`, `delay`) values (?, ?, ?, ?, ?, ?, ?, ?, ?)",
					patch.Id, patch.Disabled, patch.Label, patch.Order, patch.SystemId, patch.TalkgroupId, patch.PrimaryTalkgroupId, talkgroups, patch.Delay)
			}

			if err != nil {
				break
			}

		} else if _, err = db.Exec("update `rdioScannerPatches` set `disabled` = ?, `label` = ?, `order` = ?, `systemId` = ?, `talkgroupId` = ?, `primaryTalkgroupId` = ?, `talkgroups` = ?, `delay` = ? where `_id` = ?",
			patch.Disabled, patch.Label, patch.Order, patch.SystemId, patch.TalkgroupId, patch.PrimaryTalkgroupId, talkgroups, patch.Delay, patch.Id); err != nil {
			break
		}
	}

	if err != nil {
		return formatError(err)
	}

	return nil
}
