# Change log

## Unreleased

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
