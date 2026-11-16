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
	"errors"
	"testing"
	"time"
)

func watchOn(t *testing.T) (*Controller, *Dirwatches) {
	t.Helper()

	controller := &Controller{Logs: &Logs{}}

	dirwatches := NewDirwatches()
	dirwatches.FromMap([]any{map[string]any{"directory": t.TempDir(), "type": "default", "systemId": float64(1), "talkgroupId": float64(1)}})

	if len(dirwatches.List) != 1 {
		t.Fatalf("the watch was not parsed: %d entries", len(dirwatches.List))
	}

	return controller, dirwatches
}

// A config save stops every watch and starts them again at the end. The admin
// form only sends the sections that changed, so most saves carry no dirWatch
// section at all — and the stop used to throw the list away, leaving the
// start with nothing. Saving one option stopped all directory ingestion until
// the server was restarted.
func TestDirwatchesComeBackAfterAStopThatIsNotFollowedByANewList(t *testing.T) {
	controller, dirwatches := watchOn(t)

	dirwatches.Start(controller)
	defer dirwatches.Stop()

	if dirwatches.List[0].watcher == nil {
		t.Fatal("the watch did not start")
	}

	// What a save without a dirWatch section does.
	dirwatches.Stop()

	if len(dirwatches.List) != 1 {
		t.Fatalf("stopping the watches left %d configured, want 1", len(dirwatches.List))
	}

	dirwatches.Start(controller)

	if dirwatches.List[0].watcher == nil {
		t.Fatal("the watch did not come back after the save")
	}
}

// A burst of files overflows the kernel's change buffer and the watcher says
// so on its error channel. That is a report, not the end of the watcher —
// but the loop took it as one and returned, and nothing watched the directory
// again until a restart.
func TestADirwatchSurvivesAWatcherError(t *testing.T) {
	controller, dirwatches := watchOn(t)

	dirwatches.Start(controller)
	defer dirwatches.Stop()

	watch := dirwatches.List[0]
	watcher := watch.watcher

	send := func(n int) {
		select {
		case watcher.Errors <- errors.New("overflow"):
		case <-time.After(5 * time.Second):
			t.Fatalf("the watch loop was not there to take error %d; it stopped after the one before", n)
		}
	}

	// Each is only received if the loop is still running after the last.
	send(1)
	send(2)
	send(3)

	if watch.watcher == nil {
		t.Fatal("the watch was torn down by an error")
	}
}
