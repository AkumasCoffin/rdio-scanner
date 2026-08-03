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
	"net/http"
	"net/http/httptest"
	"testing"
)

// A plugin reads its own request headers, and there are two spellings of every
// one of them.
//
// Go canonicalises to Authorization; JavaScript authors write
// req.headers.authorization, because fetch and Node both hand them lowercase.
// Property access is case-sensitive, so the natural spelling returns undefined,
// and a plugin checking an admin token that way rejects every request from its
// own settings page — a 401 with nothing anywhere to explain it.

func requestHeaders(t *testing.T, set map[string]string) map[string]any {
	t.Helper()

	r := httptest.NewRequest(http.MethodGet, "/api/plugin/example/settings", nil)
	for key, value := range set {
		r.Header.Set(key, value)
	}

	return pluginRequestHeaders(r.Header)
}

func TestAPluginSeesHeadersInEitherSpelling(t *testing.T) {
	headers := requestHeaders(t, map[string]string{
		"Authorization": "a-token",
		"Content-Type":  "application/json",
	})

	for _, spelling := range []string{"Authorization", "authorization"} {
		if got, _ := headers[spelling].(string); got != "a-token" {
			t.Errorf("headers[%q] = %q, want the token", spelling, got)
		}
	}

	for _, spelling := range []string{"Content-Type", "content-type"} {
		if got, _ := headers[spelling].(string); got != "application/json" {
			t.Errorf("headers[%q] = %q, want application/json", spelling, got)
		}
	}
}

// A header already spelled lowercase must not be entered twice — a plugin
// iterating headers should not see one of them appear as its own duplicate.
func TestAnAlreadyLowercaseHeaderIsNotDuplicated(t *testing.T) {
	// Set on the map directly: Header.Set would canonicalise it, and the case
	// under test is the one that arrives already lowercase.
	headers := pluginRequestHeaders(http.Header{"x-trace": []string{"abc"}})

	if len(headers) != 1 {
		t.Errorf("one lowercase header produced %d entries: %v", len(headers), headers)
	}

	if got, _ := headers["x-trace"].(string); got != "abc" {
		t.Errorf(`headers["x-trace"] = %q, want abc`, got)
	}
}
