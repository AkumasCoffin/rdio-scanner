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
	"encoding/base64"
	"net/http/httptest"
	"testing"
	"time"
)

// A controller with access codes configured and nothing else, which is all the
// gate in ProcessMessage looks at.
func newAccessGateController(t *testing.T, accesses ...*Access) *Controller {
	t.Helper()

	controller := &Controller{
		Accesses: NewAccesses(),
		Clients:  NewClients(),
		Groups:   NewGroups(),
		Logs:     &Logs{},
		Options:  NewOptions(),
		Plugins:  NewPlugins(),
		Systems:  NewSystems(),
		Tags:     NewTags(),
	}
	controller.PluginDispatch = NewPluginDispatch(controller)
	controller.Accesses.List = accesses

	return controller
}

func newAccessGateClient(controller *Controller) *Client {
	return &Client{
		Access:     &Access{},
		Controller: controller,
		Send:       make(chan *Message, 16),
		request:    httptest.NewRequest("GET", "/", nil),
	}
}

// sendAndRead runs one message through the controller and returns the command
// of whatever came back, or "" if nothing did.
func sendAndRead(t *testing.T, controller *Controller, client *Client, command string, payload any) string {
	t.Helper()

	if err := controller.ProcessMessage(client, &Message{Command: command, Payload: payload}); err != nil {
		t.Fatalf("%s failed: %v", command, err)
	}

	select {
	case reply := <-client.Send:
		command, _ := reply.Command.(string)
		return command
	default:
		return ""
	}
}

func pin(code string) string {
	return base64.StdEncoding.EncodeToString([]byte(code))
}

// The server answered an expired code with XPR and an over-limit one with MAX,
// and both were advisory: the access had already been attached to the
// connection before either was checked, and the gate only asks whether an
// access is attached. A client that ignored the refusal and asked for the
// config got it, along with the live feed, search and audio. The stock webapp
// obeys; anything else did not have to.
func TestAnExpiredAccessCodeDoesNotOpenTheGate(t *testing.T) {
	controller := newAccessGateController(t, &Access{
		Id:         uint(1),
		Code:       "old",
		Ident:      "lapsed",
		Systems:    "*",
		Expiration: time.Now().Add(-time.Hour),
	})
	client := newAccessGateClient(controller)

	if got := sendAndRead(t, controller, client, MessageCommandPin, pin("old")); got != MessageCommandExpired {
		t.Fatalf("an expired code was answered with %q, want %q", got, MessageCommandExpired)
	}

	if got := sendAndRead(t, controller, client, MessageCommandConfig, nil); got != MessageCommandPin {
		t.Fatalf("after an expired code, asking for the config was answered with %q, want another %q", got, MessageCommandPin)
	}

	if client.Access.HasAccess(&Call{System: 1, Talkgroup: 1}) {
		t.Fatal("a connection refused for an expired code can still receive calls")
	}
}

func TestAnOverLimitAccessCodeDoesNotOpenTheGate(t *testing.T) {
	controller := newAccessGateController(t, &Access{
		Id:      uint(1),
		Code:    "shared",
		Ident:   "team",
		Systems: "*",
		Limit:   uint(1),
	})

	// One listener already connected on this code.
	seated := newAccessGateClient(controller)
	seated.Access = controller.Accesses.List[0]
	controller.Clients.Map[seated] = true

	client := newAccessGateClient(controller)

	if got := sendAndRead(t, controller, client, MessageCommandPin, pin("shared")); got != MessageCommandMax {
		t.Fatalf("a second connection on a limit of one was answered with %q, want %q", got, MessageCommandMax)
	}

	if got := sendAndRead(t, controller, client, MessageCommandConfig, nil); got != MessageCommandPin {
		t.Fatalf("after being refused for the limit, asking for the config was answered with %q, want %q", got, MessageCommandPin)
	}
}

// The limit is how many may be connected, counted the same way whether the
// connection asking is new or is one already seated answering a fresh prompt.
// It used to count a seated connection against itself: after any config
// change every listener is asked for its code again, each one found the
// count over the limit, and all of them were refused at once.
func TestTheConnectionLimitDoesNotCountTheOneAsking(t *testing.T) {
	controller := newAccessGateController(t, &Access{
		Id:      uint(1),
		Code:    "shared",
		Ident:   "team",
		Systems: "*",
		Limit:   uint(2),
	})

	first := newAccessGateClient(controller)
	second := newAccessGateClient(controller)

	for _, client := range []*Client{first, second} {
		if got := sendAndRead(t, controller, client, MessageCommandPin, pin("shared")); got != MessageCommandConfig {
			t.Fatalf("a connection within the limit was answered with %q, want %q", got, MessageCommandConfig)
		}
		controller.Clients.Map[client] = true
	}

	// A config change prompts everyone again.
	for _, client := range []*Client{first, second} {
		if got := sendAndRead(t, controller, client, MessageCommandPin, pin("shared")); got != MessageCommandConfig {
			t.Fatalf("a seated connection answering a fresh prompt was answered with %q, want %q", got, MessageCommandConfig)
		}
	}

	third := newAccessGateClient(controller)
	if got := sendAndRead(t, controller, third, MessageCommandPin, pin("shared")); got != MessageCommandMax {
		t.Fatalf("a third connection on a limit of two was answered with %q, want %q", got, MessageCommandMax)
	}
}

// A code that has been deleted has to stop working for the connection that was
// using it, at the point that connection is asked for it again.
func TestARevokedCodeIsDroppedWhenAskedForAgain(t *testing.T) {
	controller := newAccessGateController(t,
		&Access{Id: uint(1), Code: "keep", Ident: "a", Systems: "*"},
		&Access{Id: uint(2), Code: "gone", Ident: "b", Systems: "*"},
	)
	client := newAccessGateClient(controller)

	if got := sendAndRead(t, controller, client, MessageCommandPin, pin("gone")); got != MessageCommandConfig {
		t.Fatalf("a valid code was answered with %q", got)
	}

	// The admin deletes the code; the server prompts, the client answers with
	// the code it has.
	controller.Accesses.List = controller.Accesses.List[:1]

	if got := sendAndRead(t, controller, client, MessageCommandPin, pin("gone")); got != MessageCommandPin {
		t.Fatalf("a deleted code was answered with %q, want %q", got, MessageCommandPin)
	}

	if client.Access.HasAccess(&Call{System: 1, Talkgroup: 1}) {
		t.Fatal("a connection whose code was deleted can still receive calls")
	}
}
