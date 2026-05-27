# Change log

## Unreleased

_(nothing yet — bullets land here as work is merged to master)_

---

## Released

## Version 6.11.0-beta.10

UX polish on the per-provider model fields in Admin → Options.

### Webapp

- Each provider's model field gains a **filterable autocomplete dropdown** powered by `MatAutocompleteModule`. The fields stay plain-text inputs — users can still type anything their backend accepts — but a list of common preset model identifiers drops down below the field for quick selection.
- Preset lists per provider:
  - **Groq:** `whisper-large-v3-turbo`, `whisper-large-v3`.
  - **Whisper (OpenAI):** `whisper-1`.
  - **Whisper (self-hosted):** `whisper-1`, the four `whisper-*` size variants (large-v3-turbo, large-v3, large-v2, medium, small, base, tiny), and the parallel `Systran/faster-whisper-*` set for faster-whisper-server users.
- Typing into the field filters the dropdown by case-insensitive substring match — useful for the longer self-hosted list.
- Added `MatAutocompleteModule` to the shared material module.

## Version 6.11.0-beta.9

Breaks an infinite transcript-forwarding loop in cyclic downstream topologies (server A is B's downstream **and** B is A's downstream — bidirectional setup). Previously the same transcript would bounce between the two servers endlessly, generating thousands of `transcript push received` / `downstream.transcript: ... success` log lines per minute and pegging the network.

### The fix

Mirrors what `CheckDuplicate` does for calls: "if a record already exists, treat the new arrival as a duplicate and reject." For transcripts: **if the call already has any transcript on it, treat the new push as a duplicate** — skip the broadcast and the chain-forward.

### Server

- **New `Calls.UpdateTranscriptIfEmpty(id, transcript, db) (applied bool, err error)`** in `call.go`. Reads the existing transcript first; only writes (and returns `applied=true`) when the existing transcript is empty/null. Returns `applied=false` when the call already has a transcript, mirroring how `CheckDuplicate` short-circuits on existing call records.
- **`CallTranscriptHandler` uses `UpdateTranscriptIfEmpty`** on both apply paths (synchronous and race-window-close). When the call already has a transcript, the handler:
  - Skips `EmitTranscript` (no point re-rendering the same call row)
  - Skips the chain-forward to downstreams (this is what breaks the loop)
  - Still cancels any pending fallback-transcription timer (the upstream did try to deliver)
  - Returns HTTP 200 with "Transcript already applied (no-op)" — the upstream's send is acknowledged successfully
- **New log line** matching the call-side `duplicate call rejected` wording:
  `transcript push duplicate: [ident] system=X talkgroup=Y id=Z (call already has a transcript, rejected)`

### What stays the same

- **First-arrived wins** for any given call's transcript — exactly like calls. If upstream A and upstream B both transcribe the same call independently, whichever push lands first sticks. Second push is rejected.
- **Local transcription** (`TranscribeCallAsync`) still uses plain `UpdateTranscript` — by definition it just produced this transcript, so the call had none before.
- **`IngestCall` cache-hit** path still uses plain `UpdateTranscript` — the call was just `WriteCall`'d with no transcript, applying from cache is the first write.
- **Admin retranscribe** button still uses plain `UpdateTranscript` — user explicitly clicked, they want to override.
- **A → B chain forwarding** still works: B has empty transcript, applies, forwards onward.
- **A → B → C linear chain** still works: each hop has empty transcript on first arrival.

### What this prevents

- The loop you saw with `Richad`'s server: hundreds of duplicate log lines per second, every transcript pushed back and forth across the network endlessly. Now the second arrival is dedup'd at the receiver and the chain-forward stops.

## Version 6.11.0-beta.8

Bump the wait-for-transcript hold from 15 s → 20 s. Self-hosted Whisper on average hardware regularly takes 10–15 s for typical radio call audio; 20 s gives the legitimate transcript enough headroom to land before the timeout fires while still being short enough that a stuck call doesn't sit too long without playing.

`TRANSCRIPT_WAIT_MAX_MS = 20000` in `rdio-scanner.service.ts`. Calls that arrive late (after 20 s) still get their transcript spliced into the already-played call via `applyLateTranscript`, so no transcript is ever lost — just appears retroactively.

## Version 6.11.0-beta.7

Closes the last transcript-reliability gap: when an upstream forwards a call with `transcriptPending=1` but its own Whisper runtime-fails (rate-limited, network error, all keys paused) and never delivers the transcript, this server now falls back to transcribing the call locally after 2 minutes.

### Server

- **New `FallbackTranscripts` struct** (`server/fallback_transcripts.go`) — a thin wrapper around `map[uint]*time.Timer` with `Schedule(id, fn)` and `Cancel(id) bool`. Wired onto `Controller.FallbackTranscripts`.
- **`IngestCall` schedules a fallback** when all of the following are true:
  - The call arrived with `transcriptPending=1` (upstream claimed it would transcribe)
  - No transcript was applied from the server-side `PendingTranscripts` cache (so we don't already have one)
  - Local transcription is configured on this server (`Transcriber.Enabled()` and audio passes the same size predicate the regular transcribe path uses)
  
  Timer fires after `fallbackTranscriptTTL` (2 minutes). When it fires, the call is refetched from the DB (audio is stored, in-memory blob is GC'd by then), and if no transcript was applied in the interim, `TranscribeCallAsync` runs as a normal local transcription. The result flows through the existing pipeline — `EmitTranscript` to WS clients, chain-forward to downstreams, etc.

- **`CallTranscriptHandler` cancels the fallback** when the upstream's transcript finally lands (both the synchronous apply path and the race-window-close path). Avoids the wasted local Whisper call.

- **`IngestCall` cache-hit path also cancels** defensively (the schedule check wouldn't have fired anyway, but cancel-by-id is idempotent).

### New log lines

- `fallback transcription scheduled: id=X (will run local Whisper in 2m0s if upstream transcript hasn't arrived)` — at schedule time
- `fallback transcription cancelled: id=X (transcript arrived from upstream)` — when upstream's transcript lands within the window
- `fallback transcription firing: id=X system=Y talkgroup=Z (upstream transcript never arrived, running local Whisper)` — when timer fires
- `fallback transcription: cannot refetch call id=X: <err>` — DB error trying to refetch (rare)

### TTL is hardcoded

`fallbackTranscriptTTL = 2 * time.Minute` is a constant for now. Most upstream transcripts arrive within 30s, so 2 min is comfortably past "should have shown up by now" without making the user wait noticeably longer than necessary. If you want to tune this per-deployment, I can wire it into the admin Options as `TranscriptionFallbackSeconds` in a follow-up.

### Listener-side UX

Combined with the `applyLateTranscript` change from beta.6, the worst-case experience for a failed-upstream transcript is:

1. Call plays at +15 s (wait-for-transcript timeout — `TRANSCRIPT_WAIT_MAX_MS`)
2. Around +2:00 the fallback fires
3. Local Whisper finishes (a few more seconds)
4. Transcript appears retroactively on the already-played call in the LCD/livefeed/history

No more permanently-untranscribed calls because the upstream silently failed.

## Version 6.11.0-beta.6

UX tweak to the wait-for-transcript hold based on real-world self-hosted Whisper latencies.

### Webapp

- **Hold timeout reduced from 30 s → 15 s.** Self-hosted Whisper runs can be unpredictable; 30 s held calls back too aggressively. 15 s strikes a better balance — most transcripts complete by then, and a call gets played reasonably promptly when one doesn't.
- **Late-arriving transcripts now apply in-place.** Previously, if the 15/30-second timeout fired and released a call without a transcript, a transcript that came in afterwards was effectively lost (the resolver had already been cleaned up). New `applyLateTranscript(id, text)` runs on every WS-pushed transcript and splices the text into:
  - any pending-transcript entry still in the pre-queue (so it has the transcript when released)
  - the currently-playing call (re-emits so the LCD/livefeed panel re-renders)
  - any call still sitting in the main playback queue
  This is on top of the existing `transcriptReady` event so the history rows still get notified.

### Not yet (Q1 from user)

The user asked: "will calls that don't get transcribed by the first server get transcribed by the second server?". Two cases:

- **First server's transcription is disabled / doesn't apply** (system or talkgroup `Transcribe` off, audio too short, etc.) — `transcriptPending=1` is never set on the forwarded call, the second server transcribes locally. **Already works.**
- **First server tried to transcribe but failed at runtime** (Whisper rate-limited, network error, all keys paused) — `transcriptPending=1` was set, the second server skipped local transcription, and the transcript push never arrived. The call stays untranscribed after the 5-min server-side cache TTL expires. **Not handled yet.** Adding a server-side fallback transcription timer is a follow-up.

## Version 6.11.0-beta.5

Fixes the bug beta.4 was supposed to fix.

**The case beta.4 missed:** in a forwarding setup, an upstream's transcript can arrive at the receiving server **before its call**, and on this server the deferred transcript is applied to the call's `IngestCall` path immediately. By the time the call hits the WebSocket frame heading out to the listener, it already has `call.transcript` populated.

The webapp's `queue()` was checking `!call.transcript` to decide whether to hold the call — so pre-transcribed calls **skipped the pre-queue entirely** and went straight to the main queue, jumping ahead of earlier calls still parked waiting on a local Whisper run. Exactly what was observed: a Richard call that arrived 30+ seconds *after* five Ambulance calls played first because Richard's transcript was already attached on arrival, while the Ambulance calls were still parked in the pre-queue.

### Fix

In `queue()`, when `waitForTranscript` is on, **all** non-priority calls are routed through `holdPendingTranscript` regardless of whether they arrived with a transcript. The pre-queue is the single ordering point.

`holdPendingTranscript` now handles two cases:
- Call arrived with a transcript: entry goes in marked `ready: true`, no fetch/timeout timers. `drainPendingHead` releases it the moment all earlier entries are also ready.
- Call arrived without a transcript: existing behaviour — entry goes in `ready: false` with poll + timeout timers, flipped to ready when the transcript lands or the timeout fires.

Net effect: arrival-order playback is now actually enforced when wait-for-transcript is enabled, regardless of whether transcripts came pre-attached or have to be fetched.

## Version 6.11.0-beta.4

Call-ordering audit fixes. Plays calls strictly in arrival order even when transcript-forwarding latency varies or when multiple emit paths fire concurrently on the server.

### Webapp — pre-queue head-of-line release

**The bug:** when `WaitForTranscript` was on, the `pendingTranscriptCalls` Map released held calls in transcript-arrival order, not call-arrival order. A short audio's transcript would finish first and that call would skip ahead of an earlier longer call still waiting on Whisper.

**The fix:** `pendingTranscriptCalls` is now an ordered array. Each entry has a `ready` flag flipped by either the transcript landing or the timeout firing. The new `drainPendingHead()` pops consecutive head-of-line ready entries off the array — entries behind a not-yet-ready head wait until the head is released, so arrival order is preserved end-to-end. `flushPendingTranscripts()` and the talkgroup-deactivated cleanup both walk the array in order too.

### Server — serialized emit dispatchers

**The race:** `EmitCallToClients` and `EmitCallToDownstreams` previously spawned a goroutine per call (`go Clients.EmitCall(...)` / `go Downstreams.Send(...)`). Two concurrent goroutines competing for the same per-client `Send` channel had no FIFO guarantee — Go's scheduler could pick either one first. Microsecond window in practice but real.

**The fix:** New `clientEmitQueue` and `downstreamEmitQueue` buffered channels (8192 slots each) on `Controller`. Each has a dedicated dispatcher goroutine started in `Start()` (before `Delayer.Start()` so its catchup emits drain immediately rather than buffer with no consumer). All `EmitCallToClients` / `EmitCallToDownstreams` calls push to the appropriate channel; dispatchers drain in strict FIFO. Separate channels because the downstream path is HTTP-slow and shouldn't hold up local listener broadcasts.

### Server — Delayer restart catchup ordering

`Delayer.Start()` previously iterated the loaded `pending` map directly via `for k, v := range`, which Go intentionally randomizes. After a server restart, elapsed delayed calls were re-emitted in random order. Now sorted by scheduled timestamp ascending so the catchup pass is chronological.

### Not in this beta

Android `_calls.tryEmit` is also wrapped in `scope.launch { ... }` (`RdioClient.kt:384`), which has the same coroutine-launch reorder risk as the server-side goroutine-per-call. Microsecond window, never observed in practice. Will follow up as its own beta after this one is verified.

## Version 6.11.0-beta.3

UX iteration on the provider switching from beta.2.

### Webapp

- **Provider selector is now a `mat-button-toggle-group`** (segmented buttons) instead of a dropdown — selected provider is visible at a glance and a single click switches without an extra dropdown-open step.
- **Per-provider config blocks now use `[ngSwitch]`** (rather than three independent `*ngIf`s) with the active provider read via a component-level getter — fixes the bug where switching providers wouldn't actually swap the visible fields. Saved per-provider data is shown immediately on switch.
- **Model fields are free-text inputs** for all three providers. The mat-select with two hard-coded Groq options is gone — operators can enter whatever model identifier their provider accepts. Hints removed since the right value depends on the user's provider/version anyway.
- **Prompt cap is now provider-aware in the UI hint**: shows "X / 896" with the red-at-overflow style only when Groq is active; other providers show just "X" and a note that no length cap is enforced.

### Server

- **`Options.FromMap` now defensively strips `/audio/transcriptions` and trailing slashes** from all transcription base-URL fields before saving. Pasting the full request URL into the admin form no longer produces a doubled `/audio/transcriptions/audio/transcriptions` path at request time.
- **Prompt truncation is now provider-conditional.** The 896-character hard cap only applies when the active provider is Groq (it's Groq's API limit). OpenAI and self-hosted providers receive the prompt verbatim, no truncation. Documented in the new comment block and surfaced in the UI hint.

## Version 6.11.0-beta.2

Simplification of the provider dropdown from beta.1. Faster-Whisper is no longer its own option — since whisper.cpp, openai-whisper-server, and faster-whisper-server all expose the same OpenAI-compatible HTTP protocol and only differ in their accepted model identifiers, they collapse cleanly into a single "Whisper (self-hosted)" option where the user supplies their server's preferred model name.

### Server

- Dropped `faster-whisper-selfhosted` from the `TranscriptionProvider` enum. Three values now: `groq`, `openai`, `whisper-selfhosted`.
- Removed the `TranscriptionFasterWhisperBaseUrl` / `TranscriptionFasterWhisperApiKey` / `TranscriptionFasterWhisperModel` fields.
- Anyone testing beta.1 with the Faster-Whisper option needs to move their values into the Whisper (self-hosted) fields after upgrading.

### Webapp

- Provider dropdown now reads "Groq", "Whisper (OpenAI)", "Whisper (self-hosted)" — the OpenAI label clarifies it's specifically the hosted Whisper API.
- The Whisper (self-hosted) field hint mentions `whisper-1`, `whisper-large-v3`, and `Systran/faster-whisper-large-v3` as common model values.

## Version 6.11.0-beta.1

Multi-provider transcription support. The existing Groq integration is now one of four selectable backends; OpenAI and self-hosted Whisper / Faster-Whisper join the lineup. All four speak the same OpenAI-compatible `POST /audio/transcriptions` protocol, so the existing key-rotation / 429-backoff / rate-limit machinery is shared — only the URL, model, and auth requirement differ per provider.

### Server

- **`TranscriptionProvider` setting** (string enum) selects which backend to use. Values: `groq` (default — existing behaviour preserved), `openai`, `whisper-selfhosted`, `faster-whisper-selfhosted`. Missing or unknown values fall back to Groq.
- **Per-provider configuration is stored independently.** Each provider has its own `…BaseUrl`, `…ApiKey`, `…Model` fields, so switching providers via the admin UI does not lose previously-entered credentials. The existing `transcriptionBaseUrl` / `transcriptionApiKey` / `transcriptionModel` rows are repurposed as Groq's slots, so any pre-upgrade Groq config is automatically the active provider's config after upgrade.
- **`Options.ActiveTranscriptionConfig()`** resolves the (URL, model, key, provider) tuple at request time, filling in provider-specific defaults when the corresponding field is empty:
  - Groq: `https://api.groq.com/openai/v1` + `whisper-large-v3-turbo`
  - OpenAI: `https://api.openai.com/v1` + `whisper-1`
  - Whisper self-hosted: no URL default (required from user) + `whisper-1`
  - Faster-Whisper self-hosted: no URL default (required from user) + `Systran/faster-whisper-large-v3`
- **API key is optional for self-hosted providers.** When the active provider is self-hosted and the configured key is empty, the `Authorization: Bearer` header is omitted from outgoing requests and the rate-limit machinery uses a single `(anonymous)` slot. Hosted providers (Groq, OpenAI) still require a non-empty key.
- **`Enabled()` is provider-aware.** Hosted providers need a key; self-hosted providers need a Base URL (since there's no default). `TranscriptionEnabled` still gates everything globally.
- **Multi-key rotation stays Groq-only.** The existing comma/whitespace/newline-split key ring continues to apply to the Groq slot. Other providers use a single-key field for v1.

### Webapp

- Admin → Options → Call Transcription gains a provider dropdown. Switching providers shows a different per-provider block (Base URL, API Key, Model) below the dropdown; the inactive providers' fields stay in the form state (and on the server) so credentials persist across switches.
- Shared settings (Language, Prompt, Max/min) appear once below the per-provider block.

### Backwards compat

- Existing installs default to `groq` provider and keep using their existing `TranscriptionBaseUrl` / `TranscriptionApiKey` / `TranscriptionModel` values — zero behavioural change.
- Edge case: if a user had repurposed the existing Groq fields to point at OpenAI (via custom URL), after upgrade those values still live in the Groq slot. To get the cleaner OpenAI experience, switch the provider dropdown to "OpenAI" and (re-)enter credentials in the OpenAI fields.


## Version 6.10.3

Cross-instance transcript forwarding. Server-only feature; the webapp and Android client ride the version bump. Fully backwards compatible with the original [chuot/rdio-scanner](https://github.com/chuot/rdio-scanner) repo — wire format unchanged, downstreams running stock or older builds behave identically (they just don't receive forwarded transcripts, and they don't need to). Iterated through nine prerelease betas in production against a live two-server setup before this release.

### Server

- **Push completed transcripts to downstream instances.** When this server finishes transcribing a call, the transcript is automatically forwarded to any configured downstream that supports the new feature. Two new HTTP endpoints:
  - `GET /api/capabilities` — advertises `{"features":["transcript-forward"]}`. Used by upstreams to detect support; legacy servers return 404 and are silently skipped.
  - `POST /api/call-transcript` — receives `{key, system, talkgroup, dateTime, transcript}`, validates the API key with the same `Apikeys.HasAccess` check used by `/api/call-upload`, looks up the matching call by a ±500 ms `(system, talkgroup, dateTime)` window, persists the transcript via `UpdateTranscript`, and broadcasts to live WebSocket listeners via the existing `EmitTranscript` path.
- **Per-downstream capability cache.** Each `Downstream` probes its target's `/api/capabilities` and caches the result with a 1-hour TTL. Lets a server upgraded mid-session start receiving pushes within an hour without a restart. State transitions log once (Unknown→No, Unknown→Yes, Yes→No, No→Yes) so the log isn't spammed for legacy downstreams.
- **Suppress double-transcription across the forwarding hop.** When the upstream is going to transcribe, the call-upload includes a `transcriptPending=1` form field. The downstream parses it and skips its own `TranscribeCallAsync` for that call — only the upstream runs Whisper; the result is pushed via `/api/call-transcript`. The field is only set when transcription is *actually* going to be attempted (system+talkgroup opted in, key configured, audio passes the same `<= 44` byte and `TranscriptionMinAudioBytes` predicates Whisper would use), so downstreams aren't told to wait for transcripts that will never come.
- **Order-independent receive via in-memory pending-transcripts cache.** On servers where Whisper finishes before the call upload completes, the transcript push (small JSON) often beats the call upload (large multipart) on the wire. The receiver now holds early-arriving pushes in an in-memory map keyed by `(system, talkgroup, dateTime)` with a 5-minute TTL and a 1000-entry cap (lazy prune on every Store, oldest dropped if still over cap). `IngestCall` checks the cache after `WriteCall` and applies any held transcript immediately. A race-window close in `CallTranscriptHandler` re-checks the DB after Store to catch the microsecond gap where a call arrived between the first lookup and the Store.
- **Chain forwarding (A → B → C).** When a server receives a transcript from upstream (synchronously, or from the pending cache), it now also forwards it to its own downstreams. Multi-hop setups where a middle server isn't transcribing locally propagate transcripts correctly.
- **Delayer now only delays listeners, not downstreams.** `Controller.EmitCall` split into `EmitCallToDownstreams` + `EmitCallToClients`. The Delayer fires only the client path; `IngestCall` fires the downstream forward synchronously before handing the call to the Delayer. Forwarded calls reach downstreams at near-real-time regardless of local listener-delay config. Original `EmitCall` is kept as a thin wrapper for compatibility.

### Logs

Full lifecycle of a forwarded call is visible:

- `transcript push received: [Richard] system=X talkgroup=Y dateTime=Z` — handler received a request
- `transcript push auth failed: ... key=…aBcD` — bad API key (last four chars shown so you can identify the misconfigured upstream)
- `transcript deferred (holding for incoming call): [Richard] ...` — push arrived before the call; held in cache
- `transcript applied from pending: [Richard] ... id=N (X chars)` — held transcript applied when the call landed
- `transcript applied (race-window close): [Richard] ... id=N (X chars)` — race-close caught the call appearing during Store
- `transcript received: [Richard] ... id=N (X chars)` — synchronous apply (call already in DB)
- `call from upstream with pending transcript: ... id=N (awaiting /api/call-transcript push)` — call arrived tagged `transcriptPending=1`
- `local transcription skipped: ... id=N (deferred to upstream)` — local Whisper bypassed for this call
- `downstream URL does not support transcript-forward` / `... supports transcript-forward` — fires once per state transition

### Backwards compatibility

- `/api/call-upload` and every existing endpoint are untouched
- Original repo downstreams: capability probe returns 404 → marked unsupported → silently skipped, no log spam after the first transition
- Original repo upstreams: never set `transcriptPending=1` → receivers transcribe locally as before
- All-original-repo deployments: zero behavioural change

## Version 6.10.2

Android-focused patch on top of 6.10.1, resolving the persistent "WS drops in the background" issue on Samsung Android 16. Server and webapp have no functional changes; they ride the version bump so the whole stack stays at a single number.

### Android

- **Battery-optimization exemption prompt + permission-gated background mode.** Mirrors the POST_NOTIFICATIONS pattern: at first launch the app fires `Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS`, which pops a real OS dialog asking the user to allow the app to ignore battery optimization. The ask happens exactly once per install (tracked in DataStore); the user re-grants or revokes from Settings > Apps > Rdio Scanner > Battery from then on. All background-mode machinery — the foreground service promotion, the WifiLock / partial WakeLock pair, and the TCP-keepalive sockets — is gated on `PowerManager.isIgnoringBatteryOptimizations()` at runtime, so when the user denies (or revokes) the exemption the app simply runs as a foreground-only app instead of fighting the OEM power-restriction layer. No in-app UI changes; the system permission is the only knob.
- **Foreground-service promotion moved to a Composable `LaunchedEffect`.** Earlier `combine(state, isPlaying)` observer fired from `Dispatchers.Main.immediate` and threw `ForegroundServiceStartNotAllowedException` on every WS-state change after the activity backgrounded (Android 12+ refuses `startForeground` from a background coroutine). New design dispatches `ACTION_ENTER_FG` / `ACTION_EXIT_FG` via `ContextCompat.startForegroundService` from inside `RdioApp()`'s `LaunchedEffect`, which only runs while the composable is composing — a state the activity guarantees is TOP. The `AudioService.onStartCommand` handler does the actual `startForeground` call, well inside the 5-second post-`startForegroundService` exemption window.
- **Kernel-level TCP keepalive on every WS socket.** New `KeepAliveSocketFactory` plugged into OkHttp sets `SO_KEEPALIVE=true` plus `TCP_KEEPIDLE=30s`, `TCP_KEEPINTVL=10s`, `TCP_KEEPCNT=3` via reflection on `java.net.Socket` (not subject to the Android hidden-API restriction). The Linux network stack sends keepalive probes itself, on its own clock, so the path stays warm even when OkHttp's `pingInterval` scheduler is deferred under light-Doze. App-level `pingInterval` shortened from 30s → 15s as a second layer; both well under Cloudflare's 100s idle ceiling.
- **WifiLock (high-perf) + partial WakeLock.** Acquired in `RdioClient.connect`, released in `disconnect`, owned by `NetworkMonitor`. WiFi lock prevents the radio from power-saving between DTIM intervals; partial wake lock keeps OkHttp's ping scheduler firing on time during light-Doze maintenance windows. Both acquisitions no-op when the battery-opt exemption isn't granted, so battery isn't drained chasing a foreground state the OEM will defeat anyway.
- **Verbose diagnostic logging** at every step of the FG promotion path (`RdioNav`, `AudioService`, `NetworkMonitor`, `KeepAliveSocketFactory`) so future "still broken" reports can be triaged from a single logcat capture.
- **Version label on the Connect screen** (`v6.10.2 · debug` when applicable) sourced from `BuildConfig.VERSION_NAME`, so QA can confirm at a glance which build is loaded.

## Version 6.10.1

Android-focused patch on top of 6.10.0. Three independent fixes covering text rendering on the LCD/history/search surfaces and the post-background reconnect path. Server and webapp have no functional changes; they ride the version bump so the stack stays at a single number.

### Android

- **Stop truncating talkgroup names and transcripts.** Five sites were silently ellipsizing strings — the LCD's big talkgroup-name row (was `maxLines = 1` + fixed `height(32.dp)`), the live transcript panel (`maxLines = 4`), the history table's Name column (single-line via `LcdText` default), the per-row transcript snippet under each history row (`maxLines = 2`), and the search-screen transcript line (`maxLines = 4`). `LcdText` and `HistoryCell` now take a `maxLines` parameter (defaults preserved at 1 for tidy column rows); the talkgroup-name row uses `heightIn(min = 32.dp)` so it grows downward and the rows below reflow cleanly.
- **Fix DNS race on resume-from-background.** Symptom: "UNABLE TO RESOLVE HOST" on the Connect screen after returning to the app, requiring a manual tap to reconnect. Root cause: `ON_RESUME` fires `connectWithSavedCredentials` immediately, but on many devices the Wi-Fi/cell radios are still being restored — OkHttp's DNS lookup races the resolver and throws `UnknownHostException` before the new nameservers are picked up. The exponential backoff (1s → 30s plateau) then drifts to 30s waits while the user gives up. New `NetworkMonitor` wraps `ConnectivityManager` and exposes `awaitNetwork(timeoutMs)`; `RdioClient.openSocket()` consults it and defers the WS open until the system reports an internet-capable network. `Listener.onFailure` also detects `UnknownHostException` specifically and resets `reconnectAttempts = 0` so retries stay fast even if DNS still hiccups.
- **Pin AudioService foreground while connected to keep the WS alive in the background.** Root cause for "why did the socket drop at all": MediaSessionService only foregrounds while ExoPlayer is actively playing. The silent gaps between calls let Android demote the service, after a short device-idle period Doze kicks in, defers our 30s WebSocket ping for minutes at a time, and the reverse proxy in front of the server (Cloudflare's 100s idle ceiling is the common case) closes the "idle" socket. `AudioService` now drives its foreground state off a `combine(state, isPlaying)` flow — `startForeground` with a low-priority "Listening to live feed" notification whenever the WS is connected and ExoPlayer isn't playing, hand-off to Media3's playback notification during a call, then re-assert ours when playback ends. `stopForeground(REMOVE)` only on actual disconnect or swipe-from-recents. With the service pinned foreground, Doze restrictions don't apply, the ping fires on schedule, and the WS survives long background periods.

## Version 6.10.0

Two-feature release: server-side call delay and per-talkgroup/system alert tones, wired through the admin UI, webapp playback, and Android playback. Field shapes match upstream chuot/rdio-scanner v7-wip (`Talkgroup.Alert`, `System.Alert`, `Talkgroup.Delay`, `System.Delay`) so the on-the-wire schema stays compatible with the upstream feature — except the `delay` value is **seconds** here, vs **minutes** upstream, chosen for finer control.

### Server

- **Delayer:** per-talkgroup and per-system `delay` (seconds) holds calls server-side before forwarding to listeners and downstreams. Talkgroup delay overrides system delay; either being 0 (the default) falls through to immediate emit, so existing deployments behave identically until an admin sets a value. Pending delays persist across restarts via a new `rdioScannerDelayed` table — `Delayer.Start()` on boot replays queued calls, emitting immediately for any whose timestamp has already passed and re-arming the rest. `IngestCall` routes through `Delayer.Delay(call)` instead of calling `EmitCall` directly. Schema migrations add the `delay` column to `rdioScannerTalkgroups` and `rdioScannerSystems` (default 0) on all three supported drivers and create the `rdioScannerDelayed` table.
- **Alert tones:** per-talkgroup and per-system `alert` (string) selects one of 9 built-in oscillator presets (`alert1`..`alert9`) to play as an announcement tone before each call. Talkgroup alert wins; falls back to system alert; empty on both = no tone. Server stores the preset name on both the talkgroup and system rows, exposes the full preset library (`Alerts` from `alert.go`) in the CFG payload, and surfaces both choices in the scoped systems/talkgroups maps. Two migrations add the `alert` column to `rdioScannerTalkgroups` and `rdioScannerSystems`. `oscillator.go` defines the `OscillatorData` struct shared with the webapp's existing `keypadBeeps`.
- **Admin config GET:** `GetConfig()` now emits `system.delay` and `system.alert` in each system map. Without these the admin form would post a value, the server would write it, and the next reload would zero out the form because the GET response didn't surface the field — talkgroups weren't affected because they round-trip via the `Talkgroup` struct's JSON tags.

### Webapp

- **Admin UI:** new "Delay (seconds)" input and "Alert Tone" dropdown (None / alert1..alert9) on both the talkgroup and system config forms. Captions explain the talkgroup-then-system precedence and that 0/None disables the feature.
- **Playback:** when a call arrives, the resolved alert preset's oscillator sequence is scheduled on the audio context at `currentTime` and the call audio source's `start()` is offset by the preset's duration — alert plays first, audio follows, no overlap. The CFG message handler now copies the `alerts` library off the wire payload so the playback code can look it up by name.

### Android

- New `AlertPlayer` synthesizes square / sine / triangle / sawtooth waveforms from `OscillatorBeep[]` into 16-bit mono 44.1 kHz PCM and plays via `AudioTrack`, suspending until drain.
- `CallPlayer` carries the resolved preset on each `QueuedCall`. The transition listener pauses ExoPlayer, plays the alert on `Dispatchers.IO`, then resumes the call audio — with a per-mediaId guard so seeks and pause/resume don't re-fire the tone.
- `AudioService.resolveAlertBeeps` looks up `talkgroup.alert` → `system.alert` → `config.alerts[name]` before each `enqueue`/`playNow`, matching the webapp's precedence.
- `TalkgroupDto`, `SystemDto`, `ConfigDto` carry the alert name and preset library across the wire (auto-populated by kotlinx.serialization).

## Version 6.9.2

Android-only patch release covering two background-resume bugs reported after 6.9.1 shipped. Server and webapp are unchanged but get the version bump so the whole stack stays at a single number.

### Android

- **Reconnect on resume:** users would return to the app after a long background and find themselves stuck on the Connect screen with no automatic recovery. Two real-world failure modes both ended there — Doze froze `RdioClient.scheduleReconnect`'s `delay(30s)` backoff so the retry timer never fired, and an activity destroy during a mid-reconnect cycle pinned the visible state at the last `Error`. Now `MainActivity` hooks `Lifecycle.Event.ON_RESUME` and re-fires `connectWithSavedCredentials()` when state has dropped to `Disconnected`/`Error` but a session was established earlier in this process. Cold starts (no prior session) keep landing on the profile picker — intentional UX for multi-profile setups.
- **System media notification:** lock-screen, notification-panel, Quick Settings tile, and Bluetooth-display surfaces were all blank even while audio was playing. `MediaSessionService` only calls `startForeground()` and posts its notification once a `MediaController` connects to the session; the UI was driving `CallPlayer.player` directly, so nothing ever bound and the service stayed a plain started service. `MainActivity` now builds a `MediaController` against `AudioService` in `onCreate` and releases it in `onDestroy` — UI commands keep going through `CallPlayer`, the controller exists only to wake the notification flow.
- **Audio focus:** wired `AudioAttributes(USAGE_MEDIA, CONTENT_TYPE_SPEECH, handleAudioFocus = true)` on the `ExoPlayer`. The previous setup did neither ducking for nav prompts nor pausing for phone calls, and some OEM media surfaces wouldn't recognise the player as media playback worth surfacing.
- **Diagnostic logging:** added warning-level logs at the four playback handoff points (`RdioClient.handle(Incoming.Call)` stale-generation drops and tryEmit failures, `AudioService.pipeJob` state-mismatch drops, `CallPlayer.enqueue/playNow` empty-audio drops, `Player.Listener.onPlayerError`) so future "calls aren't playing" reports can be triaged from a logcat without a code change. Drop logs at debug level for expected filter paths (hold / avoid / livefeed disabled).

## Version 6.9.1

Android transcripts integration plus a 20-item audit pass across server, webapp, and Android. Android `versionName` is now synced with the webapp/server (`6.9.1`) so the whole stack tracks a single number.

### Server

- **Crash fix:** `Clients.Map` was iterated without holding the mutex that `Add`/`Remove` took — guaranteed Go-runtime panic on concurrent map iteration vs write. Promoted to `sync.RWMutex` and added `RLock` around every iteration in `client.go` (AccessCount, Count, EmitCall, EmitConfig, EmitTranscript, EmitListenersCount).
- **Crash fix:** `db.Query()` error paths in call/log search were masked by `err != sql.ErrNoRows`, which `Query` never returns. Real errors fell through with `rows == nil` and the next `rows.Next()` segfaulted. Bail on any non-nil error; `rows.Close()` is now `defer`red right after a successful Query.
- **Data integrity:** `isDuplicateCall` formatted timestamps via `%v`, producing Go's default `2026-05-18 12:34:56 +0000 UTC` form that no DB accepts — every call was treated as non-duplicate. Now uses `db.DateTimeFormat`.
- **Data integrity:** the ingest path in `parsers.go` wrote `call.Source` as `int` while every downstream consumer type-switches on `uint`, so the source field was silently dropped from forwarded payloads. Normalised to `uint`.
- **Data integrity:** the `Sort` type switch in `call.go` had `case int:`, but `fromMap` stores Sort as `float64` (encoding/json's default for JSON numbers), so the int branch was dead and every search defaulted to ascending. Mirror of the Android `encodeDefaults` bug; switched to `case float64:`.
- **Data integrity:** LCD patch-by-talkgroup LIKE clause missed single-element JSON array literals (`[42]`), which is the most common patch shape for trunked systems. Added the `[%v]` branch.
- **Data integrity:** `log.go` date-range arithmetic used `time.Duration(v.Hour())` — treating hours as nanoseconds — so the previous-day range for descending log searches was off by up to 23 hours. Fixed to multiply by `time.Hour`.
- **Data integrity:** `log.go` DateStop query was a copy-paste of DateStart (`asc` on both); switched the second to `desc`.
- **Hardening:** RFC3339 wire format on `call.go`, `public_api.go`, and `downstream.go` now goes through `.UTC()` before `.Format` so the server's local offset can't leak when the SQL driver hands back non-UTC times.
- **Hardening:** `metaCache` had no bound and `InvalidateSearchMeta` was never called — a search hitting random `q` values grew the map without limit. Capped to 256 entries with clear-on-overflow and invalidated on `WriteCall` for both Postgres and non-Postgres branches.
- **Hardening:** Whisper transcription spawned one goroutine per call holding the full audio blob for the round-trip. Bursts could pin hundreds of audio blobs in memory. Now fronted by a buffered semaphore (cap 8).
- LCD: PATCH flag lights on every patched call, not just patched-and-avoided ones.

### Webapp

- **Stats correctness:** Calls/Hour chart was wrong on half-hour-offset timezones (IST, Newfoundland, Nepal, parts of AU). The lookup derived a UTC-hour key via `setUTCMinutes(0,0,0)`, which only zeroes minutes — for a `:30`-offset zone the result fell in the previous UTC hour and the lookup missed every server bucket. Replaced with a window-sum across the local hour's `[slot, slot+1h)` range.
- **Subscriber race:** the NO-LINK fix patched only `linked` state, but the same hazard applied to `config` / `categories` / `livefeedMap` — the early WS can fire those events before any component subscribes (`EventEmitter` has no replay). Late-mounted search/select/main panels would start with empty filter lists. Added `getConfig()` / `getCategories()` / `getLivefeedMap()` snapshot accessors; consumers seed in `ngOnInit`.
- **Resource leaks:** `main.component` and `search.component` had `ngOnDestroy` paths that only unsubscribed the event stream; their per-call `dimmerTimer`/`replayTimer` and `qDebounce`/`highlightClearTimer` `setTimeout` handles leaked across destroys. Cleared in `ngOnDestroy`.
- **Resource leaks:** when the user avoided a talkgroup whose call was being held for transcript, the per-call `setInterval` + `setTimeout` kept polling for up to 30s and would eventually push the call into a queue that no longer wanted it. `cleanQueue` now walks `pendingTranscriptCalls` and clears timers for entries whose talkgroup is no longer active; `enqueuePending` re-checks active state.

### Android

- New native client (Kotlin/Compose) ships full Whisper transcripts integration — inline on the LCD, in the history table, and in search results, with auto-fetch when a call is selected and per-row snippets under each history entry. New `TRX` WebSocket command for on-demand transcript fetches.
- 12-hour AM/PM time formatting across the LCD, call history, and search.
- RFC3339 call timestamps now parse via `java.time` (minSdk 26) so times render in the correct timezone regardless of precision or offset format.
- LCD history is now a bounded scrollable box (up to 100 rows) with a custom scrollbar overlay when content overflows, anchored to the newest call when the user is at the top.
- LCD history column widths rebalanced so the AM/PM time and long talkgroup names no longer get clipped.
- Search: results auto-refresh (debounced) as new calls land on the live feed.
- Search: the Newest/Oldest sort toggle now actually reorders results — the `sort` field was silently stripped from the WebSocket payload by `encodeDefaults = false`, so the server always returned ascending.
- Search: removed the redundant Group filter and gated the Tag filter on the server's `tagsToggle` option.
- Search: tapping play on a different row stops the current call and switches immediately instead of queueing; the active row shows Stop-icon + accent-border styling.
- **Profile switch fix:** also clears the cached `searchResults`, the held transcript map, and resets `livefeedEnabled` to true — previously a user who toggled LIVE FEED off on profile A and then switched profiles would silently drop every call on profile B until they tapped the button again.
- **Profile switch fix:** talkgroup selection defaults back to all-on for the new server (refinement of the v6.8.0 fix).
- **State race:** `CallPlayer.stopAndClear` wrote `_playing`/`_isPlaying` directly while the ExoPlayer Listener wrote the same flows asynchronously. A rapid stop-then-`playNow` could have the late listener callback flip state back to "not playing". Removed the direct writes — listener is the single writer.
- **State race:** the WebSocket message handler emitted CAL/TRX onto SharedFlows via `scope.launch`, where `scope` is the client-level scope rather than the listener's. A CAL decoded on the current listener could land in the new repo state after a profile switch, leaking a profile-A call into profile-B. The listener's generation is now re-checked inside the launched block before emitting.

## Version 6.8.1

- LCD: fixed "NO LINK" sticking after a cold reload when the websocket opened before the Angular component subscribed.
- README rewritten for the AkumasCoffin fork with setup instructions and a per-area feature overview.

## Version 6.8.0

- Stats: server now emits hourly UTC buckets and the browser bins every chart in the viewer's local IANA timezone, so Calls/Hour and Last-Hour Activity stop drifting between client and server.
- Stats: `LastCall` emitted as RFC3339 so the browser parses it as UTC.
- Android (`android-v1.0.2`): fixed profile switching kicking the app to the home screen and carrying stale system/talkgroup state across servers.

## Version 6.7.2

- Stats: aggregate time buckets in Go rather than via per-DB-vendor SQL date functions, so MySQL/SQLite/Postgres behave identically.

## Version 6.7.1

- Whisper-based call transcription with per-system enable; transcripts are cached server-side and exposed on every CAL frame.
- Postgres `pg_trgm` and BRIN indexes on the calls table for fast transcript keyword search.
- New public REST API for read-only call queries (system/talkgroup/date filters, pagination).
- Stats: per-talkgroup and per-unit leaderboards now count units pulled from per-call `sources` JSON, picking up entries that never appear as the call's primary `source`.
- New native Android client (Kotlin/Compose) with livefeed playback, talkgroup selector, presets, multi-profile connection management, deeplink-shareable call URLs, and Media3-backed background audio (`android-v1.0.0`).
- Android: APK outputs named `rdio-<version>[-<type>].apk`; release builds ship hardened R8 rules so CAL frames decode after minification.
- Android: edge-to-edge layout respects the system bars (`android-v1.0.1`).

## Version 6.6.6

- Fixed admin config save race condition that could freeze the webapp and prevent settings from displaying.
- Fixed server log showing system hostname instead of actual listen address.
- Fixed handling of empty/null Umami analytics fields when saving from admin panel.
- Binaries now include the fully built Angular webapp.

## Version 6.6.5

- New Umami analytics integration, configurable from the admin options page.
- Dynamic script injection for Umami tracking, loaded/removed when settings change.
- Event tracking for livefeed start/stop, call playback, call search, and call download.

## Version 6.6.4

- API call upload log messages now display the API key's ident name for easier source identification.
- Added PostgreSQL database support.
