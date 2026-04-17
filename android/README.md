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
./gradlew assembleRelease          # minified release APK (see Signing below)
./gradlew :app:lintDebug           # Android Lint
./gradlew :app:compileDebugKotlin  # fast type-check loop
```

## Signing a release build

1. Generate a keystore (keep the file and passwords safe — losing them
   means you can't ship updates to anyone who installed an earlier build):

   ```bash
   keytool -genkeypair -v \
       -keystore release.keystore \
       -keyalg RSA -keysize 4096 -validity 10000 \
       -alias rdio
   ```

2. Copy `signing.properties.example` to `signing.properties` in the
   `android/` directory and point it at the keystore:

   ```properties
   storeFile=release.keystore
   storePassword=…
   keyAlias=rdio
   keyPassword=…
   ```

   `signing.properties` is gitignored. For CI, set `RDIO_STOREFILE`,
   `RDIO_STOREPASSWORD`, `RDIO_KEYALIAS`, `RDIO_KEYPASSWORD` env vars
   instead.

3. `./gradlew assembleRelease` — APK at `app/build/outputs/apk/release/`.

If no credentials are provided the release build still succeeds but is
signed with the debug key (a warning is logged). That APK is fine for
sideloading but rejected by the Play Store.

## Replacing the launcher icon

The adaptive icon lives entirely in vector XML:

- `app/src/main/res/drawable/ic_launcher_foreground.xml` — full-color foreground
- `app/src/main/res/drawable/ic_launcher_monochrome.xml` — Android 13+ themed-icon silhouette
- `app/src/main/res/values/colors.xml` → `ic_launcher_background` — adaptive-icon background color

Keep the primary content inside the center 66dp of the 108dp viewport so
it survives circle / squircle / squared masking on different OEM launchers.
Replace either drawable with your own vector and the launcher / notification
/ Auto surfaces pick it up automatically.

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
│   │           ├── ConnectScreen.kt        # profile list + editor
│   │           ├── LivefeedScreen.kt       # scanner LCD + control grid
│   │           ├── SearchScreen.kt         # LCL history + download
│   │           └── SelectorScreen.kt       # talkgroup picker + presets
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

## Known limitations

- No automated tests yet.
- Cleartext traffic is permitted by `network_security_config.xml` so
  self-hosted `http://` / `ws://` LAN servers still work. Tighten to
  `cleartextTrafficPermitted="false"` if you only connect over HTTPS.
- The WebSocket lives in the Application process — if the OS kills the
  process while backgrounded, the live feed stops until the user reopens
  the app. A proper always-on scanner would move the connection into the
  foreground `AudioService`.
