#!/usr/bin/env bash
#
# CORE-107: upgrade the itervox binary on a systemd host, with checksum
# verification, an atomic swap and automatic rollback.
#
# Usage:
#   sudo ./upgrade.sh --version v0.2.2 [options]
#
# Options:
#   --version <tag|latest>  release tag to install                 (default: latest)
#   --bin <path>            installed binary                       (default: /usr/local/bin/itervox)
#   --service <name>        systemd unit to restart                (default: itervox)
#   --ready-url <url>       readiness probe checked after restart  (default: http://127.0.0.1:8090/api/v1/ready)
#   --ready-timeout <sec>   how long the new binary gets to turn ready (default: 420; the
#                           restart itself waits for the old daemon's drain, up to TimeoutStopSec=300)
#   --base-url <url>        release download base                  (default: https://github.com/vnovick/itervox/releases/download)
#   --arch <amd64|arm64>    override the detected architecture
#
# Order of operations (every step before the restart is abortable with the
# running service untouched):
#   1. download itervox_linux_<arch>.tar.gz and checksums.txt for the tag;
#   2. verify the archive's sha256 against its line in checksums.txt — a
#      missing line or a mismatch aborts here, BEFORE any systemctl call;
#   3. extract and run `<new> --version` — a binary that cannot start aborts;
#   4. keep the current binary as <bin>.prev, stage the new one as <bin>.new
#      in the same directory and rename(2) it over <bin> (atomic: a reader
#      sees the old or the new file, never a partial one; the running daemon
#      keeps its already-open inode);
#   5. systemctl restart <service> (SIGTERM → CORE-057 drain → new binary);
#   6. poll --ready-url until it answers 200 or --ready-timeout passes;
#   7. on a failed restart or readiness check: rename <bin>.prev back into
#      place, restart again and re-check readiness.
#
# /ready (not /health) is the gate on purpose: /health is 200 as soon as the
# HTTP server listens, /ready also needs a live event loop and a working
# tracker poll (CORE-043). A tracker outage during the upgrade therefore rolls
# back too; rerun once the tracker is healthy.
#
# Exit: 0 upgraded (or already on that version); 1 aborted before the swap
# (nothing changed); 2 rolled back to the previous binary (service ready on
# it); 3 rollback also failed to turn ready, or <bin>.prev could not be
# restored — investigate by hand; 64 usage.
set -euo pipefail

VERSION="latest"
BIN="/usr/local/bin/itervox"
SERVICE="itervox"
READY_URL="http://127.0.0.1:8090/api/v1/ready"
READY_TIMEOUT=420
BASE_URL="https://github.com/vnovick/itervox/releases/download"
LATEST_API="https://api.github.com/repos/vnovick/itervox/releases/latest"
ARCH=""
# Poll interval for the readiness loop; tests set it to 0.
POLL_INTERVAL="${ITERVOX_UPGRADE_POLL_INTERVAL:-3}"

usage() { sed -n '4,20p' "$0" >&2; exit 64; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version)       VERSION="${2:?}"; shift 2 ;;
    --bin)           BIN="${2:?}"; shift 2 ;;
    --service)       SERVICE="${2:?}"; shift 2 ;;
    --ready-url)     READY_URL="${2:?}"; shift 2 ;;
    --ready-timeout) READY_TIMEOUT="${2:?}"; shift 2 ;;
    --base-url)      BASE_URL="${2:?}"; shift 2 ;;
    --arch)          ARCH="${2:?}"; shift 2 ;;
    -h|--help)       usage ;;
    *) echo "upgrade: unknown option: $1" >&2; exit 64 ;;
  esac
done

log() { printf 'upgrade: %s\n' "$*" >&2; }
die() { log "error: $*"; exit 1; }

[[ "$READY_TIMEOUT" =~ ^[0-9]+$ ]] || die "--ready-timeout must be whole seconds"
[[ -x "$BIN" ]] || die "$BIN is not an installed executable (use bootstrap.sh for a first install)"

if [[ -z "$ARCH" ]]; then
  case "$(uname -m)" in
    x86_64|amd64)  ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    *) die "unsupported architecture $(uname -m)" ;;
  esac
fi

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  else
    shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

TMP="$(mktemp -d)"
cleanup() { rm -rf "$TMP"; rm -f "$BIN.new"; }
trap cleanup EXIT

if [[ "$VERSION" == "latest" ]]; then
  VERSION="$(curl -fsSL "$LATEST_API" | sed -nE 's/.*"tag_name"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/p' | head -n1)"
  [[ -n "$VERSION" ]] || die "could not resolve the latest release tag"
fi
[[ "$VERSION" =~ ^v?[0-9A-Za-z._+-]+$ ]] || die "refusing odd version string: $VERSION"

ARCHIVE="itervox_linux_${ARCH}.tar.gz"
log "downloading $ARCHIVE and checksums.txt for $VERSION"
curl -fsSL -o "$TMP/$ARCHIVE" "$BASE_URL/$VERSION/$ARCHIVE" || die "download of $ARCHIVE failed"
curl -fsSL -o "$TMP/checksums.txt" "$BASE_URL/$VERSION/checksums.txt" || die "download of checksums.txt failed"

# ── 2. checksum (before anything touches the service) ───────────────────────
want="$(awk -v f="$ARCHIVE" '$2 == f || $2 == "*"f {print $1; exit}' "$TMP/checksums.txt")"
[[ -n "$want" ]] || die "checksums.txt has no line for $ARCHIVE; not installing"
got="$(sha256_of "$TMP/$ARCHIVE")"
if [[ "$got" != "$want" ]]; then
  die "sha256 mismatch for $ARCHIVE (want $want, got $got); not installing"
fi
log "sha256 ok: $got"

# ── 3. extract + smoke the new binary ───────────────────────────────────────
tar -xzf "$TMP/$ARCHIVE" -C "$TMP" itervox || die "archive has no itervox binary"
new_version="$("$TMP/itervox" --version 2>&1)" || die "new binary does not run: $new_version"
old_version="$("$BIN" --version 2>&1 || true)"
log "installed: $old_version"
log "candidate: $new_version"
if cmp -s "$TMP/itervox" "$BIN"; then
  log "$BIN is already this build; nothing to do"
  exit 0
fi

# ── 4. atomic swap, previous binary kept ────────────────────────────────────
# cp -p then rename: <bin>.prev is a full copy, and <bin> is replaced by a
# rename within one directory (same filesystem), never written in place.
cp -p "$BIN" "$BIN.prev.tmp"
mv -f "$BIN.prev.tmp" "$BIN.prev"
install -m 0755 "$TMP/itervox" "$BIN.new"
mv -f "$BIN.new" "$BIN"
log "swapped $BIN (previous kept as $BIN.prev)"

wait_ready() {
  local deadline=$(( $(date +%s) + READY_TIMEOUT )) code
  while :; do
    code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$READY_URL" || true)"
    if [[ "$code" == "200" ]]; then
      return 0
    fi
    if (( $(date +%s) >= deadline )); then
      log "not ready after ${READY_TIMEOUT}s (last status ${code:-none})"
      return 1
    fi
    sleep "$POLL_INTERVAL"
  done
}

rollback() {
  log "ROLLING BACK to $BIN.prev"
  # Guarded (M6-close): under set -e a failed copy used to end the script
  # with exit 1 and no word, leaving the new, not-ready build in place.
  if ! cp -p "$BIN.prev" "$BIN.new" || ! mv -f "$BIN.new" "$BIN"; then
    rm -f "$BIN.new"
    log "could not restore $BIN from $BIN.prev; the new build is still installed — restore it by hand"
    exit 3
  fi
  # A unit that failed repeatedly on the bad build may have hit its start
  # limit; clear it so the rollback restart is not refused.
  systemctl reset-failed "$SERVICE" 2>/dev/null || true
  if systemctl restart "$SERVICE" && wait_ready; then
    log "rolled back; $SERVICE is ready on: $("$BIN" --version 2>&1 || true)"
    exit 2
  fi
  log "rollback did not turn ready either; check 'journalctl -u $SERVICE' and '$BIN doctor'"
  exit 3
}

# ── 5-7. restart, readiness gate, rollback ──────────────────────────────────
log "restarting $SERVICE (waits for the old daemon's drain)"
if ! systemctl restart "$SERVICE"; then
  log "systemctl restart failed"
  rollback
fi
if ! wait_ready; then
  rollback
fi
log "upgraded to $new_version; $SERVICE is ready"
