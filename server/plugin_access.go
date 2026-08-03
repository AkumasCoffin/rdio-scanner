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
	"time"
)

// Authentication, opened up.
//
// Two verbs are in play at every point here, and the split is what makes an
// external auth system possible rather than merely an external auth check:
//
//	provide — rdio does not recognise this credential. A plugin may say who it
//	          belongs to and what it may see, which is how LDAP, OIDC, a shared
//	          subscriber database, or anything else becomes the source of truth
//	          without rdio knowing those words.
//	filter  — rdio does recognise it. A plugin may narrow the scope, or refuse
//	          it outright with {drop: true}.
//
// Provide only runs when the local lookup found nothing, so adding an auth
// plugin never weakens the accounts already configured. Filter always runs, so
// a plugin can tighten but the local configuration is what it tightens from.
//
// A plugin that throws or times out is skipped and the local answer stands.
// That direction is deliberate: an auth plugin failing open would be a security
// hole, and failing closed would lock everyone out of a server whose own
// accounts are fine. Neither — the plugin simply does not get a say.

// pluginAccessValue is the shape an access grant takes in both directions.
//
// `systems` is handed over already unmarshalled, matching what Accesses.Read
// produces, because that is what HasAccess reads. Handing over the raw JSON
// string the database column holds would make every plugin parse it, and a
// plugin echoing it back unchanged would produce an access that silently
// matches nothing.
func pluginAccessValue(access *Access, code string, remoteAddr string, found bool) map[string]any {
	value := map[string]any{
		"code":  code,
		"ip":    remoteAddr,
		"found": found,
	}

	if access != nil {
		value["ident"] = access.Ident
		value["systems"] = access.Systems
		value["limit"] = access.Limit

		if when, ok := access.Expiration.(time.Time); ok && !when.IsZero() {
			value["expiration"] = when.UTC().Format(time.RFC3339)
		}
	}

	return value
}

// normalizeJsonNumbers rewrites integers as float64 throughout a structure.
//
// Everything that reads a systems grant — Access.HasAccess, Apikey.HasAccess,
// the config scoping — was written against values that came out of
// encoding/json, where every number is a float64. A JavaScript runtime hands
// back an int64 for a literal, so a plugin returning the perfectly reasonable
// [{id: 1, talkgroups: '*'}] produces a grant that type-switches to nothing and
// matches no system at all.
//
// The failure is silent and reads as a denial rather than an error, which is
// the worst possible shape for an auth bug: the plugin looks like it worked,
// the listener is admitted, and they simply see an empty server.
func normalizeJsonNumbers(value any) any {
	switch v := value.(type) {
	case []any:
		out := make([]any, len(v))
		for i, entry := range v {
			out[i] = normalizeJsonNumbers(entry)
		}
		return out

	case map[string]any:
		out := make(map[string]any, len(v))
		for key, entry := range v {
			out[key] = normalizeJsonNumbers(entry)
		}
		return out

	case int64:
		return float64(v)
	case int:
		return float64(v)
	case uint:
		return float64(v)
	case uint64:
		return float64(v)
	case int32:
		return float64(v)
	}

	return value
}

// applyPluginAccess writes a plugin's answer onto an access grant.
//
// Numbers are read through pluginUint rather than a float64 type switch, which
// is what Access.FromMap does. FromMap exists to decode JSON, where every number
// is a float64; a JavaScript runtime hands back an int64 for a literal, so
// reusing it here would silently ignore every limit a plugin ever set.
func applyPluginAccess(access *Access, fields map[string]any) {
	if ident, ok := fields["ident"].(string); ok && ident != "" {
		access.Ident = ident
	}

	if systems, present := fields["systems"]; present {
		switch v := systems.(type) {
		case string:
			access.Systems = v
		case []any:
			access.Systems = normalizeJsonNumbers(v)
		}
	}

	if limit, ok := pluginUint(fields["limit"]); ok && limit > 0 {
		access.Limit = limit
	}

	if expiration, ok := fields["expiration"].(string); ok {
		if when, err := time.Parse(time.RFC3339, expiration); err == nil {
			access.Expiration = when.UTC()
		}
	}
}

// cloneAccess copies a grant so per-client changes stay per client.
//
// The pointer GetAccess returns is the live entry in Accesses.List, shared by
// every client using that code. Applying a plugin's answer to it directly would
// rewrite the scope for all of them — and, worse, the change would outlive the
// connection, so one client's narrowed view would quietly become everyone's
// until the next reload.
func cloneAccess(access *Access) *Access {
	if access == nil {
		return nil
	}

	copied := *access

	// Systems is held by reference, and deeply so: the slice contains maps
	// describing each system. Copying only the slice left those maps shared, so
	// `value.systems[0].talkgroups = [...]` in a filter still wrote through to
	// the table entry — the exact leak this function exists to stop, one level
	// further down than the original fix reached.
	copied.Systems = clonePluginValue(access.Systems)

	return &copied
}

// cloneApikey does the same for an upload key.
//
// GetApikey returns the live entry from Apikeys.List, and unlike accesses this
// was being handed straight to a filter and written back to. A filter narrowing
// one upload's systems rewrote the key for every later upload until the table
// reloaded — and because uploads are served on independent HTTP goroutines, the
// write raced concurrent readers of the same field.
func cloneApikey(apikey *Apikey) *Apikey {
	if apikey == nil {
		return nil
	}

	copied := *apikey
	copied.Systems = clonePluginValue(apikey.Systems)

	return &copied
}

// CheckAccess settles whether an access code is good and what it grants.
//
// `found` is what rdio's own table produced, or nil. Reports the access to use
// and whether the client is allowed in at all.
func (dispatch *PluginDispatch) CheckAccess(code string, remoteAddr string, found *Access) (*Access, bool) {
	if !dispatch.Active(PointAccessCheck) {
		return found, found != nil
	}

	access := cloneAccess(found)

	if access == nil {
		// rdio does not know this code. Ask whether anything else does.
		result, ok := dispatch.Provide(
			PointAccessCheck,
			pluginAccessValue(nil, code, remoteAddr, false),
			pointTimeout(PointAccessCheck),
		)

		if !ok {
			return nil, false
		}

		fields, isMap := result.(map[string]any)
		if !isMap {
			return nil, false
		}

		// A provider that answers without granting anything is refusing, not
		// granting everything. NewAccess defaults Systems to "*", so building on
		// it without an explicit scope would hand a stranger the whole server.
		if _, present := fields["systems"]; !present {
			return nil, false
		}

		access = &Access{Ident: "plugin", Code: code}
		applyPluginAccess(access, fields)
	}

	value := pluginAccessValue(access, code, remoteAddr, found != nil)

	dispatch.Notify(PointAccessCheck, value)

	filtered, allowed := dispatch.Filter(PointAccessCheck, value, pointTimeout(PointAccessCheck))
	if !allowed {
		return nil, false
	}

	if fields, ok := filtered.(map[string]any); ok {
		applyPluginAccess(access, fields)
	}

	return access, true
}

// ScopeAccess narrows what a client may see.
//
// Distinct from CheckAccess because it also runs on a server with no access
// codes at all, where there is no credential to check but there is still a
// question of what this particular listener should be shown.
// Returns the access to use. Always a copy when anything is registered, because
// the caller's access may be the shared table entry and a per-client scope must
// not leak into it.
func (dispatch *PluginDispatch) ScopeAccess(access *Access, remoteAddr string, restricted bool) *Access {
	if access == nil || !dispatch.Active(PointAccessScope) {
		return access
	}

	scoped := cloneAccess(access)

	value := pluginAccessValue(scoped, scoped.Code, remoteAddr, restricted)
	value["restricted"] = restricted

	dispatch.Notify(PointAccessScope, value)

	filtered, ok := dispatch.Filter(PointAccessScope, value, pointTimeout(PointAccessScope))
	if !ok {
		// A veto here means "show this client nothing" rather than "disconnect
		// it", because scope is a question about visibility, not admission.
		scoped.Systems = []any{}
		return scoped
	}

	if fields, isMap := filtered.(map[string]any); isMap {
		applyPluginAccess(scoped, fields)
	}

	return scoped
}

// CheckApikey settles whether an upload key is good.
//
// Same shape as CheckAccess: provide covers a key rdio has never heard of,
// filter narrows or refuses one it has.
func (dispatch *PluginDispatch) CheckApikey(key string, call *Call, found *Apikey) (*Apikey, bool) {
	if !dispatch.Active(PointApikeyCheck) {
		return found, found != nil
	}

	apikey := cloneApikey(found)

	value := map[string]any{
		"key":       key,
		"found":     found != nil,
		"system":    call.System,
		"talkgroup": call.Talkgroup,
	}

	if apikey != nil {
		value["ident"] = apikey.Ident
		value["systems"] = apikey.Systems
		value["disabled"] = apikey.Disabled
	}

	if apikey == nil {
		result, ok := dispatch.Provide(PointApikeyCheck, value, pointTimeout(PointApikeyCheck))
		if !ok {
			return nil, false
		}

		fields, isMap := result.(map[string]any)
		if !isMap {
			return nil, false
		}

		// Same reasoning as access: no scope means no grant.
		if _, present := fields["systems"]; !present {
			return nil, false
		}

		apikey = &Apikey{Key: key, Ident: "plugin"}

		if ident, ok := fields["ident"].(string); ok && ident != "" {
			apikey.Ident = ident
		}

		switch v := fields["systems"].(type) {
		case string:
			apikey.Systems = v
		case []any:
			apikey.Systems = normalizeJsonNumbers(v)
		}

		value["ident"] = apikey.Ident
		value["systems"] = apikey.Systems
		value["found"] = true
	}

	dispatch.Notify(PointApikeyCheck, value)

	filtered, allowed := dispatch.Filter(PointApikeyCheck, value, pointTimeout(PointApikeyCheck))
	if !allowed {
		return nil, false
	}

	if fields, ok := filtered.(map[string]any); ok {
		if ident, ok := fields["ident"].(string); ok && ident != "" {
			apikey.Ident = ident
		}
		switch v := fields["systems"].(type) {
		case string:
			apikey.Systems = v
		case []any:
			apikey.Systems = normalizeJsonNumbers(v)
		}
		// The payload has always carried `disabled` and nothing read it back,
		// so a plugin returning {disabled: true} had no effect at all — while
		// the caller gates uploads on exactly that field. Honoured now, in the
		// one direction that is safe: a plugin may turn a key off, never on.
		// Re-enabling a key an operator disabled is not a plugin's decision.
		if disabled, ok := fields["disabled"].(bool); ok && disabled {
			apikey.Disabled = true
		}
	}

	return apikey, true
}

// CheckAdmin settles an admin login.
//
// The password reaches a provider, and nothing else. An external directory
// cannot verify a credential it is not given, so a provider needs it; observers
// and filters cannot use it and no longer see it. It is never logged, here or
// anywhere downstream.
//
// `passed` is whether rdio's own bcrypt compare succeeded. Provide only runs
// when it did not, so an auth plugin cannot lock out the local password; filter
// always runs, so a plugin can add a second factor or an address restriction on
// top of it.
func (dispatch *PluginDispatch) CheckAdmin(password string, remoteAddr string, passed bool) bool {
	if !dispatch.Active(PointAdminCheck) {
		return passed
	}

	// The password goes only to a provider, and only when the local check has
	// already failed.
	//
	// A provider needs it: verifying a credential against an external directory
	// is the entire reason that verb exists there, and it cannot be done with a
	// hash. Nothing else does. Observers and filters were being handed the
	// plaintext admin password too, which made three lines of JavaScript in any
	// installed plugin enough to exfiltrate it — for a capability none of them
	// could use.
	granted := passed

	if !granted {
		attempt := map[string]any{
			"password": password,
			"ip":       remoteAddr,
			"passed":   false,
		}

		if result, ok := dispatch.Provide(PointAdminCheck, attempt, pointTimeout(PointAdminCheck)); ok {
			if fields, isMap := result.(map[string]any); isMap {
				granted, _ = fields["ok"].(bool)
			}
		}
	}

	if !granted {
		return false
	}

	value := map[string]any{
		"ip":     remoteAddr,
		"passed": true,
	}

	dispatch.Notify(PointAdminCheck, value)

	if _, allowed := dispatch.Filter(PointAdminCheck, value, pointTimeout(PointAdminCheck)); !allowed {
		dispatch.controller.Logs.LogEvent(LogLevelWarn, fmt.Sprintf(
			"admin login refused by plugin for ip %s", remoteAddr,
		))
		return false
	}

	return true
}
