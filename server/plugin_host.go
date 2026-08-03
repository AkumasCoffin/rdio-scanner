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
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sync"
	"time"
)

// Host services a plugin needs to implement a server-to-server protocol.
//
// These exist because moving a feature out of core does not move the protocol
// it speaks: a plugin still has to authenticate an inbound push against the
// server's API keys, find the call a push refers to, and forward on to
// downstreams. Doing any of that from JavaScript would mean handing the plugin
// the server's credentials, so the host does it instead.

const (
	// pluginFeatureCacheTTL is how long a downstream's advertised capabilities
	// are trusted before re-probing. An hour lets a peer that gains a feature
	// be picked up without a restart, while keeping the probe off the hot path.
	pluginFeatureCacheTTL = time.Hour

	pluginForwardTimeout = 30 * time.Second
	pluginProbeTimeout   = 10 * time.Second
)

type pluginFeatureEntry struct {
	supported bool
	checked   time.Time
}

// PluginFeatureCache remembers which features each downstream advertises.
//
// Generalised from the transcript-specific probe that used to live on
// Downstream: the feature name is now data, so any plugin speaking any
// server-to-server protocol gets the same caching.
type PluginFeatureCache struct {
	mutex   sync.Mutex
	entries map[string]pluginFeatureEntry
}

func NewPluginFeatureCache() *PluginFeatureCache {
	return &PluginFeatureCache{entries: map[string]pluginFeatureEntry{}}
}

func (cache *PluginFeatureCache) get(key string) (bool, bool) {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()

	entry, ok := cache.entries[key]
	if !ok || time.Since(entry.checked) >= pluginFeatureCacheTTL {
		return false, false
	}

	return entry.supported, true
}

func (cache *PluginFeatureCache) put(key string, supported bool) {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()

	cache.entries[key] = pluginFeatureEntry{supported: supported, checked: time.Now()}
}

// downstreamSupportsFeature reports whether a peer advertises a feature on
// /api/capabilities. A network failure is reported as "no" but not cached, so a
// peer that is briefly unreachable is retried rather than written off for an
// hour.
func (controller *Controller) downstreamSupportsFeature(downstream *Downstream, feature string) bool {
	if feature == "" {
		return true
	}

	cacheKey := downstream.Url + "|" + feature

	if supported, ok := controller.pluginFeatures.get(cacheKey); ok {
		return supported
	}

	u, err := url.Parse(downstream.Url)
	if err != nil {
		return false
	}
	u.Path = path.Join(u.Path, "/api/capabilities")

	response, err := (&http.Client{Timeout: pluginProbeTimeout}).Get(u.String())
	if err != nil {
		return false
	}
	defer response.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))

	supported := false
	if response.StatusCode == http.StatusOK {
		var capabilities struct {
			Features []string `json:"features"`
		}
		if json.Unmarshal(body, &capabilities) == nil {
			for _, candidate := range capabilities.Features {
				if candidate == feature {
					supported = true
					break
				}
			}
		}
	}

	controller.pluginFeatures.put(cacheKey, supported)

	return supported
}

// ForwardToDownstreams POSTs a JSON body to every downstream that covers the
// given system/talkgroup.
//
// The downstream's API key is injected here rather than handed to the plugin:
// forwarding needs the credential, reading it does not, and a plugin that never
// sees it cannot leak it.
func (controller *Controller) ForwardToDownstreams(
	routePath string,
	system uint,
	talkgroup uint,
	body map[string]any,
	requireFeature string,
) []map[string]any {
	probe := &Call{System: system, Talkgroup: talkgroup}

	controller.Downstreams.mutex.Lock()
	list := make([]*Downstream, len(controller.Downstreams.List))
	copy(list, controller.Downstreams.List)
	controller.Downstreams.mutex.Unlock()

	results := []map[string]any{}

	for _, downstream := range list {
		if downstream.Disabled || !downstream.HasAccess(probe) {
			continue
		}

		if !controller.downstreamSupportsFeature(downstream, requireFeature) {
			// A peer that doesn't advertise the feature is skipped silently —
			// mixed-version fleets are normal and this is not an error.
			results = append(results, map[string]any{
				"url":     downstream.Url,
				"skipped": true,
				"reason":  "does not support " + requireFeature,
			})
			continue
		}

		result := map[string]any{"url": downstream.Url}

		u, err := url.Parse(downstream.Url)
		if err != nil {
			result["error"] = err.Error()
			results = append(results, result)
			continue
		}
		u.Path = path.Join(u.Path, routePath)

		payload := map[string]any{}
		for key, value := range body {
			payload[key] = value
		}
		payload["key"] = downstream.Apikey

		encoded, err := json.Marshal(payload)
		if err != nil {
			result["error"] = err.Error()
			results = append(results, result)
			continue
		}

		response, err := (&http.Client{Timeout: pluginForwardTimeout}).
			Post(u.String(), "application/json", bytes.NewReader(encoded))
		if err != nil {
			result["error"] = err.Error()
			results = append(results, result)
			continue
		}

		io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		response.Body.Close()

		result["status"] = response.StatusCode
		results = append(results, result)
	}

	return results
}

// PluginSystemsList returns the configured systems and talkgroups.
//
// No permission gates this: it is the same configuration every websocket client
// already receives, minus nothing. A plugin that reacts per-talkgroup needs it,
// and withholding it would only push plugins into storing a stale copy.
// PluginSystemsList is the configured systems, as a plugin sees them.
//
// It used to carry three fields per talkgroup — id, label, name — and nothing
// else, which made the most ordinary plugin task impossible: "alert on any
// talkgroup in the Fire group" needs the group, and resolving one meant falling
// back to rdio.models and joining three collections by hand.
//
// Group and tag arrive resolved to their labels as well as their ids, because
// an id is not what anyone is matching on and looking it up is the same join
// every plugin would otherwise write for itself. Units come along for the same
// reason: unit-id to alias is the commonest lookup a transcription or
// notification plugin does, and units exist nowhere else in the plugin API.
func (controller *Controller) PluginSystemsList() []map[string]any {
	systems := []map[string]any{}

	controller.Systems.mutex.Lock()
	list := make([]*System, len(controller.Systems.List))
	copy(list, controller.Systems.List)
	controller.Systems.mutex.Unlock()

	label := func(get func(any) (string, bool), id uint) string {
		if id == 0 {
			return ""
		}
		if text, ok := get(id); ok {
			return text
		}
		return ""
	}

	groupLabel := func(id any) (string, bool) {
		if group, ok := controller.Groups.GetGroup(id); ok && group != nil {
			return group.Label, true
		}
		return "", false
	}

	tagLabel := func(id any) (string, bool) {
		if tag, ok := controller.Tags.GetTag(id); ok && tag != nil {
			return tag.Label, true
		}
		return "", false
	}

	for _, system := range list {
		if system == nil {
			continue
		}

		talkgroups := []map[string]any{}
		for _, talkgroup := range system.Talkgroups.List {
			if talkgroup == nil {
				continue
			}

			talkgroups = append(talkgroups, map[string]any{
				"id":        talkgroup.Id,
				"label":     talkgroup.Label,
				"name":      talkgroup.Name,
				"groupId":   talkgroup.GroupId,
				"group":     label(groupLabel, talkgroup.GroupId),
				"tagId":     talkgroup.TagId,
				"tag":       label(tagLabel, talkgroup.TagId),
				"frequency": talkgroup.Frequency,
				"delay":     talkgroup.Delay,
				"alert":     talkgroup.Alert,
				"led":       talkgroup.Led,
				"order":     talkgroup.Order,
			})
		}

		units := []map[string]any{}
		for _, unit := range system.Units.List {
			if unit == nil {
				continue
			}
			units = append(units, map[string]any{
				"id":    unit.Id,
				"label": unit.Label,
				"order": unit.Order,
			})
		}

		systems = append(systems, map[string]any{
			"id":         system.Id,
			"label":      system.Label,
			"talkgroups": talkgroups,
			"units":      units,
			"delay":      system.Delay,
			"alert":      system.Alert,
			"led":        system.Led,
			"order":      system.Order,
		})
	}

	return systems
}

// PluginSystem answers for one system, so a plugin holding a call's system id
// does not have to scan the whole list to name it.
func (controller *Controller) PluginSystem(systemId uint) map[string]any {
	for _, system := range controller.PluginSystemsList() {
		if id, ok := jsonUint(system["id"]); ok && id == systemId {
			return system
		}
	}

	return nil
}

// PluginTalkgroup answers for one talkgroup. This is the lookup a call handler
// actually wants: call.stored carries system and talkgroup as bare integers,
// and turning them into something a person can read was an O(n) scan the plugin
// had to write.
func (controller *Controller) PluginTalkgroup(systemId uint, talkgroupId uint) map[string]any {
	system := controller.PluginSystem(systemId)
	if system == nil {
		return nil
	}

	talkgroups, _ := system["talkgroups"].([]map[string]any)
	for _, talkgroup := range talkgroups {
		if id, ok := jsonUint(talkgroup["id"]); ok && id == talkgroupId {
			return talkgroup
		}
	}

	return nil
}

// PluginUnit answers for one unit alias.
func (controller *Controller) PluginUnit(systemId uint, unitId uint) map[string]any {
	system := controller.PluginSystem(systemId)
	if system == nil {
		return nil
	}

	units, _ := system["units"].([]map[string]any)
	for _, unit := range units {
		if id, ok := jsonUint(unit["id"]); ok && id == unitId {
			return unit
		}
	}

	return nil
}

// PluginVerifyApikey checks an API key against a system/talkgroup, the same way
// a call upload is checked. Returns the key's ident on success so a plugin can
// attribute what it received.
//
// The key is supplied by the caller and only validated here — nothing about the
// server's configured keys is revealed either way.
func (controller *Controller) PluginVerifyApikey(key string, system uint, talkgroup uint) (bool, string) {
	probe := &Call{System: system, Talkgroup: talkgroup}

	apikey, ok := controller.Apikeys.GetApikey(key)
	if !ok || !apikey.HasAccess(probe) {
		return false, ""
	}

	return true, apikey.Ident
}

// PluginVerifyAdminToken validates an admin session token, so a plugin can
// protect an endpoint the admin panel calls without reimplementing the JWT
// handling or being handed the signing secret.
func (controller *Controller) PluginVerifyAdminToken(token string) bool {
	return controller.Admin.ValidateToken(token)
}

// PluginFindCallId resolves the call matching a system/talkgroup/time, which is
// how an out-of-band push identifies the record it belongs to. Returns 0 when
// there is no such call.
func (controller *Controller) PluginFindCallId(system uint, talkgroup uint, dateTime string) (uint, error) {
	parsed, err := time.Parse(time.RFC3339, dateTime)
	if err != nil {
		return 0, fmt.Errorf("dateTime must be RFC3339: %v", err)
	}

	return controller.Calls.GetIdByKey(system, talkgroup, parsed, controller.Database)
}
