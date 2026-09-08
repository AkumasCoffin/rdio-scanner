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
	"log"
	"os"
	"syscall"
)

// restartSelf replaces the running process image with the freshly-swapped
// binary via execve(2). The PID is preserved, so a systemd / Docker supervisor
// sees the same process and is happy, and it works even with no supervisor.
func restartSelf(exe string) {
	err := syscall.Exec(exe, os.Args, os.Environ())

	// Only reachable when exec failed — on success this process image is gone.
	//
	// Exiting 1 rather than 0 is the difference between a supervisor bringing
	// the server back and a service that quietly stays dead: systemd's
	// Restart=on-failure treats a zero exit as a job well done, so the old
	// code turned a failed restart into an outage with nothing in the journal
	// to explain it.
	log.Printf("restart: cannot execute %s (%v); exiting so a supervisor can relaunch", exe, err)
	os.Exit(1)
}
