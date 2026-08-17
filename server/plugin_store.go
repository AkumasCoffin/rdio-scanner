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
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultPluginRepo is the official plugin repository. Anything else a user
// adds is third-party and flagged as such in the admin panel.
const DefaultPluginRepo = "https://github.com/AkumasCoffin/rdio-scanner-plugins"

// pluginRepoOptionKey stores the user's additional repositories, as JSON, in
// the same rdioScannerConfigs table the rest of the options use.
const pluginRepoOptionKey = "pluginRepos"

const (
	// pluginStoreCacheTTL keeps browsing responsive without hammering the
	// GitHub API, whose unauthenticated limit is 60 requests per hour per IP —
	// low enough that an admin clicking between branches would exhaust it.
	pluginStoreCacheTTL = 10 * time.Minute

	pluginStoreHttpTimeout = 30 * time.Second

	// pluginArchiveMaxSize bounds what will be extracted from a downloaded
	// repository archive, so a hostile repo can't fill the disk.
	pluginArchiveMaxSize = 64 << 20 // 64 MiB

	// pluginArchiveMaxFiles bounds the file count for the same reason.
	pluginArchiveMaxFiles = 4096
)

// PluginRepo is one source of plugins.
type PluginRepo struct {
	Url string `json:"url"`
	// Token is an optional GitHub token for private repositories or to lift
	// the unauthenticated rate limit. Never sent back to the browser.
	Token string `json:"token,omitempty"`
}

// IsOfficial reports whether this is the repository shipped with the product.
// Drives the third-party warning in the admin panel.
func (repo *PluginRepo) IsOfficial() bool {
	a, b, errA := parseGitHubRepo(repo.Url)
	c, d, errB := parseGitHubRepo(DefaultPluginRepo)
	if errA != nil || errB != nil {
		return false
	}
	return strings.EqualFold(a, c) && strings.EqualFold(b, d)
}

// PluginStore browses and installs plugins from git repositories.
type PluginStore struct {
	Controller *Controller

	mutex sync.Mutex
	cache map[string]*pluginStoreCacheEntry

	// installMutex serialises installs. Two at once raced over the same
	// directory — one could remove what the other had just put there — and
	// Plugins.Write is check-then-insert, so two first-installs of the same
	// plugin raced the unique constraint and one came back a 500.
	//
	// Separate from mutex, which guards only the listing cache: an install
	// takes minutes and must not block someone browsing.
	installMutex sync.Mutex
}

type pluginStoreCacheEntry struct {
	fetched time.Time
	value   any
}

func NewPluginStore(controller *Controller) *PluginStore {
	return &PluginStore{
		Controller: controller,
		cache:      map[string]*pluginStoreCacheEntry{},
	}
}

func (store *PluginStore) cached(key string, fetch func() (any, error)) (any, error) {
	store.mutex.Lock()
	entry, ok := store.cache[key]
	store.mutex.Unlock()

	if ok && time.Since(entry.fetched) < pluginStoreCacheTTL {
		return entry.value, nil
	}

	value, err := fetch()
	if err != nil {
		// Serve a stale entry rather than failing outright — a rate-limited or
		// briefly unreachable GitHub shouldn't blank the plugin list.
		if ok {
			return entry.value, nil
		}
		return nil, err
	}

	store.mutex.Lock()
	store.cache[key] = &pluginStoreCacheEntry{fetched: time.Now(), value: value}
	store.mutex.Unlock()

	return value, nil
}

// InvalidateCache clears cached listings, so an install or a newly added
// repository shows up immediately instead of after the TTL.
func (store *PluginStore) InvalidateCache() {
	store.mutex.Lock()
	store.cache = map[string]*pluginStoreCacheEntry{}
	store.mutex.Unlock()
}

// Repos returns the configured repositories, official first.
func (store *PluginStore) Repos() []*PluginRepo {
	repos := []*PluginRepo{{Url: DefaultPluginRepo}}

	raw := store.Controller.Options.PluginRepos
	if strings.TrimSpace(raw) == "" {
		return repos
	}

	var stored []*PluginRepo
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return repos
	}

	for _, repo := range stored {
		if repo == nil || strings.TrimSpace(repo.Url) == "" {
			continue
		}
		if repo.IsOfficial() {
			continue
		}
		repos = append(repos, repo)
	}

	return repos
}

// SetRepos persists the user-added repositories. The official one is implicit
// and never stored.
func (store *PluginStore) SetRepos(repos []*PluginRepo) error {
	keep := []*PluginRepo{}

	for _, repo := range repos {
		if repo == nil || strings.TrimSpace(repo.Url) == "" {
			continue
		}
		if _, _, err := parseGitHubRepo(repo.Url); err != nil {
			return fmt.Errorf("%q is not a GitHub repository url", repo.Url)
		}
		if repo.IsOfficial() {
			continue
		}
		keep = append(keep, repo)
	}

	encoded, err := json.Marshal(keep)
	if err != nil {
		return err
	}

	store.Controller.Options.PluginRepos = string(encoded)

	if err := store.Controller.Options.Write(store.Controller.Database); err != nil {
		return err
	}

	store.InvalidateCache()

	return nil
}

// findRepo locates a configured repository by url, so an install request can
// only ever target something the admin has already added.
func (store *PluginStore) findRepo(rawUrl string) (*PluginRepo, error) {
	if strings.TrimSpace(rawUrl) == "" {
		return &PluginRepo{Url: DefaultPluginRepo}, nil
	}

	wantOwner, wantName, err := parseGitHubRepo(rawUrl)
	if err != nil {
		return nil, err
	}

	for _, repo := range store.Repos() {
		owner, name, err := parseGitHubRepo(repo.Url)
		if err != nil {
			continue
		}
		if strings.EqualFold(owner, wantOwner) && strings.EqualFold(name, wantName) {
			return repo, nil
		}
	}

	return nil, fmt.Errorf("repository %q is not configured", rawUrl)
}

// escapeBranch escapes a branch for use in a URL path, per segment.
//
// url.PathEscape turns a slash into %2F, and a branch name is allowed to
// contain slashes — feature/x is the commonest convention there is. GitHub
// wants those slashes as path separators, so escaping them made every such
// branch 404: it listed in the admin panel, and neither its manifests nor its
// tarball could be fetched.
func escapeBranch(branch string) string {
	parts := strings.Split(branch, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func (store *PluginStore) githubRequest(repo *PluginRepo, rawUrl string) (*http.Response, error) {
	request, err := http.NewRequest(http.MethodGet, rawUrl, nil)
	if err != nil {
		return nil, err
	}

	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "Rdio-Scanner")

	if repo != nil && strings.TrimSpace(repo.Token) != "" {
		request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(repo.Token))
	}

	response, err := (&http.Client{Timeout: pluginStoreHttpTimeout}).Do(request)
	if err != nil {
		return nil, err
	}

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		response.Body.Close()
		return nil, fmt.Errorf("github returned %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}

	return response, nil
}

type githubBranch struct {
	Name string `json:"name"`
}

// Branches lists a repository's branches.
//
// Every branch is offered, not just the default one: work-in-progress plugins
// live on branches, and being able to install from them deliberately is the
// point. The admin panel labels non-default branches as untested.
func (store *PluginStore) Branches(rawUrl string) ([]string, error) {
	repo, err := store.findRepo(rawUrl)
	if err != nil {
		return nil, err
	}

	owner, name, err := parseGitHubRepo(repo.Url)
	if err != nil {
		return nil, err
	}

	value, err := store.cached("branches:"+owner+"/"+name, func() (any, error) {
		api := fmt.Sprintf("https://api.github.com/repos/%s/%s/branches?per_page=100",
			url.PathEscape(owner), url.PathEscape(name))

		response, err := store.githubRequest(repo, api)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()

		var branches []githubBranch
		if err := json.NewDecoder(response.Body).Decode(&branches); err != nil {
			return nil, err
		}

		names := []string{}
		for _, branch := range branches {
			names = append(names, branch.Name)
		}

		return names, nil
	})
	if err != nil {
		return nil, err
	}

	names, _ := value.([]string)

	return names, nil
}

type githubContent struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Type string `json:"type"`
}

// AvailablePlugin is one plugin as offered by a repository.
type AvailablePlugin struct {
	Repo         string          `json:"repo"`
	Branch       string          `json:"branch"`
	Official     bool            `json:"official"`
	Manifest     *PluginManifest `json:"manifest"`
	Compatible   bool            `json:"compatible"`
	Incompatible string          `json:"incompatible,omitempty"`
	Installed    bool            `json:"installed"`
	// UpdateAvailable is true when this offering is newer than what is
	// installed, comparing versions rather than assuming any difference is an
	// upgrade — installing from a branch can legitimately move backwards.
	UpdateAvailable bool `json:"updateAvailable"`
}

// Available lists the plugins a repository offers on a given branch.
//
// fresh defeats caching all the way down, including GitHub's own CDN in front
// of raw.githubusercontent.com. Without that last part, pressing Refresh
// straight after pushing a plugin still shows the old manifest for several
// minutes, which reads as the button not working.
func (store *PluginStore) Available(rawUrl string, branch string, fresh bool) ([]*AvailablePlugin, error) {
	repo, err := store.findRepo(rawUrl)
	if err != nil {
		return nil, err
	}

	owner, name, err := parseGitHubRepo(repo.Url)
	if err != nil {
		return nil, err
	}

	if strings.TrimSpace(branch) == "" {
		branch = "main"
	}

	cacheKey := fmt.Sprintf("available:%s/%s@%s", owner, name, branch)

	value, err := store.cached(cacheKey, func() (any, error) {
		listing := fmt.Sprintf("https://api.github.com/repos/%s/%s/contents/plugins?ref=%s",
			url.PathEscape(owner), url.PathEscape(name), url.QueryEscape(branch))

		response, err := store.githubRequest(repo, listing)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()

		var contents []githubContent
		if err := json.NewDecoder(response.Body).Decode(&contents); err != nil {
			return nil, err
		}

		manifests := []*PluginManifest{}

		for _, entry := range contents {
			if entry.Type != "dir" {
				continue
			}

			// Raw content rather than the contents API: it isn't rate-limited
			// the same way, and one plugin's broken manifest shouldn't stop the
			// rest of the listing.
			raw := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/plugins/%s/%s",
				url.PathEscape(owner), url.PathEscape(name), escapeBranch(branch),
				url.PathEscape(entry.Name), PluginManifestName)

			if fresh {
				raw += "?rdio-refresh=" + strconv.FormatInt(time.Now().UnixNano(), 36)
			}

			manifestResponse, err := store.githubRequest(repo, raw)
			if err != nil {
				continue
			}

			body, err := io.ReadAll(io.LimitReader(manifestResponse.Body, 1<<20))
			manifestResponse.Body.Close()
			if err != nil {
				continue
			}

			manifest, err := ParsePluginManifest(body)
			if err != nil {
				continue
			}
			if manifest.Id != entry.Name {
				continue
			}

			manifests = append(manifests, manifest)
		}

		return manifests, nil
	})
	if err != nil {
		return nil, err
	}

	manifests, _ := value.([]*PluginManifest)

	available := []*AvailablePlugin{}

	for _, manifest := range manifests {
		entry := &AvailablePlugin{
			Repo:     repo.Url,
			Branch:   branch,
			Official: repo.IsOfficial(),
			Manifest: manifest,
		}

		entry.Compatible, entry.Incompatible = manifest.CompatibleWith(Version)

		if installed, ok := store.Controller.Plugins.Get(manifest.Id); ok {
			entry.Installed = true
			entry.UpdateAvailable = compareVersions(manifest.Version, installed.Version) > 0
		}

		available = append(available, entry)
	}

	return available, nil
}

// PluginUpdate is one installed plugin measured against the repository and
// branch it was installed from.
type PluginUpdate struct {
	PluginId         string `json:"pluginId"`
	Name             string `json:"name"`
	InstalledVersion string `json:"installedVersion"`
	LatestVersion    string `json:"latestVersion,omitempty"`
	Repo             string `json:"repo"`
	Branch           string `json:"branch"`
	UpdateAvailable  bool   `json:"updateAvailable"`
	// Compatible describes the *offered* version. An update that this server is
	// too old to run is worth showing and worth refusing to install, but it is
	// not worth hiding — otherwise the answer to "why am I not being offered
	// 2.0" is invisible.
	Compatible   bool   `json:"compatible"`
	Incompatible string `json:"incompatible,omitempty"`
	// Error explains why this particular plugin could not be checked. One
	// unreachable repository must not blank the whole report.
	Error string `json:"error,omitempty"`
}

// Updates checks every installed plugin against the repository and branch it
// came from.
//
// Plugins are grouped by source so a repository is listed once however many
// plugins were installed from it, which matters against GitHub's rate limit.
func (store *PluginStore) Updates(fresh bool) []*PluginUpdate {
	store.Controller.Plugins.mutex.RLock()
	installed := append([]*Plugin{}, store.Controller.Plugins.List...)
	store.Controller.Plugins.mutex.RUnlock()

	// Group by the exact pair the plugin was installed from. A plugin tracks
	// the branch it came from, not main — someone running a plugin from a
	// development branch wants updates from that branch.
	type sourceKey struct{ repo, branch string }

	groups := map[sourceKey][]*Plugin{}
	order := []sourceKey{}

	for _, plugin := range installed {
		repo := plugin.Source
		if strings.TrimSpace(repo) == "" {
			repo = DefaultPluginRepo
		}

		branch := plugin.Branch
		if strings.TrimSpace(branch) == "" {
			branch = "main"
		}

		key := sourceKey{repo: repo, branch: branch}
		if _, seen := groups[key]; !seen {
			order = append(order, key)
		}
		groups[key] = append(groups[key], plugin)
	}

	updates := []*PluginUpdate{}

	for _, key := range order {
		available, err := store.Available(key.repo, key.branch, fresh)

		offered := map[string]*AvailablePlugin{}
		for _, entry := range available {
			if entry.Manifest != nil {
				offered[entry.Manifest.Id] = entry
			}
		}

		for _, plugin := range groups[key] {
			update := &PluginUpdate{
				PluginId:         plugin.PluginId,
				Name:             plugin.Name,
				InstalledVersion: plugin.Version,
				Repo:             key.repo,
				Branch:           key.branch,
			}

			switch {
			case err != nil:
				update.Error = err.Error()

			case offered[plugin.PluginId] == nil:
				// Installed from somewhere it is no longer offered: a removed
				// plugin, a renamed branch, or a hand-placed folder. Not an
				// error to report loudly, but not silently "up to date" either.
				update.Error = fmt.Sprintf("no longer offered by %s on %s", key.repo, key.branch)

			default:
				entry := offered[plugin.PluginId]
				update.LatestVersion = entry.Manifest.Version
				update.Compatible = entry.Compatible
				update.Incompatible = entry.Incompatible
				update.UpdateAvailable = compareVersions(entry.Manifest.Version, plugin.Version) > 0
			}

			updates = append(updates, update)
		}
	}

	return updates
}

// Install downloads a plugin and writes it into the plugins directory.
//
// The whole branch archive is fetched and only the one plugin's directory is
// extracted. That is one request instead of one per file, which matters against
// GitHub's rate limit, and it gets the plugin's bundled assets (libraries,
// stylesheets) without having to walk the tree.
func (store *PluginStore) Install(rawUrl string, branch string, pluginId string) (*PluginManifest, string, error) {
	if !pluginIdRegexp.MatchString(pluginId) {
		return nil, "", fmt.Errorf("invalid plugin id %q", pluginId)
	}

	store.installMutex.Lock()
	defer store.installMutex.Unlock()

	repo, err := store.findRepo(rawUrl)
	if err != nil {
		return nil, "", err
	}

	owner, name, err := parseGitHubRepo(repo.Url)
	if err != nil {
		return nil, "", err
	}

	if strings.TrimSpace(branch) == "" {
		branch = "main"
	}

	archiveUrl := fmt.Sprintf("https://api.github.com/repos/%s/%s/tarball/%s",
		url.PathEscape(owner), url.PathEscape(name), escapeBranch(branch))

	response, err := store.githubRequest(repo, archiveUrl)
	if err != nil {
		return nil, "", err
	}
	defer response.Body.Close()

	pluginsDir := store.Controller.Plugins.Dir(store.Controller.Config)
	if err := os.MkdirAll(pluginsDir, 0o770); err != nil {
		return nil, "", err
	}

	// Extract to a staging directory first so a failed or malicious archive
	// never leaves a half-written plugin where the loader would find it.
	//
	// Beside the plugins directory rather than inside it. A process killed mid
	// install skips the cleanup below, and staging inside meant the leftover
	// was picked up by the directory scan as a plugin named ".install-123456"
	// — which then could not be removed from the admin panel either, because
	// the id validator rejects the leading dot. Same filesystem, so the rename
	// at the end is still atomic.
	staging, err := os.MkdirTemp(store.Controller.Config.BaseDir, ".rdio-plugin-install-*")
	if err != nil {
		return nil, "", err
	}
	defer os.RemoveAll(staging)

	wanted := "plugins/" + pluginId + "/"

	commit, err := extractPluginFromTarball(response.Body, wanted, staging)
	if err != nil {
		return nil, "", err
	}

	manifest, err := ReadPluginManifest(staging)
	if err != nil {
		return nil, "", fmt.Errorf("downloaded plugin has no readable %s: %v", PluginManifestName, err)
	}

	if manifest.Id != pluginId {
		return nil, "", fmt.Errorf("downloaded plugin declares id %q but was requested as %q", manifest.Id, pluginId)
	}

	if ok, reason := manifest.CompatibleWith(Version); !ok {
		return nil, "", fmt.Errorf("%s", reason)
	}

	target := filepath.Join(pluginsDir, pluginId)

	// Move the old install aside rather than deleting it, so a failure leaves
	// the plugin that was working still there.
	//
	// It used to remove and then rename. A crash between the two, or a rename
	// that failed for any reason, destroyed the installed plugin — and the
	// staging copy was removed by the deferred cleanup on the way out, so the
	// only two copies went at once. On Windows it is worse than a crash window:
	// RemoveAll fails outright while any file under it is open, and the asset
	// handler holds plugin files open, so updating a running plugin failed
	// there routinely.
	aside := ""

	if _, err := os.Stat(target); err == nil {
		aside = target + ".replacing"

		// A leftover from an earlier interrupted attempt.
		os.RemoveAll(aside)

		if err := os.Rename(target, aside); err != nil {
			return nil, "", fmt.Errorf("cannot move the installed plugin aside: %v", err)
		}
	}

	if err := os.Rename(staging, target); err != nil {
		// Put back what was working.
		if aside != "" {
			if restoreErr := os.Rename(aside, target); restoreErr != nil {
				return nil, "", fmt.Errorf(
					"install failed (%v) and the previous version could not be restored from %s (%v); the plugin is not installed",
					err, aside, restoreErr,
				)
			}
		}

		return nil, "", err
	}

	if aside != "" {
		// Best effort. The new version is in place either way, and a leftover
		// .replacing directory is skipped by the scan.
		os.RemoveAll(aside)
	}

	store.InvalidateCache()

	return manifest, commit, nil
}

// extractPluginFromTarball pulls the files under prefix out of a GitHub
// tarball, and returns the commit the archive was built from.
//
// GitHub wraps everything in a single generated top-level directory named
// owner-repo-<sha>, so that first path segment is stripped before matching —
// and its sha is exactly the provenance worth recording: it says precisely
// which revision of a moving branch got installed.
func extractPluginFromTarball(body io.Reader, prefix string, dest string) (string, error) {
	gz, err := gzip.NewReader(io.LimitReader(body, pluginArchiveMaxSize))
	if err != nil {
		return "", err
	}
	defer gz.Close()

	reader := tar.NewReader(gz)

	files := 0
	written := int64(0)
	found := false
	commit := ""

	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}

		if files++; files > pluginArchiveMaxFiles {
			return "", fmt.Errorf("archive contains too many files")
		}

		// Strip GitHub's generated top-level directory, recording its trailing
		// sha the first time we see it.
		name := header.Name
		if i := strings.Index(name, "/"); i >= 0 {
			if commit == "" {
				if dash := strings.LastIndex(name[:i], "-"); dash >= 0 {
					commit = name[dash+1 : i]
				}
			}
			name = name[i+1:]
		} else {
			continue
		}

		if !strings.HasPrefix(name, prefix) {
			continue
		}

		relative := strings.TrimPrefix(name, prefix)
		if relative == "" {
			continue
		}

		// Reject anything that would land outside the destination. Archive
		// entry names are attacker-controlled input.
		cleaned := path.Clean("/" + relative)
		targetPath := filepath.Join(dest, filepath.FromSlash(strings.TrimPrefix(cleaned, "/")))

		absDest, err := filepath.Abs(dest)
		if err != nil {
			return "", err
		}
		absTarget, err := filepath.Abs(targetPath)
		if err != nil {
			return "", err
		}
		if absTarget != absDest && !strings.HasPrefix(absTarget, absDest+string(os.PathSeparator)) {
			return "", fmt.Errorf("archive entry %q escapes the plugin directory", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(targetPath, 0o770); err != nil {
				return "", err
			}
			found = true

		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(targetPath), 0o770); err != nil {
				return "", err
			}

			file, err := os.Create(targetPath)
			if err != nil {
				return "", err
			}

			n, err := io.Copy(file, io.LimitReader(reader, pluginArchiveMaxSize-written))
			file.Close()
			if err != nil {
				return "", err
			}

			written += n
			if written >= pluginArchiveMaxSize {
				return "", fmt.Errorf("archive is too large")
			}

			found = true

		default:
			// Symlinks and device nodes have no legitimate use in a plugin and
			// are the classic archive-extraction escape, so they are skipped.
			continue
		}
	}

	if !found {
		return "", fmt.Errorf("no plugin found at %s in that branch", strings.TrimSuffix(prefix, "/"))
	}

	return commit, nil
}
