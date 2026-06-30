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

//go:build !windows

package main

import (
	"os"
	"syscall"
)

// restartSelf replaces the running process image with the freshly-swapped
// binary via execve(2). The PID is preserved, so a systemd / Docker supervisor
// sees the same process and is happy, and it works even with no supervisor.
func restartSelf(exe string) {
	_ = syscall.Exec(exe, os.Args, os.Environ())
	// If exec failed for some reason, exit so a supervisor can relaunch.
	os.Exit(0)
}
