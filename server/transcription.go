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
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	transcriptionHTTPTimeout = 120 * time.Second
	// Groq enforces a 896-character hard cap on the `prompt` multipart
	// field (stricter than OpenAI's 224-token guideline). Anything longer
	// returns HTTP 400. We truncate on our side, keeping the trailing
	// characters (where domain-specific vocabulary tends to live) so
	// Whisper still sees the most useful hints.
	transcriptionPromptMaxChars = 896
)

// errTranscriptionSkipped is returned by Transcribe when no key is currently
// usable (all paused on backoff or at per-key cap). Callers can tell this
// apart from a real upstream failure and log at Info instead of Warn.
var errTranscriptionSkipped = errors.New("transcription skipped")

type Transcriber struct {
	Controller *Controller

	keysMu     sync.Mutex
	keys       []*transcriptionKey
	keysHash   string
	nextKeyIdx int
}

type transcriptionKey struct {
	value       string
	pausedUntil time.Time
	// recent holds the timestamps of requests in the last rolling minute so
	// each key is rate-limited independently. Groq's 30/min is a per-key
	// cap, so rotation only helps if we track per-key usage.
	recent []time.Time
}

func NewTranscriber(controller *Controller) *Transcriber {
	return &Transcriber{Controller: controller}
}

// refreshKeys parses the configured transcriptionApiKey value (which may hold
// multiple keys separated by commas, newlines, or whitespace) and updates the
// in-memory key ring when it changes. Safe to call from any goroutine.
func (t *Transcriber) refreshKeys() {
	raw := t.Controller.Options.TranscriptionApiKey
	splitter := func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == ' ' || r == '\t' || r == ';'
	}
	parts := strings.FieldsFunc(raw, splitter)

	t.keysMu.Lock()
	defer t.keysMu.Unlock()

	hash := strings.Join(parts, "|")
	if hash == t.keysHash {
		return
	}

	t.keysHash = hash
	t.keys = make([]*transcriptionKey, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			t.keys = append(t.keys, &transcriptionKey{value: p})
		}
	}
	if t.nextKeyIdx >= len(t.keys) {
		t.nextKeyIdx = 0
	}
}

// reserveKey atomically walks the key ring starting at nextKeyIdx, picks the
// first key that is (a) not in a 429 cooldown and (b) not already at its
// per-key per-minute cap, records the current timestamp against that key,
// advances the round-robin cursor, and returns the key value. When nothing
// is currently usable, returns ("", reason) with a human-readable explanation.
func (t *Transcriber) reserveKey() (key string, reason string) {
	t.refreshKeys()

	t.keysMu.Lock()
	defer t.keysMu.Unlock()

	if len(t.keys) == 0 {
		return "", "no transcription api key configured"
	}

	now := time.Now()
	cutoff := now.Add(-time.Minute)
	max := t.Controller.Options.TranscriptionMaxPerMinute

	n := len(t.keys)
	allPaused := true
	var earliestResume time.Time

	for i := 0; i < n; i++ {
		idx := (t.nextKeyIdx + i) % n
		k := t.keys[idx]

		if now.Before(k.pausedUntil) {
			if earliestResume.IsZero() || k.pausedUntil.Before(earliestResume) {
				earliestResume = k.pausedUntil
			}
			continue
		}
		allPaused = false

		// Prune this key's window.
		trimmed := k.recent[:0]
		for _, ts := range k.recent {
			if ts.After(cutoff) {
				trimmed = append(trimmed, ts)
			}
		}
		k.recent = trimmed

		if max > 0 && uint(len(k.recent)) >= max {
			continue
		}

		k.recent = append(k.recent, now)
		t.nextKeyIdx = (idx + 1) % n
		return k.value, ""
	}

	if allPaused && !earliestResume.IsZero() {
		return "", fmt.Sprintf("all keys paused ~%.0fs (upstream backoff)", earliestResume.Sub(now).Seconds())
	}
	if max > 0 {
		return "", fmt.Sprintf("all keys at per-key cap (%d/min)", max)
	}
	return "", "no key available"
}

func (t *Transcriber) pauseKey(key string, until time.Time) {
	t.keysMu.Lock()
	defer t.keysMu.Unlock()
	for _, k := range t.keys {
		if k.value == key && until.After(k.pausedUntil) {
			k.pausedUntil = until
		}
	}
}

// isPromptSpace reports whether b is whitespace-ish for purposes of
// truncating a prompt at a safe boundary.
func isPromptSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == ',' || b == ';' || b == '.'
}

// parseUpstreamBackoff inspects a 429 response and returns how long we
// should sit out. Falls back to a 60-second pause if nothing obvious.
func parseUpstreamBackoff(resp *http.Response, body []byte) time.Duration {
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(ra)); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
		if t, err := http.ParseTime(ra); err == nil {
			if d := time.Until(t); d > 0 {
				return d
			}
		}
	}
	// Groq-style "Please try again in 43.2s."
	re := regexp.MustCompile(`(?i)try again in\s+([0-9]+(?:\.[0-9]+)?)\s*(ms|s|m)`)
	if m := re.FindStringSubmatch(string(body)); len(m) == 3 {
		v, _ := strconv.ParseFloat(m[1], 64)
		switch strings.ToLower(m[2]) {
		case "ms":
			return time.Duration(v * float64(time.Millisecond))
		case "m":
			return time.Duration(v * float64(time.Minute))
		default:
			return time.Duration(v * float64(time.Second))
		}
	}
	return 60 * time.Second
}

func (t *Transcriber) Enabled() bool {
	opts := t.Controller.Options
	if !opts.TranscriptionEnabled {
		return false
	}
	t.refreshKeys()
	t.keysMu.Lock()
	n := len(t.keys)
	t.keysMu.Unlock()
	return n > 0
}

func (t *Transcriber) audioFilename(call *Call) string {
	if name, ok := call.AudioName.(string); ok && name != "" {
		return name
	}
	if typ, ok := call.AudioType.(string); ok {
		switch typ {
		case "audio/mp4", "audio/m4a", "audio/x-m4a":
			return "call.m4a"
		case "audio/mpeg", "audio/mp3":
			return "call.mp3"
		case "audio/wav", "audio/x-wav":
			return "call.wav"
		case "audio/ogg":
			return "call.ogg"
		case "audio/flac":
			return "call.flac"
		case "audio/webm":
			return "call.webm"
		}
	}
	return "call.m4a"
}

// Transcribe runs a single call's audio through the Whisper endpoint.
// On a 429 (rate limit) or transient server error, the current key is
// marked paused / skipped and the request is automatically retried on the
// next available key until one succeeds, all keys are exhausted, or we
// hit a permanent error.
func (t *Transcriber) Transcribe(call *Call) (string, error) {
	opts := t.Controller.Options

	if !opts.TranscriptionEnabled {
		return "", errors.New("transcription disabled")
	}

	if len(call.Audio) <= 44 {
		return "", errors.New("no audio")
	}

	baseUrl := strings.TrimRight(strings.TrimSpace(opts.TranscriptionBaseUrl), "/")
	if baseUrl == "" {
		baseUrl = "https://api.groq.com/openai/v1"
	}

	model := strings.TrimSpace(opts.TranscriptionModel)
	if model == "" {
		model = "whisper-large-v3-turbo"
	}

	// Build the multipart body once; reuse the bytes for every retry so we
	// never re-read / re-encode the audio blob per attempt.
	body, contentType, err := t.buildTranscriptionBody(call, model)
	if err != nil {
		return "", err
	}

	// Walk the key ring, retrying on 429 / 5xx / network errors. Tracked
	// keys-already-tried prevents infinite loops when reserveKey would
	// hand us the same key again after many are paused.
	tried := make(map[string]bool)
	var lastErr error

	for attempt := 0; attempt < 8; attempt++ {
		apiKey, reason := t.reserveKey()
		if apiKey == "" {
			if lastErr != nil {
				return "", lastErr
			}
			if reason == "" {
				reason = "no transcription api key available"
			}
			return "", fmt.Errorf("%w: %s", errTranscriptionSkipped, reason)
		}
		if tried[apiKey] {
			// reserveKey gave us a key we already tried this call — the
			// remaining keys must all be paused/capped, so stop.
			if lastErr != nil {
				return "", lastErr
			}
			return "", fmt.Errorf("%w: exhausted all keys", errTranscriptionSkipped)
		}
		tried[apiKey] = true

		text, err, retryable := t.callTranscriptionAPI(baseUrl, apiKey, contentType, body)
		if err == nil {
			return text, nil
		}
		lastErr = err
		if !retryable {
			return "", err
		}
		// Otherwise loop around; next reserveKey() call will skip paused
		// keys and hand us a fresh one.
	}

	if lastErr != nil {
		return "", lastErr
	}
	return "", errors.New("transcription exhausted retries")
}

// buildTranscriptionBody produces the multipart body bytes + content-type
// exactly once; callTranscriptionAPI wraps them in a fresh Reader on
// every attempt so retries don't consume the stream.
func (t *Transcriber) buildTranscriptionBody(call *Call, model string) ([]byte, string, error) {
	opts := t.Controller.Options

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	fileWriter, err := writer.CreateFormFile("file", t.audioFilename(call))
	if err != nil {
		return nil, "", fmt.Errorf("multipart: %v", err)
	}
	if _, err = fileWriter.Write(call.Audio); err != nil {
		return nil, "", fmt.Errorf("multipart write: %v", err)
	}

	if err = writer.WriteField("model", model); err != nil {
		return nil, "", err
	}
	if err = writer.WriteField("response_format", "json"); err != nil {
		return nil, "", err
	}
	if lang := strings.TrimSpace(opts.TranscriptionLanguage); lang != "" {
		if err = writer.WriteField("language", lang); err != nil {
			return nil, "", err
		}
	}
	prompt := ""
	if sys, ok := t.Controller.Systems.GetSystem(call.System); ok && sys != nil {
		prompt = strings.TrimSpace(sys.TranscriptionPrompt)
	}
	if prompt == "" {
		prompt = strings.TrimSpace(opts.TranscriptionPrompt)
	}
	if origLen := len(prompt); origLen > transcriptionPromptMaxChars {
		start := origLen - transcriptionPromptMaxChars
		for start < origLen && !isPromptSpace(prompt[start]) {
			start++
		}
		prompt = strings.TrimSpace(prompt[start:])
		t.Controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf("transcription prompt truncated to %d chars (was %d)", len(prompt), origLen))
	}
	if prompt != "" {
		if err = writer.WriteField("prompt", prompt); err != nil {
			return nil, "", err
		}
	}

	if err = writer.Close(); err != nil {
		return nil, "", err
	}
	return body.Bytes(), writer.FormDataContentType(), nil
}

// callTranscriptionAPI performs a single HTTP attempt. Returns:
//   - (text, nil, _)           on success
//   - ("",   err, true)        on 429 / 5xx / network error → caller should
//                              retry with a fresh key
//   - ("",   err, false)       on permanent failure → caller should stop
func (t *Transcriber) callTranscriptionAPI(baseUrl, apiKey, contentType string, body []byte) (string, error, bool) {
	req, err := http.NewRequest(http.MethodPost, baseUrl+"/audio/transcriptions", bytes.NewReader(body))
	if err != nil {
		return "", err, false
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", contentType)

	client := &http.Client{Timeout: transcriptionHTTPTimeout}
	resp, err := client.Do(req)
	if err != nil {
		// Network-level failure — try the next key, it might be a DNS or
		// TLS blip specific to this request path.
		return "", fmt.Errorf("transcription api network error: %v", err), true
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("transcription api read: %v", err), true
	}

	if resp.StatusCode == 429 {
		backoff := parseUpstreamBackoff(resp, respBody)
		t.pauseKey(apiKey, time.Now().Add(backoff))
		t.Controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf("transcription 429 on key …%s, paused %s; trying next key", keyTail(apiKey), backoff))
		return "", fmt.Errorf("transcription api 429 rate-limited on key; paused %s", backoff), true
	}

	if resp.StatusCode >= 500 {
		return "", fmt.Errorf("transcription api status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody))), true
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("transcription api status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody))), false
	}

	var parsed struct {
		Text  string `json:"text"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err = json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("transcription response: %v", err), false
	}
	if parsed.Error.Message != "" {
		return "", errors.New(parsed.Error.Message), false
	}

	return strings.TrimSpace(parsed.Text), nil, false
}

// keyTail returns the trailing 4 characters of an API key for safe log
// lines ("…Kf2A") without leaking the full secret.
func keyTail(k string) string {
	if len(k) <= 4 {
		return k
	}
	return k[len(k)-4:]
}

func (t *Transcriber) TranscribeCallAsync(id uint, call *Call) {
	if !t.Enabled() {
		return
	}

	if minBytes := t.Controller.Options.TranscriptionMinAudioBytes; minBytes > 0 && uint(len(call.Audio)) < minBytes {
		return
	}

	go func(id uint, call *Call) {
		text, err := t.Transcribe(call)
		if err != nil {
			if errors.Is(err, errTranscriptionSkipped) {
				t.Controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf("transcription skipped for call %v: %v", id, err))
			} else {
				t.Controller.Logs.LogEvent(LogLevelWarn, fmt.Sprintf("transcription failed for call %v: %v", id, err))
			}
			return
		}

		if err = t.Controller.Calls.UpdateTranscript(id, text, t.Controller.Database); err != nil {
			t.Controller.Logs.LogEvent(LogLevelError, fmt.Sprintf("transcription persist failed for call %v: %v", id, err))
			return
		}

		call.Transcript = text
		// Broadcast to live clients so anything showing call history
		// (main LCD, search list) can splice the transcript into its
		// row the moment Whisper finishes.
		t.Controller.Clients.EmitTranscript(id, call.System, call.Talkgroup, text, t.Controller.Accesses.IsRestricted())
		t.Controller.Logs.LogEvent(LogLevelInfo, fmt.Sprintf("transcribed call %v (%d chars)", id, len(text)))
	}(id, call)
}
