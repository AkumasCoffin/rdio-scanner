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
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/dop251/goja"
)

// Sockets, so a plugin can be a source of calls rather than only a reaction to
// them.
//
// Everything else a plugin can reach is request-shaped: it answers an HTTP
// route, or it makes an outbound request. Anything that pushes data at a fixed
// port — a recorder, a control-channel decoder, a CAD feed — had no way in. It
// needed a separate program bridging to HTTP, which is a second thing to
// install and supervise for no reason.
//
// Messages arrive on the plugin's own event loop, like every other handler, so
// a plugin never deals with goroutines. What it does deal with is that a socket
// is unbounded and an interpreter is not: the read limit and the connection cap
// are what stop a busy or hostile peer turning into memory.

const (
	// pluginNetMaxConnections bounds concurrent accepted connections per
	// listener. A plugin serving a decoder wants a handful; anything reaching
	// this is either wrong or being probed.
	pluginNetMaxConnections = 64

	// pluginNetMaxMessage bounds one datagram or one read. Larger than any
	// control-channel message and far smaller than a plugin's heap.
	pluginNetMaxMessage = 1 << 20 // 1 MiB

	// pluginNetMaxListeners bounds how many sockets one plugin may hold open.
	pluginNetMaxListeners = 16

	// pluginNetDialTimeout is the default for a one-shot send.
	pluginNetDialTimeout = 10 * time.Second
)

// pluginListener is one socket a plugin opened.
type pluginListener struct {
	network string
	address string

	tcp *net.TCPListener
	udp *net.UDPConn

	mutex  sync.Mutex
	closed bool
}

func (listener *pluginListener) Close() {
	listener.mutex.Lock()
	defer listener.mutex.Unlock()

	if listener.closed {
		return
	}

	listener.closed = true

	if listener.tcp != nil {
		listener.tcp.Close()
	}
	if listener.udp != nil {
		listener.udp.Close()
	}
}

func (listener *pluginListener) isClosed() bool {
	listener.mutex.Lock()
	defer listener.mutex.Unlock()

	return listener.closed
}

func (rt *PluginRuntime) bindNet(vm *goja.Runtime, rdio *goja.Object, throw func(string, ...any)) {
	netObj := vm.NewObject()

	// listen(network, address, handler)
	//
	// The handler runs on the plugin's loop with {data, text, remote, reply}.
	// reply writes back on the same connection or to the same datagram sender,
	// so a request/response protocol needs nothing else.
	netObj.Set("listen", func(network string, address string, handler goja.Callable) goja.Value {
		if handler == nil {
			throw("net.listen requires a handler function")
		}

		rt.mutex.RLock()
		count := len(rt.listeners)
		rt.mutex.RUnlock()

		if count >= pluginNetMaxListeners {
			throw("net.listen: this plugin already holds %d listeners", pluginNetMaxListeners)
		}

		listener, err := rt.openListener(network, address, handler)
		if err != nil {
			throw("net.listen: %v", err)
		}

		rt.mutex.Lock()
		rt.listeners = append(rt.listeners, listener)
		rt.mutex.Unlock()

		handle := vm.NewObject()
		handle.Set("network", listener.network)
		// The resolved address, so a plugin that asked for port 0 can find out
		// what it actually got.
		handle.Set("address", listener.address)
		handle.Set("close", func() goja.Value {
			listener.Close()
			return goja.Undefined()
		})

		return handle
	})

	// send(network, address, data, options) — one write to something already
	// listening. Returns a promise, carrying the reply when one was asked for.
	netObj.Set("send", func(network string, address string, data goja.Value, options goja.Value) goja.Value {
		body, err := pluginBytes(data.Export())
		if err != nil {
			throw("net.send: %v", err)
		}

		timeout := pluginNetDialTimeout
		wantReply := false

		if options != nil && !goja.IsUndefined(options) && !goja.IsNull(options) {
			if m, ok := options.Export().(map[string]any); ok {
				if ms, ok := numberFromMap(m, "timeoutMs"); ok && ms > 0 {
					timeout = time.Duration(ms) * time.Millisecond
				}
				if want, ok := m["reply"].(bool); ok {
					wantReply = want
				}
			}
		}

		return rt.promiseFrom(vm, func() (any, error) {
			conn, err := net.DialTimeout(network, address, timeout)
			if err != nil {
				return nil, err
			}
			defer conn.Close()

			if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
				return nil, err
			}

			if _, err := conn.Write(body); err != nil {
				return nil, err
			}

			if !wantReply {
				return nil, nil
			}

			buffer := make([]byte, pluginNetMaxMessage)

			read, err := conn.Read(buffer)
			if err != nil && err != io.EOF {
				return nil, err
			}

			return buffer[:read], nil
		})
	})

	rdio.Set("net", netObj)
}

// openListener starts accepting on a socket and delivers what arrives onto the
// plugin's event loop.
func (rt *PluginRuntime) openListener(network string, address string, handler goja.Callable) (*pluginListener, error) {
	listener := &pluginListener{network: network, address: address}

	switch network {
	case "tcp", "tcp4", "tcp6":
		resolved, err := net.ResolveTCPAddr(network, address)
		if err != nil {
			return nil, err
		}

		tcp, err := net.ListenTCP(network, resolved)
		if err != nil {
			return nil, err
		}

		listener.tcp = tcp
		listener.address = tcp.Addr().String()

		go rt.acceptTcp(listener, handler)

	case "udp", "udp4", "udp6":
		resolved, err := net.ResolveUDPAddr(network, address)
		if err != nil {
			return nil, err
		}

		udp, err := net.ListenUDP(network, resolved)
		if err != nil {
			return nil, err
		}

		listener.udp = udp
		listener.address = udp.LocalAddr().String()

		go rt.readUdp(listener, handler)

	default:
		return nil, fmt.Errorf("unsupported network %q; use tcp or udp", network)
	}

	return listener, nil
}

func (rt *PluginRuntime) acceptTcp(listener *pluginListener, handler goja.Callable) {
	connections := make(chan struct{}, pluginNetMaxConnections)

	for {
		conn, err := listener.tcp.Accept()
		if err != nil {
			if listener.isClosed() {
				return
			}

			// A transient accept error must not quietly end the listener — a
			// plugin would then be waiting on a socket nothing is reading.
			rt.controller.Logs.LogEvent(LogLevelWarn, fmt.Sprintf(
				"plugin %s listener on %s: %v", rt.manifest.Id, listener.address, err,
			))

			continue
		}

		select {
		case connections <- struct{}{}:
		default:
			// At the cap. Refusing beats queueing: a plugin that cannot keep up
			// should shed rather than accumulate sockets nobody is reading.
			conn.Close()
			continue
		}

		go func(conn net.Conn) {
			defer func() {
				conn.Close()
				<-connections
			}()

			rt.serveConn(conn, handler)
		}(conn)
	}
}

func (rt *PluginRuntime) serveConn(conn net.Conn, handler goja.Callable) {
	buffer := make([]byte, pluginNetMaxMessage)

	for {
		read, err := conn.Read(buffer)

		if read > 0 {
			// Copied, because the buffer is reused for the next read and the
			// payload crosses onto another goroutine.
			payload := make([]byte, read)
			copy(payload, buffer[:read])

			rt.deliverNet(handler, payload, conn.RemoteAddr().String(), func(reply []byte) {
				conn.Write(reply)
			})
		}

		if err != nil {
			return
		}
	}
}

func (rt *PluginRuntime) readUdp(listener *pluginListener, handler goja.Callable) {
	buffer := make([]byte, pluginNetMaxMessage)

	for {
		read, remote, err := listener.udp.ReadFromUDP(buffer)

		if read > 0 {
			payload := make([]byte, read)
			copy(payload, buffer[:read])

			target := remote

			rt.deliverNet(handler, payload, remote.String(), func(reply []byte) {
				listener.udp.WriteToUDP(reply, target)
			})
		}

		if err != nil {
			if listener.isClosed() {
				return
			}

			rt.controller.Logs.LogEvent(LogLevelWarn, fmt.Sprintf(
				"plugin %s listener on %s: %v", rt.manifest.Id, listener.address, err,
			))

			return
		}
	}
}

// deliverNet hands one message to the plugin, on its own event loop.
//
// Through runOnLoop, so it obeys the same queue bound as everything else: a
// plugin slower than its socket sheds messages rather than growing a backlog
// until the process dies. The reply is written on the socket's goroutine rather
// than the loop, so a slow peer cannot stall the runtime.
func (rt *PluginRuntime) deliverNet(handler goja.Callable, payload []byte, remote string, reply func([]byte)) {
	rt.runOnLoop("net", func(vm *goja.Runtime) {
		stop := rt.armWatchdog(vm, "net", pluginCallTimeout)
		defer stop()

		message := vm.NewObject()
		message.Set("data", vm.NewArrayBuffer(payload))
		// Both forms, because a plugin reading a line protocol should not have
		// to decode a buffer, and one reading a binary frame must not be handed
		// a string that mangles half its bytes.
		message.Set("text", string(payload))
		message.Set("remote", remote)
		message.Set("reply", func(data goja.Value) goja.Value {
			body, err := pluginBytes(data.Export())
			if err != nil {
				panic(vm.NewGoError(fmt.Errorf("reply: %v", err)))
			}

			go reply(body)

			return goja.Undefined()
		})

		if _, err := handler(goja.Undefined(), message); err != nil {
			rt.logCallError("net", err)
		}
	})
}

// closeListeners shuts every socket this plugin opened, so a disabled or
// uninstalled plugin does not leave a port bound.
func (rt *PluginRuntime) closeListeners() {
	rt.mutex.Lock()
	listeners := append([]*pluginListener{}, rt.listeners...)
	rt.listeners = nil
	rt.mutex.Unlock()

	for _, listener := range listeners {
		listener.Close()
	}
}
