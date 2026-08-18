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

type Talkgroup struct {
	Frequency  any `json:"frequency"`
	group      string
	GroupId    uint   `json:"groupId"`
	Id         uint   `json:"id"`
	Label      string `json:"label"`
	Led        any    `json:"led"`
	Led2       any    `json:"led2"`
	Name       string `json:"name"`
	Order      uint   `json:"order"`
	TagId      uint   `json:"tagId"`
	Delay      uint   `json:"delay"`
	Alert      string `json:"alert"`
	tag        string
}

func (talkgroup *Talkgroup) FromMap(m map[string]any) *Talkgroup {

	if v, ok := jsonUint(m["id"]); ok {
		talkgroup.Id = v
	}

	if v, ok := jsonUint(m["frequency"]); ok {
		talkgroup.Frequency = v
	}

	switch v := m["group"].(type) {
	case string:
		talkgroup.group = v
	}

	if v, ok := jsonUint(m["groupId"]); ok {
		talkgroup.GroupId = v
	}

	switch v := m["label"].(type) {
	case string:
		talkgroup.Label = v
	}

	switch v := m["led"].(type) {
	case string:
		talkgroup.Led = v
	}

	switch v := m["led2"].(type) {
	case string:
		talkgroup.Led2 = v
	}

	switch v := m["name"].(type) {
	case string:
		talkgroup.Name = v
	}

	if v, ok := jsonUint(m["order"]); ok {
		talkgroup.Order = v
	}

	switch v := m["tag"].(type) {
	case string:
		talkgroup.tag = v
	}

	if v, ok := jsonUint(m["tagId"]); ok {
		talkgroup.TagId = v
	}

	if v, ok := jsonUint(m["delay"]); ok {
		talkgroup.Delay = v
	}

	switch v := m["alert"].(type) {
	case string:
		talkgroup.Alert = v
	}

	return talkgroup
}

type TalkgroupMap map[string]any

type Talkgroups struct {
	List  []*Talkgroup
	mutex sync.Mutex
}

func NewTalkgroups() *Talkgroups {
	return &Talkgroups{
		List:  []*Talkgroup{},
		mutex: sync.Mutex{},
	}
}

func (talkgroups *Talkgroups) FromMap(f []any) *Talkgroups {
	talkgroups.mutex.Lock()
	defer talkgroups.mutex.Unlock()

	talkgroups.List = []*Talkgroup{}

	for _, r := range f {
		switch m := r.(type) {
		case map[string]any:
			talkgroup := &Talkgroup{}
			talkgroup.FromMap(m)
			talkgroups.List = append(talkgroups.List, talkgroup)
		}
	}

	return talkgroups
}

func (talkgroups *Talkgroups) GetTalkgroup(f any) (system *Talkgroup, ok bool) {
	talkgroups.mutex.Lock()
	defer talkgroups.mutex.Unlock()

	switch v := f.(type) {
	case uint:
		for _, talkgroup := range talkgroups.List {
			if talkgroup.Id == v {
				return talkgroup, true
			}
		}
	case string:
		for _, talkgroup := range talkgroups.List {
			if talkgroup.Label == v {
				return talkgroup, true
			}
		}
	}

	return nil, false
}

func (talkgroups *Talkgroups) Read(db *Database, systemId uint) error {
	var (
		err       error
		frequency sql.NullFloat64
		led       sql.NullString
		led2      sql.NullString
		rows      *sql.Rows
	)

	talkgroups.mutex.Lock()
	defer talkgroups.mutex.Unlock()

	talkgroups.List = []*Talkgroup{}

	formatError := func(err error) error {
		return fmt.Errorf("talkgroups.read: %v", err)
	}

	var alert sql.NullString
	if rows, err = db.Query("select `frequency`, `groupId`, `id`, `label`, `led`, `led2`, `name`, `order`, `tagId`, `delay`, `alert` from `rdioScannerTalkgroups` where `systemId` = ?", systemId); err != nil {
		return formatError(err)
	}

	for rows.Next() {
		talkgroup := &Talkgroup{}

		if err = rows.Scan(&frequency, &talkgroup.GroupId, &talkgroup.Id, &talkgroup.Label, &led, &led2, &talkgroup.Name, &talkgroup.Order, &talkgroup.TagId, &talkgroup.Delay, &alert); err != nil {
			break
		}

		if alert.Valid {
			talkgroup.Alert = alert.String
		}

		if frequency.Valid && frequency.Float64 > 0 {
			talkgroup.Frequency = uint(frequency.Float64)
		}

		if led.Valid && len(led.String) > 0 {
			talkgroup.Led = led.String
		}

		if led2.Valid && len(led2.String) > 0 {
			talkgroup.Led2 = led2.String
		}

		talkgroups.List = append(talkgroups.List, talkgroup)
	}

	rows.Close()

	if err != nil {
		return formatError(err)
	}

	sort.Slice(talkgroups.List, func(i int, j int) bool {
		return talkgroups.List[i].Order < talkgroups.List[j].Order
	})

	return nil
}

func (talkgroups *Talkgroups) Write(db *Database, systemId uint) error {
	var (
		count uint
		err   error
		ids   = []uint{}
		rows  *sql.Rows
	)

	talkgroups.mutex.Lock()
	defer talkgroups.mutex.Unlock()

	formatError := func(err error) error {
		return fmt.Errorf("talkgroups.write: %v", err)
	}

	if rows, err = db.Query("select `id` from `rdioScannerTalkgroups` where `systemId` = ?", systemId); err != nil {
		return formatError(err)
	}

	for rows.Next() {
		var id uint
		if err = rows.Scan(&id); err != nil {
			break
		}
		remove := true
		for _, talkgroup := range talkgroups.List {
			if talkgroup.Id == id {
				remove = false
				break
			}
		}
		if remove {
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
			q := fmt.Sprintf("delete from `rdioScannerTalkgroups` where `id` in %v and `systemId` = %v", s, systemId)
			if _, err = db.Exec(q); err != nil {
				return formatError(err)
			}
		}
	}

	for _, talkgroup := range talkgroups.List {
		if err = db.QueryRow("select count(*) from `rdioScannerTalkgroups` where `id` = ? and `systemId` = ?", talkgroup.Id, systemId).Scan(&count); err != nil {
			break
		}

		if count == 0 {
			if _, err = db.Exec("insert into `rdioScannerTalkgroups` (`frequency`, `groupId`, `id`, `label`, `led`, `led2`, `name`, `order`, `systemId`, `tagId`, `delay`, `alert`) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", talkgroup.Frequency, talkgroup.GroupId, talkgroup.Id, talkgroup.Label, talkgroup.Led, talkgroup.Led2, talkgroup.Name, talkgroup.Order, systemId, talkgroup.TagId, talkgroup.Delay, talkgroup.Alert); err != nil {
				break
			}

		} else if _, err = db.Exec("update `rdioScannerTalkgroups` set `frequency` = ?, `groupId` = ?, `label` = ?, `led` = ?, `led2` = ?, `name` = ?, `order` = ?, `tagId` = ?, `delay` = ?, `alert` = ? where `id` = ? and `systemId` = ?", talkgroup.Frequency, talkgroup.GroupId, talkgroup.Label, talkgroup.Led, talkgroup.Led2, talkgroup.Name, talkgroup.Order, talkgroup.TagId, talkgroup.Delay, talkgroup.Alert, talkgroup.Id, systemId); err != nil {
			break
		}
	}

	if err != nil {
		return formatError(err)
	}

	return nil
}

type TalkgroupsMap []TalkgroupMap
