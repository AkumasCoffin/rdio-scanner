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
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The anchors are a promise, so something has to hold us to it.
//
// `data-rdio` exists because class names are styling and change freely — a
// plugin targeting `.history tr` breaks the first time someone reworks the
// table. The anchors are the part that does not change, which is only true for
// as long as nobody removes one while tidying a template. Nothing about
// deleting an attribute looks like breaking an API, and the plugin that used it
// fails silently in someone else's browser.
//
// So the list lives here, the templates are read as the source of truth, and
// the documentation is checked against the same list.

// pluginAnchors is the published contract. Adding to it is a feature; removing
// from it breaks plugins and has to be a deliberate act with this list edited.
var pluginAnchors = []string{
	// Shell and panels.
	"app",
	"panel-search",
	"panel-select",
	"panel-stats",
	"panel-plugin",
	"fab-plugin-views",
	"fab-stream-edit",

	// Main screen.
	"status",
	"branding",
	"led",
	"led2",
	"lcd",
	"lcd-transcript",
	"history",
	"history-row",
	"controls",
	"autojump",
	"volume",
	"help",

	// Every control button individually. Before these existed a plugin could
	// only tell them apart by their text, so hiding or restyling one meant
	// matching on innerText or counting siblings — both of which break the
	// moment a button is added or reworded.
	"control-livefeed",
	"control-hold-sys",
	"control-hold-tg",
	"control-replay",
	"control-skip",
	"control-avoid",
	"control-search",
	"control-pause",
	"control-select",
	"control-stats",
	"control-autojump",

	// Search.
	"search",
	"search-filters",
	"search-actions",
	"search-results",
	"search-row",

	// Talkgroup selection.
	"select-presets",
	"select-preset",
	"select-category",
	"select-group",
	"select-talkgroup",

	// Admin.
	"admin",
	"admin-login",
	"admin-stats",
	"admin-config",
	"admin-plugins",
	"admin-logs",
	"admin-tools",
	"admin-logout",
}

const componentsDir = "../client/src/app/components/rdio-scanner"

var anchorAttribute = regexp.MustCompile(`data-rdio="([a-z0-9-]+)"`)

// anchorsInTemplates is every anchor actually present in the markup.
func anchorsInTemplates(t *testing.T) map[string]int {
	t.Helper()

	found := map[string]int{}

	err := filepath.Walk(componentsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".html") {
			return err
		}

		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		for _, match := range anchorAttribute.FindAllStringSubmatch(string(body), -1) {
			found[match[1]]++
		}

		return nil
	})

	if err != nil {
		t.Skipf("client templates not present alongside the server: %v", err)
	}

	if len(found) == 0 {
		t.Skip("no templates found to check")
	}

	return found
}

func TestEveryPublishedAnchorIsInTheMarkup(t *testing.T) {
	found := anchorsInTemplates(t)

	missing := []string{}

	for _, anchor := range pluginAnchors {
		if found[anchor] == 0 {
			missing = append(missing, anchor)
		}
	}

	if len(missing) > 0 {
		t.Errorf(
			"these anchors are published to plugin authors but are in no template: %s",
			strings.Join(missing, ", "),
		)
	}
}

// The reverse direction. An anchor added to a template but never published is
// not part of the contract, so a plugin author has no way to know it exists and
// the next tidy-up removes it without anyone noticing.
func TestEveryAnchorInTheMarkupIsPublished(t *testing.T) {
	found := anchorsInTemplates(t)

	published := map[string]bool{}
	for _, anchor := range pluginAnchors {
		published[anchor] = true
	}

	unpublished := []string{}

	for anchor := range found {
		if !published[anchor] {
			unpublished = append(unpublished, anchor)
		}
	}

	sort.Strings(unpublished)

	if len(unpublished) > 0 {
		t.Errorf(
			"these anchors are in the markup but not published: %s — add them to pluginAnchors and the docs, or drop them",
			strings.Join(unpublished, ", "),
		)
	}
}

// The documentation carries the same table, and it is what an author actually
// reads. A contract present in the markup and absent from the docs is one
// nobody will use.
func TestEveryAnchorIsDocumented(t *testing.T) {
	const reference = "../../rdio-scanner-plugins/docs/frontend-api.md"

	body, err := os.ReadFile(reference)
	if err != nil {
		t.Skipf("plugins checkout not present alongside the server: %v", err)
	}

	documented := string(body)

	missing := []string{}

	for _, anchor := range pluginAnchors {
		// Quoted the way the docs write a selector, so a bare mention of the
		// word "search" in prose cannot pass for the `search` anchor.
		if !strings.Contains(documented, `data-rdio="`+anchor+`"`) {
			missing = append(missing, anchor)
		}
	}

	if len(missing) > 0 {
		t.Errorf("these anchors are not in frontend-api.md: %s", strings.Join(missing, ", "))
	}
}
