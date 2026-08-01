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
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"time"
)

type Controller struct {
	Admin       *Admin
	Api         *Api
	PublicApi   *PublicApi
	Calls       *Calls
	Config      *Config
	Database    *Database
	Delayer     *Delayer
	Accesses    *Accesses
	Apikeys     *Apikeys
	Dirwatches  *Dirwatches
	Downstreams *Downstreams
	FFMpeg      *FFMpeg
	Groups      *Groups
	Logs        *Logs
	Options     *Options
	Plugins     *Plugins
	PluginStore *PluginStore
	Scheduler   *Scheduler
	Stats       *Stats
	Systems     *Systems
	Tags        *Tags
	// pluginFeatures caches which features each downstream advertises, so a
	// plugin speaking a server-to-server protocol doesn't re-probe per call.
	pluginFeatures *PluginFeatureCache
	// calLogThrottle rate-limits the per-request CAL diagnostic log lines
	// (not-found / access-denied) so bot traffic to dead share links can't
	// flood the logs table.
	calLogThrottle *LogThrottle
	Clients            *Clients
	Register           chan *Client
	Unregister         chan *Client
	Ingest             chan *Call
	// clientEmitQueue serializes broadcasts to live WebSocket listeners so
	// concurrent EmitCallToClients callers (single ingest goroutine + N
	// Delayer timer goroutines) can't race each other and reorder calls on
	// the wire. A single dispatcher drains it FIFO.
	clientEmitQueue chan *Call
	// downstreamEmitQueue is the same idea for forwarded-call HTTP POSTs to
	// downstream instances. Kept separate from clientEmitQueue because the
	// downstream path is slow (HTTP) and we don't want a slow downstream to
	// hold up local listener broadcasts.
	downstreamEmitQueue chan *Call
	running             bool

	// Cached "unrestricted access" view of the systems/groups/tags maps.
	// Most clients hit the server with no access code so they all get the
	// same payload — build it once and reuse it instead of re-scoping on
	// every CFG request.
	configCacheMu sync.RWMutex
	configCache   *configCache
}

type configCache struct {
	SystemsMap SystemsMap
	GroupsMap  GroupsMap
	TagsMap    TagsMap
}

func NewController(config *Config) *Controller {
	controller := &Controller{
		Config:      config,
		Accesses:    NewAccesses(),
		Apikeys:     NewApikeys(),
		Calls:       NewCalls(),
		Dirwatches:  NewDirwatches(),
		Downstreams: NewDownstreams(),
		FFMpeg:      NewFFMpeg(),
		Groups:      NewGroups(),
		Logs:        NewLogs(),
		Options:     NewOptions(),
		Plugins:     NewPlugins(),
		Systems:     NewSystems(),
		Tags:        NewTags(),
		Clients:     NewClients(),
		Register:            make(chan *Client, 8192),
		Unregister:          make(chan *Client, 8192),
		Ingest:              make(chan *Call, 8192),
		clientEmitQueue:     make(chan *Call, 8192),
		downstreamEmitQueue: make(chan *Call, 8192),
	}

	controller.Admin = NewAdmin(controller)
	controller.Api = NewApi(controller)
	controller.PublicApi = NewPublicApi(controller)
	controller.Database = NewDatabase(config)
	controller.Delayer = NewDelayer(controller)
	controller.PluginStore = NewPluginStore(controller)
	controller.pluginFeatures = NewPluginFeatureCache()
	controller.Scheduler = NewScheduler(controller)
	controller.Stats = NewStats(controller)
	// At most 5 CAL not-found/denied log lines per source IP per minute.
	controller.calLogThrottle = NewLogThrottle(5, time.Minute)

	controller.Logs.setDaemon(config.daemon)
	controller.Logs.setDatabase(controller.Database)

	return controller
}

// getUnrestrictedConfigCache returns the cached unrestricted scoping maps
// for the config payload, building (and remembering) them on first use.
// Invalidated whenever EmitConfig fires (after an admin save).
func (controller *Controller) getUnrestrictedConfigCache() *configCache {
	controller.configCacheMu.RLock()
	c := controller.configCache
	controller.configCacheMu.RUnlock()
	if c != nil {
		return c
	}

	controller.configCacheMu.Lock()
	defer controller.configCacheMu.Unlock()
	if controller.configCache != nil {
		return controller.configCache
	}

	// Build using a synthesized "no access code" probe so GetScopedSystems
	// returns the full set.
	probe := &Client{Access: &Access{}}
	systems := controller.Systems.GetScopedSystems(probe, controller.Groups, controller.Tags, controller.Options.SortTalkgroups)
	groups := controller.Groups.GetGroupsMap(&systems)
	tags := controller.Tags.GetTagsMap(&systems)
	controller.configCache = &configCache{
		SystemsMap: systems,
		GroupsMap:  groups,
		TagsMap:    tags,
	}
	return controller.configCache
}

// InvalidateConfigCache wipes the cached unrestricted maps. Call after any
// change to systems / groups / tags / options that affects the config
// payload.
func (controller *Controller) InvalidateConfigCache() {
	controller.configCacheMu.Lock()
	controller.configCache = nil
	controller.configCacheMu.Unlock()
}

// EmitCallToDownstreams forwards a call to configured downstream servers.
// Bypasses the Delayer — downstreams receive calls immediately on ingest so
// transcript-forward setups don't add network/Delayer time on top of each
// other, and downstream-side delays (if any) stay the responsibility of the
// downstream's own admin config.
//
// Serialized through downstreamEmitQueue + a single dispatcher in Start() so
// concurrent callers (multiple IngestCall paths, Delayer timers) can't race
// and reorder forwarded calls between downstream servers.
func (controller *Controller) EmitCallToDownstreams(call *Call) {
	controller.downstreamEmitQueue <- call
}

// EmitCallToClients pushes a call to live WebSocket listeners. Subject to the
// Delayer's per-talkgroup/per-system hold so listener UX matches the
// configured rebroadcast delay.
//
// Serialized through clientEmitQueue + a single dispatcher in Start() so
// concurrent emit paths (delay=0 ingest + Delayer timer fires + Start()
// catchup) can't reorder messages on the per-client WS connections.
func (controller *Controller) EmitCallToClients(call *Call) {
	controller.clientEmitQueue <- call
}

// EmitCall is the legacy "do both" path. Preserved for any external/future
// callers but no longer used internally — Delayer fires EmitCallToClients;
// IngestCall fires EmitCallToDownstreams synchronously on ingest.
func (controller *Controller) EmitCall(call *Call) {
	controller.EmitCallToDownstreams(call)
	controller.EmitCallToClients(call)
}

func (controller *Controller) EmitConfig() {
	controller.InvalidateConfigCache()
	go controller.Clients.EmitConfig(controller.Groups, controller.Options, controller.Systems, controller.Tags, controller.Accesses.IsRestricted())
	go controller.Admin.BroadcastConfig()
}

// PluginExposedConfig collects the keys running plugins want included in the
// CFG payload sent to webapp clients. Keys are namespaced by nothing — a plugin
// picks its own names — so a later plugin wins a collision, which is visible
// and debuggable rather than silently merged.
func (controller *Controller) PluginExposedConfig() map[string]any {
	exposed := map[string]any{}

	for _, plugin := range controller.Plugins.Enabled() {
		if plugin.runtime == nil {
			continue
		}
		for key, value := range plugin.runtime.ExposedConfig() {
			exposed[key] = value
		}
	}

	return exposed
}

func (controller *Controller) IngestCall(call *Call) {
	var (
		err        error
		group      *Group
		groupId    uint
		groupLabel string
		id         uint
		ok         bool
		populated  bool
		system     *System
		tag        *Tag
		tagId      uint
		tagLabel   string
		talkgroup  *Talkgroup
	)

	logCall := func(call *Call, level string, message string) {
		if call.apiKeyIdent != "" {
			controller.Logs.LogEvent(level, fmt.Sprintf("newcall: [%v] system=%v talkgroup=%v file=%v %v", call.apiKeyIdent, call.System, call.Talkgroup, call.AudioName, message))
		} else {
			controller.Logs.LogEvent(level, fmt.Sprintf("newcall: system=%v talkgroup=%v file=%v %v", call.System, call.Talkgroup, call.AudioName, message))
		}
	}

	logError := func(err error) {
		controller.Logs.LogEvent(LogLevelError, fmt.Sprintf("controller.ingestcall: %v", err.Error()))
	}

	if system, ok = controller.Systems.GetSystem(call.System); ok {
		if system.Blacklists.IsBlacklisted(call.Talkgroup) {
			logCall(call, LogLevelInfo, "blacklisted")
			return
		}
		talkgroup, _ = system.Talkgroups.GetTalkgroup(call.Talkgroup)
	}

	if controller.Options.AutoPopulate && system == nil {
		populated = true

		system = NewSystem()
		system.Id = call.System

		switch v := call.systemLabel.(type) {
		case string:
			system.Label = v
		default:
			system.Label = fmt.Sprintf("System %v", call.System)
		}

		controller.Systems.List = append(controller.Systems.List, system)
	}

	if controller.Options.AutoPopulate || (system != nil && system.AutoPopulate) {
		if system != nil && talkgroup == nil {
			populated = true

			switch v := call.talkgroupGroup.(type) {
			case string:
				groupLabel = v
			default:
				groupLabel = "Unknown"
			}

			switch v := call.talkgroupTag.(type) {
			case string:
				tagLabel = v
			default:
				tagLabel = "Untagged"
			}

			if group, ok = controller.Groups.GetGroup(groupLabel); !ok {
				group = &Group{Label: groupLabel}

				controller.Groups.List = append(controller.Groups.List, group)

				if err = controller.Groups.Write(controller.Database); err != nil {
					logError(err)
					return
				}

				if err = controller.Groups.Read(controller.Database); err != nil {
					logError(err)
					return
				}

				if group, ok = controller.Groups.GetGroup(groupLabel); !ok {
					logError(fmt.Errorf("unable to get group %s", groupLabel))
					return
				}
			}

			switch v := group.Id.(type) {
			case uint:
				groupId = v
			default:
				logError(fmt.Errorf("unable to get group id for group %s", groupLabel))
				return
			}

			if tag, ok = controller.Tags.GetTag(tagLabel); !ok {
				tag = &Tag{Label: tagLabel}

				controller.Tags.List = append(controller.Tags.List, tag)

				if err = controller.Tags.Write(controller.Database); err != nil {
					logError(err)
					return
				}

				if err = controller.Tags.Read(controller.Database); err != nil {
					logError(err)
					return
				}

				if tag, ok = controller.Tags.GetTag(tagLabel); !ok {
					logError(fmt.Errorf("unable to get tag %s", tagLabel))
					return
				}
			}

			switch v := tag.Id.(type) {
			case uint:
				tagId = v
			default:
				logError(fmt.Errorf("unable to get tag id for tag %s", tagLabel))
				return
			}

			talkgroup = &Talkgroup{
				GroupId: groupId,
				Id:      call.Talkgroup,
				Label:   fmt.Sprintf("%d", call.Talkgroup),
				TagId:   tagId,
			}

			system.Talkgroups.List = append(system.Talkgroups.List, talkgroup)
		}

		switch v := call.talkgroupLabel.(type) {
		case string:
			if talkgroup.Label != v {
				populated = true
				talkgroup.Label = v
			}
		}

		switch v := call.talkgroupName.(type) {
		case string:
			if talkgroup.Name != v {
				populated = true
				talkgroup.Name = v
			}
		default:
			if len(talkgroup.Name) == 0 {
				populated = true
				talkgroup.Name = talkgroup.Label
			}
		}

		switch v := call.units.(type) {
		case *Units:
			if v != nil {
				populated = system.Units.Merge(v)
			}
		}
	}

	if populated {
		if err = controller.Systems.Write(controller.Database); err != nil {
			logError(err)
			return
		}

		if err = controller.Systems.Read(controller.Database); err != nil {
			logError(err)
			return
		}

		controller.EmitConfig()
	}

	if system == nil || talkgroup == nil {
		logCall(call, LogLevelWarn, "no matching system/talkgroup")
		return
	}

	if !controller.Options.DisableDuplicateDetection {
		if controller.Calls.CheckDuplicate(call, controller.Options.DuplicateDetectionTimeFrame, controller.Database) {
			logCall(call, LogLevelWarn, "duplicate call rejected")
			return
		}
	}

	if err := controller.FFMpeg.Convert(call, controller.Systems, controller.Tags, controller.Options.AudioConversion); err != nil {
		controller.Logs.LogEvent(LogLevelWarn, err.Error())
	}

	// Plugins see the call before it is written. Dispatch is asynchronous
	// inside each runtime, so this cannot stall the single ingest goroutine —
	// which also means a plugin cannot reliably mutate what gets stored. That
	// tradeoff is deliberate: ingest throughput matters more than giving
	// plugins a synchronous veto.
	controller.Plugins.EmitEvent(PluginEventCallIngested, pluginCallValue(call, false))

	if id, err = controller.Calls.WriteCall(call, controller.Database); err == nil {
		call.Id = id
		call.systemLabel = system.Label
		call.talkgroupLabel = talkgroup.Label
		call.talkgroupName = talkgroup.Name

		if group == nil {
			if group, ok = controller.Groups.GetGroup(talkgroup.GroupId); ok {
				call.talkgroupGroup = group.Label
			}
		}

		if tag == nil {
			if tag, ok = controller.Tags.GetTag(talkgroup.TagId); ok {
				call.talkgroupTag = tag.Label
			}
		}

		logCall(call, LogLevelInfo, "success")

		// Fire downstream forwarding immediately — Delayer below only holds
		// the local listener emit. Forwarding before the delay means
		// downstreams receive calls at near-real-time, which matters for
		// plugin protocols that push a follow-up (a transcript, say) and would
		// otherwise race their own call upload.
		controller.EmitCallToDownstreams(call)

		// Now that the call has an id, plugins can key their own tables to it.
		// This is the hook most plugins actually want, and it is where anything
		// that enriches a call — transcription included — now happens.
		controller.Plugins.EmitEvent(PluginEventCallStored, pluginCallValue(call, false))

		controller.Delayer.Delay(call)

	} else {
		logError(err)
	}
}

func (controller *Controller) LogClientsCount() {
	controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf("listeners count is %v", controller.Clients.Count()))
}

func (controller *Controller) ProcessMessage(client *Client, message *Message) error {
	if message.Command == MessageCommandVersion {
		controller.ProcessMessageCommandVersion(client)

	} else if controller.Accesses.IsRestricted() && client.Access.Systems == nil && message.Command != MessageCommandPin {
		client.enqueue(&Message{Command: MessageCommandPin})

	} else if message.Command == MessageCommandCall {
		if err := controller.ProcessMessageCommandCall(client, message); err != nil {
			return err
		}

	} else if message.Command == MessageCommandConfig {
		client.SendConfig(controller.Groups, controller.Options, controller.Systems, controller.Tags)

	} else if message.Command == MessageCommandListCall {
		if err := controller.ProcessMessageCommandListCall(client, message); err != nil {
			return err
		}

	} else if message.Command == MessageCommandLivefeedMap {
		controller.ProcessMessageCommandLivefeedMap(client, message)

	} else if message.Command == MessageCommandPin {
		if err := controller.ProcessMessageCommandPin(client, message); err != nil {
			return err
		}

	} else {
		// Anything core doesn't recognise is offered to the plugins. Without
		// this the chain silently swallowed unknown commands, which is what
		// lets a plugin add its own protocol messages over the connection the
		// client already has open.
		controller.ProcessMessageCommandPlugin(client, message)
	}

	return nil
}

// ProcessMessageCommandPlugin routes an unrecognised websocket command to
// whichever plugin claimed it. Unclaimed commands are ignored, exactly as they
// were before plugins existed.
func (controller *Controller) ProcessMessageCommandPlugin(client *Client, message *Message) {
	command, ok := message.Command.(string)
	if !ok {
		return
	}

	for _, plugin := range controller.Plugins.Enabled() {
		if plugin.runtime == nil || !plugin.runtime.HasWsHandler(command) {
			continue
		}
		plugin.runtime.DispatchWs(client, command, message.Payload)
		return
	}
}

func (controller *Controller) ProcessMessageCommandCall(client *Client, message *Message) error {
	var (
		call *Call
		err  error
		i    int
		id   uint
	)

	switch v := message.Payload.(type) {
	case float64:
		id = uint(v)
	case string:
		if i, err = strconv.Atoi(v); err == nil {
			id = uint(i)
		} else {
			return err
		}
	}

	if call, err = controller.Calls.GetCall(id, controller.Database); err != nil {
		if err == sql.ErrNoRows {
			// Share-link to a call that's been purged (retention) or never
			// existed. Logged so operators can confirm "the link doesn't
			// work" is a missing-call situation rather than a delivery bug.
			// Throttled per-IP: bots crawling dead share links would otherwise
			// flood the logs table (every LogEvent is a DB insert).
			if controller.calLogThrottle.Allow(client.GetRemoteAddr()) {
				controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf("CAL request: call id=%v not found (ip=%s ident=%s flag=%s)", id, client.GetRemoteAddr(), client.Access.Ident, message.Flag))
			}
			return nil
		}
		return err
	}

	if controller.Accesses.IsRestricted() && !client.Access.HasAccess(call) {
		// Restricted server: the requester's access code doesn't cover this
		// call's system/talkgroup. Previously silent — logged so a shared
		// link landing on a viewer who can't actually see the call shows up
		// in the server log instead of producing a blank UI. Throttled per-IP
		// for the same flood reason as the not-found case above.
		if controller.calLogThrottle.Allow(client.GetRemoteAddr()) {
			controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf("CAL request: call id=%v denied (ip=%s ident=%s system=%v talkgroup=%v flag=%s)", id, client.GetRemoteAddr(), client.Access.Ident, call.System, call.Talkgroup, message.Flag))
		}
		return nil
	}

	// Fill in any plugin-contributed fields before serving the call. This is
	// the replay path, where a plugin's value has almost certainly been
	// computed by now even if it wasn't ready when the call was first emitted.
	controller.ApplyPluginFields(call)

	client.enqueue(&Message{Command: MessageCommandCall, Payload: call, Flag: message.Flag})

	return nil
}

func (controller *Controller) ProcessMessageCommandListCall(client *Client, message *Message) error {
	switch v := message.Payload.(type) {
	case map[string]any:
		searchOptions := CallsSearchOptions{searchPatchedTalkgroups: controller.Options.SearchPatchedTalkgroups}
		searchOptions.fromMap(v)
		if searchResults, err := controller.Calls.Search(&searchOptions, client); err == nil {
			client.enqueue(&Message{Command: MessageCommandListCall, Payload: searchResults})
		} else {
			return fmt.Errorf("controller.processmessage.commandlistcall: %v", err)
		}
	}
	return nil
}

func (controller *Controller) ProcessMessageCommandLivefeedMap(client *Client, message *Message) {
	client.Livefeed.FromMap(message.Payload)
	client.enqueue(&Message{Command: MessageCommandLivefeedMap, Payload: !client.Livefeed.IsAllOff()})
}

func (controller *Controller) ProcessMessageCommandPin(client *Client, message *Message) error {
	const maxAuthCount = 5

	switch v := message.Payload.(type) {
	case string:
		b, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			return fmt.Errorf("controller.processmessage.commandpin: %v", err)
		}

		client.AuthCount++
		if client.AuthCount > maxAuthCount {
			client.enqueue(&Message{Command: MessageCommandPin})
			return nil
		}

		if controller.Accesses.IsRestricted() {
			code := string(b)
			if access, ok := controller.Accesses.GetAccess(code); ok {
				client.Access = access
			} else {
				controller.Logs.LogEvent(LogLevelWarn, fmt.Sprintf("invalid access code %s for ip %s", code, client.GetRemoteAddr()))
				client.enqueue(&Message{Command: MessageCommandPin})
				return nil
			}

			if client.AuthCount == maxAuthCount {
				controller.Logs.LogEvent(LogLevelWarn, fmt.Sprintf("locked access for ident %s locked", client.Access.Ident))
				client.enqueue(&Message{Command: MessageCommandPin})
				return nil
			}

			if client.Access.HasExpired() {
				controller.Logs.LogEvent(LogLevelWarn, fmt.Sprintf("expired access for ident %s", client.Access.Ident))
				client.enqueue(&Message{Command: MessageCommandExpired})
				return nil
			}

			switch v := client.Access.Limit.(type) {
			case uint:
				if controller.Clients.AccessCount(client) > int(v) {
					controller.Logs.LogEvent(LogLevelWarn, fmt.Sprintf("too many concurrent connections for ident %s, limit is %d", client.Access.Ident, client.Access.Limit))
					client.enqueue(&Message{Command: MessageCommandMax})
					return nil
				}
			}
		}

		client.AuthCount = 0

		client.SendConfig(controller.Groups, controller.Options, controller.Systems, controller.Tags)
	}

	return nil
}

func (controller *Controller) ProcessMessageCommandVersion(client *Client) {
	p := map[string]string{"version": Version}

	if len(controller.Options.Branding) > 0 {
		p["branding"] = controller.Options.Branding
	}

	if len(controller.Options.Email) > 0 {
		p["email"] = controller.Options.Email
	}

	client.enqueue(&Message{Command: MessageCommandVersion, Payload: p})
}

func (controller *Controller) Start() error {
	var err error

	if controller.running {
		return errors.New("controller already running")
	} else {
		controller.running = true
	}

	controller.Logs.LogEvent(LogLevelWarn, "server started")

	if len(controller.Config.BaseDir) > 0 {
		log.Printf("base folder is %s\n", controller.Config.BaseDir)
	}

	// Report a missing ffmpeg here, next to the boot banner, rather than only
	// on the first call that wanted converting. That lazy warning fires once
	// per process at an arbitrary moment, so an admin who wasn't watching the
	// log right then never learned why their audio was never converted.
	if !controller.FFMpeg.Available() {
		controller.Logs.LogEvent(LogLevelWarn, controller.FFMpeg.UnavailableMessage())
	}

	if err = controller.Accesses.Read(controller.Database); err != nil {
		return err
	}
	if err = controller.Apikeys.Read(controller.Database); err != nil {
		return err
	}
	if err = controller.Dirwatches.Read(controller.Database); err != nil {
		return err
	}
	if err = controller.Downstreams.Read(controller.Database); err != nil {
		return err
	}
	if err = controller.Groups.Read(controller.Database); err != nil {
		return err
	}
	if err = controller.Options.Read(controller.Database); err != nil {
		return err
	}
	if err = controller.Systems.Read(controller.Database); err != nil {
		return err
	}
	if err = controller.Tags.Read(controller.Database); err != nil {
		return err
	}

	// Plugins load after everything they can reach is already populated, so a
	// startup handler sees a fully-configured controller. A failure to read the
	// registry is non-fatal: a broken plugin directory must not stop the server
	// from serving calls.
	if err = controller.Plugins.Read(controller.Database, controller.Config); err != nil {
		controller.Logs.LogEvent(LogLevelError, fmt.Sprintf("plugins read: %v", err))
	} else if err = controller.Plugins.Start(controller); err != nil {
		controller.Logs.LogEvent(LogLevelError, fmt.Sprintf("plugins start: %v", err))
	}

	if err = controller.Admin.Start(); err != nil {
		return err
	}
	if err = controller.Scheduler.Start(); err != nil {
		return err
	}

	// Start emit dispatchers BEFORE Delayer.Start() so any catchup emits
	// from rdioScannerDelayed get drained immediately rather than piling
	// into the channel buffer with no consumer.
	go func() {
		for call := range controller.clientEmitQueue {
			// Fill in plugin-contributed fields on the way out, so a value a
			// plugin has already computed reaches the live feed — and Android,
			// which reads those fields inline off the call payload.
			controller.ApplyPluginFields(call)
			controller.Clients.EmitCall(call, controller.Accesses.IsRestricted())
		}
	}()
	go func() {
		for call := range controller.downstreamEmitQueue {
			controller.Downstreams.Send(controller, call)
		}
	}()

	if err = controller.Delayer.Start(); err != nil {
		// Delayer restore failure is non-fatal — log and continue. Any
		// orphaned rows in rdioScannerDelayed will retry on next boot.
		controller.Logs.LogEvent(LogLevelError, fmt.Sprintf("delayer start: %v", err))
	}

	// Warm the unrestricted CFG cache so the very first client connect
	// doesn't pay the build cost.
	go controller.getUnrestrictedConfigCache()

	// Warm the stats cache so the first /api/admin/stats doesn't run
	// the heavy aggregations on a cold table.
	go controller.Stats.cachedBuild(controller.Database)

	go func() {
		c := make(chan os.Signal, 8)
		signal.Notify(c, os.Interrupt)
		<-c
		controller.Terminate()
	}()

	go func() {
		for {
			call := <-controller.Ingest
			controller.IngestCall(call)
		}
	}()

	// Keep the unscoped search metadata (dateStart/dateStop/count) warm so the
	// first user hit never waits on a cold count(*) over the whole table.
	go func() {
		controller.Calls.WarmSearchMeta(controller.Database)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			controller.Calls.WarmSearchMeta(controller.Database)
		}
	}()

	go func() {
		const (
			minTimeout = 3
			maxTimeout = 15
		)

		var (
			timeout time.Duration = minTimeout
			timer   *time.Timer
		)

		doClientsCount := func() {
			if timer != nil {
				timer.Stop()

				timeout++
				if timeout > maxTimeout {
					timeout = maxTimeout
				}
			}

			timer = time.AfterFunc(timeout*time.Second, func() {
				timer = nil
				timeout = minTimeout

				controller.LogClientsCount()

				if controller.Options.ShowListenersCount {
					controller.Clients.EmitListenersCount()
				}
			})
		}

		for {
			select {
			case client := <-controller.Register:
				controller.Clients.Add(client)
				doClientsCount()

			case client := <-controller.Unregister:
				controller.Clients.Remove(client)
				doClientsCount()
			}
		}
	}()

	controller.Dirwatches.Start(controller)

	return nil
}

func (controller *Controller) Terminate() {
	controller.Dirwatches.Stop()

	// Stop plugins before closing the database — a shutdown handler that wants
	// to flush state needs its tables to still be reachable.
	controller.Plugins.Stop()

	if err := controller.Database.Sql.Close(); err != nil {
		log.Println(err)
	}

	log.Println("terminated")

	os.Exit(0)
}
