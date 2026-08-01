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
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// pluginRequestMaxBody bounds what a plugin route will read from a client.
const pluginRequestMaxBody = 8 << 20 // 8 MiB

// PluginApiHandler serves /api/plugin/<id>/<path>, dispatching to whichever
// route the named plugin registered.
func (controller *Controller) PluginApiHandler(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/plugin/")

	parts := strings.SplitN(rest, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "no plugin specified", http.StatusNotFound)
		return
	}

	pluginId := parts[0]
	routePath := ""
	if len(parts) > 1 {
		routePath = strings.Trim(parts[1], "/")
	}

	plugin, ok := controller.Plugins.Get(pluginId)
	if !ok || !plugin.Enabled || !plugin.Running || plugin.runtime == nil {
		http.Error(w, "plugin not available", http.StatusNotFound)
		return
	}

	for _, route := range plugin.runtime.Routes() {
		if route.absolute {
			continue
		}
		if route.method != "" && route.method != r.Method {
			continue
		}
		if route.path != routePath {
			continue
		}

		controller.servePluginRoute(w, r, plugin, route)
		return
	}

	http.Error(w, "no such plugin route", http.StatusNotFound)
}

// coreHttpPatterns are the paths the server registers itself. A plugin cannot
// claim one of these: DefaultServeMux panics on a duplicate pattern, and even
// if it didn't, letting a plugin shadow a core endpoint would break the
// protocol for every client.
//
// This is the list core actually registers in main.go. When an endpoint's
// handling moves out of core into a plugin, it comes off this list.
var coreHttpPatterns = map[string]bool{
	"/":                                 true,
	"/api/admin/config":                 true,
	"/api/admin/login":                  true,
	"/api/admin/logout":                 true,
	"/api/admin/logs":                   true,
	"/api/admin/password":               true,
	"/api/admin/plugins":                true,
	"/api/admin/plugins/":               true,
	"/api/admin/stats":                  true,
	"/api/admin/stats/talkgroup-units":  true,
	"/api/admin/transcribe":             true,
	"/api/admin/update/apply":           true,
	"/api/admin/update/cancel":          true,
	"/api/admin/update/check":           true,
	"/api/admin/update/download":        true,
	"/api/admin/update/source":          true,
	"/api/admin/updates":                true,
	"/api/admin/user-add":               true,
	"/api/admin/user-remove":            true,
	"/api/call-transcript":              true,
	"/api/call-upload":                  true,
	"/api/capabilities":                 true,
	"/api/plugin/":                      true,
	"/api/stats":                        true,
	"/api/stats/talkgroup-units":        true,
	"/api/trunk-recorder-call-upload":   true,
	"/api/v1/calls":                     true,
	"/api/v1/calls/":                    true,
	"/plugins/":                         true,
}

// ServePluginAbsoluteRoute handles a path a plugin claimed outright, and
// reports whether it did. Called from the catch-all handler, so a claim takes
// effect and stops applying purely from the plugin's enabled state — no mux
// registration, nothing to unregister.
//
// A plugin can never shadow a core endpoint: those are registered on the mux
// and never reach this handler, and coreHttpPatterns rejects the claim up front
// so the attempt is visible in the log rather than silently ineffective.
func (controller *Controller) ServePluginAbsoluteRoute(w http.ResponseWriter, r *http.Request) bool {
	for _, plugin := range controller.Plugins.Enabled() {
		if plugin.runtime == nil {
			continue
		}

		for _, route := range plugin.runtime.Routes() {
			if !route.absolute || route.path != r.URL.Path {
				continue
			}

			if coreHttpPatterns[route.path] {
				controller.Logs.LogEvent(LogLevelWarn, fmt.Sprintf(
					"plugin %s cannot claim %s: the server already serves that path",
					plugin.PluginId, route.path,
				))
				continue
			}

			controller.servePluginRoute(w, r, plugin, route)
			return true
		}
	}

	return false
}

// servePluginRoute marshals the request into the shape plugins see, invokes the
// handler, and writes back whatever it returned.
func (controller *Controller) servePluginRoute(w http.ResponseWriter, r *http.Request, plugin *Plugin, route *pluginRoute) {
	body, err := io.ReadAll(io.LimitReader(r.Body, pluginRequestMaxBody))
	if err != nil {
		http.Error(w, "cannot read request body", http.StatusBadRequest)
		return
	}

	query := map[string]any{}
	for key, values := range r.URL.Query() {
		if len(values) > 0 {
			query[key] = values[0]
		}
	}

	headers := map[string]any{}
	for key, values := range r.Header {
		if len(values) > 0 {
			headers[key] = values[0]
		}
	}

	request := map[string]any{
		"method":  r.Method,
		"path":    r.URL.Path,
		"query":   query,
		"headers": headers,
		"body":    string(body),
	}

	result, err := plugin.runtime.DispatchRoute(route, request)
	if err != nil {
		controller.Logs.LogEvent(
			LogLevelError,
			fmt.Sprintf("plugin %s route %s failed: %v", plugin.PluginId, route.path, err),
		)
		http.Error(w, "plugin error", http.StatusInternalServerError)
		return
	}

	writePluginResponse(w, result)
}

// writePluginResponse turns a handler's return value into an HTTP response. A
// plugin may return nothing (204), a bare value (200 with a JSON body), or a
// {status, headers, body} object for full control.
func writePluginResponse(w http.ResponseWriter, result any) {
	if result == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	response, ok := result.(map[string]any)
	if !ok {
		writeJson(w, result)
		return
	}

	// A map without a status is a payload, not a response envelope.
	statusValue, hasStatus := numberFromMap(response, "status")
	_, hasBody := response["body"]
	if !hasStatus && !hasBody {
		writeJson(w, result)
		return
	}

	status := http.StatusOK
	if hasStatus && statusValue >= 100 && statusValue <= 599 {
		status = int(statusValue)
	}

	contentType := ""
	if headers, ok := response["headers"].(map[string]any); ok {
		for key, value := range headers {
			text := fmt.Sprintf("%v", value)
			w.Header().Set(key, text)
			if strings.EqualFold(key, "Content-Type") {
				contentType = text
			}
		}
	}

	body := response["body"]

	if body == nil {
		w.WriteHeader(status)
		return
	}

	// A string body is written as-is so a plugin can return HTML, XML or
	// anything else; everything else is serialised as JSON.
	if text, ok := body.(string); ok {
		if contentType == "" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		}
		w.WriteHeader(status)
		w.Write([]byte(text))
		return
	}

	if data, err := pluginBytes(body); err == nil && data != nil {
		if contentType == "" {
			w.Header().Set("Content-Type", "application/octet-stream")
		}
		w.WriteHeader(status)
		w.Write(data)
		return
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		http.Error(w, "plugin returned an unserialisable body", http.StatusInternalServerError)
		return
	}

	if contentType == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(status)
	w.Write(encoded)
}

// PluginAssetHandler serves /plugins/<id>/... from disk.
//
// Deliberately not go:embed'd: plugin assets arrive after the binary is built,
// which is the entire point of installing a plugin without recompiling.
func (controller *Controller) PluginAssetHandler(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/plugins/")

	// The loader is generated rather than read from disk.
	if rest == "loader.js" {
		controller.servePluginLoader(w, r)
		return
	}

	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		http.NotFound(w, r)
		return
	}

	pluginId := parts[0]
	assetPath := parts[1]

	if !pluginIdRegexp.MatchString(pluginId) {
		http.NotFound(w, r)
		return
	}

	plugin, ok := controller.Plugins.Get(pluginId)
	// Assets of a disabled plugin are not served, so turning a plugin off
	// really does stop its frontend code from loading.
	if !ok || !plugin.Enabled || !plugin.Running || plugin.dir == "" {
		http.NotFound(w, r)
		return
	}

	// Resolve inside the plugin directory and verify the result is still under
	// it — assetPath comes straight from the URL.
	cleaned := path.Clean("/" + assetPath)
	target := filepath.Join(plugin.dir, filepath.FromSlash(strings.TrimPrefix(cleaned, "/")))

	resolvedDir, err := filepath.Abs(plugin.dir)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	resolvedTarget, err := filepath.Abs(target)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if resolvedTarget != resolvedDir && !strings.HasPrefix(resolvedTarget, resolvedDir+string(os.PathSeparator)) {
		http.NotFound(w, r)
		return
	}

	// The manifest is metadata, not an asset; serving it would leak whatever a
	// plugin author left in it.
	if strings.EqualFold(filepath.Base(resolvedTarget), PluginManifestName) {
		http.NotFound(w, r)
		return
	}

	info, err := os.Stat(resolvedTarget)
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}

	content, err := os.ReadFile(resolvedTarget)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	contentType := mime.TypeByExtension(filepath.Ext(resolvedTarget))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	// Match the webapp handler's treatment of scripts, which sets an explicit
	// charset rather than relying on the OS mime table.
	if strings.HasSuffix(resolvedTarget, ".js") {
		contentType = "text/javascript; charset=utf-8"
	}

	w.Header().Set("Content-Type", contentType)
	// Plugin assets are not content-hashed, and a plugin can be updated in
	// place, so they must revalidate rather than be cached indefinitely the way
	// the webapp's hashed bundles are.
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(content)
}

// servePluginLoader emits the small script that boots plugin frontend code.
//
// Generated rather than shipped so it always reflects exactly which plugins are
// enabled right now, and so an install with no plugins serves an empty file
// instead of 404-ing a script tag in index.html.
func (controller *Controller) servePluginLoader(w http.ResponseWriter, r *http.Request) {
	entries := controller.PluginWebEntries()

	encoded, err := json.Marshal(entries)
	if err != nil {
		encoded = []byte("[]")
	}

	script := fmt.Sprintf(`// Generated by Rdio Scanner. Lists the frontend entry points of enabled plugins.
(function () {
    window.rdioScannerPluginEntries = %s;
    window.dispatchEvent(new CustomEvent('rdio-scanner-plugin-entries', {
        detail: window.rdioScannerPluginEntries
    }));
})();
`, string(encoded))

	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write([]byte(script))
}
