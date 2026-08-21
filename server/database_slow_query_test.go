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
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
	"time"
)

// A statement that overruns has to name itself. Every diagnosis of the freezes
// so far has been inference from which caller happened to be stuck, which
// twice pointed at the wrong thing; the statement and its duration are the
// fact that ends the argument.
func TestSlowQueryReportsTheStatement(t *testing.T) {
	var captured bytes.Buffer

	log.SetOutput(&captured)
	defer log.SetOutput(os.Stderr)

	slowQueryThrottle = NewLogThrottle(3, time.Minute)

	traceSlowQuery("select id from `rdioScannerCalls` where system = 4", time.Now().Add(-3*time.Second))

	out := captured.String()

	if !strings.Contains(out, "slow query") {
		t.Fatalf("nothing reported for a statement over the threshold; got %q", out)
	}

	if !strings.Contains(out, "rdioScannerCalls") {
		t.Errorf("report does not name the statement: %q", out)
	}
}

// A quick statement is the overwhelming majority of them and must stay silent,
// or the report is noise and gets ignored — which is the same as not having it.
func TestFastQueryIsNotReported(t *testing.T) {
	var captured bytes.Buffer

	log.SetOutput(&captured)
	defer log.SetOutput(os.Stderr)

	slowQueryThrottle = NewLogThrottle(3, time.Minute)

	traceSlowQuery("select 1", time.Now())

	if out := captured.String(); out != "" {
		t.Errorf("a fast statement was reported: %q", out)
	}
}

// The log table is written through the same Exec being timed, so reporting it
// would have a slow log insert report itself, and that report would be a slow
// log insert.
func TestTheLogTableIsExemptFromSlowQueryReports(t *testing.T) {
	var captured bytes.Buffer

	log.SetOutput(&captured)
	defer log.SetOutput(os.Stderr)

	slowQueryThrottle = NewLogThrottle(3, time.Minute)

	traceSlowQuery("insert into `rdioScannerLogs` (`dateTime`) values (?)", time.Now().Add(-3*time.Second))

	if out := captured.String(); out != "" {
		t.Errorf("the log table's own insert was reported, which recurses: %q", out)
	}
}
