// Copyright (C) 2019-2026 Chrystian Huot <chrystian.huot@saubeo.solutions>
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
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// DefaultUpdateRepo is the GitHub repository the admin auto-updater checks for
// new releases when no custom Update URL is set in the admin panel.
//
// FORK MAINTAINERS: change this one line to point the updater at your own fork
// (or override it at build time without editing the source, with:
//
//	go build -ldflags "-X main.DefaultUpdateRepo=https://github.com/you/your-fork"
//
// ).
var DefaultUpdateRepo = "https://github.com/AkumasCoffin/rdio-scanner"

// githubRelease / githubAsset mirror the subset of the GitHub Releases API the
// updater consumes (https://api.github.com/repos/{owner}/{repo}/releases).
type githubRelease struct {
	TagName     string        `json:"tag_name"`
	Name        string        `json:"name"`
	Branch      string        `json:"target_commitish"`
	Prerelease  bool          `json:"prerelease"`
	Draft       bool          `json:"draft"`
	PublishedAt string        `json:"published_at"`
	HtmlUrl     string        `json:"html_url"`
	Assets      []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name string `json:"name"`
	Url  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

// updateRepoUrl returns the custom Update URL set in the admin options, or the
// fork's DefaultUpdateRepo when none is set.
func (admin *Admin) updateRepoUrl() string {
	if u := strings.TrimSpace(admin.Controller.Options.UpdateUrl); u != "" {
		return u
	}
	return DefaultUpdateRepo
}

// parseGitHubRepo extracts owner/repo from a github URL or an "owner/repo"
// shorthand, tolerating a scheme, a trailing slash and a trailing ".git".
func parseGitHubRepo(raw string) (string, string, error) {
	s := strings.TrimSpace(raw)
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".git")
	if i := strings.Index(strings.ToLower(s), "github.com"); i >= 0 {
		s = s[i+len("github.com"):]
	}
	s = strings.TrimPrefix(s, "/")
	parts := strings.Split(s, "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("expected a https://github.com/owner/repo URL")
	}
	return parts[0], parts[1], nil
}

// platformToken is the OS+arch substring expected in a server release asset
// name, e.g. "linux-amd64" or "windows-amd64".
func platformToken() string {
	osName := runtime.GOOS
	if osName == "darwin" {
		osName = "macos"
	}
	return osName + "-" + runtime.GOARCH
}

// assetForPlatform picks the release asset that matches this server's own
// OS/arch (the server-side OS check). Only raw binaries are accepted — archives
// (older .zip releases, the .apk, etc.) are skipped since the updater swaps the
// file in directly and can't unpack them.
func assetForPlatform(r githubRelease) *githubAsset {
	token := platformToken()
	for i := range r.Assets {
		name := strings.ToLower(r.Assets[i].Name)
		if !strings.Contains(name, token) {
			continue
		}
		switch {
		case strings.HasSuffix(name, ".zip"),
			strings.HasSuffix(name, ".apk"),
			strings.HasSuffix(name, ".gz"),
			strings.HasSuffix(name, ".tar"),
			strings.HasSuffix(name, ".tgz"),
			strings.HasSuffix(name, ".bz2"),
			strings.HasSuffix(name, ".xz"):
			continue
		}
		return &r.Assets[i]
	}
	return nil
}

func fetchReleases(owner, repo string) ([]githubRelease, error) {
	api := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases?per_page=100",
		url.PathEscape(owner), url.PathEscape(repo))
	req, err := http.NewRequest(http.MethodGet, api, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "rdio-scanner")
	res, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return nil, fmt.Errorf("github api returned %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	var releases []githubRelease
	if err := json.NewDecoder(res.Body).Decode(&releases); err != nil {
		return nil, err
	}
	return releases, nil
}

// isGithubDownloadUrl guards the downloader against SSRF: only release-asset
// URLs served from github.com are accepted.
func isGithubDownloadUrl(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return (host == "github.com" || strings.HasSuffix(host, ".github.com")) &&
		strings.Contains(u.Path, "/releases/download/")
}

func normalizeVersion(s string) string {
	return strings.TrimPrefix(strings.TrimSpace(s), "v")
}

func parseVersion(s string) ([3]int, []string) {
	s = normalizeVersion(s)
	pre := ""
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		pre = s[i+1:]
		s = s[:i]
	}
	var nums [3]int
	for i, p := range strings.SplitN(s, ".", 3) {
		if i > 2 {
			break
		}
		nums[i], _ = strconv.Atoi(strings.TrimFunc(p, func(r rune) bool { return r < '0' || r > '9' }))
	}
	var preParts []string
	if pre != "" {
		preParts = strings.FieldsFunc(pre, func(r rune) bool { return r == '.' || r == '-' })
	}
	return nums, preParts
}

// compareVersions returns >0 if a is newer than b, <0 if older, 0 if equal.
// A release without a prerelease suffix is newer than the same X.Y.Z with one,
// and prerelease identifiers compare numerically where both are numbers.
func compareVersions(a, b string) int {
	na, pa := parseVersion(a)
	nb, pb := parseVersion(b)
	for i := 0; i < 3; i++ {
		if na[i] != nb[i] {
			if na[i] > nb[i] {
				return 1
			}
			return -1
		}
	}
	if len(pa) == 0 && len(pb) == 0 {
		return 0
	}
	if len(pa) == 0 {
		return 1
	}
	if len(pb) == 0 {
		return -1
	}
	for i := 0; i < len(pa) && i < len(pb); i++ {
		ai, aerr := strconv.Atoi(pa[i])
		bi, berr := strconv.Atoi(pb[i])
		if aerr == nil && berr == nil {
			if ai != bi {
				if ai > bi {
					return 1
				}
				return -1
			}
		} else if c := strings.Compare(pa[i], pb[i]); c != 0 {
			return c
		}
	}
	if len(pa) != len(pb) {
		if len(pa) > len(pb) {
			return 1
		}
		return -1
	}
	return 0
}

// availableUpdate is the newest release for this platform/channel that is newer
// than the running version, as found by the hourly checker.
type availableUpdate struct {
	Version     string
	Branch      string
	Prerelease  bool
	PublishedAt string
	HtmlUrl     string
	assetUrl    string
	assetName   string
	assetSize   int64
}

// updateChecker runs an initial check shortly after boot, then hourly.
func (admin *Admin) updateChecker() {
	time.Sleep(15 * time.Second)
	for {
		admin.checkForUpdate()
		time.Sleep(time.Hour)
	}
}

// checkForUpdate fetches releases from the configured repo, picks the newest
// one carrying a binary for this server on the selected channel (stable, or
// including prereleases), and caches it when it is newer than the running
// version.
func (admin *Admin) checkForUpdate() {
	owner, repo, err := parseGitHubRepo(admin.updateRepoUrl())
	if err != nil {
		admin.setUpdateResult(nil, err.Error())
		return
	}
	releases, err := fetchReleases(owner, repo)
	if err != nil {
		admin.setUpdateResult(nil, err.Error())
		return
	}

	prereleases := admin.Controller.Options.UpdatePrereleases
	var best *githubRelease
	var bestAsset *githubAsset
	for i := range releases {
		r := &releases[i]
		if r.Draft || (r.Prerelease && !prereleases) {
			continue
		}
		a := assetForPlatform(*r)
		if a == nil {
			continue
		}
		if best == nil || compareVersions(r.TagName, best.TagName) > 0 {
			best = r
			bestAsset = a
		}
	}

	if best == nil || compareVersions(best.TagName, Version) <= 0 {
		admin.setUpdateResult(nil, "")
		return
	}
	admin.setUpdateResult(&availableUpdate{
		Version:     best.TagName,
		Branch:      best.Branch,
		Prerelease:  best.Prerelease,
		PublishedAt: best.PublishedAt,
		HtmlUrl:     best.HtmlUrl,
		assetUrl:    bestAsset.Url,
		assetName:   bestAsset.Name,
		assetSize:   bestAsset.Size,
	}, "")
}

func (admin *Admin) setUpdateResult(a *availableUpdate, errMsg string) {
	admin.updateMu.Lock()
	admin.updateAvail = a
	admin.updateErr = errMsg
	admin.updateChecked = time.Now()
	admin.updateMu.Unlock()
}

func writeJson(w http.ResponseWriter, v any) {
	b, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

func writeJsonError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	b, _ := json.Marshal(map[string]string{"error": msg})
	w.Write(b)
}

// downloadTo streams url into dst (atomically, via a temp file) and verifies the
// size when expectedSize > 0.
func downloadTo(rawUrl, dst string, expectedSize int64) error {
	req, err := http.NewRequest(http.MethodGet, rawUrl, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "rdio-scanner")
	res, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned %d", res.StatusCode)
	}

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".rdio-update-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	n, err := io.Copy(tmp, res.Body)
	tmp.Close()
	if err != nil {
		os.Remove(tmpName)
		return err
	}
	if expectedSize > 0 && n != expectedSize {
		os.Remove(tmpName)
		return fmt.Errorf("size mismatch: got %d, expected %d", n, expectedSize)
	}
	if runtime.GOOS != "windows" {
		os.Chmod(tmpName, 0o755)
	}
	os.Remove(dst)
	return os.Rename(tmpName, dst)
}

// pendingPath is where a downloaded-but-not-yet-applied binary is staged. It
// sits next to the running executable with a ".pending" suffix so a normal or
// supervisor restart never picks it up by accident — only the Update button
// renames it into place.
func pendingPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return exe + ".pending", nil
}

// writeUpdateState writes the current auto-update state (cached availability +
// settings) used by both the GET and the manual-check endpoints.
func (admin *Admin) writeUpdateState(w http.ResponseWriter) {
	admin.updateMu.Lock()
	avail := admin.updateAvail
	checkErr := admin.updateErr
	checkedAt := admin.updateChecked
	admin.updateMu.Unlock()

	owner, repo, _ := parseGitHubRepo(admin.updateRepoUrl())

	staged := false
	if p, err := pendingPath(); err == nil {
		if _, err := os.Stat(p); err == nil {
			staged = true
		}
	}

	var available any
	if avail != nil {
		available = map[string]any{
			"version":     avail.Version,
			"branch":      avail.Branch,
			"prerelease":  avail.Prerelease,
			"publishedAt": avail.PublishedAt,
			"htmlUrl":     avail.HtmlUrl,
			"asset":       avail.assetName,
		}
	}

	checked := ""
	if !checkedAt.IsZero() {
		checked = checkedAt.UTC().Format(time.RFC3339)
	}

	writeJson(w, map[string]any{
		"repo":           owner + "/" + repo,
		"repoUrl":        admin.updateRepoUrl(),
		"defaultRepo":    DefaultUpdateRepo,
		"customUrl":      strings.TrimSpace(admin.Controller.Options.UpdateUrl),
		"prereleases":    admin.Controller.Options.UpdatePrereleases,
		"currentVersion": Version,
		"platform":       platformToken(),
		"available":      available,
		"pending":        staged,
		"checkedAt":      checked,
		"error":          checkErr,
	})
}

// UpdatesHandler (GET /api/admin/updates) returns the cached auto-update state.
// If nothing has been checked yet (fresh boot), it runs one check first.
func (admin *Admin) UpdatesHandler(w http.ResponseWriter, r *http.Request) {
	if !admin.ValidateToken(admin.GetAuthorization(r)) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	admin.updateMu.Lock()
	never := admin.updateChecked.IsZero()
	admin.updateMu.Unlock()
	if never {
		admin.checkForUpdate()
	}
	admin.writeUpdateState(w)
}

// UpdateCheckHandler (POST /api/admin/update/check) forces a fresh check now.
func (admin *Admin) UpdateCheckHandler(w http.ResponseWriter, r *http.Request) {
	if !admin.ValidateToken(admin.GetAuthorization(r)) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	admin.checkForUpdate()
	admin.writeUpdateState(w)
}

// UpdateSourceHandler (POST /api/admin/update/source) persists the custom
// Update URL (empty = revert to the fork's DefaultUpdateRepo) and the release
// channel (prereleases on/off), then re-checks and returns the new state.
func (admin *Admin) UpdateSourceHandler(w http.ResponseWriter, r *http.Request) {
	if !admin.ValidateToken(admin.GetAuthorization(r)) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Url         string `json:"url"`
		Prereleases *bool  `json:"prereleases"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJsonError(w, http.StatusBadRequest, "invalid request")
		return
	}
	u := strings.TrimSpace(body.Url)
	if u != "" {
		if _, _, err := parseGitHubRepo(u); err != nil {
			writeJsonError(w, http.StatusBadRequest, fmt.Sprintf("invalid update URL: %v", err))
			return
		}
	}
	admin.Controller.Options.UpdateUrl = u
	if body.Prereleases != nil {
		admin.Controller.Options.UpdatePrereleases = *body.Prereleases
	}
	if err := admin.Controller.Options.Write(admin.Controller.Database); err != nil {
		writeJsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	admin.checkForUpdate()
	admin.writeUpdateState(w)
}

// UpdateDownloadHandler (POST /api/admin/update/download) downloads the binary
// for the currently-available update (matched to THIS server's OS/arch by the
// checker) and stages it as <exe>.pending. It does not touch the running binary.
func (admin *Admin) UpdateDownloadHandler(w http.ResponseWriter, r *http.Request) {
	if !admin.ValidateToken(admin.GetAuthorization(r)) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	admin.updateMu.Lock()
	avail := admin.updateAvail
	admin.updateMu.Unlock()
	if avail == nil {
		writeJsonError(w, http.StatusBadRequest, "no update available")
		return
	}
	if !isGithubDownloadUrl(avail.assetUrl) {
		writeJsonError(w, http.StatusBadRequest, "refusing to download from a non-GitHub URL")
		return
	}

	pending, err := pendingPath()
	if err != nil {
		writeJsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	admin.Controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf("admin: downloading update %s (%s)", avail.Version, avail.assetName))
	if err := downloadTo(avail.assetUrl, pending, avail.assetSize); err != nil {
		admin.Controller.Logs.LogEvent(LogLevelError, fmt.Sprintf("admin: update download failed: %v", err))
		writeJsonError(w, http.StatusBadGateway, fmt.Sprintf("download failed: %v", err))
		return
	}

	writeJson(w, map[string]any{
		"ok":      true,
		"version": avail.Version,
		"branch":  avail.Branch,
		"asset":   avail.assetName,
		"pending": pending,
		"size":    avail.assetSize,
	})
}

// UpdateCancelHandler (POST /api/admin/update/cancel) discards a staged update.
func (admin *Admin) UpdateCancelHandler(w http.ResponseWriter, r *http.Request) {
	if !admin.ValidateToken(admin.GetAuthorization(r)) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if p, err := pendingPath(); err == nil {
		os.Remove(p)
	}
	writeJson(w, map[string]any{"ok": true})
}

// UpdateApplyHandler (POST /api/admin/update/apply) swaps the staged
// <exe>.pending into place (backing up the running binary as <exe>.old) and
// restarts the server so the new binary takes over.
func (admin *Admin) UpdateApplyHandler(w http.ResponseWriter, r *http.Request) {
	if !admin.ValidateToken(admin.GetAuthorization(r)) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	exe, err := os.Executable()
	if err != nil {
		writeJsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	pending := exe + ".pending"
	if _, err := os.Stat(pending); err != nil {
		writeJsonError(w, http.StatusBadRequest, "no staged update — download one first")
		return
	}

	old := exe + ".old"
	os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		writeJsonError(w, http.StatusInternalServerError, fmt.Sprintf("could not back up current binary: %v", err))
		return
	}
	if err := os.Rename(pending, exe); err != nil {
		os.Rename(old, exe) // roll back
		writeJsonError(w, http.StatusInternalServerError, fmt.Sprintf("could not swap in update: %v", err))
		return
	}

	admin.Controller.Logs.LogEvent(LogLevelWarn, "admin: applying update and restarting")
	writeJson(w, map[string]any{"ok": true, "restarting": true})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	// Give the response a moment to flush to the client, then restart into the
	// freshly-swapped binary (platform-specific — see update_unix/windows.go).
	go func() {
		time.Sleep(750 * time.Millisecond)
		restartSelf(exe)
	}()
}
