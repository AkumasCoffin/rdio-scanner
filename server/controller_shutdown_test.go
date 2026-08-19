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

// Ctrl+C used to do nothing on a server that had been up a while: shutdown
// called sql.DB.Close, which waits for every in-flight query, and on a busy
// database there is always one. Cleanup is now given a deadline and the
// process leaves regardless.
func TestShutdownGivesUpOnWorkThatOverrunsItsDeadline(t *testing.T) {
	started := time.Now()

	finished := runWithTimeout(func() {
		// Stands in for a close waiting behind a long query.
		time.Sleep(2 * time.Second)
	}, 50*time.Millisecond)

	elapsed := time.Since(started)

	if finished {
		t.Error("reported success for work that had not finished")
	}

	if elapsed > time.Second {
		t.Errorf("waited %s for a 50ms deadline — shutdown is still blocked by the work", elapsed)
	}
}

// The deadline must not truncate cleanup that is behaving itself: plugins get
// their shutdown handlers, and the database gets closed properly.
func TestShutdownWaitsForCleanupThatFinishesInTime(t *testing.T) {
	done := false

	finished := runWithTimeout(func() {
		time.Sleep(10 * time.Millisecond)
		done = true
	}, 5*time.Second)

	if !finished {
		t.Error("reported a timeout for work that finished well inside it")
	}

	if !done {
		t.Error("returned before the work had run")
	}
}
