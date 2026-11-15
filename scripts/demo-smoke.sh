#!/usr/bin/env bash
# demo-smoke.sh — run `itervox demo` and wait until one demo issue reaches
# Done (#259). The Windows CI job runs it under Git Bash against itervox.exe;
# it works the same against a Linux or macOS binary.
#
# usage: scripts/demo-smoke.sh <itervox-binary> [timeout-seconds]
#
# The demo needs no tracker account, API key or agent CLI: it uses the
# memory tracker and the in-process demo agent, and serves the dashboard
# API without a token. Exits 0 once /api/v1/issues lists an issue in Done,
# 1 on timeout or if the daemon exits first (its output is printed).
set -euo pipefail

bin=${1:?usage: demo-smoke.sh <itervox-binary> [timeout-seconds]}
timeout=${2:-180}
scratch=$(mktemp -d)
log="$scratch.log"

"$bin" demo --dir "$scratch" --no-open >"$log" 2>&1 &
pid=$!
cleanup() {
  kill "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
}
trap cleanup EXIT

fail() {
  echo "demo-smoke: $1" >&2
  echo "--- itervox demo output ---" >&2
  cat "$log" >&2 || true
  exit 1
}

start=$SECONDS
url=""
while (( SECONDS - start < timeout )); do
  if ! kill -0 "$pid" 2>/dev/null; then
    fail "the daemon exited before a demo issue reached Done"
  fi
  if [[ -z "$url" && -s "$scratch/.itervox/dashboard_url" ]]; then
    url=$(tr -d '\r\n' <"$scratch/.itervox/dashboard_url")
    url=${url%/}
    echo "demo-smoke: dashboard at $url"
  fi
  if [[ -n "$url" ]]; then
    issues=$(curl -fsS --max-time 5 "$url/api/v1/issues" 2>/dev/null || true)
    if grep -Eq '"state": ?"Done"' <<<"$issues"; then
      echo "demo-smoke: a demo issue reached Done after $((SECONDS - start))s"
      exit 0
    fi
  fi
  sleep 2
done
fail "no demo issue reached Done within ${timeout}s"
