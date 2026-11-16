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
	"strings"
	"sync"

	"github.com/google/uuid"
)

type Apikey struct {
	Id       any    `json:"_id"`
	Disabled bool   `json:"disabled"`
	Ident    string `json:"ident"`
	Key      string `json:"key"`
	Order    any    `json:"order"`
	Systems  any    `json:"systems"`
}

func (apikey *Apikey) FromMap(m map[string]any) *Apikey {
	if v, ok := jsonUint(m["_id"]); ok {
		apikey.Id = v
	}

	switch v := m["disabled"].(type) {
	case bool:
		apikey.Disabled = v
	}

	switch v := m["ident"].(type) {
	case string:
		apikey.Ident = v
	}

	switch v := m["key"].(type) {
	case string:
		apikey.Key = v
	}

	if v, ok := jsonUint(m["order"]); ok {
		apikey.Order = v
	}

	switch v := m["systems"].(type) {
	case []any:
		// Kept as the list it is, the shape a read from the database gives
		// and the only one HasAccess understands. It used to be turned into
		// its JSON text here and stay that way until the save finished and
		// re-read the table — so for the length of every config save a
		// scoped entry matched nothing.
		apikey.Systems = v
	case string:
		apikey.Systems = v
	}

	return apikey
}

func (apikey *Apikey) HasAccess(call *Call) bool {
	switch v := apikey.Systems.(type) {
	case []any:
		for _, f := range v {
			switch v := f.(type) {
			case map[string]any:
				if id, ok := jsonUint(v["id"]); ok {
					if id == call.System {
						switch tg := v["talkgroups"].(type) {
						case string:
							if tg == "*" {
								return true
							}
						case []any:
							for _, f := range tg {
								if tg, ok := jsonUint(f); ok {
									if tg == call.Talkgroup {
										return true
									}
								}
							}
						}
					}
				}
			}
		}

	case string:
		if v == "*" {
			return true
		}
	}

	return false
}

type Apikeys struct {
	List  []*Apikey
	mutex sync.Mutex
}

func NewApikeys() *Apikeys {
	return &Apikeys{
		List:  []*Apikey{},
		mutex: sync.Mutex{},
	}
}

func (apikeys *Apikeys) FromMap(f []any) *Apikeys {
	apikeys.mutex.Lock()
	defer apikeys.mutex.Unlock()

	apikeys.List = []*Apikey{}

	for _, r := range f {
		switch m := r.(type) {
		case map[string]any:
			apikey := &Apikey{}
			apikey.FromMap(m)
			apikeys.List = append(apikeys.List, apikey)
		}
	}

	return apikeys
}

func (apikeys *Apikeys) GetApikey(key string) (apikey *Apikey, ok bool) {
	apikeys.mutex.Lock()
	defer apikeys.mutex.Unlock()

	for _, apikey := range apikeys.List {
		if apikey.Key == key && !apikey.Disabled {
			return apikey, true
		}
	}
	return nil, false
}

func (apikeys *Apikeys) Read(db *Database) error {
	var (
		err     error
		id      sql.NullFloat64
		order   sql.NullFloat64
		rows    *sql.Rows
		systems string
	)

	apikeys.mutex.Lock()
	defer apikeys.mutex.Unlock()

	// Built aside and swapped in on success: see Groups.Read.
	list := []*Apikey{}

	formatError := func(err error) error {
		return fmt.Errorf("apikeys.read: %v", err)
	}

	if rows, err = db.Query("select `_id`, `disabled`, `ident`, `key`, `order`, `systems` from `rdioScannerApiKeys`"); err != nil {
		return formatError(err)
	}

	for rows.Next() {
		apikey := &Apikey{}

		if err = rows.Scan(&id, &apikey.Disabled, &apikey.Ident, &apikey.Key, &order, &systems); err != nil {
			break
		}

		if id.Valid && id.Float64 > 0 {
			apikey.Id = uint(id.Float64)
		}

		if len(apikey.Ident) == 0 {
			apikey.Ident = defaults.apikey.ident
		}

		if len(apikey.Key) == 0 {
			apikey.Key = uuid.New().String()
		}

		if order.Valid && order.Float64 > 0 {
			apikey.Order = uint(order.Float64)
		}

		if err = json.Unmarshal([]byte(systems), &apikey.Systems); err != nil {
			apikey.Systems = []any{}
		}

		list = append(list, apikey)
	}

	if err == nil {
		err = rows.Err()
	}

	rows.Close()

	if err != nil {
		return formatError(err)
	}

	apikeys.List = list

	return nil
}

func (apikeys *Apikeys) Write(db *Database) error {
	var (
		count   uint
		err     error
		rows    *sql.Rows
		rowIds  = []uint{}
		systems any
	)

	apikeys.mutex.Lock()
	defer apikeys.mutex.Unlock()

	formatError := func(err error) error {
		return fmt.Errorf("apikeys.write %v", err)
	}

	if rows, err = db.Query("select `_id` from `rdioScannerApiKeys`"); err != nil {
		return formatError(err)
	}

	for rows.Next() {
		var id uint
		if err = rows.Scan(&id); err != nil {
			break
		}
		remove := true
		for _, apikey := range apikeys.List {
			// See Accesses.Write: a new entry must not keep every old row.
			if apikey.Id == id {
				remove = false
				break
			}
		}
		if remove {
			rowIds = append(rowIds, id)
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
			q := fmt.Sprintf("delete from `rdioScannerApiKeys` where `_id` in %v", s)
			if _, err = db.Exec(q); err != nil {
				return formatError(err)
			}
		}
	}

	for _, apikey := range apikeys.List {
		systems = scopeColumnValue(apikey.Systems)

		if err = db.QueryRow("select count(*) from `rdioScannerApiKeys` where `_id` = ?", apikey.Id).Scan(&count); err != nil {
			break
		}

		if count == 0 {
			idVal, hasId := apikey.Id.(uint)
			if db.Config.DbType == DbTypePostgres && (!hasId || idVal == 0) {
				_, err = db.Exec("insert into `rdioScannerApiKeys` (`disabled`, `ident`, `key`, `order`, `systems`) values (?, ?, ?, ?, ?)", apikey.Disabled, apikey.Ident, apikey.Key, apikey.Order, systems)
			} else {
				_, err = db.Exec("insert into `rdioScannerApiKeys` (`_id`, `disabled`, `ident`, `key`, `order`, `systems`) values (?, ?, ?, ?, ?, ?)", apikey.Id, apikey.Disabled, apikey.Ident, apikey.Key, apikey.Order, systems)
			}
			if err != nil {
				break
			}

		} else if _, err = db.Exec("update `rdioScannerApiKeys` set `_id` = ?, `disabled` = ?, `ident` = ?, `key` = ?, `order` = ?, `systems` = ? where `_id` = ?", apikey.Id, apikey.Disabled, apikey.Ident, apikey.Key, apikey.Order, systems, apikey.Id); err != nil {
			break
		}
	}

	if err != nil {
		return formatError(err)
	}

	return nil
}

// scopeColumnValue renders a systems scope for the `systems` column of the
// accesses, API keys and downstreams tables, which hold it as JSON text.
//
// The scope lives in memory as "*" or as the decoded list. The writers used to
// bind whatever they were holding, which was only ever right for a value fresh
// from a config save: anything read back from the database is a list, and a
// list is not something a driver can bind. Every write that did not come
// straight from the admin form — the user-add and user-remove commands, a
// plugin editing accesses, keys or downstreams — therefore failed outright as
// soon as one entry on the server was scoped to particular systems.
func scopeColumnValue(scope any) any {
	switch v := scope.(type) {
	case nil:
		return nil

	case string:
		if v == "*" {
			return `"*"`
		}
		return v

	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil
		}
		return string(b)
	}
}
