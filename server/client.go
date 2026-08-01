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
	"errors"
	"fmt"
	"log"
	"net/http"
	"path"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

type Client struct {
	Access     *Access
	AuthCount  int
	Controller *Controller
	Conn       *websocket.Conn
	Send       chan *Message
	Systems    []System
	GroupsMap  GroupsMap
	TagsMap    TagsMap
	Livefeed   *Livefeed
	SystemsMap SystemsMap
	request    *http.Request
	// Overlay is true for the /stream OBS overlay page. It still receives
	// calls, but is excluded from the listener count so a user running both
	// the main page and /stream isn't counted twice.
	Overlay bool
	// closed is set by the writer goroutine as it exits, so enqueue stops
	// handing messages to a channel nobody is draining any more.
	closed atomic.Bool
	// dropped counts messages shed by enqueue because the send buffer was
	// full. Reported once on disconnect — never logged from the emit path,
	// which runs under Clients.mutex.
	dropped atomic.Uint64
}

// enqueue hands a message to this client's writer goroutine. It NEVER blocks:
// if the send buffer is full (a stalled socket, or a writer goroutine that has
// already exited) the message is dropped and false returned.
//
// Blocking here used to deadlock the whole server. The emit helpers below run
// under Clients.mutex.RLock(), so a blocked send held that read lock forever,
// which in turn blocked Clients.Remove() on the write lock — so the stalled
// client could never be reaped, and Go's RWMutex then starves every subsequent
// RLock() too. The shared clientEmitQueue dispatcher wedged, its 8192-deep
// buffer filled, and the single ingest goroutine blocked behind it: no calls
// stored, no calls broadcast, while the frozen client map kept reporting the
// last known listener count. One unresponsive listener was enough.
//
// Dropping a message only degrades the one client that can't keep up. Its
// socket is torn down by the read/write deadlines soon after regardless.
func (client *Client) enqueue(message *Message) bool {
	if client.closed.Load() {
		return false
	}

	select {
	case client.Send <- message:
		return true
	default:
		client.dropped.Add(1)
		return false
	}
}

func (client *Client) Init(controller *Controller, request *http.Request, conn *websocket.Conn) error {
	const (
		pongWait   = 60 * time.Second
		pingPeriod = pongWait / 10 * 9
		writeWait  = 10 * time.Second
	)

	if conn == nil {
		return errors.New("client.init: no websocket connection")
	}

	if controller.Clients.Count() >= int(controller.Options.MaxClients) {
		conn.Close()
		return nil
	}

	client.Access = &Access{}
	client.Controller = controller
	client.Conn = conn
	client.Livefeed = NewLivefeed()
	client.Send = make(chan *Message, 8192)
	client.request = request
	// The /stream overlay connects its WebSocket to the /stream path; flag it
	// so it isn't double-counted as a listener.
	client.Overlay = path.Base(request.URL.Path) == "stream"

	go func() {
		defer func() {
			controller.Unregister <- client

			// Surface shed messages here rather than from enqueue: this runs
			// once per disconnect, off the emit path and outside
			// Clients.mutex, so it can afford LogEvent's database write.
			dropped := ""
			if n := client.dropped.Load(); n > 0 {
				dropped = fmt.Sprintf(" (%d message(s) dropped, send buffer full)", n)
			}

			if len(client.Access.Ident) > 0 {
				controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf("listener disconnected from ip %s with ident %s%s", client.GetRemoteAddr(), client.Access.Ident, dropped))

			} else {
				controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf("listener disconnected from ip %s%s", client.GetRemoteAddr(), dropped))
			}

			client.Conn.Close()
		}()

		client.Conn.SetReadDeadline(time.Now().Add(pongWait))

		client.Conn.SetPongHandler(func(string) error {
			client.Conn.SetReadDeadline(time.Now().Add(pongWait))
			return nil
		})

		for {
			_, b, err := client.Conn.ReadMessage()
			if err != nil {
				return
			}

			message := &Message{}
			if err = message.FromJson(b); err != nil {
				log.Println(fmt.Errorf("client.message.fromjson: %v", err))
				continue
			}

			if err = client.Controller.ProcessMessage(client, message); err != nil {
				log.Println(fmt.Errorf("client.processmessage: %v", err))
				continue
			}
		}
	}()

	go func() {
		ticker := time.NewTicker(pingPeriod)

		timer := time.AfterFunc(pongWait, func() {
			client.Conn.Close()
		})

		defer func() {
			ticker.Stop()

			if timer != nil {
				timer.Stop()
			}

			// Mark the client dead before draining so enqueue stops adding to
			// a channel that no longer has a consumer. Closing client.Send is
			// deliberately NOT done — concurrent senders would panic on a
			// closed channel; enqueue's non-blocking default case is what
			// keeps them safe instead.
			client.closed.Store(true)

			client.Conn.Close()

			// Shed anything already buffered (including sends that raced the
			// flag above) so the queued *Message values, each holding a call
			// payload, don't sit around until the Client itself is collected.
			for {
				select {
				case <-client.Send:
				default:
					return
				}
			}
		}()

		for {
			select {
			case message, ok := <-client.Send:
				if !ok {
					return
				}

				if message.Command == MessageCommandConfig {
					if timer != nil {
						timer.Stop()
						timer = nil

						controller.Register <- client

						if len(client.Access.Ident) > 0 {
							controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf("new listener from ip %s with ident %s", client.GetRemoteAddr(), client.Access.Ident))

						} else {
							controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf("new listener from ip %s", client.GetRemoteAddr()))
						}
					}
				}

				b, err := message.ToJson()
				if err != nil {
					log.Println(fmt.Errorf("client.message.tojson: %v", err))

				} else {
					client.Conn.SetWriteDeadline(time.Now().Add(writeWait))

					if err = client.Conn.WriteMessage(websocket.TextMessage, b); err != nil {
						return
					}
				}

			case <-ticker.C:
				client.Conn.SetWriteDeadline(time.Now().Add(writeWait))

				if err := client.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			}
		}
	}()

	return nil
}

func (client *Client) GetRemoteAddr() string {
	return GetRemoteAddr(client.request)
}

func (client *Client) SendConfig(groups *Groups, options *Options, systems *Systems, tags *Tags) {
	// Clients without a restricted access scope (the common case) all see
	// the same systems/groups/tags view. Reuse a single cached build
	// instead of rebuilding for every WebSocket connection.
	unrestricted := client.Access == nil ||
		client.Access.Systems == nil ||
		(isString(client.Access.Systems) && client.Access.Systems == "*")

	if unrestricted && client.Controller != nil {
		cached := client.Controller.getUnrestrictedConfigCache()
		client.SystemsMap = cached.SystemsMap
		client.GroupsMap = cached.GroupsMap
		client.TagsMap = cached.TagsMap
	} else {
		client.SystemsMap = systems.GetScopedSystems(client, groups, tags, options.SortTalkgroups)
		client.GroupsMap = groups.GetGroupsMap(&client.SystemsMap)
		client.TagsMap = tags.GetTagsMap(&client.SystemsMap)
	}

	var payload = map[string]any{
		"alerts":             Alerts,
		"branding":           options.Branding,
		"dimmerDelay":        options.DimmerDelay,
		"email":              options.Email,
		"groups":             client.GroupsMap,
		"keypadBeeps":        GetKeypadBeeps(options),
		"playbackGoesLive":   options.PlaybackGoesLive,
		"showListenersCount": options.ShowListenersCount,
		"sortByGroups":       options.SortByGroups,
		"sortByTags":         options.SortByTags,
		"systems":            client.SystemsMap,
		"tags":               client.TagsMap,
		"tagsToggle":         options.TagsToggle,
		"time12hFormat":      options.Time12hFormat,
	}

	if len(options.AfsSystems) > 0 {
		payload["afs"] = options.AfsSystems
	}

	if len(options.UmamiUrl) > 0 && len(options.UmamiWebsiteId) > 0 {
		payload["umamiUrl"] = options.UmamiUrl
		payload["umamiWebsiteId"] = options.UmamiWebsiteId
	}

	// Keys plugins asked to publish. Merged last but never allowed to overwrite
	// a core key — a plugin must not be able to change what the webapp thinks
	// its own settings are.
	if client.Controller != nil {
		for key, value := range client.Controller.PluginExposedConfig() {
			if _, taken := payload[key]; taken {
				continue
			}
			payload[key] = value
		}

		// The webapp needs to know which plugins have frontend code to load,
		// and it asks for this before it has an admin session.
		if entries := client.Controller.PluginWebEntries(); len(entries) > 0 {
			payload["plugins"] = entries
		}
	}

	client.enqueue(&Message{Command: MessageCommandConfig, Payload: payload})

	// Send the listener count immediately so the LCD doesn't show an empty
	// "L:" counter for the 3-15 s debounce window used by the controller's
	// register/unregister broadcaster.
	if options.ShowListenersCount && client.Controller != nil {
		client.SendListenersCount(client.Controller.Clients.CountListeners())
	}
}

func isString(v any) bool {
	_, ok := v.(string)
	return ok
}

func (client *Client) SendListenersCount(count int) {
	client.enqueue(&Message{
		Command: MessagecommandListenersCount,
		Payload: count,
	})
}

type Clients struct {
	Map   map[*Client]bool
	mutex sync.RWMutex
}

func NewClients() *Clients {
	return &Clients{
		Map:   map[*Client]bool{},
		mutex: sync.RWMutex{},
	}
}

func (clients *Clients) AccessCount(client *Client) int {
	count := 0

	clients.mutex.RLock()
	defer clients.mutex.RUnlock()

	for c := range clients.Map {
		if c.Access == client.Access {
			count++
		}
	}

	return count
}

func (clients *Clients) Add(client *Client) {
	clients.mutex.Lock()
	defer clients.mutex.Unlock()

	clients.Map[client] = true
}

func (clients *Clients) Count() int {
	clients.mutex.RLock()
	defer clients.mutex.RUnlock()

	return len(clients.Map)
}

// CountListeners is the number of clients shown as "listeners" — every
// connection except /stream overlay pages (so opening /stream alongside the
// main page doesn't count as a second user).
func (clients *Clients) CountListeners() int {
	clients.mutex.RLock()
	defer clients.mutex.RUnlock()

	count := 0
	for c := range clients.Map {
		if !c.Overlay {
			count++
		}
	}

	return count
}

func (clients *Clients) EmitCall(call *Call, restricted bool) {
	clients.mutex.RLock()
	defer clients.mutex.RUnlock()

	for c := range clients.Map {
		if (!restricted || c.Access.HasAccess(call)) && c.Livefeed.IsEnabled(call) {
			c.enqueue(&Message{Command: MessageCommandCall, Payload: call})
		}
	}
}

func (clients *Clients) EmitConfig(groups *Groups, options *Options, systems *Systems, tags *Tags, restricted bool) {
	clients.mutex.RLock()
	defer clients.mutex.RUnlock()

	count := 0
	for c := range clients.Map {
		if !c.Overlay {
			count++
		}
	}

	for c := range clients.Map {
		if restricted {
			c.enqueue(&Message{Command: MessageCommandPin})
		} else {
			c.SendConfig(groups, options, systems, tags)
		}

		if options.ShowListenersCount {
			c.SendListenersCount(count)
		}
	}
}

// EmitPluginMessage broadcasts a plugin-defined command. When scoped is true
// the message is only delivered to clients allowed to see the given
// system/talkgroup, which is what stops a plugin leaking data about restricted
// systems to listeners who can't see them.
//
// Like every other emit here, delivery is best effort — enqueue drops to
// clients that have fallen behind rather than blocking under Clients.mutex.
func (clients *Clients) EmitPluginMessage(message *Message, scoped bool, system uint, talkgroup uint, restricted bool) {
	probe := &Call{System: system, Talkgroup: talkgroup}

	clients.mutex.RLock()
	defer clients.mutex.RUnlock()

	for c := range clients.Map {
		if scoped && restricted && c.Access != nil && !c.Access.HasAccess(probe) {
			continue
		}
		c.enqueue(message)
	}
}

func (clients *Clients) EmitListenersCount() {
	clients.mutex.RLock()
	defer clients.mutex.RUnlock()

	count := 0
	for c := range clients.Map {
		if !c.Overlay {
			count++
		}
	}

	for c := range clients.Map {
		c.SendListenersCount(count)
	}
}

func (clients *Clients) Remove(client *Client) {
	clients.mutex.Lock()
	defer clients.mutex.Unlock()

	delete(clients.Map, client)
}
