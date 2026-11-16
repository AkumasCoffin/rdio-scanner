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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/gorilla/websocket"
)

// An admin with its socket loop running and one valid session, served over a
// real listener so the tests speak to it the way a browser does.
func newAdminSocketFixture(t *testing.T) (admin *Admin, token string, dial func() *websocket.Conn) {
	t.Helper()

	admin = NewAdmin(&Controller{Options: &Options{secret: "test-secret"}})

	go admin.serveSockets()
	t.Cleanup(func() { close(admin.Broadcast) })

	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{ID: "test"}).SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	admin.Tokens = append(admin.Tokens, signed)

	server := httptest.NewServer(http.HandlerFunc(admin.ConfigHandler))
	t.Cleanup(server.Close)

	dial = func() *websocket.Conn {
		t.Helper()

		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
		if err != nil {
			t.Fatalf("cannot open the admin socket: %v", err)
		}
		t.Cleanup(func() { conn.Close() })

		return conn
	}

	return admin, signed, dial
}

// broadcast pushes one payload through the loop and fails the test if the loop
// does not take it, which is what a wedged loop looks like from outside.
func broadcastToAdmins(t *testing.T, admin *Admin, payload string) {
	t.Helper()

	data := []byte(payload)

	select {
	case admin.Broadcast <- &data:
	case <-time.After(5 * time.Second):
		t.Fatalf("the admin socket loop did not accept a broadcast of %q; it is stuck", payload)
	}
}

// The configuration that goes down this socket holds every access code, every
// API key and the downstream credentials. It used to be sent to any connection
// that had opened the socket, because the connection was added to the
// broadcast list first and asked for its token afterwards — so one that never
// sent a token simply stayed on the list.
func TestAdminSocketSendsNothingBeforeAToken(t *testing.T) {
	admin, _, dial := newAdminSocketFixture(t)

	silent := dial()

	// Give the server time to do whatever it does with a new connection.
	time.Sleep(200 * time.Millisecond)

	broadcastToAdmins(t, admin, `{"config":"secret"}`)

	silent.SetReadDeadline(time.Now().Add(time.Second))
	if _, data, err := silent.ReadMessage(); err == nil {
		t.Fatalf("a connection that never authenticated was sent %q", data)
	}
}

func TestAdminSocketRefusesAWrongToken(t *testing.T) {
	admin, _, dial := newAdminSocketFixture(t)

	conn := dial()
	if err := conn.WriteMessage(websocket.TextMessage, []byte("not-a-token")); err != nil {
		t.Fatal(err)
	}

	time.Sleep(200 * time.Millisecond)

	broadcastToAdmins(t, admin, `{"config":"secret"}`)

	conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, data, err := conn.ReadMessage(); err == nil {
		t.Fatalf("a connection with a wrong token was sent %q", data)
	}
}

func TestAdminSocketDeliversToAnAuthenticatedSession(t *testing.T) {
	admin, token, dial := newAdminSocketFixture(t)

	conn := dial()
	if err := conn.WriteMessage(websocket.TextMessage, []byte(token)); err != nil {
		t.Fatal(err)
	}

	// Registration is asynchronous; keep broadcasting until it lands.
	deadline := time.Now().Add(5 * time.Second)
	for {
		broadcastToAdmins(t, admin, `{"config":"ok"}`)

		conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		_, data, err := conn.ReadMessage()
		if err == nil {
			if string(data) != `{"config":"ok"}` {
				t.Fatalf("received %q", data)
			}
			return
		}

		if time.Now().After(deadline) {
			t.Fatalf("an authenticated session never received a broadcast: %v", err)
		}

		// A read deadline leaves a gorilla connection unusable, so each
		// attempt needs its own.
		conn = dial()
		if err := conn.WriteMessage(websocket.TextMessage, []byte(token)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// A connection whose write fails has to be dropped by the loop itself. It used
// to hand the connection to its own unregister channel instead — a channel
// only the loop reads — and so stopped for good, in the middle of a
// broadcast, taking every later config push, user add and plugin install
// with it. The window is a tab closing while a push is on its way out: the
// write fails before the reader's own unregister has been taken.
func TestAdminSocketLoopSurvivesAFailedWrite(t *testing.T) {
	admin, _, _ := newAdminSocketFixture(t)

	// A server-side connection that is already closed, so the only thing
	// that will ever notice is the loop's write.
	accepted := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		accepted <- conn
	}))
	defer server.Close()

	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	dead := <-accepted
	dead.Close()

	admin.Register <- dead

	broadcastToAdmins(t, admin, `{"config":"first"}`)
	broadcastToAdmins(t, admin, `{"config":"second"}`)
}
