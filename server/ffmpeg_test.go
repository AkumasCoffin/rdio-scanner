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
	"strings"
	"testing"
	"unicode/utf8"
)

func TestLinuxFFmpegInstallCommand(t *testing.T) {
	for _, tc := range []struct {
		name string
		ids  []string
		want string
	}{
		{"debian", []string{"debian"}, "apt install ffmpeg"},
		{"ubuntu", []string{"ubuntu", "debian"}, "apt install ffmpeg"},
		{"arch", []string{"arch"}, "pacman -S ffmpeg"},
		{"alpine", []string{"alpine"}, "apk add ffmpeg"},
		{"opensuse tumbleweed", []string{"opensuse-tumbleweed", "suse"}, "zypper install ffmpeg"},

		// An unrecognised distro should still get a usable answer via the
		// family it declares in ID_LIKE.
		{"unknown deriving from debian", []string{"somedistro", "debian"}, "apt install ffmpeg"},

		// The distro's own ID must win over the family it declares, otherwise
		// a Debian-compatible Arch derivative gets the wrong manager.
		{"own id beats id_like", []string{"arch", "debian"}, "pacman -S ffmpeg"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := linuxFFmpegInstallCommand(tc.ids); !strings.Contains(got, tc.want) {
				t.Errorf("ids %v: expected a command containing %q, got %q", tc.ids, tc.want, got)
			}
		})
	}
}

// An unknown or absent /etc/os-release must not produce a command that doesn't
// exist — naming the package is more honest than guessing.
func TestLinuxFFmpegInstallCommandFallsBack(t *testing.T) {
	for _, ids := range [][]string{nil, {}, {"plan9"}, {"weird", "alsoweird"}} {
		got := linuxFFmpegInstallCommand(ids)

		if !strings.Contains(got, "ffmpeg") {
			t.Errorf("ids %v: fallback should still name ffmpeg, got %q", ids, got)
		}
		for _, mgr := range []string{"apt", "dnf", "pacman", "apk", "zypper"} {
			if strings.Contains(got, mgr) {
				t.Errorf("ids %v: fallback should not guess %q, got %q", ids, mgr, got)
			}
		}
	}
}

// The message is written to rdioScannerLogs.message, so it has to fit the
// column on its own — a warning clipped mid-command is useless. Checked against
// every platform's command rather than only the one this test binary was built
// for, since releases target linux and windows too.
func TestUnavailableMessageFitsLogColumn(t *testing.T) {
	commands := []string{
		"brew install ffmpeg",
		"sudo pkg install ffmpeg",
		`winget install Gyan.FFmpeg  (then reopen your terminal so PATH is picked up)`,
		"your platform's ffmpeg package",
	}
	for _, ids := range [][]string{
		nil,
		{"debian"}, {"fedora"}, {"rhel"}, {"arch"}, {"alpine"},
		{"opensuse-leap"}, {"void"}, {"gentoo"},
	} {
		commands = append(commands, linuxFFmpegInstallCommand(ids))
	}

	for _, command := range commands {
		message := ffmpegUnavailableMessage(command)

		if n := utf8.RuneCountInString(message); n > logMessageMaxLen {
			t.Errorf("message is %d chars, exceeds the %d-char log column: %q", n, logMessageMaxLen, message)
		}
		if !strings.Contains(message, "ffmpeg") {
			t.Errorf("message should name ffmpeg: %q", message)
		}
	}

	// And the real one for this platform, wording included.
	if got := (&FFMpeg{}).UnavailableMessage(); !strings.Contains(got, "Install it with:") {
		t.Errorf("message should carry an install command: %q", got)
	}
}

func TestAvailableReflectsProbe(t *testing.T) {
	if (&FFMpeg{available: false}).Available() {
		t.Error("Available() should be false when the probe found no binary")
	}
	if !(&FFMpeg{available: true}).Available() {
		t.Error("Available() should be true when the probe found a binary")
	}
}
