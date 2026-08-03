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

// Numbers reach the model structures from two places that disagree about type.
//
// The admin panel's values arrive through encoding/json, which decodes every
// number as float64. A plugin's arrive through goja, which gives an integer
// literal an int64. A type switch naming only float64 therefore reads the admin
// panel perfectly and silently ignores everything a plugin sets.
//
// The failure is unusually hard to spot because it is partial. A field the
// plugin never touched still holds the float64 it was decoded with, so a
// read-modify-write appears to work — right up until it is a field the plugin
// actually wrote, which is the only kind anyone cares about. This has now been
// found three times in three different places; these helpers exist so there is
// one implementation to get right rather than one per struct.

// jsonFloat reads a number regardless of which side it came from.
func jsonFloat(raw any) (float64, bool) {
	switch v := raw.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int64:
		return float64(v), true
	case int:
		return float64(v), true
	case int32:
		return float64(v), true
	case uint:
		return float64(v), true
	case uint32:
		return float64(v), true
	case uint64:
		return float64(v), true
	}

	return 0, false
}

// jsonUint is the same for the identifiers and counts that make up most of the
// model fields. A negative value is refused rather than wrapped: it is never a
// valid id, order or delay, and wrapping would turn a plugin's mistake into an
// enormous number that looks deliberate.
func jsonUint(raw any) (uint, bool) {
	value, ok := jsonFloat(raw)
	if !ok || value < 0 {
		return 0, false
	}

	return uint(value), true
}

// jsonUintFrom is the map-keyed form, which is how nearly every caller reads.
func jsonUintFrom(m map[string]any, key string) (uint, bool) {
	return jsonUint(m[key])
}
