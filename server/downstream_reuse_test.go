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
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// Forwarding a call must hand its connection back when it is done. Each one
// used to be left open, so the next call dialled again and the old socket sat
// until the client timeout reaped it.
func TestDownstreamReusesItsConnection(t *testing.T) {
	var opened atomic.Int32

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Call imported successfully.\n"))
	}))
	server.Config.ConnState = func(conn net.Conn, state http.ConnState) {
		if state == http.StateNew {
			opened.Add(1)
		}
	}
	server.Start()
	defer server.Close()

	downstream := &Downstream{Url: server.URL, Apikey: "k", Systems: "*"}

	call := NewCall()
	call.Audio = bytes.Repeat([]byte{1}, 100)
	call.AudioName = "a.m4a"
	call.AudioType = "audio/mp4"
	call.DateTime = time.Now().UTC()
	call.System = 1
	call.Talkgroup = 100

	for i := 0; i < 8; i++ {
		if err := downstream.Send(call); err != nil {
			t.Fatalf("send %d failed: %v", i, err)
		}
	}

	if got := opened.Load(); got != 1 {
		t.Fatalf("eight forwarded calls opened %d connections, want 1", got)
	}
}
