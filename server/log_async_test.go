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
	"testing"
	"time"
)

// Logging sits on every path there is — accepting a listener, ingesting a
// call, running a plugin handler. When it wrote its row inline, a database
// that stopped answering stopped all of those with it: the server went from
// "slow dashboard" to "will not accept listeners", because the connect path
// was parked in an INSERT.
//
// The caller must hand the line off and carry on, however far behind the
// database is.
func TestLogEventDoesNotWaitForTheDatabase(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	logs := NewLogs()
	logs.setDatabase(db)

	// Fill the queue past its capacity without draining it, which is the state
	// a stalled database produces.
	for i := 0; i < logWriteBuffer*2; i++ {
		started := time.Now()
		if err := logs.LogEvent(LogLevelInfo, "load"); err != nil {
			t.Fatalf("LogEvent returned an error: %v", err)
		}

		// Per call, because an average would hide one pathological wait among
		// thousands of fast ones — and one is all it takes to stall a connect.
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Fatalf("LogEvent blocked for %s on call %d; callers are still waiting on the database", elapsed, i)
		}
	}
}

// Shedding is the right failure when the database cannot keep up, but it has
// to be visible: the line is still on stdout, and the count of what was lost
// is reported rather than quietly swallowed.
func TestLogEventCountsWhatItSheds(t *testing.T) {
	logs := NewLogs()

	// A database that is set but whose writer never runs, so nothing drains.
	logs.database = &Database{}

	for i := 0; i < logWriteBuffer+64; i++ {
		logs.LogEvent(LogLevelInfo, "load")
	}

	if dropped := logs.dropped.Load(); dropped == 0 {
		t.Error("the queue overflowed without recording a single dropped line")
	}
}
