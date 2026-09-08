// Copyright (C) 2019-2026 Chrystian Huot <chrystian.huot@saubeo.solutions>
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
	"testing"
)

// An update applies by renaming the running binary to <exe>.old and the
// download into its place. On Linux os.Executable() reads /proc/self/exe,
// which follows the inode, so after that rename the process reports
// <exe>.old — and re-executing it brings the version that was just replaced
// back up, with the same PID and an unchanged banner. Nothing looks wrong.
//
// restartTarget is the part that can be tested anywhere: given a path, which
// binary should come back up.
func TestRestartTargetPrefersTheSiblingOfAnOldBinary(t *testing.T) {
	dir := t.TempDir()

	real := filepath.Join(dir, "rdio-scanner")
	old := real + ".old"

	for _, path := range []string{real, old} {
		if err := os.WriteFile(path, []byte("binary"), 0o755); err != nil {
			t.Fatalf("cannot create %s: %v", path, err)
		}
	}

	if got := restartTarget(old); got != real {
		t.Fatalf("restartTarget(%q) = %q, want the un-suffixed sibling %q", old, got, real)
	}
}

// With no sibling to climb back to there is nothing better to do than re-exec
// what is there — refusing would turn a restart into an outage.
func TestRestartTargetKeepsOldWhenNothingToReturnTo(t *testing.T) {
	dir := t.TempDir()

	old := filepath.Join(dir, "rdio-scanner.old")
	if err := os.WriteFile(old, []byte("binary"), 0o755); err != nil {
		t.Fatalf("cannot create %s: %v", old, err)
	}

	if got := restartTarget(old); got != old {
		t.Fatalf("restartTarget(%q) = %q, want it unchanged", old, got)
	}
}

// A directory named like the sibling is not a binary to exec.
func TestRestartTargetIgnoresADirectorySibling(t *testing.T) {
	dir := t.TempDir()

	real := filepath.Join(dir, "rdio-scanner")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatalf("cannot create directory: %v", err)
	}

	old := real + ".old"
	if err := os.WriteFile(old, []byte("binary"), 0o755); err != nil {
		t.Fatalf("cannot create %s: %v", old, err)
	}

	if got := restartTarget(old); got != old {
		t.Fatalf("restartTarget(%q) = %q, want it unchanged when the sibling is a directory", old, got)
	}
}

// The ordinary case has to stay untouched, including a path that merely
// contains the word somewhere harmless.
func TestRestartTargetLeavesOrdinaryPathsAlone(t *testing.T) {
	for _, path := range []string{
		"/opt/rdio-scanner/rdio-scanner",
		"/opt/rdio-scanner.old/rdio-scanner",
		"/usr/local/bin/rdio-scanner",
	} {
		if got := restartTarget(path); got != path {
			t.Fatalf("restartTarget(%q) = %q, want it unchanged", path, got)
		}
	}
}

// The updater matches assets by substring, so anything published beside the
// binary under the same platform name matches too — and would be staged and
// swapped in as the server.
func TestAssetForPlatformSkipsSidecars(t *testing.T) {
	token := platformToken()

	release := githubRelease{Assets: []githubAsset{
		{Name: "rdio-scanner-" + token + "-v6.14.2.sha256"},
		{Name: "rdio-scanner-" + token + "-v6.14.2.sig"},
		{Name: "rdio-scanner-" + token + "-v6.14.2.zip"},
		{Name: "rdio-scanner-" + token + "-v6.14.2"},
	}}

	got := assetForPlatform(release)
	if got == nil {
		t.Fatal("no asset selected")
	}

	want := "rdio-scanner-" + token + "-v6.14.2"
	if got.Name != want {
		t.Fatalf("selected %q, want the bare binary %q", got.Name, want)
	}
}
