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
	Scheduler   *Scheduler
	Stats       *Stats
	Systems     *Systems
	Tags        *Tags
	Transcriber         *Transcriber
	PendingTranscripts  *PendingTranscripts
	FallbackTranscripts *FallbackTranscripts
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
	controller.Scheduler = NewScheduler(controller)
	controller.Stats = NewStats(controller)
	controller.Transcriber = NewTranscriber(controller)
	controller.PendingTranscripts = NewPendingTranscripts()
	controller.FallbackTranscripts = NewFallbackTranscripts()

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
		system.Transcribe = true

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
				GroupId:    groupId,
				Id:         call.Talkgroup,
				Label:      fmt.Sprintf("%d", call.Talkgroup),
				TagId:      tagId,
				Transcribe: true,
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

		if call.transcriptPending {
			controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf("call from upstream with pending transcript: system=%v talkgroup=%v id=%v (awaiting /api/call-transcript push)", call.System, call.Talkgroup, id))
		}

		// Pick up any transcript that raced ahead of this call. Common case:
		// upstream's small JSON push beat its own large multipart upload on
		// the wire, so CallTranscriptHandler stashed the transcript instead
		// of 404-ing it.
		if held, heldIdent, ok := controller.PendingTranscripts.Take(call.System, call.Talkgroup, call.DateTime); ok {
			if err := controller.Calls.UpdateTranscript(id, held, controller.Database); err != nil {
				controller.Logs.LogEvent(LogLevelError, fmt.Sprintf("transcript apply from pending failed: id=%v %v", id, err))
			} else {
				call.Transcript = held
				controller.Clients.EmitTranscript(id, call.System, call.Talkgroup, held, controller.Accesses.IsRestricted())
				controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf("transcript applied from pending: [%v] system=%v talkgroup=%v id=%v (%d chars)", heldIdent, call.System, call.Talkgroup, id, len(held)))
				// Cancel any fallback timer that might have been scheduled by
				// an earlier code path (defensive — usually the schedule
				// below sees call.Transcript already set and never fires).
				controller.FallbackTranscripts.Cancel(id)
				// Chain-forward to our own downstreams so multi-hop setups
				// (A -> B -> C) propagate the transcript past us. Runs in its
				// own goroutine inside Downstreams.SendTranscript via per-
				// downstream HTTP, so it doesn't block ingest.
				go controller.Downstreams.SendTranscript(controller, call)
			}
		}

		// Hint to downstream instances that this server will transcribe the call
		// and forward the result. Only set when transcription is *actually*
		// going to be attempted here — feature enabled, system+talkgroup opted
		// in, and the audio passes the same size predicates TranscribeCallAsync
		// uses to decide whether to dispatch the goroutine. Otherwise the
		// downstream would wait for a transcript that's never coming (audio
		// too short, no Whisper attempt fires, no push is sent).
		//
		// Runtime skips (all Groq keys paused on 429, or all at per-key cap)
		// can't be predicted here — those are caught by the receiver's
		// pending-transcripts cache TTL.
		if system.Transcribe && talkgroup.Transcribe && controller.Transcriber.Enabled() {
			audioLen := uint(len(call.Audio))
			minBytes := uint(45) // hard floor: <=44 = no real audio in Transcribe()
			if cfgMin := controller.Options.TranscriptionMinAudioBytes; cfgMin > minBytes {
				minBytes = cfgMin
			}
			if audioLen >= minBytes {
				call.transcriptWillForward = true
			}
		}

		// Fire downstream forwarding immediately — Delayer below only holds
		// the local listener emit. Forwarding before the delay means
		// downstreams receive calls at near-real-time and the transcript-push
		// race (where small JSON beats large multipart) loses its head start.
		controller.EmitCallToDownstreams(call)

		controller.Delayer.Delay(call)

		// transcriptPending means an upstream server sent this call and will push
		// the transcript separately — skip local transcription to avoid doing
		// the same work twice. Schedule a fallback timer so that if the
		// upstream's push never arrives (Whisper failed on its side,
		// network broke, etc.), we transcribe locally after fallbackTranscriptTTL
		// instead of leaving the call permanently untranscribed.
		if system.Transcribe && talkgroup.Transcribe {
			if call.transcriptPending {
				controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf("local transcription skipped: system=%v talkgroup=%v id=%v (deferred to upstream)", call.System, call.Talkgroup, id))

				// Only arm a fallback if this server is actually capable of
				// transcribing locally (transcription enabled + key/url
				// configured + audio passes the size predicate). And only if
				// we don't already have a transcript from the pending-cache
				// hit above — in that case there's nothing to fall back from.
				if controller.Transcriber.Enabled() && call.Transcript == nil {
					audioLen := uint(len(call.Audio))
					minBytes := uint(45)
					if cfgMin := controller.Options.TranscriptionMinAudioBytes; cfgMin > minBytes {
						minBytes = cfgMin
					}
					if audioLen >= minBytes {
						fallbackId := id
						controller.FallbackTranscripts.Schedule(fallbackId, func() {
							// Refetch the call from DB — call.Audio in the
							// closure would keep the original audio blob alive
							// in memory for 2 min unnecessarily; the DB already
							// has it.
							refreshed, err := controller.Calls.GetCall(fallbackId, controller.Database)
							if err != nil {
								controller.Logs.LogEvent(LogLevelWarn, fmt.Sprintf("fallback transcription: cannot refetch call id=%v: %v", fallbackId, err))
								return
							}
							if refreshed == nil {
								// Call was pruned — nothing to do.
								return
							}
							// Skip if a transcript was applied between the
							// timer firing and the refetch (race window).
							if t, ok := refreshed.Transcript.(string); ok && t != "" {
								return
							}
							controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf("fallback transcription firing: id=%v system=%v talkgroup=%v (upstream transcript never arrived, running local Whisper)", fallbackId, refreshed.System, refreshed.Talkgroup))
							controller.Transcriber.TranscribeCallAsync(fallbackId, refreshed)
						})
						controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf("fallback transcription scheduled: id=%v (will run local Whisper in %v if upstream transcript hasn't arrived)", id, fallbackTranscriptTTL))
					}
				}
			} else {
				controller.Transcriber.TranscribeCallAsync(id, call)
			}
		}

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
		client.Send <- &Message{Command: MessageCommandPin}

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

	} else if message.Command == MessageCommandTranscript {
		if err := controller.ProcessMessageCommandTranscript(client, message); err != nil {
			return err
		}
	}

	return nil
}

func (controller *Controller) ProcessMessageCommandTranscript(client *Client, message *Message) error {
	var (
		err error
		i   int
		id  uint
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

	if id == 0 {
		return nil
	}

	system, talkgroup, transcript, err := controller.Calls.GetTranscript(id, controller.Database)
	if err != nil {
		return err
	}

	if controller.Accesses.IsRestricted() {
		probe := &Call{System: system, Talkgroup: talkgroup}
		if !client.Access.HasAccess(probe) {
			return nil
		}
	}

	client.Send <- &Message{
		Command: MessageCommandTranscript,
		Payload: map[string]any{"id": id, "transcript": transcript},
	}
	return nil
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
		return err
	}

	if !controller.Accesses.IsRestricted() || client.Access.HasAccess(call) {
		client.Send <- &Message{Command: MessageCommandCall, Payload: call, Flag: message.Flag}
	}

	return nil
}

func (controller *Controller) ProcessMessageCommandListCall(client *Client, message *Message) error {
	switch v := message.Payload.(type) {
	case map[string]any:
		searchOptions := CallsSearchOptions{searchPatchedTalkgroups: controller.Options.SearchPatchedTalkgroups}
		searchOptions.fromMap(v)
		if searchResults, err := controller.Calls.Search(&searchOptions, client); err == nil {
			client.Send <- &Message{Command: MessageCommandListCall, Payload: searchResults}
		} else {
			return fmt.Errorf("controller.processmessage.commandlistcall: %v", err)
		}
	}
	return nil
}

func (controller *Controller) ProcessMessageCommandLivefeedMap(client *Client, message *Message) {
	client.Livefeed.FromMap(message.Payload)
	client.Send <- &Message{Command: MessageCommandLivefeedMap, Payload: !client.Livefeed.IsAllOff()}
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
			client.Send <- &Message{Command: MessageCommandPin}
			return nil
		}

		if controller.Accesses.IsRestricted() {
			code := string(b)
			if access, ok := controller.Accesses.GetAccess(code); ok {
				client.Access = access
			} else {
				controller.Logs.LogEvent(LogLevelWarn, fmt.Sprintf("invalid access code %s for ip %s", code, client.GetRemoteAddr()))
				client.Send <- &Message{Command: MessageCommandPin}
				return nil
			}

			if client.AuthCount == maxAuthCount {
				controller.Logs.LogEvent(LogLevelWarn, fmt.Sprintf("locked access for ident %s locked", client.Access.Ident))
				client.Send <- &Message{Command: MessageCommandPin}
				return nil
			}

			if client.Access.HasExpired() {
				controller.Logs.LogEvent(LogLevelWarn, fmt.Sprintf("expired access for ident %s", client.Access.Ident))
				client.Send <- &Message{Command: MessageCommandExpired}
				return nil
			}

			switch v := client.Access.Limit.(type) {
			case uint:
				if controller.Clients.AccessCount(client) > int(v) {
					controller.Logs.LogEvent(LogLevelWarn, fmt.Sprintf("too many concurrent connections for ident %s, limit is %d", client.Access.Ident, client.Access.Limit))
					client.Send <- &Message{Command: MessageCommandMax}
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

	client.Send <- &Message{Command: MessageCommandVersion, Payload: p}
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

	if err := controller.Database.Sql.Close(); err != nil {
		log.Println(err)
	}

	log.Println("terminated")

	os.Exit(0)
}
