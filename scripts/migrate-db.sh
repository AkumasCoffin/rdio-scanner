#!/usr/bin/env bash
#
# migrate-db.sh — copy an Rdio Scanner Postgres database to another Postgres
# instance. Streams pg_dump | pg_restore so no intermediate file is needed,
# but supports a --file mode for offline restore too.
#
# Usage:
#   ./migrate-db.sh \
#       --src   "postgres://user:pass@old-host:5432/rdio_scanner" \
#       --dst   "postgres://user:pass@new-host:5432/rdio_scanner" \
#       [--file backup.dump]   # write to file instead of streaming
#       [--restore-from FILE]  # restore from a previously-written file
#       [--no-confirm]         # skip the destructive-action prompt
#       [--rdio-only]          # filter to rdioScanner* tables only
#
# Examples:
#   # Stream live from old to new:
#   ./migrate-db.sh --src "$OLD_DSN" --dst "$NEW_DSN"
#
#   # Two-step (dump now, restore later from a different machine):
#   ./migrate-db.sh --src "$OLD_DSN" --file rdio.dump
#   ./migrate-db.sh --restore-from rdio.dump --dst "$NEW_DSN"
#
# Requires pg_dump and pg_restore from a Postgres client whose major version
# is >= the SOURCE server. Install: `apt install postgresql-client-16` (or
# equivalent for your distro / Postgres version).

set -euo pipefail

SRC=""
DST=""
FILE=""
RESTORE_FROM=""
NO_CONFIRM=0
RDIO_ONLY=0

usage() {
    sed -n '2,/^$/p' "$0" | sed 's/^# \{0,1\}//'
    exit 1
}

while [ $# -gt 0 ]; do
    case "$1" in
        --src)           SRC="$2"; shift 2 ;;
        --dst)           DST="$2"; shift 2 ;;
        --file)          FILE="$2"; shift 2 ;;
        --restore-from)  RESTORE_FROM="$2"; shift 2 ;;
        --no-confirm)    NO_CONFIRM=1; shift ;;
        --rdio-only)     RDIO_ONLY=1; shift ;;
        -h|--help)       usage ;;
        *)               echo "unknown arg: $1" >&2; usage ;;
    esac
done

# Validate flag combinations.
if [ -n "$RESTORE_FROM" ]; then
    [ -n "$DST" ]       || { echo "--restore-from requires --dst" >&2; exit 2; }
    [ -z "$SRC$FILE" ]  || { echo "--restore-from is incompatible with --src/--file" >&2; exit 2; }
elif [ -n "$FILE" ] && [ -z "$DST" ]; then
    [ -n "$SRC" ]       || { echo "--file requires --src" >&2; exit 2; }
else
    [ -n "$SRC" ] && [ -n "$DST" ] || { echo "need both --src and --dst (or --file)" >&2; usage; }
fi

require() { command -v "$1" >/dev/null 2>&1 || { echo "missing tool: $1" >&2; exit 3; }; }
require pg_dump
require pg_restore

# Build the table-include list when --rdio-only is set. Postgres folds
# unquoted identifiers to lowercase, but Rdio Scanner schema is camelCase,
# so the patterns must be double-quoted.
DUMP_TABLE_FLAGS=()
if [ "$RDIO_ONLY" -eq 1 ]; then
    while IFS= read -r tbl; do
        DUMP_TABLE_FLAGS+=( --table="$tbl" )
    done <<'EOF'
"rdioScannerCalls"
"rdioScannerConfigs"
"rdioScannerSystems"
"rdioScannerTalkgroups"
"rdioScannerGroups"
"rdioScannerTags"
"rdioScannerUnits"
"rdioScannerApikeys"
"rdioScannerDirwatches"
"rdioScannerDownstreams"
"rdioScannerLogs"
"rdioScannerMeta"
"rdioScannerAccesses"
EOF
fi

# Common pg_dump flags. --no-owner / --no-privileges keeps the dump clean
# when the destination role differs from the source role. -F c is the
# custom binary format pg_restore expects.
DUMP_BASE_FLAGS=( -F c --no-owner --no-privileges --verbose )
RESTORE_BASE_FLAGS=( --no-owner --no-privileges --verbose --clean --if-exists )

confirm_destructive() {
    [ "$NO_CONFIRM" -eq 1 ] && return 0
    echo
    echo "WARNING: --clean drops any existing rdioScanner* tables in the target"
    echo "         before restoring. The destination database is:"
    echo "         $1"
    printf "Continue? [y/N] "
    read -r ans
    case "$ans" in [yY]*) return 0 ;; *) echo "aborted." >&2; exit 4 ;; esac
}

dump_to_file() {
    echo "==> dumping $SRC -> $FILE"
    pg_dump "${DUMP_BASE_FLAGS[@]}" "${DUMP_TABLE_FLAGS[@]}" \
        --file="$FILE" \
        "$SRC"
    echo "==> wrote $FILE ($(du -h "$FILE" | cut -f1))"
}

stream_dump_restore() {
    confirm_destructive "$DST"
    echo "==> streaming $SRC -> $DST"
    pg_dump "${DUMP_BASE_FLAGS[@]}" "${DUMP_TABLE_FLAGS[@]}" "$SRC" \
        | pg_restore "${RESTORE_BASE_FLAGS[@]}" -d "$DST"
    echo "==> migration complete"
}

restore_from_file() {
    confirm_destructive "$DST"
    echo "==> restoring $RESTORE_FROM -> $DST"
    pg_restore "${RESTORE_BASE_FLAGS[@]}" -d "$DST" "$RESTORE_FROM"
    echo "==> restore complete"
}

if [ -n "$RESTORE_FROM" ]; then
    restore_from_file
elif [ -n "$FILE" ] && [ -z "$DST" ]; then
    dump_to_file
else
    stream_dump_restore
fi

# Sanity check: count rows in the calls table on the destination.
if [ -n "$DST" ]; then
    require psql
    echo "==> destination call count:"
    psql "$DST" -c 'select count(*) as calls from "rdioScannerCalls";' || true
fi
