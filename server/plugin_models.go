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
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
)

// Read and write access to the server's configuration.
//
// Every collection in rdio already exposes the same three methods — FromMap to
// replace the set, Write to persist, Read to reload — because that is what the
// admin panel's save path uses. One generic wrapper over that shape covers all
// of them, which is why adding a model here is a single table entry rather than
// a new API.
//
// The shapes plugins see are the structs' own JSON tags, so a plugin and the
// admin panel are looking at exactly the same thing.

type pluginModel struct {
	// name is what a plugin addresses: rdio.models.<name>.
	name string

	// key identifies an entry within the collection. Almost always "_id".
	key string

	// describes what the model holds, for the generated reference.
	summary string

	// sample is a zero value of the entry type, used to document its fields.
	sample any

	// list returns the current entries.
	list func(*Controller) ([]any, error)

	// replace swaps the whole collection and persists it.
	replace func(*Controller, []any) error
}

// pluginModels is the catalogue. The generated reference is built from this, so
// a model added here is documented automatically and one removed cannot leave a
// stale entry behind.
var pluginModels = []pluginModel{
	{
		name: "systems", key: "_id", sample: System{},
		summary: "Radio systems, with their talkgroups and units.",
		list:    func(c *Controller) ([]any, error) { return modelToList(c.Systems.List) },
		replace: func(c *Controller, v []any) error {
			c.Systems.FromMap(v)
			if err := c.Systems.Write(c.Database); err != nil {
				return err
			}
			return c.Systems.Read(c.Database)
		},
	},
	{
		name: "groups", key: "_id", sample: Group{},
		summary: "Talkgroup groupings shown in the selector.",
		list:    func(c *Controller) ([]any, error) { return modelToList(c.Groups.List) },
		replace: func(c *Controller, v []any) error {
			c.Groups.FromMap(v)
			if err := c.Groups.Write(c.Database); err != nil {
				return err
			}
			return c.Groups.Read(c.Database)
		},
	},
	{
		name: "tags", key: "_id", sample: Tag{},
		summary: "Talkgroup tags shown in the selector.",
		list:    func(c *Controller) ([]any, error) { return modelToList(c.Tags.List) },
		replace: func(c *Controller, v []any) error {
			c.Tags.FromMap(v)
			if err := c.Tags.Write(c.Database); err != nil {
				return err
			}
			return c.Tags.Read(c.Database)
		},
	},
	{
		name: "access", key: "_id", sample: Access{},
		summary: "Access codes and the systems each one unlocks.",
		list:    func(c *Controller) ([]any, error) { return modelToList(c.Accesses.List) },
		replace: func(c *Controller, v []any) error {
			c.Accesses.FromMap(v)
			if err := c.Accesses.Write(c.Database); err != nil {
				return err
			}
			return c.Accesses.Read(c.Database)
		},
	},
	{
		name: "apikeys", key: "_id", sample: Apikey{},
		summary: "API keys accepted for call uploads.",
		list:    func(c *Controller) ([]any, error) { return modelToList(c.Apikeys.List) },
		replace: func(c *Controller, v []any) error {
			c.Apikeys.FromMap(v)
			if err := c.Apikeys.Write(c.Database); err != nil {
				return err
			}
			return c.Apikeys.Read(c.Database)
		},
	},
	{
		name: "downstreams", key: "_id", sample: Downstream{},
		summary: "Other Rdio Scanner servers calls are forwarded to.",
		list:    func(c *Controller) ([]any, error) { return modelToList(c.Downstreams.List) },
		replace: func(c *Controller, v []any) error {
			c.Downstreams.FromMap(v)
			if err := c.Downstreams.Write(c.Database); err != nil {
				return err
			}
			return c.Downstreams.Read(c.Database)
		},
	},
	{
		name: "dirwatches", key: "_id", sample: Dirwatch{},
		summary: "Watched directories that calls are ingested from.",
		list:    func(c *Controller) ([]any, error) { return modelToList(c.Dirwatches.List) },
		replace: func(c *Controller, v []any) error {
			// Stopped and restarted around the write, the way the admin save
			// path does it. Without that the row was stored and shown in the
			// panel and in list(), and no files were ingested from the new
			// directory until the server restarted — a plugin adding a watch
			// appeared to succeed and silently did nothing.
			c.Dirwatches.Stop()

			c.Dirwatches.FromMap(v)

			if err := c.Dirwatches.Write(c.Database); err != nil {
				c.Dirwatches.Start(c)
				return err
			}

			if err := c.Dirwatches.Read(c.Database); err != nil {
				c.Dirwatches.Start(c)
				return err
			}

			c.Dirwatches.Start(c)

			return nil
		},
	},
}

// modelToList renders a typed collection as plain values using the structs' own
// JSON tags, so a plugin sees the same shape the admin API does.
func modelToList(v any) ([]any, error) {
	encoded, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}

	var list []any
	if err := json.Unmarshal(encoded, &list); err != nil {
		return nil, err
	}

	if list == nil {
		list = []any{}
	}

	return list, nil
}

func findPluginModel(name string) (*pluginModel, bool) {
	for i := range pluginModels {
		if pluginModels[i].name == name {
			return &pluginModels[i], true
		}
	}
	return nil, false
}

// --- operations ------------------------------------------------------------

func (controller *Controller) modelList(name string) ([]any, error) {
	model, ok := findPluginModel(name)
	if !ok {
		return nil, fmt.Errorf("no such model %q", name)
	}
	return model.list(controller)
}

func (controller *Controller) modelGet(name string, key any) (any, error) {
	model, ok := findPluginModel(name)
	if !ok {
		return nil, fmt.Errorf("no such model %q", name)
	}

	entries, err := model.list(controller)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if m, ok := entry.(map[string]any); ok && sameKey(m[model.key], key) {
			return m, nil
		}
	}

	return nil, nil
}

// modelSet inserts or updates one entry, leaving the rest of the collection
// alone. The underlying models only know how to replace the whole set, so the
// merge happens here rather than making every plugin re-send everything.
func (controller *Controller) modelSet(name string, entry map[string]any) error {
	model, ok := findPluginModel(name)
	if !ok {
		return fmt.Errorf("no such model %q", name)
	}

	entries, err := model.list(controller)
	if err != nil {
		return err
	}

	key := entry[model.key]
	replaced := false

	if key != nil {
		for i, existing := range entries {
			if m, ok := existing.(map[string]any); ok && sameKey(m[model.key], key) {
				entries[i] = entry
				replaced = true
				break
			}
		}
	}

	if !replaced {
		entries = append(entries, entry)
	}

	return model.replace(controller, entries)
}

func (controller *Controller) modelRemove(name string, key any) error {
	model, ok := findPluginModel(name)
	if !ok {
		return fmt.Errorf("no such model %q", name)
	}

	entries, err := model.list(controller)
	if err != nil {
		return err
	}

	kept := []any{}
	for _, entry := range entries {
		if m, ok := entry.(map[string]any); ok && sameKey(m[model.key], key) {
			continue
		}
		kept = append(kept, entry)
	}

	return model.replace(controller, kept)
}

// sameKey compares identifiers across the numeric types JSON and JavaScript
// hand back — an id can arrive as float64, int64 or a string depending on the
// path it took, and they should all match the same row.
func sameKey(a any, b any) bool {
	if a == nil || b == nil {
		return false
	}
	return fmt.Sprintf("%v", normalizeKey(a)) == fmt.Sprintf("%v", normalizeKey(b))
}

func normalizeKey(v any) any {
	switch t := v.(type) {
	case float64:
		if t == float64(int64(t)) {
			return int64(t)
		}
		return t
	case float32:
		return int64(t)
	case int:
		return int64(t)
	case uint:
		return int64(t)
	case uint64:
		return int64(t)
	case int32:
		return int64(t)
	default:
		return v
	}
}

// --- documentation ---------------------------------------------------------

// modelFields describes an entry's shape from the struct's JSON tags, so the
// generated reference is produced from the same declaration the code uses and
// cannot describe something that is not there.
func modelFields(sample any) []string {
	t := reflect.TypeOf(sample)
	if t == nil || t.Kind() != reflect.Struct {
		return nil
	}

	fields := []string{}

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}

		tag := field.Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}

		name := tag
		for j := 0; j < len(tag); j++ {
			if tag[j] == ',' {
				name = tag[:j]
				break
			}
		}

		if name == "" {
			continue
		}

		fields = append(fields, name)
	}

	sort.Strings(fields)

	return fields
}
