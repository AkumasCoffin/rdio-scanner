![Rdio Scanner](./docs/images/rdio-scanner.png?raw=true)

# Rdio Scanner — AkumasCoffin fork

Fork of [chuot/rdio-scanner](https://github.com/chuot/rdio-scanner) with
PostgreSQL as a first-class database, Whisper transcription, a public REST API,
a stats dashboard, a redesigned search page with shareable deep links, and a
native Android client. All upstream functionality is preserved.

Original project, credit, and documentation:
**[github.com/chuot/rdio-scanner](https://github.com/chuot/rdio-scanner)**.

## Recorder compatibility

Works with any recorder that writes one audio file per call:

| Recorder                                                       | API | Dirwatch |
| -------------------------------------------------------------- | --- | -------- |
| [Trunk Recorder](https://github.com/robotastic/trunk-recorder) | X   | X        |
| [RTLSDR-Airband](https://github.com/szpajder/RTLSDR-Airband)   |     | X        |
| [SDRTrunk](https://github.com/DSheirer/sdrtrunk)               |     | X        |
| [voxcall](https://github.com/aaknitt/voxcall)                  | X   |          |
| [ProScan](https://www.proscan.org/)                            |     | X        |
| [DSDPlus Fast Lane](https://www.dsdplus.com/)                  |     | X        |
| DSD FME (with custom metadata mask)                            |     | X        |

---

# Install from release (recommended)

Grab the binary for your platform from the
[latest release](https://github.com/AkumasCoffin/rdio-scanner/releases/latest):

| OS      | Arch  | File                                       |
| ------- | ----- | ------------------------------------------ |
| Linux   | amd64 | `rdio-scanner-linux-amd64-vX.Y.Z`          |
| Windows | amd64 | `rdio-scanner-windows-amd64-vX.Y.Z.exe`    |

The binary embeds the web app — no separate front-end deploy.

```bash
# Linux: drop in place, persist config, run.
sudo install -d /opt/rdio-scanner
sudo mv rdio-scanner-linux-amd64-vX.Y.Z /opt/rdio-scanner/rdio-scanner
sudo chmod +x /opt/rdio-scanner/rdio-scanner

# Postgres (see next section for DB setup):
/opt/rdio-scanner/rdio-scanner \
    --db_type postgres \
    --db_host localhost --db_port 5432 \
    --db_user rdio --db_pass 'CHANGEME' --db_name rdio_scanner \
    --listen :3000 --config_save

# Or SQLite (zero-config, fine for small setups):
/opt/rdio-scanner/rdio-scanner --listen :3000 --config_save
```

`--config_save` writes the flags to `rdio-scanner.ini` next to the binary;
subsequent runs need no arguments.

Open `http://<host>:3000`. Admin UI is at `/admin`, first-time login is
**`rdio-scanner`** — change it on first save.

## Run as a systemd service (Linux)

`/etc/systemd/system/rdio-scanner.service`:

```ini
[Unit]
Description=Rdio Scanner
After=network.target postgresql.service

[Service]
Type=simple
User=rdio
WorkingDirectory=/opt/rdio-scanner
ExecStart=/opt/rdio-scanner/rdio-scanner
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

```bash
sudo useradd -r -s /bin/false rdio
# Own the whole folder (not just the binary) with the service account — the
# in-app auto-updater renames files *inside* this folder, which needs write
# access to the directory itself, not only the binary.
sudo chown -R rdio:rdio /opt/rdio-scanner
sudo systemctl daemon-reload
sudo systemctl enable --now rdio-scanner
journalctl -u rdio-scanner -f
```

> **Auto-updates & permissions.** The in-app updater (Admin → Tools → Updates)
> downloads the new binary, swaps it into place and restarts. For that to work
> the account running rdio-scanner must own / be able to **write to the binary's
> folder** (e.g. `/opt/rdio-scanner`) — the `chown -R rdio:rdio` above provides
> this. Each update backs the previous binary up as `rdio-scanner.old` in the
> same folder, and a download that hasn't been applied yet is staged as
> `rdio-scanner.pending`. Read-only or package-managed installs can't
> self-update — the updater reports the error rather than half-applying, and you
> update the binary the same way you installed it.

---

# Build from source

Prerequisites: **Go 1.21+**, **Node.js 18+** with **npm**, **Git** (and
**Android Studio + JDK 17** if you want to build the Android app).

```bash
git clone https://github.com/AkumasCoffin/rdio-scanner.git
cd rdio-scanner

# 1) Angular web app → ../server/webapp/
cd client && npm ci && npx ng build --configuration=production && cd ..

# 2) Go server, which embeds the webapp at compile time
cd server && go build -o rdio-scanner .

# Cross-compile examples
GOOS=linux   GOARCH=amd64 go build -o rdio-scanner-linux-amd64 .
GOOS=windows GOARCH=amd64 go build -o rdio-scanner-windows-amd64.exe .
GOOS=darwin  GOARCH=arm64 go build -o rdio-scanner-macos-arm64 .
GOOS=linux   GOARCH=arm64 go build -o rdio-scanner-linux-arm64 .

# Android release APK (optional)
cd ../android && ./gradlew assembleRelease
# Output: android/app/build/outputs/apk/release/rdio-X.Y.Z.apk
```

---

# PostgreSQL setup

SQLite is the zero-config default. Postgres is recommended for any non-trivial
deployment — it survives concurrent ingest + transcription + search, scales to
millions of calls, and unlocks the trigram + BRIN indexes used by transcript
search and stats.

```bash
# Install
sudo apt update && sudo apt install postgresql
sudo systemctl enable --now postgresql

# Create user + database + trigram extension
sudo -u postgres psql <<'SQL'
CREATE USER rdio WITH PASSWORD 'CHANGEME';
CREATE DATABASE rdio_scanner OWNER rdio;
\c rdio_scanner
CREATE EXTENSION IF NOT EXISTS pg_trgm;
GRANT CREATE ON DATABASE rdio_scanner TO rdio;
SQL
```

For remote rdio-scanner ↔ Postgres, edit `pg_hba.conf` to allow the client
host (e.g. `host rdio_scanner rdio 192.168.1.0/24 scram-sha-256`) and set
`listen_addresses = '*'` in `postgresql.conf`, then restart.

Point rdio-scanner at it with `--db_type postgres --db_host … --db_user … --db_pass … --db_name … --config_save`. Schema migrations run automatically on
first start (BRIN index, pg_trgm GIN index, composite search index, etc).

---

# Configuration reference

| Flag                | Default            | Notes                                       |
| ------------------- | ------------------ | ------------------------------------------- |
| `--listen`          | `:3000`            | HTTP listen address                         |
| `--config`          | `rdio-scanner.ini` | Config file path                            |
| `--config_save`     |                    | Write current flags to config file and run  |
| `--db_type`         | `sqlite`           | `sqlite`, `mariadb`, `mysql`, or `postgres` |
| `--db_file`         | `rdio-scanner.db`  | SQLite file path                            |
| `--db_host`         | `127.0.0.1`        | DB host                                     |
| `--db_port`         | `5432` / `3306`    | DB port                                     |
| `--db_user`         |                    | DB user                                     |
| `--db_pass`         |                    | DB password                                 |
| `--db_name`         |                    | DB name                                     |
| `--admin_password`  |                    | Set admin password and exit                 |
| `--version`         |                    | Print version and exit                      |

Everything else (systems, talkgroups, API keys, downstreams, transcription
settings, Umami analytics) is set via the admin UI at `/admin`.

---

# What this fork adds over upstream

- **PostgreSQL backend** as a first-class option alongside SQLite / MySQL /
  MariaDB, with a pg_trgm GIN index on `transcript`, a BRIN index on `dateTime`,
  and a composite `(system, talkgroup, dateTime)` index for fast filtered search.
- **Call transcription** via Groq Whisper — multi-key round-robin, per-key rate
  limiting, automatic retry on the next key for 429/5xx, per-system and
  per-talkgroup toggles, per-system custom prompts, optional
  `waitForTranscript` mode that holds calls until their text arrives.
- **Public REST API** at `/api/v1/calls{,/:id,/transcript,/audio}` with API-key
  auth (`Authorization: Bearer`, `X-API-Key`, or `?key=`) for downstream tooling.
- **Stats dashboard** — overview cards, hourly / daily charts, top systems /
  talkgroups / units, last-hour activity with per-unit drill-down. UTC on the
  wire so the browser renders every bucket in the viewer's local timezone.
- **Search redesign** — card-row layout, sticky filter grid, full transcripts
  inline, debounced transcript search, share-link deep linking (`?call=<id>`
  opens search, anchors the date, highlights the row, autoplays on first
  gesture).
- **LCD redesign** — live transcript box with LED-color-coded styling, 15-row
  history with inline transcripts, persistent call info between calls.
- **Native Android client** in `android/` — Kotlin + Compose, multi-connection
  profiles, full LCD parity, Media3 background playback, OkHttp WS with
  exponential-backoff reconnect.
- **Performance** — HTTP gzip middleware, WebSocket `permessage-deflate`,
  immutable `Cache-Control` on hashed assets, early-WS opener so the socket
  handshakes before Angular bootstraps, cached CFG and stats with startup warm.
- **Reliability** — audio decode generation counter (no stale-decode replay),
  config save wrapped in a single Postgres transaction (no 524 timeouts),
  instant listener count, buffered WS sends, share-link UI advances without
  user gesture.
- **Optional Umami analytics** with admin-configurable URL + website ID.

Per-version detail in the
[release notes](https://github.com/AkumasCoffin/rdio-scanner/releases).

---

# Help and support

This fork: **[issues](https://github.com/AkumasCoffin/rdio-scanner/issues)** ·
**[discussions](https://github.com/AkumasCoffin/rdio-scanner/discussions)**.

Upstream (larger community, most general questions answered there):
**[wiki](https://github.com/chuot/rdio-scanner/wiki)** ·
**[discussions](https://github.com/chuot/rdio-scanner/discussions)** ·
[Discord](https://discord.com/invite/pebyc3Sj2x).

If you get value from Rdio Scanner, please
**[star the upstream repo](https://github.com/chuot/rdio-scanner/stargazers)**
and **[sponsor the original author](https://github.com/sponsors/chuot)**. The
official mobile apps are also from upstream:

[![Available on the App Store](./docs/images/app-store-badge.png?raw=true)](https://apps.apple.com/us/app/rdio-scanner/id1563065667#?platform=iphone)
[![Get it on Google Play](./docs/images/google-play-badge.png?raw=true)](https://play.google.com/store/apps/details?id=solutions.saubeo.rdioScanner)

**Happy Rdio scanning !**
