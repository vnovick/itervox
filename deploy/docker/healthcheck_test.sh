#!/usr/bin/env bash
#
# M4-close D7 test for deploy/docker/healthcheck.sh, with a stubbed curl on
# PATH that records the URL it was asked to probe. Run: make deploy-test.
#
# Proves the port resolution: ITERVOX_SERVER_PORT > PORT > dashboard_url >
# 8090, where an env port of 0 ("the OS picks", CORE-058) is NOT a port to
# probe: the daemon's actual bound port is read from dashboard_url instead.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HC="$HERE/healthcheck.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

fails=0
pass() { printf 'ok   %s\n' "$*"; }
fail() { printf 'FAIL %s\n' "$*"; fails=$((fails + 1)); }

mkdir -p "$WORK/bin" "$WORK/repo/.itervox"
# shellcheck disable=SC2016 # ${*: -1} must expand in the stub, not here
printf '#!/usr/bin/env bash\nprintf "%%s\\n" "${*: -1}" > "%s/url"\n' "$WORK" > "$WORK/bin/curl"
chmod +x "$WORK/bin/curl"

# probe DASHBOARD_URL [env...] — runs the healthcheck; sets URL.
probe() {
  local dash="$1"; shift
  rm -f "$WORK/url" "$WORK/repo/.itervox/dashboard_url"
  if [[ -n "$dash" ]]; then printf '%s\n' "$dash" > "$WORK/repo/.itervox/dashboard_url"; fi
  URL=""
  env -u PORT -u ITERVOX_SERVER_PORT -u ITERVOX_SERVER_HOST -u ITERVOX_HEALTHCHECK_URL \
    PATH="$WORK/bin:$PATH" ITERVOX_REPO_DIR="$WORK/repo" "$@" bash "$HC" || true
  if [[ -f "$WORK/url" ]]; then URL="$(cat "$WORK/url")"; fi
}

check() { # NAME WANT
  if [[ "$URL" == "$2" ]]; then pass "$1 -> $URL"; else fail "$1: got '$URL', want '$2'"; fi
}

probe "" PORT=9000
check "PORT=9000" "http://127.0.0.1:9000/api/v1/health"
probe "" PORT=9000 ITERVOX_SERVER_PORT=9100
check "ITERVOX_SERVER_PORT wins over PORT" "http://127.0.0.1:9100/api/v1/health"
probe "http://0.0.0.0:38359/"
check "no env: port from dashboard_url" "http://127.0.0.1:38359/api/v1/health"
probe "http://0.0.0.0:38359/" PORT=0
check "PORT=0: the bound port from dashboard_url, never port 0" "http://127.0.0.1:38359/api/v1/health"
probe "http://[::]:41234/?token=x" ITERVOX_SERVER_PORT=0
check "ITERVOX_SERVER_PORT=0 with an IPv6 wildcard dashboard_url: probe the v6 family" "http://[::1]:41234/api/v1/health"
probe "" PORT=0
if [[ -z "$URL" ]]; then
  pass "PORT=0 and no dashboard_url yet: unhealthy without probing port 0"
else
  fail "PORT=0 without dashboard_url probed '$URL'"
fi
probe ""
check "nothing set: default 8090" "http://127.0.0.1:8090/api/v1/health"

# CORE-174: probe the bound address family.
probe "http://[::1]:8090/"
check "daemon bound solely to ::1 (dashboard_url)" "http://[::1]:8090/api/v1/health"
probe "" PORT=8090 ITERVOX_SERVER_HOST=::1
check "ITERVOX_SERVER_HOST=::1" "http://[::1]:8090/api/v1/health"
probe "" PORT=8090 ITERVOX_SERVER_HOST=::
check "ITERVOX_SERVER_HOST=:: (v6 wildcard)" "http://[::1]:8090/api/v1/health"
probe "" PORT=8090 ITERVOX_SERVER_HOST=0.0.0.0
check "ITERVOX_SERVER_HOST=0.0.0.0" "http://127.0.0.1:8090/api/v1/health"
probe "http://[::1]:8090/" ITERVOX_SERVER_HOST=0.0.0.0
check "ITERVOX_SERVER_HOST wins over dashboard_url" "http://127.0.0.1:8090/api/v1/health"
probe "http://127.0.0.2:9001/"
check "IPv4 bind from dashboard_url" "http://127.0.0.2:9001/api/v1/health"
probe "" PORT=8090 "ITERVOX_SERVER_HOST=[fd00::5]"
check "bracketed v6 literal" "http://[fd00::5]:8090/api/v1/health"

if [[ $fails -eq 0 ]]; then
  echo "healthcheck_test: PASS"
else
  echo "healthcheck_test: $fails FAILED"
  exit 1
fi
