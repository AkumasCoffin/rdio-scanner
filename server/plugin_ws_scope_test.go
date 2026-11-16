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
	"testing"

	"github.com/dop251/goja"
)

// A plugin answering one client has to be able to respect that client's
// access code. Naming the client used to bypass the scope check entirely.
func TestPluginReplyToAClientHonoursItsAccess(t *testing.T) {
	scoped := &Access{Id: uint(1), Code: "one", Ident: "system one only", Systems: []any{
		map[string]any{"id": float64(1), "talkgroups": "*"},
	}}

	controller := &Controller{
		Accesses: NewAccesses(),
		Clients:  NewClients(),
		Logs:     &Logs{},
	}
	controller.Accesses.List = []*Access{scoped}

	rt := &PluginRuntime{controller: controller, manifest: &PluginManifest{Id: "replier"}}

	client := &Client{Access: scoped, Send: make(chan *Message, 4)}
	vm := goja.New()

	emit := func(system int, talkgroup int) bool {
		filter := vm.ToValue(map[string]any{
			"client":    &pluginClientHandle{client: client},
			"system":    system,
			"talkgroup": talkgroup,
		})

		rt.emitWs(filter, "TRX", map[string]any{"id": 1, "transcript": "text"})

		select {
		case <-client.Send:
			return true
		default:
			return false
		}
	}

	if !emit(1, 100) {
		t.Error("a reply about a call the client may hear was withheld")
	}
	if emit(2, 200) {
		t.Error("a reply about a call on a system the client has no access to was delivered")
	}

	// With no system and talkgroup given there is nothing to test against, and
	// the reply goes out as it always has.
	rt.emitWs(vm.ToValue(map[string]any{"client": &pluginClientHandle{client: client}}), "PONG", nil)
	select {
	case <-client.Send:
	default:
		t.Error("an unscoped reply to a client was withheld")
	}

	// On a server with no access codes everyone may hear everything.
	controller.Accesses.List = nil
	if !emit(2, 200) {
		t.Error("an open server withheld a reply")
	}
}
