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
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

type Unit struct {
	Id    uint   `json:"id"`
	Label string `json:"label"`
	Order uint   `json:"order"`

	// stored is what this unit looked like when it was last read from the
	// database: see Units.Write.
	stored string
}

// signature is every stored field of the unit, as text, for telling whether
// it has changed since it was read.
func (unit *Unit) signature() string {
	return fmt.Sprintf("%d\x00%s\x00%d", unit.Id, unit.Label, unit.Order)
}

func (unit *Unit) FromMap(m map[string]any) *Unit {
	if v, ok := jsonUint(m["id"]); ok {
		unit.Id = v
	}

	switch v := m["label"].(type) {
	case string:
		unit.Label = v
	}

	if v, ok := jsonUint(m["order"]); ok {
		unit.Order = v
	}

	return unit
}

type Units struct {
	List  []*Unit
	mutex sync.Mutex
}

func NewUnits() *Units {
	return &Units{
		List:  []*Unit{},
		mutex: sync.Mutex{},
	}
}

func (units *Units) Add(id uint, label string) (*Units, bool) {
	added := true

	for _, u := range units.List {
		if u.Id == id {
			added = false
			break
		}
	}

	if added {
		units.List = append(units.List, &Unit{Id: id, Label: label})
	}

	return units, added
}

func (units *Units) FromMap(f []any) *Units {
	units.mutex.Lock()
	defer units.mutex.Unlock()

	units.List = []*Unit{}

	for _, r := range f {
		switch m := r.(type) {
		case map[string]any:
			unit := &Unit{}
			unit.FromMap(m)
			units.List = append(units.List, unit)
		}
	}

	return units
}

func (u *Units) Merge(units *Units) bool {
	merged := false

	if units != nil {
		u.mutex.Lock()
		defer u.mutex.Unlock()

		for _, v := range units.List {
			if _, added := u.Add(v.Id, v.Label); added {
				merged = added
			}
		}
	}

	return merged
}

func (units *Units) Read(db *Database, systemId uint) error {
	var (
		err  error
		rows *sql.Rows
	)

	units.mutex.Lock()
	defer units.mutex.Unlock()

	// Built aside and swapped in on success: see Groups.Read.
	list := []*Unit{}

	formatError := func(err error) error {
		return fmt.Errorf("units.read: %v", err)
	}

	if rows, err = db.Query("select `id`, `label`, `order` from `rdioScannerUnits` where `systemId` = ?", systemId); err != nil {
		return formatError(err)
	}

	for rows.Next() {
		unit := &Unit{}

		if err = rows.Scan(&unit.Id, &unit.Label, &unit.Order); err != nil {
			break
		}

		list = append(list, unit)
	}

	if err == nil {
		err = rows.Err()
	}

	rows.Close()

	if err != nil {
		return formatError(err)
	}

	sort.Slice(list, func(i int, j int) bool {
		return list[i].Order < list[j].Order
	})

	for _, unit := range list {
		unit.stored = unit.signature()
	}

	units.List = list

	return nil
}

func (units *Units) Write(db *Database, systemId uint) error {
	var (
		count uint
		err   error
		ids   = []uint{}
		rows  *sql.Rows
	)

	units.mutex.Lock()
	defer units.mutex.Unlock()

	formatError := func(err error) error {
		return fmt.Errorf("units.write: %v", err)
	}

	if rows, err = db.Query("select `id` from `rdioScannerUnits` where `systemId` = ?", systemId); err != nil {
		return formatError(err)
	}

	// Looked up by id rather than by walking the list for each row, which is
	// the difference between a few thousand comparisons and a few million.
	listed := make(map[uint]bool, len(units.List))
	for _, unit := range units.List {
		listed[unit.Id] = true
	}

	present := map[uint]bool{}

	for rows.Next() {
		var id uint
		if err = rows.Scan(&id); err != nil {
			break
		}
		present[id] = true
		if !listed[id] {
			ids = append(ids, id)
		}
	}

	rows.Close()

	if err != nil {
		return formatError(err)
	}

	if len(ids) > 0 {
		if b, err := json.Marshal(ids); err == nil {
			s := string(b)
			s = strings.ReplaceAll(s, "[", "(")
			s = strings.ReplaceAll(s, "]", ")")
			q := fmt.Sprintf("delete from `rdioScannerUnits` where `id` in %v and `systemId` = %v", s, systemId)
			if _, err = db.Exec(q); err != nil {
				return formatError(err)
			}
		}
	}

	for _, unit := range units.List {
		// Untouched since it was read, and its row is still there: nothing
		// to write.
		//
		// Every unit used to be looked up and rewritten on every write, and
		// a write happens whenever a call teaches the server anything — one
		// new unit id is enough. On a system with a few thousand units and
		// talkgroups that was nine thousand statements, two seconds on
		// Postgres, run on the goroutine that ingests calls: every other
		// call waited behind it, each time a radio nobody had seen keyed up.
		if unit.stored != "" && unit.stored == unit.signature() && present[unit.Id] {
			continue
		}

		if err = db.QueryRow("select count(*) from `rdioScannerUnits` where `id` = ? and `systemId` = ?", unit.Id, systemId).Scan(&count); err != nil {
			break
		}

		if count == 0 {
			if _, err = db.Exec("insert into `rdioScannerUnits` (`id`, `label`, `order`, `systemId`) values (?, ?, ?, ?)", unit.Id, unit.Label, unit.Order, systemId); err != nil {
				break
			}

		} else if _, err = db.Exec("update `rdioScannerUnits` set `label` = ?, `order` = ? where `id` = ? and `systemId` = ?", unit.Label, unit.Order, unit.Id, systemId); err != nil {
			break
		}
	}

	if err != nil {
		return formatError(err)
	}

	return nil
}

// MarshalJSON renders the units as the list they are, for the reason
// Talkgroups.MarshalJSON gives.
func (units *Units) MarshalJSON() ([]byte, error) {
	if units == nil || units.List == nil {
		return []byte("[]"), nil
	}

	return json.Marshal(units.List)
}
