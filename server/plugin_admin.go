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
	"net/http"
	"strings"
	"time"
)

// installedPluginView is what the admin panel sees for an installed plugin.
// Config values are included so the settings form can be rendered, minus
// anything the manifest marked as a password.
type installedPluginView struct {
	*Plugin
	Config map[string]any `json:"config,omitempty"`
	// RestartRequired is true when the plugin's enabled state doesn't match
	// what is actually loaded, which is the whole reason the UI shows a
	// restart banner.
	RestartRequired bool `json:"restartRequired"`
}

// PluginsHandler serves the installed-plugin list and the configured
// repositories. GET only; everything that changes state lives under
// /api/admin/plugins/.
func (admin *Admin) PluginsHandler(w http.ResponseWriter, r *http.Request) {
	if !admin.ValidateToken(admin.GetAuthorization(r)) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	controller := admin.Controller

	installed := []*installedPluginView{}

	controller.Plugins.mutex.RLock()
	list := append([]*Plugin{}, controller.Plugins.List...)
	controller.Plugins.mutex.RUnlock()

	for _, plugin := range list {
		view := &installedPluginView{
			Plugin:          plugin,
			RestartRequired: plugin.Enabled != plugin.Running,
		}

		if plugin.Manifest != nil {
			if config, err := ReadPluginConfig(controller.Database, plugin.Manifest); err == nil {
				view.Config = redactPluginConfig(plugin.Manifest, config)
			}
		}

		installed = append(installed, view)
	}

	repos := []map[string]any{}
	for _, repo := range controller.PluginStore.Repos() {
		repos = append(repos, map[string]any{
			"url":      repo.Url,
			"official": repo.IsOfficial(),
			// Report only whether a token is set. Sending it back would put a
			// credential in the browser for no reason.
			"hasToken": strings.TrimSpace(repo.Token) != "",
		})
	}

	writeJson(w, map[string]any{
		"plugins":       installed,
		"repos":         repos,
		"serverVersion": Version,
		"pluginsDir":    controller.Plugins.Dir(controller.Config),
	})
}

// redactPluginConfig blanks values the manifest declared as passwords, so a
// stored API key is never sent to the browser. The admin form treats an empty
// password field as "unchanged".
func redactPluginConfig(manifest *PluginManifest, config map[string]any) map[string]any {
	redacted := map[string]any{}

	secrets := map[string]bool{}
	for i := range manifest.Config {
		if manifest.Config[i].Type == "password" {
			secrets[manifest.Config[i].Key] = true
		}
	}

	for key, value := range config {
		if secrets[key] {
			if text, ok := value.(string); ok && text != "" {
				redacted[key] = ""
				continue
			}
		}
		redacted[key] = value
	}

	return redacted
}

// PluginsActionHandler routes the state-changing plugin endpoints. They are
// separate from the bulk config save because installing is a long, fallible
// network operation that has no business inside the config transaction.
func (admin *Admin) PluginsActionHandler(w http.ResponseWriter, r *http.Request) {
	if !admin.ValidateToken(admin.GetAuthorization(r)) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	action := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/admin/plugins/"), "/")

	switch action {
	case "branches":
		admin.pluginBranches(w, r)
	case "available":
		admin.pluginAvailable(w, r)
	case "install":
		admin.pluginInstall(w, r)
	case "toggle":
		admin.pluginToggle(w, r)
	case "uninstall":
		admin.pluginUninstall(w, r)
	case "purge":
		admin.pluginPurge(w, r)
	case "config":
		admin.pluginConfig(w, r)
	case "repos":
		admin.pluginRepos(w, r)
	default:
		writeJsonError(w, http.StatusNotFound, "unknown plugin action")
	}
}

func decodePluginBody(r *http.Request, dest any) error {
	if r.Body == nil {
		return fmt.Errorf("request has no body")
	}
	return json.NewDecoder(r.Body).Decode(dest)
}

// wantsFreshListing reports whether the caller explicitly asked to bypass the
// listing cache. Pressing Refresh should really re-fetch — otherwise a plugin
// pushed a minute ago stays invisible for the rest of the cache TTL, which
// reads as the feature being broken.
func wantsFreshListing(r *http.Request) bool {
	v := r.URL.Query().Get("refresh")
	return v == "1" || v == "true"
}

func (admin *Admin) pluginBranches(w http.ResponseWriter, r *http.Request) {
	repoUrl := r.URL.Query().Get("repo")

	if wantsFreshListing(r) {
		admin.Controller.PluginStore.InvalidateCache()
	}

	branches, err := admin.Controller.PluginStore.Branches(repoUrl)
	if err != nil {
		writeJsonError(w, http.StatusBadGateway, err.Error())
		return
	}

	writeJson(w, map[string]any{"branches": branches})
}

func (admin *Admin) pluginAvailable(w http.ResponseWriter, r *http.Request) {
	repoUrl := r.URL.Query().Get("repo")
	branch := r.URL.Query().Get("branch")

	fresh := wantsFreshListing(r)
	if fresh {
		admin.Controller.PluginStore.InvalidateCache()
	}

	available, err := admin.Controller.PluginStore.Available(repoUrl, branch, fresh)
	if err != nil {
		writeJsonError(w, http.StatusBadGateway, err.Error())
		return
	}

	writeJson(w, map[string]any{"available": available})
}

func (admin *Admin) pluginInstall(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Repo     string `json:"repo"`
		Branch   string `json:"branch"`
		PluginId string `json:"pluginId"`
		Enable   *bool  `json:"enable"`
	}

	if err := decodePluginBody(r, &body); err != nil {
		writeJsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	controller := admin.Controller

	manifest, err := controller.PluginStore.Install(body.Repo, body.Branch, body.PluginId)
	if err != nil {
		writeJsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	existing, wasInstalled := controller.Plugins.Get(manifest.Id)

	plugin := &Plugin{
		PluginId:    manifest.Id,
		Name:        manifest.Name,
		Version:     manifest.Version,
		Source:      body.Repo,
		Branch:      body.Branch,
		InstalledAt: time.Now().UTC(),
		Manifest:    manifest,
	}

	if body.Repo == "" {
		plugin.Source = DefaultPluginRepo
	}

	switch {
	case body.Enable != nil:
		plugin.Enabled = *body.Enable
	case wasInstalled:
		// Updating an already-installed plugin must not silently turn it on or
		// off; keep whatever the admin chose before.
		plugin.Enabled = existing.Enabled
	default:
		// A freshly installed plugin is enabled, because installing it is the
		// admin saying they want it. It still needs a restart to load.
		plugin.Enabled = true
	}

	if err := controller.Plugins.Write(controller.Database, plugin); err != nil {
		writeJsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Create the plugin's tables now rather than at next boot, so its settings
	// can be configured before it ever runs.
	if err := CreatePluginSchema(controller.Database, manifest); err != nil {
		writeJsonError(w, http.StatusInternalServerError, fmt.Sprintf("schema: %v", err))
		return
	}

	// Seed manifest defaults only for keys with no stored value, so an update
	// never overwrites settings the admin already entered.
	if config, err := ReadPluginConfig(controller.Database, manifest); err == nil {
		for key, value := range manifest.DefaultConfig() {
			if _, ok := config[key]; !ok {
				WritePluginConfigValue(controller.Database, manifest, key, value)
			}
		}
	}

	if err := controller.Plugins.Read(controller.Database, controller.Config); err != nil {
		writeJsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf(
		"plugin %s %s installed from %s (%s); restart required to load it",
		manifest.Id, manifest.Version, plugin.Source, plugin.Branch,
	))

	admin.BroadcastConfig()

	writeJson(w, map[string]any{
		"pluginId":        manifest.Id,
		"version":         manifest.Version,
		"restartRequired": true,
	})
}

func (admin *Admin) pluginToggle(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PluginId string `json:"pluginId"`
		Enabled  bool   `json:"enabled"`
	}

	if err := decodePluginBody(r, &body); err != nil {
		writeJsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	controller := admin.Controller

	if err := controller.Plugins.SetEnabled(controller.Database, body.PluginId, body.Enabled); err != nil {
		writeJsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	state := "disabled"
	if body.Enabled {
		state = "enabled"
	}
	controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf("plugin %s %s; restart required to take effect", body.PluginId, state))

	// Disabling stops the plugin's assets being served immediately, so tell
	// connected clients right away even though the runtime keeps going until
	// restart.
	controller.EmitConfig()

	plugin, _ := controller.Plugins.Get(body.PluginId)

	writeJson(w, map[string]any{
		"pluginId":        body.PluginId,
		"enabled":         body.Enabled,
		"restartRequired": plugin != nil && plugin.Enabled != plugin.Running,
	})
}

func (admin *Admin) pluginUninstall(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PluginId string `json:"pluginId"`
	}

	if err := decodePluginBody(r, &body); err != nil {
		writeJsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	controller := admin.Controller

	if _, ok := controller.Plugins.Get(body.PluginId); !ok {
		writeJsonError(w, http.StatusNotFound, "no such plugin")
		return
	}

	if err := controller.Plugins.RemoveFiles(controller.Config, body.PluginId); err != nil {
		writeJsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if err := controller.Plugins.Delete(controller.Database, body.PluginId); err != nil {
		writeJsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if err := controller.Plugins.Read(controller.Database, controller.Config); err != nil {
		writeJsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// The plugin's tables are deliberately left in place. Reinstalling brings
	// the settings and data straight back; removing them is a separate,
	// explicit action.
	controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf(
		"plugin %s uninstalled; its settings and data were kept", body.PluginId,
	))

	controller.EmitConfig()

	writeJson(w, map[string]any{"pluginId": body.PluginId, "restartRequired": true})
}

func (admin *Admin) pluginPurge(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PluginId string `json:"pluginId"`
	}

	if err := decodePluginBody(r, &body); err != nil {
		writeJsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	controller := admin.Controller

	plugin, ok := controller.Plugins.Get(body.PluginId)
	if !ok {
		writeJsonError(w, http.StatusNotFound, "no such plugin")
		return
	}

	if plugin.Running {
		writeJsonError(w, http.StatusConflict, "disable the plugin and restart before purging its data")
		return
	}

	if err := controller.Plugins.PurgeData(controller.Database, plugin); err != nil {
		writeJsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	controller.Logs.LogEvent(LogLevelWarn, fmt.Sprintf("plugin %s data purged", body.PluginId))

	writeJson(w, map[string]any{"pluginId": body.PluginId, "purged": true})
}

func (admin *Admin) pluginConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PluginId string         `json:"pluginId"`
		Config   map[string]any `json:"config"`
	}

	if err := decodePluginBody(r, &body); err != nil {
		writeJsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	controller := admin.Controller

	plugin, ok := controller.Plugins.Get(body.PluginId)
	if !ok || plugin.Manifest == nil {
		writeJsonError(w, http.StatusNotFound, "no such plugin")
		return
	}

	declared := map[string]*PluginConfigField{}
	for i := range plugin.Manifest.Config {
		declared[plugin.Manifest.Config[i].Key] = &plugin.Manifest.Config[i]
	}

	for key, value := range body.Config {
		field, ok := declared[key]
		if !ok {
			// Ignore keys the manifest doesn't declare rather than failing the
			// save — a stale admin form shouldn't block a legitimate change.
			continue
		}

		// An empty password means "leave it alone": the form never received the
		// stored value, so writing the blank back would erase it.
		if field.Type == "password" {
			if text, ok := value.(string); ok && text == "" {
				continue
			}
		}

		if err := WritePluginConfigValue(controller.Database, plugin.Manifest, key, value); err != nil {
			writeJsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	// Push the new values into the running runtime and let the plugin react,
	// so configuration changes apply without a restart even though installs
	// need one.
	if plugin.runtime != nil {
		if config, err := ReadPluginConfig(controller.Database, plugin.Manifest); err == nil {
			plugin.runtime.configMu.Lock()
			plugin.runtime.config = config
			plugin.runtime.configMu.Unlock()
		}
		plugin.runtime.Emit(PluginEventConfigChanged, nil)
	}

	admin.BroadcastConfig()

	writeJson(w, map[string]any{"pluginId": body.PluginId, "saved": true})
}

func (admin *Admin) pluginRepos(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Repos []*PluginRepo `json:"repos"`
	}

	if err := decodePluginBody(r, &body); err != nil {
		writeJsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	controller := admin.Controller

	// Carry forward a token the browser didn't send back, since the list
	// endpoint deliberately never reveals it.
	existing := map[string]string{}
	for _, repo := range controller.PluginStore.Repos() {
		if strings.TrimSpace(repo.Token) != "" {
			existing[strings.ToLower(repo.Url)] = repo.Token
		}
	}

	for _, repo := range body.Repos {
		if repo == nil || strings.TrimSpace(repo.Token) != "" {
			continue
		}
		if token, ok := existing[strings.ToLower(repo.Url)]; ok {
			repo.Token = token
		}
	}

	if err := controller.PluginStore.SetRepos(body.Repos); err != nil {
		writeJsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	repos := []map[string]any{}
	for _, repo := range controller.PluginStore.Repos() {
		repos = append(repos, map[string]any{
			"url":      repo.Url,
			"official": repo.IsOfficial(),
			"hasToken": strings.TrimSpace(repo.Token) != "",
		})
	}

	writeJson(w, map[string]any{"repos": repos})
}
