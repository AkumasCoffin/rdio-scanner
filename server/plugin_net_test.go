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
	"net"
	"testing"
	"time"

	"github.com/dop251/goja"
	"github.com/dop251/goja_nodejs/eventloop"
)

// A plugin acting as an ingest source needs a socket. Everything else it can
// reach is request-shaped — it answers an HTTP route or makes a request — so
// anything that pushes data at a fixed port needed a separate program bridging
// to HTTP.

func netRuntime(t *testing.T) *PluginRuntime {
	t.Helper()

	rt := &PluginRuntime{
		manifest:        &PluginManifest{Id: "listener"},
		controller:      &Controller{Logs: &Logs{}},
		loopLogThrottle: NewLogThrottle(1, time.Minute),
	}

	rt.loop = eventloop.NewEventLoop(eventloop.EnableConsole(false))
	rt.loop.Start()

	t.Cleanup(func() {
		rt.closeListeners()
		rt.loop.Stop()
	})

	return rt
}

func TestAPluginCanReceiveOnUdp(t *testing.T) {
	rt := netRuntime(t)

	received := make(chan string, 4)

	var handler goja.Callable

	if err := rt.runOnLoopAndWait(5*time.Second, func(vm *goja.Runtime) {
		rt.vm = vm

		if err := vm.Set("record", func(text string) { received <- text }); err != nil {
			t.Fatal(err)
		}

		value, err := vm.RunString(`(function (msg) { record(msg.text + "|" + (msg.data.byteLength)) })`)
		if err != nil {
			t.Fatal(err)
		}

		fn, ok := goja.AssertFunction(value)
		if !ok {
			t.Fatal("not a function")
		}
		handler = fn
	}); err != nil {
		t.Fatal(err)
	}

	// Port 0, so the test never collides with anything already bound.
	listener, err := rt.openListener("udp", "127.0.0.1:0", handler)
	if err != nil {
		t.Fatalf("cannot listen: %v", err)
	}
	rt.mutex.Lock()
	rt.listeners = append(rt.listeners, listener)
	rt.mutex.Unlock()

	conn, err := net.Dial("udp", listener.address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-received:
		// Both forms are offered: text for a line protocol, bytes for a frame
		// that a JavaScript string would mangle.
		if got != "hello|5" {
			t.Fatalf("the plugin received %q", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("nothing reached the plugin")
	}
}

func TestAPluginCanAnswerOnTcp(t *testing.T) {
	rt := netRuntime(t)

	var handler goja.Callable

	if err := rt.runOnLoopAndWait(5*time.Second, func(vm *goja.Runtime) {
		rt.vm = vm

		value, err := vm.RunString(`(function (msg) { msg.reply("got " + msg.text) })`)
		if err != nil {
			t.Fatal(err)
		}

		fn, ok := goja.AssertFunction(value)
		if !ok {
			t.Fatal("not a function")
		}
		handler = fn
	}); err != nil {
		t.Fatal(err)
	}

	listener, err := rt.openListener("tcp", "127.0.0.1:0", handler)
	if err != nil {
		t.Fatalf("cannot listen: %v", err)
	}
	rt.mutex.Lock()
	rt.listeners = append(rt.listeners, listener)
	rt.mutex.Unlock()

	conn, err := net.Dial("tcp", listener.address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}

	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}

	buffer := make([]byte, 64)

	read, err := conn.Read(buffer)
	if err != nil {
		t.Fatalf("no reply came back: %v", err)
	}

	if string(buffer[:read]) != "got ping" {
		t.Fatalf("the reply was %q", buffer[:read])
	}
}

// A disabled or uninstalled plugin must not leave a port bound — otherwise
// re-enabling it fails with "address already in use" and the operator has no
// idea why.
func TestStoppingAPluginReleasesItsPorts(t *testing.T) {
	rt := netRuntime(t)

	var handler goja.Callable

	if err := rt.runOnLoopAndWait(5*time.Second, func(vm *goja.Runtime) {
		rt.vm = vm
		value, _ := vm.RunString(`(function () {})`)
		handler, _ = goja.AssertFunction(value)
	}); err != nil {
		t.Fatal(err)
	}

	listener, err := rt.openListener("tcp", "127.0.0.1:0", handler)
	if err != nil {
		t.Fatal(err)
	}

	rt.mutex.Lock()
	rt.listeners = append(rt.listeners, listener)
	rt.mutex.Unlock()

	address := listener.address

	rt.closeListeners()

	// The port is free: binding it again succeeds.
	reopened, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("the port was still bound after the plugin stopped: %v", err)
	}
	reopened.Close()
}

func TestUnsupportedNetworksAreRefused(t *testing.T) {
	rt := netRuntime(t)

	if _, err := rt.openListener("carrier-pigeon", "127.0.0.1:0", nil); err == nil {
		t.Fatal("an unsupported network was accepted")
	}
}
