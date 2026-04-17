# Rdio Scanner — Android client

Native Android app that connects to an Rdio Scanner server over WebSocket
and plays live calls.

- 100% Kotlin + Jetpack Compose (Material 3)
- Media3 ExoPlayer + `MediaSessionService` for background-capable playback
- OkHttp WebSocket with auto-reconnect and PIN auth
- DataStore-backed settings (server URL, access code, talkgroup selection)
- Min SDK 26, target / compile SDK 35
- Gradle Kotlin DSL + version catalog

## One-time setup

### 1. JDK 17 or newer

Confirm you have a modern JDK on `PATH`:

```bash
java -version
```

AGP 8.7 needs JDK 17+. JDK 21 (Temurin) is fine.

### 2. Android SDK

The Gradle build needs `ANDROID_HOME` (or `ANDROID_SDK_ROOT`) pointing at
an SDK with `platform-tools`, `platforms;android-35`, and
`build-tools;35.0.0` installed.

Fastest path on Windows with Scoop:

```bash
scoop bucket add extras
scoop install android-clt
# sdkmanager is now on PATH
sdkmanager "platform-tools" "platforms;android-35" "build-tools;35.0.0"
```

Then set `ANDROID_HOME`:

```bash
# one-liner for your current shell; add to profile for persistence
export ANDROID_HOME="$HOME/scoop/apps/android-clt/current"
```

### 3. Fetch the Gradle wrapper jar

The wrapper scripts are committed, but the binary jar is not. Run:

```bash
cd android
./bootstrap.sh
```

That downloads `gradle/wrapper/gradle-wrapper.jar` for the pinned Gradle
version (8.10.2). You only need to run it once.

## Build

From `android/`:

```bash
./gradlew assembleDebug            # debug APK at app/build/outputs/apk/debug/
./gradlew installDebug             # install on a connected device
./gradlew :app:lintDebug           # Android Lint
./gradlew :app:compileDebugKotlin  # fast type-check loop
```

Release build is minified + R8-shrunk but signs with the debug key for
now — wire a proper signing config before publishing.

## Run

1. Build and install a debug APK (`./gradlew installDebug`) or sideload
   the APK from `app/build/outputs/apk/debug/`.
2. On first launch, enter the server URL. Accepted forms:
   - `https://server.example.com` → upgrades to `wss://…`
   - `http://1.2.3.4:3000` → upgrades to `ws://…`
   - `ws://…` / `wss://…` passed through as-is
3. If the server is restricted, enter the access code. Stored in DataStore.
4. The Live Feed screen plays incoming calls. Tap the ⚙ icon to pick which
   systems/talkgroups to subscribe to.

## Project layout

```
android/
├── app/src/main/
│   ├── AndroidManifest.xml
│   ├── kotlin/solutions/saubeo/rdioscanner/
│   │   ├── RdioApplication.kt           # manual DI container
│   │   ├── MainActivity.kt
│   │   ├── audio/
│   │   │   ├── AudioService.kt          # MediaSessionService
│   │   │   └── CallPlayer.kt            # ExoPlayer wrapper + queue
│   │   ├── data/
│   │   │   ├── client/RdioClient.kt     # WebSocket + reconnect
│   │   │   ├── prefs/SettingsStore.kt   # DataStore
│   │   │   ├── protocol/
│   │   │   │   ├── Messages.kt          # Wire codec (CMD + payload + flag)
│   │   │   │   └── Models.kt            # CallDto, ConfigDto, SystemDto…
│   │   │   └── repository/RdioRepository.kt
│   │   └── ui/
│   │       ├── Navigation.kt            # NavHost
│   │       ├── ScannerViewModel.kt
│   │       ├── theme/Theme.kt
│   │       └── screens/
│   │           ├── ConnectScreen.kt
│   │           ├── LivefeedScreen.kt
│   │           └── SelectorScreen.kt
│   └── res/                             # strings, themes, launcher icons
├── build.gradle.kts                     # root build
├── settings.gradle.kts
├── gradle.properties
├── gradle/libs.versions.toml            # pinned dependency catalog
├── gradlew / gradlew.bat
└── bootstrap.sh                         # fetches gradle-wrapper.jar
```

## Protocol notes

The app speaks the same JSON-over-WebSocket protocol as the web client
(`../client/src/app/components/rdio-scanner/rdio-scanner.service.ts`).
Frames are `[command, payload?, flag?]` arrays. See
`data/protocol/Messages.kt` for supported commands.

Audio arrives embedded in `CAL` messages as `{type: "Buffer", data: [bytes…]}`
(typically MP4/AAC). `BufferAsByteArraySerializer` in `Models.kt` flattens
that to a Kotlin `ByteArray`, which `CallPlayer` writes to the audio cache
and hands to ExoPlayer as a `MediaItem`.

## Known v1 limitations

- History/search UI (`LCL`) is wired at the protocol level but has no screen yet.
- No signing config — release APK is signed with the debug key.
- No tests yet.
- `usesCleartextTraffic="true"` is enabled to support `http://` / `ws://`
  servers on local networks. Tighten with a network security config before
  shipping to Play.
- WebSocket lives in the Application process — if the OS kills the process
  while backgrounded, the live feed stops until the user reopens the app.
  A proper background-persistent scanner would move the connection into
  the foreground `AudioService`.
