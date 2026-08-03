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
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The theme contract is a published promise, and the stylesheet is where it
// actually lives.
//
// The document is what a theme author reads, so a property present in one and
// absent from the other is a defect either way round: undocumented means nobody
// uses it, and documented-but-absent means a theme sets a property that does
// nothing and cannot tell. Neither shows up in a build, which is why they are
// checked here rather than trusted.

const (
	themeStylesheet = "../client/src/styles.scss"
	themeReference  = "../../rdio-scanner-plugins/docs/theme-contract.md"
)

// Only the declarations inside the :root block are the contract. Custom
// properties used elsewhere in the stylesheet are implementation.
var themeProperty = regexp.MustCompile(`(?m)^\s*(--[a-z0-9-]+)\s*:`)

func contractProperties(t *testing.T) []string {
	t.Helper()

	body, err := os.ReadFile(themeStylesheet)
	if err != nil {
		t.Skipf("client stylesheet not present alongside the server: %v", err)
	}

	text := string(body)

	open := strings.Index(text, ":root {")
	if open < 0 {
		t.Fatal("styles.scss has no :root block")
	}

	close := strings.Index(text[open:], "\n}")
	if close < 0 {
		t.Fatal("the :root block in styles.scss is not closed")
	}

	names := []string{}
	for _, match := range themeProperty.FindAllStringSubmatch(text[open:open+close], -1) {
		names = append(names, match[1])
	}

	if len(names) == 0 {
		t.Fatal("no custom properties found in the :root block")
	}

	sort.Strings(names)

	return names
}

func TestEveryThemePropertyIsDocumented(t *testing.T) {
	body, err := os.ReadFile(themeReference)
	if err != nil {
		t.Skipf("plugins checkout not present alongside the server: %v", err)
	}

	documented := string(body)

	missing := []string{}

	for _, name := range contractProperties(t) {
		// Backticked the way the table writes it, so a property name appearing
		// inside another property's default value cannot pass for an entry.
		if !strings.Contains(documented, "`"+name+"`") {
			missing = append(missing, name)
		}
	}

	if len(missing) > 0 {
		t.Errorf(
			"these theme properties are in styles.scss but not in theme-contract.md: %s",
			strings.Join(missing, ", "),
		)
	}
}

// The other direction. A property documented but no longer declared is worse
// than an undocumented one: a theme sets it, nothing reads it, and there is
// nothing to observe.
func TestNoDocumentedThemePropertyHasBeenRemoved(t *testing.T) {
	body, err := os.ReadFile(themeReference)
	if err != nil {
		t.Skipf("plugins checkout not present alongside the server: %v", err)
	}

	declared := map[string]bool{}
	for _, name := range contractProperties(t) {
		declared[name] = true
	}

	// Only the table rows, so prose mentioning a property in passing is not
	// mistaken for a contract entry.
	row := regexp.MustCompile("(?m)^\\|\\s*`(--[a-z0-9-]+)`\\s*\\|")

	stale := []string{}

	for _, match := range row.FindAllStringSubmatch(string(body), -1) {
		if !declared[match[1]] {
			stale = append(stale, match[1])
		}
	}

	if len(stale) > 0 {
		t.Errorf(
			"theme-contract.md documents these, but styles.scss no longer declares them: %s",
			strings.Join(stale, ", "),
		)
	}
}
