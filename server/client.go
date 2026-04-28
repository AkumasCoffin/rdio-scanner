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
	"sync"
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

	go func() {
		defer func() {
			controller.Unregister <- client

			if len(client.Access.Ident) > 0 {
				controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf("listener disconnected from ip %s with ident %s", client.GetRemoteAddr(), client.Access.Ident))

			} else {
				controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf("listener disconnected from ip %s", client.GetRemoteAddr()))
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

			client.Conn.Close()
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
		"branding":           options.Branding,
		"dimmerDelay":        options.DimmerDelay,
		"email":              options.Email,
		"groups":             client.GroupsMap,
		"keypadBeeps":        GetKeypadBeeps(options),
		"playbackGoesLive":   options.PlaybackGoesLive,
		"showListenersCount": options.ShowListenersCount,
		"systems":            client.SystemsMap,
		"tags":               client.TagsMap,
		"tagsToggle":         options.TagsToggle,
		"time12hFormat":      options.Time12hFormat,
		"waitForTranscript":  options.WaitForTranscript,
		"showRetranscribeButton": options.ShowRetranscribeButton,
	}

	if len(options.AfsSystems) > 0 {
		payload["afs"] = options.AfsSystems
	}

	if len(options.UmamiUrl) > 0 && len(options.UmamiWebsiteId) > 0 {
		payload["umamiUrl"] = options.UmamiUrl
		payload["umamiWebsiteId"] = options.UmamiWebsiteId
	}

	client.Send <- &Message{Command: MessageCommandConfig, Payload: payload}

	// Send the listener count immediately so the LCD doesn't show an empty
	// "L:" counter for the 3-15 s debounce window used by the controller's
	// register/unregister broadcaster.
	if options.ShowListenersCount && client.Controller != nil {
		client.SendListenersCount(client.Controller.Clients.Count())
	}
}

func isString(v any) bool {
	_, ok := v.(string)
	return ok
}

func (client *Client) SendListenersCount(count int) {
	client.Send <- &Message{
		Command: MessagecommandListenersCount,
		Payload: count,
	}
}

type Clients struct {
	Map   map[*Client]bool
	mutex sync.Mutex
}

func NewClients() *Clients {
	return &Clients{
		Map:   map[*Client]bool{},
		mutex: sync.Mutex{},
	}
}

func (clients *Clients) AccessCount(client *Client) int {
	count := 0

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
	return len(clients.Map)
}

func (clients *Clients) EmitCall(call *Call, restricted bool) {
	for c := range clients.Map {
		if (!restricted || c.Access.HasAccess(call)) && c.Livefeed.IsEnabled(call) {
			c.Send <- &Message{Command: MessageCommandCall, Payload: call}
		}
	}
}

func (clients *Clients) EmitConfig(groups *Groups, options *Options, systems *Systems, tags *Tags, restricted bool) {
	count := len(clients.Map)

	for c := range clients.Map {
		if restricted {
			c.Send <- &Message{Command: MessageCommandPin}
		} else {
			c.SendConfig(groups, options, systems, tags)
		}

		if options.ShowListenersCount {
			c.SendListenersCount(count)
		}
	}
}

// EmitTranscript pushes a transcript-ready notification to every client that
// would be allowed to see the underlying call. Used right after an async
// Whisper run writes a transcript to the DB, so live listeners see their
// history rows populate without having to refresh.
func (clients *Clients) EmitTranscript(id uint, system uint, talkgroup uint, transcript string, restricted bool) {
	probe := &Call{System: system, Talkgroup: talkgroup}
	payload := map[string]any{
		"id":         id,
		"system":     system,
		"talkgroup":  talkgroup,
		"transcript": transcript,
	}
	for c := range clients.Map {
		if restricted && c.Access != nil && !c.Access.HasAccess(probe) {
			continue
		}
		select {
		case c.Send <- &Message{Command: MessageCommandTranscript, Payload: payload}:
		default:
			// Drop if the client's send buffer is full rather than
			// blocking the ingest path.
		}
	}
}

func (clients *Clients) EmitListenersCount() {
	count := len(clients.Map)

	for c := range clients.Map {
		c.SendListenersCount(count)
	}
}

func (clients *Clients) Remove(client *Client) {
	clients.mutex.Lock()
	defer clients.mutex.Unlock()

	delete(clients.Map, client)
}
