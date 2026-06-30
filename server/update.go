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
// OS/arch (the server-side OS check), skipping the Android APK.
func assetForPlatform(r githubRelease) *githubAsset {
	token := platformToken()
	for i := range r.Assets {
		name := strings.ToLower(r.Assets[i].Name)
		if strings.HasSuffix(name, ".apk") {
			continue
		}
		if strings.Contains(name, token) {
			return &r.Assets[i]
		}
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

// UpdatesHandler (GET /api/admin/updates) lists the releases available from the
// configured repo, each tagged with its branch + prerelease flag, plus the
// running version, this server's platform, and whether an update is staged.
func (admin *Admin) UpdatesHandler(w http.ResponseWriter, r *http.Request) {
	if !admin.ValidateToken(admin.GetAuthorization(r)) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	repoUrl := admin.updateRepoUrl()
	owner, repo, err := parseGitHubRepo(repoUrl)
	if err != nil {
		writeJsonError(w, http.StatusBadRequest, fmt.Sprintf("invalid update URL: %v", err))
		return
	}

	releases, err := fetchReleases(owner, repo)
	if err != nil {
		writeJsonError(w, http.StatusBadGateway, err.Error())
		return
	}

	type relOut struct {
		Version     string `json:"version"`
		Branch      string `json:"branch"`
		Prerelease  bool   `json:"prerelease"`
		PublishedAt string `json:"publishedAt"`
		HtmlUrl     string `json:"htmlUrl"`
		HasAsset    bool   `json:"hasAsset"`
		Current     bool   `json:"current"`
	}

	cur := normalizeVersion(Version)
	out := []relOut{}
	for _, rel := range releases {
		if rel.Draft {
			continue
		}
		out = append(out, relOut{
			Version:     rel.TagName,
			Branch:      rel.Branch,
			Prerelease:  rel.Prerelease,
			PublishedAt: rel.PublishedAt,
			HtmlUrl:     rel.HtmlUrl,
			HasAsset:    assetForPlatform(rel) != nil,
			Current:     normalizeVersion(rel.TagName) == cur,
		})
	}

	pending := ""
	if p, err := pendingPath(); err == nil {
		if _, err := os.Stat(p); err == nil {
			pending = p
		}
	}

	writeJson(w, map[string]any{
		"repo":           owner + "/" + repo,
		"repoUrl":        repoUrl,
		"defaultRepo":    DefaultUpdateRepo,
		"customUrl":      strings.TrimSpace(admin.Controller.Options.UpdateUrl),
		"currentVersion": Version,
		"platform":       platformToken(),
		"releases":       out,
		"pending":        pending != "",
	})
}

// UpdateDownloadHandler (POST /api/admin/update/download) downloads the binary
// matching THIS server's OS/arch for the requested version and stages it as
// <exe>.pending. It does not touch the running binary.
func (admin *Admin) UpdateDownloadHandler(w http.ResponseWriter, r *http.Request) {
	if !admin.ValidateToken(admin.GetAuthorization(r)) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Version) == "" {
		writeJsonError(w, http.StatusBadRequest, "missing version")
		return
	}

	owner, repo, err := parseGitHubRepo(admin.updateRepoUrl())
	if err != nil {
		writeJsonError(w, http.StatusBadRequest, fmt.Sprintf("invalid update URL: %v", err))
		return
	}
	releases, err := fetchReleases(owner, repo)
	if err != nil {
		writeJsonError(w, http.StatusBadGateway, err.Error())
		return
	}

	var rel *githubRelease
	for i := range releases {
		if releases[i].TagName == body.Version {
			rel = &releases[i]
			break
		}
	}
	if rel == nil {
		writeJsonError(w, http.StatusNotFound, "version not found")
		return
	}

	asset := assetForPlatform(*rel)
	if asset == nil {
		writeJsonError(w, http.StatusBadRequest, fmt.Sprintf("no %s binary in release %s", platformToken(), rel.TagName))
		return
	}
	if !isGithubDownloadUrl(asset.Url) {
		writeJsonError(w, http.StatusBadRequest, "refusing to download from a non-GitHub URL")
		return
	}

	pending, err := pendingPath()
	if err != nil {
		writeJsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	admin.Controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf("admin: downloading update %s (%s)", rel.TagName, asset.Name))
	if err := downloadTo(asset.Url, pending, asset.Size); err != nil {
		admin.Controller.Logs.LogEvent(LogLevelError, fmt.Sprintf("admin: update download failed: %v", err))
		writeJsonError(w, http.StatusBadGateway, fmt.Sprintf("download failed: %v", err))
		return
	}

	writeJson(w, map[string]any{
		"ok":      true,
		"version": rel.TagName,
		"branch":  rel.Branch,
		"asset":   asset.Name,
		"pending": pending,
		"size":    asset.Size,
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
