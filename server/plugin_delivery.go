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

import "time"

// Delivery — the only genuinely hot path in the server.
//
// Everything before this runs once per call. These points can run once per
// listener per call, so on a busy server with a few hundred listeners the same
// handler is entered hundreds of times a second. Three things follow from that,
// and they are the reason this file exists rather than the dispatches being
// inlined at each call site:
//
// Timeouts are 250ms rather than 30s. A handler that is merely slow would
// otherwise stall every listener's feed rather than its own.
//
// Nothing is dispatched per listener that does not have to be. call.payload
// shapes the JSON of a call, which is the same for everyone, so it runs once per
// call and the result is reused. Only call.emit, which answers a question about
// one particular listener, runs per listener.
//
// And no dispatch happens while the clients lock is held. Clients.EmitCall
// iterates under a read lock; entering a plugin runtime inside that loop would
// hold the lock for the duration of every handler, blocking connects and
// disconnects behind the slowest plugin on the server.

// FilterCallPayload lets plugins shape the JSON a call is delivered as.
//
// Runs once per call, not once per listener. The payload of a call is the same
// for everyone receiving it, so dispatching per listener would multiply the cost
// by the listener count to recompute an identical answer. A plugin that genuinely
// needs to say something different to one client has rdio.ws for it.
//
// Returned fields are merged into the call's plugin fields rather than replacing
// the payload, so a plugin cannot remove the id or the audio and leave the
// webapp with a call it cannot play.
func (dispatch *PluginDispatch) FilterCallPayload(call *Call) {
	if !dispatch.Active(PointCallPayload) {
		return
	}

	value := pluginCallValue(call, false)

	dispatch.Notify(PointCallPayload, value)

	filtered, ok := dispatch.Filter(PointCallPayload, value, pointTimeout(PointCallPayload))
	if !ok {
		return
	}

	fields, isMap := filtered.(map[string]any)
	if !isMap {
		return
	}

	for name, entry := range fields {
		// The call's own fields are delivered by the marshaller already. Copying
		// them into the plugin map would emit each one twice, and the second
		// copy would win.
		if _, reserved := pluginCallFields[name]; reserved {
			continue
		}
		if name == "id" || name == "audioSize" || name == "drop" {
			continue
		}

		if call.pluginFields == nil {
			call.pluginFields = map[string]any{}
		}
		call.pluginFields[name] = entry
	}
}

// ShouldEmit asks whether one listener should receive one call.
//
// Called outside the clients lock, once per candidate listener, and only when a
// plugin is actually registered — the native access and livefeed checks have
// already run by this point, so this decides nothing that rdio could decide
// itself.
func (dispatch *PluginDispatch) ShouldEmit(call *Call, client *Client) bool {
	if !dispatch.Active(PointCallEmit) {
		return true
	}

	value := pluginCallValue(call, false)
	value["client"] = pluginClientValue(client)

	dispatch.Notify(PointCallEmit, value)

	_, keep := dispatch.Filter(PointCallEmit, value, pointTimeout(PointCallEmit))

	return keep
}

// pluginClientValue describes a listener to a plugin.
//
// Deliberately small: this is built once per listener per call on the hottest
// path in the server, so every field here is paid for hundreds of times a
// second on a busy install.
func pluginClientValue(client *Client) map[string]any {
	if client == nil {
		return map[string]any{}
	}

	value := map[string]any{
		"ip":      client.GetRemoteAddr(),
		"overlay": client.Overlay,
	}

	if client.Access != nil {
		value["ident"] = client.Access.Ident
		value["code"] = client.Access.Code
	}

	return value
}

// FilterDelay lets a plugin decide how long a call is held before listeners see
// it, or release it immediately.
//
// Runs once per call. `seconds` is what rdio's own per-system and per-talkgroup
// configuration produced; returning a different number overrides it, and zero
// releases the call at once.
func (dispatch *PluginDispatch) FilterDelay(call *Call, seconds uint) uint {
	if !dispatch.Active(PointCallDelay) {
		return seconds
	}

	value := pluginCallValue(call, false)
	value["delaySeconds"] = seconds

	dispatch.Notify(PointCallDelay, value)

	filtered, ok := dispatch.Filter(PointCallDelay, value, pointTimeout(PointCallDelay))
	if !ok {
		// A veto here means "do not hold it", not "do not send it". Dropping a
		// call is what the ingest points are for; this point only answers when.
		return 0
	}

	fields, isMap := filtered.(map[string]any)
	if !isMap {
		return seconds
	}

	if updated, ok := pluginUint(fields["delaySeconds"]); ok {
		return updated
	}

	return seconds
}

// ShouldSendDownstream asks whether a call should be forwarded to one
// downstream instance.
//
// Runs per downstream per call, which is a far smaller number than listeners —
// downstreams are configured, not connected — so this one can afford the
// default timeout.
func (dispatch *PluginDispatch) ShouldSendDownstream(call *Call, url string, disabled bool) bool {
	if !dispatch.Active(PointDownstreamSend) {
		return true
	}

	value := pluginCallValue(call, false)
	value["downstream"] = map[string]any{"url": url, "disabled": disabled}

	dispatch.Notify(PointDownstreamSend, value)

	_, keep := dispatch.Filter(PointDownstreamSend, value, pointTimeout(PointDownstreamSend))

	return keep
}

// FilterClientConfig lets a plugin change the configuration one client receives.
//
// This is how a themes plugin ships its settings, and how anything that varies
// by listener — a per-subscriber branding, a feature flag — reaches the webapp.
// Runs once per config, which is once per connect plus once per reconfiguration,
// not per call.
func (dispatch *PluginDispatch) FilterClientConfig(payload map[string]any, client *Client) map[string]any {
	if !dispatch.Active(PointClientConfig) {
		return payload
	}

	value := map[string]any{
		"config": payload,
		"client": pluginClientValue(client),
	}

	dispatch.Notify(PointClientConfig, value)

	filtered, ok := dispatch.Filter(PointClientConfig, value, pointTimeout(PointClientConfig))
	if !ok {
		return payload
	}

	fields, isMap := filtered.(map[string]any)
	if !isMap {
		return payload
	}

	updated, isMap := fields["config"].(map[string]any)
	if !isMap {
		return payload
	}

	// The keys the webapp cannot run without. A plugin returning a config that
	// dropped these would leave a client with no systems and no explanation, so
	// they are restored from the original rather than trusted to the result.
	for _, required := range []string{"groups", "systems", "tags"} {
		if _, present := updated[required]; !present {
			updated[required] = payload[required]
		}
	}

	return updated
}

// NotifyClient reports a listener connecting or disconnecting.
func (dispatch *PluginDispatch) NotifyClient(point string, client *Client) {
	if !dispatch.Active(point) {
		return
	}

	dispatch.Notify(point, pluginClientValue(client))
}

// NotifyEmitted reports that a call finished going out to live listeners, with
// how many of them received it.
func (dispatch *PluginDispatch) NotifyEmitted(call *Call, recipients int) {
	if !dispatch.Active(PointCallEmitted) {
		return
	}

	value := pluginCallValue(call, false)
	value["recipients"] = recipients

	dispatch.Notify(PointCallEmitted, value)
}

// pluginDeliveryTimeout is the ceiling for the per-listener points, kept here
// beside the reasoning rather than only in the timeout table.
const pluginDeliveryTimeout = 250 * time.Millisecond
