# Change log

## Unreleased

- LCD: PATCH flag lights on every patched call, not just patched-and-avoided ones.
- Android: Whisper transcripts render inline on the LCD, in the history table, and in search results, with auto-fetch when a call is selected and per-row snippets under each history entry.
- Android: 12-hour AM/PM time formatting across the LCD, call history, and search.
- Android: profile switching also clears history, cached transcripts, and avoids; talkgroup selection defaults back to all-on for the new server (refinement of the v6.8.0 fix).
- Android: RFC3339 call timestamps now parse via `java.time` so times render in the correct timezone regardless of precision or offset format.
- Android search: results auto-refresh (debounced) as new calls land on the live feed.
- Android search: the Newest/Oldest sort toggle now actually reorders results — the field was silently stripped from the WebSocket payload by `encodeDefaults = false`, so the server always returned ascending.
- Android search: removed the redundant Group filter and gated the Tag filter on the server's `tagsToggle` option.
- Android search: tapping play on a different row stops the current call and switches immediately instead of queueing; the active row shows Stop-icon + accent-border styling.
- Android LCD history: bounded scrollable box (up to 100 rows) with a custom scrollbar overlay when content overflows, anchored to the newest call when the user is at the top.
- Android LCD history: column widths rebalanced so the AM/PM time and long talkgroup names no longer get clipped.
- Android `versionName` is now synced with the webapp/server (`6.9.0-beta`) so the whole stack tracks a single number.

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
