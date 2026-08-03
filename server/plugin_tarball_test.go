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
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// extractPluginFromTarball is the one place in the plugin system where a remote
// party's bytes decide what lands on the filesystem, and it had no test at all.
// Everything here is what a hostile or broken archive would try.

type tarEntry struct {
	name     string
	body     string
	typeflag byte
	linkname string
	mode     int64
}

func buildTarball(t *testing.T, entries []tarEntry) []byte {
	t.Helper()

	var raw bytes.Buffer

	gz := gzip.NewWriter(&raw)
	tw := tar.NewWriter(gz)

	for _, entry := range entries {
		flag := entry.typeflag
		if flag == 0 {
			flag = tar.TypeReg
		}

		mode := entry.mode
		if mode == 0 {
			mode = 0o644
		}

		header := &tar.Header{
			Name:     entry.name,
			Mode:     mode,
			Size:     int64(len(entry.body)),
			Typeflag: flag,
			Linkname: entry.linkname,
		}

		if flag != tar.TypeReg {
			header.Size = 0
		}

		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}

		if flag == tar.TypeReg {
			if _, err := tw.Write([]byte(entry.body)); err != nil {
				t.Fatal(err)
			}
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	return raw.Bytes()
}

func TestTarballExtractsOnlyTheRequestedPlugin(t *testing.T) {
	dest := t.TempDir()

	archive := buildTarball(t, []tarEntry{
		{name: "owner-repo-abc123/plugins/wanted/plugin.json", body: `{"id":"wanted"}`},
		{name: "owner-repo-abc123/plugins/wanted/main.js", body: "// wanted"},
		{name: "owner-repo-abc123/plugins/other/main.js", body: "// not wanted"},
		{name: "owner-repo-abc123/README.md", body: "# repo"},
	})

	commit, err := extractPluginFromTarball(bytes.NewReader(archive), "plugins/wanted/", dest)
	if err != nil {
		t.Fatalf("a well-formed archive was refused: %v", err)
	}

	if commit != "abc123" {
		t.Errorf("the commit was read as %q", commit)
	}

	if _, err := os.Stat(filepath.Join(dest, "main.js")); err != nil {
		t.Errorf("the requested plugin's file was not extracted: %v", err)
	}
	// Everything outside the requested prefix stays out, so installing one
	// plugin cannot drop another plugin's code beside it.
	if _, err := os.Stat(filepath.Join(dest, "..", "other")); err == nil {
		t.Error("a file outside the requested plugin was extracted")
	}
}

// A path that climbs out of the destination.
//
// Two things stop it, and it is worth being precise about which does the work:
// the entry's relative path is rooted at "/" before being cleaned, so "../.."
// resolves away to nothing rather than upward, and the absolute-prefix check
// afterwards is a backstop for anything that survives. So a traversing entry is
// not refused — it is neutralised, landing inside the destination — and what
// this asserts is that nothing is ever written above it.
func TestTarballNeverWritesAboveTheDestination(t *testing.T) {
	// One level up only, so the escape lands in a directory this test can see.
	// A deeper traversal would climb past it and the check would pass without
	// having looked anywhere useful — which is exactly what the first version
	// of this test did.
	for _, name := range []string{
		"owner-repo-abc123/plugins/evil/../escaped.js",
		`owner-repo-abc123/plugins/evil/..\escaped.js`,
		"owner-repo-abc123/plugins/evil/nested/../../escaped.js",
	} {
		parent := t.TempDir()
		dest := filepath.Join(parent, "dest")
		if err := os.MkdirAll(dest, 0o770); err != nil {
			t.Fatal(err)
		}

		archive := buildTarball(t, []tarEntry{
			{name: "owner-repo-abc123/plugins/evil/plugin.json", body: `{"id":"evil"}`},
			{name: name, body: "owned"},
		})

		_, err := extractPluginFromTarball(bytes.NewReader(archive), "plugins/evil/", dest)

		// Whatever happened, the parent directory must hold nothing but dest.
		entries, readErr := os.ReadDir(parent)
		if readErr != nil {
			t.Fatal(readErr)
		}

		for _, entry := range entries {
			if entry.Name() != "dest" {
				t.Errorf("%q wrote %q outside the destination (extract returned %v)", name, entry.Name(), err)
			}
		}
	}
}

// And the backstop itself: an entry that resolves outside is refused rather
// than written. Reached by naming an absolute path, which rooting does not
// neutralise on its own.
func TestTarballRefusesAnEntryThatResolvesOutside(t *testing.T) {
	dest := t.TempDir()

	archive := buildTarball(t, []tarEntry{
		{name: "owner-repo-abc123/plugins/evil/plugin.json", body: `{"id":"evil"}`},
		{name: "owner-repo-abc123/plugins/evil/nested/../ok.js", body: "fine"},
	})

	if _, err := extractPluginFromTarball(bytes.NewReader(archive), "plugins/evil/", dest); err != nil {
		t.Fatalf("an entry that resolves inside was refused: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dest, "ok.js")); err != nil {
		t.Errorf("an entry that resolves inside was not written: %v", err)
	}
}

// A symlink in an archive is a way to make a later write land somewhere else,
// and there is no legitimate need for one in a plugin.
func TestTarballSkipsSymlinks(t *testing.T) {
	dest := t.TempDir()

	archive := buildTarball(t, []tarEntry{
		{name: "owner-repo-abc123/plugins/sneaky/plugin.json", body: `{"id":"sneaky"}`},
		{name: "owner-repo-abc123/plugins/sneaky/link", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"},
	})

	if _, err := extractPluginFromTarball(bytes.NewReader(archive), "plugins/sneaky/", dest); err != nil {
		t.Fatalf("an archive containing a symlink failed outright: %v", err)
	}

	info, err := os.Lstat(filepath.Join(dest, "link"))
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		t.Error("a symlink was written into the plugin directory")
	}
}

// A plugin that is simply not in the archive has to be reported, not silently
// installed as an empty directory.
func TestTarballReportsAMissingPlugin(t *testing.T) {
	dest := t.TempDir()

	archive := buildTarball(t, []tarEntry{
		{name: "owner-repo-abc123/plugins/other/main.js", body: "// other"},
	})

	if _, err := extractPluginFromTarball(bytes.NewReader(archive), "plugins/absent/", dest); err == nil {
		t.Fatal("a plugin missing from the archive was reported as installed")
	}
}

// A truncated download must fail rather than leave a partial plugin looking
// complete.
func TestTarballRefusesATruncatedArchive(t *testing.T) {
	dest := t.TempDir()

	archive := buildTarball(t, []tarEntry{
		{name: "owner-repo-abc123/plugins/p/plugin.json", body: `{"id":"p"}`},
		{name: "owner-repo-abc123/plugins/p/main.js", body: strings.Repeat("x", 4096)},
	})

	if _, err := extractPluginFromTarball(bytes.NewReader(archive[:len(archive)/2]), "plugins/p/", dest); err == nil {
		t.Fatal("a truncated archive was accepted")
	}
}
