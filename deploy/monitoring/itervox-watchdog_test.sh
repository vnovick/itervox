#!/usr/bin/env bash
#
# CORE-113 test for deploy/monitoring/itervox-watchdog.sh. Run: make deploy-test.
# curl and systemctl are stubs; the curl stub answers the readiness probe with
# a fixture body so each /ready shape can be replayed.
#
# Proves:
#   - a blocked event loop ("loop_fresh":false) restarts the service on the
#     3rd consecutive probe, not before, and the counter resets afterwards;
#   - a drain (503 "draining":true) never restarts, even when loop_fresh is false;
#   - a tracker outage (503, loop_fresh true) never restarts;
#   - a healthy probe in between resets the strike counter;
#   - connection refused and an inactive unit never restart;
#   - a probe timeout counts as a strike.

# Stub bodies are single-quoted on purpose.
# shellcheck disable=SC2016
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WD="$HERE/itervox-watchdog.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

fails=0
pass() { printf 'ok   %s\n' "$*"; }
fail() { printf 'FAIL %s\n' "$*"; fails=$((fails + 1)); }

mkdir -p "$WORK/bin" "$WORK/state"
mkstub() { printf '#!/usr/bin/env bash\n%s\n' "$2" > "$WORK/bin/$1"; chmod +x "$WORK/bin/$1"; }
mkstub curl 'out=""
while [[ $# -gt 0 ]]; do case "$1" in -o) out="$2"; shift 2;; -w|--max-time) shift 2;; *) shift;; esac; done
mode="$(cat "$FIX/mode")"
case "$mode" in
  refused) exit 7 ;;
  timeout) exit 28 ;;
esac
cp "$FIX/body" "$out"; printf "%s" "$mode"'
mkstub systemctl 'echo "systemctl $*" >> "$FIX/calls"
[[ "$1" == is-active ]] && { [[ -f "$FIX/inactive" ]] && exit 3; exit 0; }
exit 0'

FIX="$WORK/fix"; mkdir -p "$FIX"
probe() { # probe CODE BODY
  printf '%s' "$1" > "$FIX/mode"; printf '%s' "$2" > "$FIX/body"
  env PATH="$WORK/bin:$PATH" FIX="$FIX" STATE_DIRECTORY="$WORK/state" bash "$WD" 2>>"$FIX/log"
}
restarts() { grep -c '^systemctl restart' "$FIX/calls" || true; }
fresh() { rm -f "$WORK/state/failures" "$FIX/inactive"; : > "$FIX/calls"; : > "$FIX/log"; }

WEDGED='{"ready":false,"loop_fresh":false,"last_poll_ok":true,"config_invalid":false,"degraded":false,"tracker_rate_limited_until":null,"draining":false}'
DRAIN='{"ready":false,"loop_fresh":false,"last_poll_ok":true,"config_invalid":false,"degraded":false,"tracker_rate_limited_until":null,"draining":true}'
TRACKER='{"ready":false,"loop_fresh":true,"last_poll_ok":false,"config_invalid":false,"degraded":true,"tracker_rate_limited_until":null,"draining":false}'
OK='{"ready":true,"loop_fresh":true,"last_poll_ok":true,"config_invalid":false,"degraded":false,"tracker_rate_limited_until":null,"draining":false}'

fresh
probe 503 "$WEDGED"; probe 503 "$WEDGED"; r2=$(restarts)
probe 503 "$WEDGED"; r3=$(restarts)
if [[ "$r2" == 0 && "$r3" == 1 ]] && grep -q 'restarting itervox after 3' "$FIX/log" \
   && [[ "$(cat "$WORK/state/failures")" == 0 ]]; then
  pass "blocked event loop: restart on the 3rd consecutive probe (0 after 2), counter reset"
else
  fail "wedged: r2=$r2 r3=$r3 log=$(cat "$FIX/log")"
fi

fresh
for _ in 1 2 3 4 5; do probe 503 "$DRAIN"; done
if [[ "$(restarts)" == 0 ]]; then pass "draining daemon (loop_fresh false too): never restarted"
else fail "drain restarted"; fi

fresh
for _ in 1 2 3 4 5; do probe 503 "$TRACKER"; done
if [[ "$(restarts)" == 0 ]]; then pass "tracker outage (loop fresh): never restarted"
else fail "tracker restarted"; fi

fresh
probe 503 "$WEDGED"; probe 503 "$WEDGED"; probe 200 "$OK"; probe 503 "$WEDGED"; probe 503 "$WEDGED"
if [[ "$(restarts)" == 0 ]]; then pass "a healthy probe resets the strikes (2 + ok + 2: no restart)"
else fail "reset: restarts=$(restarts)"; fi

fresh
for _ in 1 2 3 4; do probe refused ""; done
touch "$FIX/inactive"
for _ in 1 2 3 4; do probe 503 "$WEDGED"; done
if [[ "$(restarts)" == 0 ]]; then pass "connection refused / inactive unit: never restarted"
else fail "refused/inactive restarted"; fi

fresh
probe timeout ""; probe timeout ""; probe timeout ""
if [[ "$(restarts)" == 1 ]]; then pass "hung HTTP server (probe timeout) counts: restart after 3"
else fail "timeout: restarts=$(restarts)"; fi

if [[ $fails -gt 0 ]]; then
  echo "itervox-watchdog_test: $fails FAILED"
  exit 1
fi
echo "itervox-watchdog_test: PASS"
