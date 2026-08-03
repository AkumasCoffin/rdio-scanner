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
	"strings"
	"testing"
	"time"

	"github.com/dop251/goja"
	"github.com/dop251/goja_nodejs/eventloop"
)

func watchdogRuntime() *PluginRuntime {
	return &PluginRuntime{manifest: &PluginManifest{Id: "watched"}}
}

// A plugin that will not stop has to be stoppable. This is the guarantee the
// whole timeout story rests on.
func TestWatchdogInterruptsARunawayHandler(t *testing.T) {
	rt := watchdogRuntime()
	vm := goja.New()

	stop := rt.armWatchdog(vm, "call.store", 50*time.Millisecond)

	started := time.Now()
	_, err := vm.RunString(`while (true) {}`)
	elapsed := time.Since(started)

	stop()

	if err == nil {
		t.Fatal("an infinite loop ran to completion")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("the interrupt took %v to land", elapsed)
	}
	if !strings.Contains(err.Error(), "call.store") {
		t.Errorf("the error does not name where it happened: %v", err)
	}
}

// Disarming must not leave the interrupt raised for whatever runs next.
//
// time.Stop does not wait for a timer already running, so the naive
// stop-then-clear had a window where the clear went first and the interrupt
// landed after it — with nothing to receive it. The flag stayed set and the
// next, unrelated job died immediately, reporting a timeout in the point the
// previous one had been running.
func TestDisarmingNeverPoisonsTheNextJob(t *testing.T) {
	for i := 0; i < 200; i++ {
		rt := watchdogRuntime()
		vm := goja.New()

		// A deadline of zero maximises the chance the timer fires exactly as
		// the disarm runs, which is the race being closed.
		stop := rt.armWatchdog(vm, "call.emit", 0)
		stop()

		if _, err := vm.RunString(`1 + 1`); err != nil {
			t.Fatalf("iteration %d: a later job inherited an interrupt it had nothing to do with: %v", i, err)
		}
	}
}

// Disarming twice is normal: the loop disarms when the handler settles, and the
// waiting caller disarms if it gave up first. Whichever runs second must not
// clear an interrupt belonging to something else.
func TestDisarmingIsIdempotent(t *testing.T) {
	rt := watchdogRuntime()
	vm := goja.New()

	stop := rt.armWatchdog(vm, "call.store", time.Hour)
	stop()
	stop()

	// An interrupt raised by something else afterwards must survive.
	vm.Interrupt("someone else's interrupt")
	stop()

	if _, err := vm.RunString(`1 + 1`); err == nil {
		t.Fatal("a redundant disarm cleared an interrupt raised by something else")
	}
}

// A handler that returns a promise is not finished when its synchronous part
// returns, and the watchdog used to be disarmed there by a defer. Anything the
// plugin went on to do while that promise was pending therefore ran completely
// unwatched: the per-point timeouts bounded how long core waited, never how
// long the plugin ran.
//
// The discriminating case is work on the loop while the promise is pending. A
// promise that merely never settles is not enough — the old code disarmed
// before the deadline, so nothing was interrupted and nothing looked wrong.
func TestCallSyncWatchesWorkWhileAPromiseIsPending(t *testing.T) {
	rt := watchdogRuntime()
	rt.controller = &Controller{Logs: &Logs{}}

	rt.loop = eventloop.NewEventLoop(eventloop.EnableConsole(false))
	rt.loop.Start()
	defer rt.loop.Stop()

	var handler goja.Callable

	// Returns a promise nobody settles, and leaves a spinning job behind it —
	// the shape of a handler that awaited something and kept working.
	if err := rt.runOnLoopAndWait(5*time.Second, func(vm *goja.Runtime) {
		rt.vm = vm

		value, err := vm.RunString(`
			(function () {
				setTimeout(function () { while (true) {} }, 10)
				return new Promise(function () {})
			})
		`)
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

	if _, err := rt.CallSync("call.store", 200*time.Millisecond, &gojaCallable{fn: handler}); err == nil {
		t.Fatal("a handler whose promise never settles was reported as succeeding")
	}

	// The spin must have been interrupted, which is only true if the watchdog
	// was still armed while the promise was pending. Left unwatched it runs
	// forever and this never gets a turn.
	usable := make(chan error, 1)

	if err := rt.runOnLoopAndWait(5*time.Second, func(vm *goja.Runtime) {
		_, err := vm.RunString(`1 + 1`)
		usable <- err
	}); err != nil {
		t.Fatalf("the loop never came back: the plugin's work was never interrupted (%v)", err)
	}

	if err := <-usable; err != nil {
		t.Fatalf("the next job on the loop inherited a stranded interrupt: %v", err)
	}
}
