# Change log

## Unreleased

_(nothing yet — bullets land here as work is merged to master)_

## Released

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
