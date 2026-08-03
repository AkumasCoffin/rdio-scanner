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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// PluginsDirName is the directory, relative to the server's base directory,
// where installed plugins live. The base directory is the same writable
// location that already holds the SQLite database, so this is "next to the
// binary" for a normal install and still correct for a container or service
// install where the binary's own directory isn't writable.
const PluginsDirName = "plugins"

// PluginDataDirName holds each plugin's persistent storage, deliberately kept
// out of the plugins directory.
//
// A plugin's code directory is destroyed and rewritten on every update — the
// installer removes it before unpacking the new version — so anything a plugin
// wrote alongside its own code was silently lost the first time it updated.
// This location is never touched by install or uninstall.
const PluginDataDirName = "plugin-data"

// Plugin is one installed plugin: its registry row, its manifest, and its
// runtime once started.
type Plugin struct {
	Id          any       `json:"_id"`
	PluginId    string    `json:"pluginId"`
	Name        string    `json:"name"`
	Version     string    `json:"version"`
	Source      string    `json:"source"`
	Branch      string    `json:"branch"`
	Enabled     bool      `json:"enabled"`
	InstalledAt time.Time `json:"installedAt"`

	// Commit is the revision the installed files came from. A branch moves, so
	// "installed from main" says nothing about what you actually got; this
	// does, and it is what makes an install reproducible or auditable after
	// the fact.
	Commit string `json:"commit,omitempty"`

	// Manifest is the parsed plugin.json read from disk. Nil when the registry
	// has a row but the files are missing or unreadable.
	Manifest *PluginManifest `json:"manifest,omitempty"`

	// Error explains why an installed plugin isn't running. Surfaced in the
	// admin panel so a broken plugin is visible rather than silently absent.
	Error string `json:"error,omitempty"`

	// Present reports whether the plugin's files were found on disk.
	Present bool `json:"present"`

	// Running reports whether the plugin's runtime is currently loaded. A
	// plugin enabled after boot shows enabled-but-not-running until restart,
	// which is what drives the "restart required" banner.
	Running bool `json:"running"`

	dir     string
	runtime *PluginRuntime
}

// storedManifest is what gets written to the registry's manifest column. Keeping
// a copy in the database means the admin panel can still describe a plugin whose
// files have gone missing, and lets uninstall know which tables to purge.
func (plugin *Plugin) storedManifest() (string, error) {
	if plugin.Manifest == nil {
		return "", nil
	}
	b, err := json.Marshal(plugin.Manifest)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

type Plugins struct {
	Controller *Controller

	List  []*Plugin
	mutex sync.RWMutex

	// dir is the absolute path to the plugins directory.
	dir string

	started bool
}

func NewPlugins() *Plugins {
	return &Plugins{List: []*Plugin{}}
}

// Dir returns the plugins directory, creating it on first use so a fresh
// install has somewhere to drop a manually-copied plugin.
func (plugins *Plugins) Dir(config *Config) string {
	if plugins.dir == "" {
		plugins.dir = filepath.Join(config.BaseDir, PluginsDirName)
	}
	return plugins.dir
}

// DataDir returns a plugin's persistent storage directory, creating it if it
// does not exist. Survives update and uninstall — reinstalling a plugin finds
// whatever it left behind, which is the same promise its settings already make.
func (plugins *Plugins) DataDir(config *Config, pluginId string) (string, error) {
	if !pluginIdRegexp.MatchString(pluginId) {
		return "", fmt.Errorf("invalid plugin id %q", pluginId)
	}

	dir := filepath.Join(config.BaseDir, PluginDataDirName, pluginId)

	if err := os.MkdirAll(dir, 0o770); err != nil {
		return "", err
	}

	return dir, nil
}

// Get returns an installed plugin by its manifest id.
func (plugins *Plugins) Get(pluginId string) (*Plugin, bool) {
	plugins.mutex.RLock()
	defer plugins.mutex.RUnlock()

	for _, plugin := range plugins.List {
		if plugin.PluginId == pluginId {
			return plugin, true
		}
	}

	return nil, false
}

// Enabled returns the plugins that are both enabled and currently running.
func (plugins *Plugins) Enabled() []*Plugin {
	plugins.mutex.RLock()
	defer plugins.mutex.RUnlock()

	list := []*Plugin{}
	for _, plugin := range plugins.List {
		if plugin.Enabled && plugin.Running {
			list = append(list, plugin)
		}
	}

	return list
}

// Read loads the registry from the database and reconciles it against what is
// actually on disk. Either side can be ahead of the other: a plugin dropped
// into the directory by hand has files but no row, and a plugin whose files
// were deleted has a row but no files. Both are represented rather than
// silently dropped, so the admin panel can explain what it sees.
func (plugins *Plugins) Read(db *Database, config *Config) error {
	plugins.mutex.Lock()
	defer plugins.mutex.Unlock()

	formatError := func(err error) error {
		return fmt.Errorf("plugins.read: %v", err)
	}

	// What is already running has to survive a re-read.
	//
	// Read is called after every install and every uninstall, and it used to
	// replace the list wholesale with freshly built values — dropping the
	// runtime pointer, the Running flag and the resolved directory for every
	// plugin, including the ones it was not asked about. Installing anything
	// therefore detached every other plugin: their frontends started 404ing
	// (the asset handler requires Running), lifecycle events stopped reaching
	// them, Plugins.Stop later had no runtime left to stop, and purge — which
	// only refuses while Running — would drop the tables of a plugin still
	// executing.
	live := map[string]*Plugin{}
	for _, plugin := range plugins.List {
		live[plugin.PluginId] = plugin
	}

	byPluginId := map[string]*Plugin{}
	list := []*Plugin{}

	rows, err := db.Query("select `_id`, `pluginId`, `name`, `version`, `source`, `branch`, `enabled`, `installedAt`, `manifest`, `commit` from `rdioScannerPlugins`")
	if err != nil {
		return formatError(err)
	}

	for rows.Next() {
		var (
			rowId       uint
			pluginId    string
			name        sql.NullString
			version     sql.NullString
			source      sql.NullString
			branch      sql.NullString
			enabled     sql.NullBool
			installedAt sql.NullTime
			manifest    sql.NullString
			commit      sql.NullString
		)

		if err = rows.Scan(&rowId, &pluginId, &name, &version, &source, &branch, &enabled, &installedAt, &manifest, &commit); err != nil {
			break
		}

		plugin := &Plugin{
			Id:       rowId,
			PluginId: pluginId,
			Name:     name.String,
			Version:  version.String,
			Source:   source.String,
			Branch:   branch.String,
			Enabled:  enabled.Bool,
			Commit:   commit.String,
		}

		// Carry over what belongs to the running process rather than to the
		// row. The database has no opinion about whether a plugin is loaded.
		if prior, ok := live[pluginId]; ok {
			plugin.runtime = prior.runtime
			plugin.Running = prior.Running
			plugin.dir = prior.dir
		}

		if installedAt.Valid {
			plugin.InstalledAt = installedAt.Time
		}

		// The stored manifest is a fallback only — the copy on disk wins, since
		// it is what would actually run.
		if manifest.Valid && manifest.String != "" {
			if parsed, err := ParsePluginManifest([]byte(manifest.String)); err == nil {
				plugin.Manifest = parsed
			}
		}

		byPluginId[pluginId] = plugin
		list = append(list, plugin)
	}

	rows.Close()

	if err != nil {
		return formatError(err)
	}

	// Now overlay what is on disk.
	found, err := plugins.scan(config)
	if err != nil {
		return formatError(err)
	}

	// Plugins discovered on disk with no registry row, to be persisted below.
	discovered := []*Plugin{}

	for pluginId, scanned := range found {
		plugin, ok := byPluginId[pluginId]
		if !ok {
			// Files with no registry row — someone copied a plugin in by hand.
			// Register it, disabled, so it shows up in the admin panel and can
			// be turned on deliberately rather than starting unannounced.
			plugin = &Plugin{
				PluginId:    pluginId,
				Source:      "local",
				InstalledAt: time.Now().UTC(),
			}
			byPluginId[pluginId] = plugin
			list = append(list, plugin)
			discovered = append(discovered, plugin)
		}

		plugin.Present = scanned.err == nil
		plugin.dir = scanned.dir

		if scanned.err != nil {
			plugin.Error = scanned.err.Error()
			continue
		}

		plugin.Error = ""
		plugin.Manifest = scanned.manifest
		plugin.Name = scanned.manifest.Name
		plugin.Version = scanned.manifest.Version
	}

	for pluginId, plugin := range byPluginId {
		if _, ok := found[pluginId]; !ok {
			plugin.Present = false
			if plugin.Error == "" {
				plugin.Error = "plugin files are missing from the plugins directory"
			}
		}
	}

	sort.Slice(list, func(i, j int) bool { return list[i].PluginId < list[j].PluginId })

	plugins.List = list

	// Persist anything found on disk that wasn't registered yet, so the admin
	// panel's enable toggle has a row to update and the plugin's state survives
	// a restart. Only ever inserts a disabled row — discovering files must not
	// start anything on its own.
	for _, plugin := range discovered {
		if plugin.Manifest == nil {
			continue
		}
		if err := plugins.Write(db, plugin); err != nil {
			return formatError(err)
		}
	}

	return nil
}

type scannedPlugin struct {
	dir      string
	manifest *PluginManifest
	err      error
}

// scan walks the plugins directory and parses every manifest it finds. A plugin
// whose manifest is broken is reported with its error rather than skipped, so
// the admin panel can say why it isn't available.
func (plugins *Plugins) scan(config *Config) (map[string]*scannedPlugin, error) {
	dir := plugins.Dir(config)

	found := map[string]*scannedPlugin{}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return found, nil
		}
		return nil, err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		name := entry.Name()
		pluginDir := filepath.Join(dir, name)

		manifest, err := ReadPluginManifest(pluginDir)
		if err != nil {
			if os.IsNotExist(err) {
				// A directory with no manifest isn't a plugin. Ignore it
				// rather than reporting it as broken — the plugins directory
				// is a normal filesystem location users may put things in.
				continue
			}
			found[name] = &scannedPlugin{dir: pluginDir, err: err}
			continue
		}

		// The directory name is what the install path, route prefix and table
		// prefix are derived from elsewhere, so a mismatch would mean the
		// plugin runs under a different identity than it declares.
		if manifest.Id != name {
			found[name] = &scannedPlugin{
				dir: pluginDir,
				err: fmt.Errorf("directory %q does not match plugin id %q", name, manifest.Id),
			}
			continue
		}

		found[manifest.Id] = &scannedPlugin{dir: pluginDir, manifest: manifest}
	}

	return found, nil
}

// Write upserts a single plugin's registry row. Plugins are added and removed
// through dedicated admin actions rather than the bulk config save, so this
// deliberately does not do the delete-missing sweep the other config models do.
func (plugins *Plugins) Write(db *Database, plugin *Plugin) error {
	manifest, err := plugin.storedManifest()
	if err != nil {
		return err
	}

	var count uint
	if err := db.QueryRow("select count(*) from `rdioScannerPlugins` where `pluginId` = ?", plugin.PluginId).Scan(&count); err != nil {
		return err
	}

	if count == 0 {
		// Postgres assigns the serial itself; sending an explicit null id would
		// collide with the sequence.
		if _, err := db.Exec(
			"insert into `rdioScannerPlugins` (`pluginId`, `name`, `version`, `source`, `branch`, `enabled`, `installedAt`, `manifest`, `commit`) values (?, ?, ?, ?, ?, ?, ?, ?, ?)",
			plugin.PluginId, plugin.Name, plugin.Version, plugin.Source, plugin.Branch, plugin.Enabled, plugin.InstalledAt, manifest, plugin.Commit,
		); err != nil {
			return err
		}
		return nil
	}

	_, err = db.Exec(
		"update `rdioScannerPlugins` set `name` = ?, `version` = ?, `source` = ?, `branch` = ?, `enabled` = ?, `installedAt` = ?, `manifest` = ?, `commit` = ? where `pluginId` = ?",
		plugin.Name, plugin.Version, plugin.Source, plugin.Branch, plugin.Enabled, plugin.InstalledAt, manifest, plugin.Commit, plugin.PluginId,
	)

	return err
}

// Delete removes a plugin's registry row. Its tables are deliberately left
// behind — see PurgeData.
func (plugins *Plugins) Delete(db *Database, pluginId string) error {
	_, err := db.Exec("delete from `rdioScannerPlugins` where `pluginId` = ?", pluginId)
	return err
}

// SetEnabled flips a plugin on or off and persists it. The change takes effect
// on the next restart; the caller is responsible for telling the user that.
func (plugins *Plugins) SetEnabled(db *Database, pluginId string, enabled bool) error {
	plugin, ok := plugins.Get(pluginId)
	if !ok {
		return fmt.Errorf("no such plugin %q", pluginId)
	}

	plugins.mutex.Lock()
	plugin.Enabled = enabled
	plugins.mutex.Unlock()

	return plugins.Write(db, plugin)
}

// Start loads and starts every enabled plugin. Called once during controller
// startup — plugins are not hot-loaded, which is why installing one asks for a
// restart.
func (plugins *Plugins) Start(controller *Controller) error {
	plugins.mutex.Lock()
	if plugins.started {
		plugins.mutex.Unlock()
		return fmt.Errorf("plugins already started")
	}
	plugins.started = true
	list := append([]*Plugin{}, plugins.List...)
	plugins.mutex.Unlock()

	for _, plugin := range list {
		if !plugin.Enabled || !plugin.Present || plugin.Manifest == nil {
			continue
		}

		if ok, reason := plugin.Manifest.CompatibleWith(Version); !ok {
			plugin.Error = reason
			controller.Logs.LogEvent(LogLevelWarn, fmt.Sprintf("plugin %s not started: %s", plugin.PluginId, reason))
			continue
		}

		if err := plugins.startPlugin(controller, plugin); err != nil {
			plugin.Error = err.Error()
			controller.Logs.LogEvent(LogLevelError, fmt.Sprintf("plugin %s failed to start: %v", plugin.PluginId, err))
			continue
		}

		plugin.Running = true
		plugin.Error = ""
		controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf("plugin %s %s started", plugin.PluginId, plugin.Version))
	}

	return nil
}

// startPlugin brings one plugin up: schema first, then the runtime. Schema
// creation is idempotent, so a plugin that gained a table in a new version
// picks it up here.
func (plugins *Plugins) startPlugin(controller *Controller, plugin *Plugin) error {
	if err := CreatePluginSchema(controller.Database, plugin.Manifest); err != nil {
		return fmt.Errorf("schema: %v", err)
	}

	// A frontend-only plugin has nothing to run server-side; its assets are
	// still served, so it is legitimately "running".
	if plugin.Manifest.Main == "" {
		return nil
	}

	runtime, err := NewPluginRuntime(controller, plugin)
	if err != nil {
		return err
	}

	if err := runtime.Start(); err != nil {
		runtime.Stop()
		return err
	}

	plugin.runtime = runtime

	return nil
}

// Stop shuts every running plugin down. Best effort — a plugin that refuses to
// stop must not hold up server shutdown.
func (plugins *Plugins) Stop(controller *Controller) {
	plugins.mutex.Lock()
	list := append([]*Plugin{}, plugins.List...)
	plugins.started = false
	plugins.mutex.Unlock()

	for _, plugin := range list {
		if plugin.runtime != nil {
			// Deregister before stopping, so nothing can be dispatched into a
			// runtime that is on its way down.
			if controller != nil && controller.PluginDispatch != nil {
				controller.PluginDispatch.Unregister(plugin.PluginId)
			}
			// Same for the plugin bus. A method left registered would route
			// into a dead runtime and fail every call, rather than reporting
			// honestly that nobody offers it any more.
			if controller != nil && controller.PluginRpc != nil {
				controller.PluginRpc.Unregister(plugin.PluginId)
			}
			plugin.runtime.Stop()
			plugin.runtime = nil
		}
		plugin.Running = false
	}
}

// StopOne shuts a single plugin down, deregistering it first so nothing can be
// dispatched into a runtime on its way out.
//
// Uninstall used to remove the files and the registry row and stop there, which
// left the plugin running with its code deleted: its timers kept firing, its
// routes and RPC methods stayed registered, and every extension point kept
// dispatching into it. Reports whether anything was actually stopped.
func (plugins *Plugins) StopOne(controller *Controller, pluginId string) bool {
	plugins.mutex.Lock()

	var target *Plugin
	for _, plugin := range plugins.List {
		if plugin.PluginId == pluginId {
			target = plugin
			break
		}
	}

	if target == nil {
		plugins.mutex.Unlock()
		return false
	}

	runtime := target.runtime
	target.runtime = nil
	target.Running = false

	plugins.mutex.Unlock()

	if controller != nil && controller.PluginDispatch != nil {
		controller.PluginDispatch.Unregister(pluginId)
	}
	if controller != nil && controller.PluginRpc != nil {
		controller.PluginRpc.Unregister(pluginId)
	}

	if runtime == nil {
		return false
	}

	runtime.Stop()

	return true
}

// EmitEvent fans a lifecycle event out to every running plugin. Dispatch is
// asynchronous inside each runtime, so this never blocks the caller — which
// matters because the loudest caller is the single-goroutine ingest path.
func (plugins *Plugins) EmitEvent(event string, payload any) {
	for _, plugin := range plugins.Enabled() {
		if plugin.runtime == nil {
			continue
		}
		plugin.runtime.Emit(event, payload)
	}
}

// PurgeData removes everything a plugin left behind: its tables, its settings,
// and its data directory. Only reachable from the explicit admin action —
// uninstalling deliberately keeps all of it.
func (plugins *Plugins) PurgeData(db *Database, config *Config, plugin *Plugin) error {
	if plugin.Manifest == nil {
		return fmt.Errorf("plugin %q has no manifest; cannot determine which tables to remove", plugin.PluginId)
	}

	if err := DropPluginSchema(db, plugin.Manifest); err != nil {
		return err
	}

	// The data directory outlives uninstall by design, so purge is the only
	// thing that clears it.
	dir, err := plugins.DataDir(config, plugin.PluginId)
	if err != nil {
		return err
	}

	if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
		return err
	}

	return nil
}

// RemoveFiles deletes a plugin's directory from disk.
func (plugins *Plugins) RemoveFiles(config *Config, pluginId string) error {
	if !pluginIdRegexp.MatchString(pluginId) {
		return fmt.Errorf("invalid plugin id %q", pluginId)
	}

	dir := filepath.Join(plugins.Dir(config), pluginId)

	// Guard against removing anything outside the plugins directory, since the
	// id reaches this from an HTTP request.
	parent := plugins.Dir(config)
	rel, err := filepath.Rel(parent, dir)
	if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) {
		return fmt.Errorf("refusing to remove %q", dir)
	}

	if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
		return err
	}

	return nil
}
