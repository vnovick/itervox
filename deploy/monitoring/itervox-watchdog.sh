#!/usr/bin/env bash
#
# CORE-113: optional external watchdog for the itervox event loop, run every
# minute by deploy/systemd/itervox-watchdog.timer.
#
# Why not systemd WatchdogSec=? That needs the daemon to send sd_notify
# WATCHDOG=1 pings from its loop, and itervox does not (grep NOTIFY_SOCKET in
# cmd/ internal/ → nothing). Type=notify without pings would kill a healthy
# daemon every WatchdogSec. This script watches the same signal from outside:
# GET /api/v1/ready reports "loop_fresh":false when the orchestrator event loop
# has not finished an iteration within 3x the poll interval, or one tick has
# run past 10 minutes (internal/server/ready.go).
#
# Restart policy — only for a wedged loop, never for what a restart can't fix:
#   200                                  healthy                  → reset counter
#   503 with "draining":true             shutdown/reload drain    → reset (never interrupt a drain)
#   503 with "loop_fresh":false          wedged event loop        → count
#   503 otherwise (tracker down, config) restart would not help   → reset
#   connection refused                   process down/restarting  → reset (Restart=always owns this)
#   timeout / other transport error      HTTP server hung         → count
# After ITERVOX_WATCHDOG_FAILURES consecutive counted probes (default 3, i.e.
# ~3 minutes on the 60 s timer) it runs `systemctl restart <service>` — a
# normal SIGTERM stop, so in-flight turns still get the drain.
#
# Env: ITERVOX_WATCHDOG_READY_URL (default http://127.0.0.1:8090/api/v1/ready),
#      ITERVOX_WATCHDOG_FAILURES, ITERVOX_WATCHDOG_SERVICE (default itervox),
#      STATE_DIRECTORY (set by systemd StateDirectory=; default /var/lib/itervox-watchdog).
set -euo pipefail

URL="${ITERVOX_WATCHDOG_READY_URL:-http://127.0.0.1:8090/api/v1/ready}"
LIMIT="${ITERVOX_WATCHDOG_FAILURES:-3}"
SERVICE="${ITERVOX_WATCHDOG_SERVICE:-itervox}"
STATE="${STATE_DIRECTORY:-/var/lib/itervox-watchdog}/failures"

[[ "$LIMIT" =~ ^[1-9][0-9]*$ ]] || { echo "watchdog: ITERVOX_WATCHDOG_FAILURES must be >= 1" >&2; exit 64; }
mkdir -p "$(dirname "$STATE")"

log() { printf 'itervox-watchdog: %s\n' "$*" >&2; }
reset() { echo 0 > "$STATE"; }

if ! systemctl is-active --quiet "$SERVICE"; then
  reset; exit 0   # stopped, starting or stopping: systemd owns it
fi

body="$(mktemp)"
trap 'rm -f "$body"' EXIT
rc=0
code="$(curl -sS --max-time 10 -o "$body" -w '%{http_code}' "$URL" 2>/dev/null)" || rc=$?

json_false() { grep -Eq "\"$1\"[[:space:]]*:[[:space:]]*false" "$body"; }
json_true()  { grep -Eq "\"$1\"[[:space:]]*:[[:space:]]*true" "$body"; }

reason=""
if [[ $rc -eq 7 ]]; then
  reset; exit 0
elif [[ $rc -ne 0 ]]; then
  reason="ready probe transport error (curl exit $rc)"
elif [[ "$code" == 200 ]]; then
  reset; exit 0
elif json_true draining; then
  reset; exit 0
elif json_false loop_fresh; then
  reason="event loop not fresh (loop_fresh=false)"
else
  reset; exit 0
fi

n=$(( $(cat "$STATE" 2>/dev/null || echo 0) + 1 ))
echo "$n" > "$STATE"
log "$reason — strike $n/$LIMIT"
if (( n >= LIMIT )); then
  log "restarting $SERVICE after $n consecutive wedged probes"
  reset
  systemctl restart "$SERVICE"
fi
