#!/usr/bin/env bash
#
# CORE-105: container smoke test. Starts the image against a throwaway
# memory-tracker project (a local bare git repo, no network, no tokens, no
# model call) and requires GET /api/v1/health and GET /api/v1/ready to answer
# 200 on the published port.
#
# Usage: deploy/docker/smoke_test.sh [image]      (default image: itervox:smoke)
#   Build first:  docker build -f deploy/docker/Dockerfile -t itervox:smoke .
#   Or:           make deploy-smoke   (builds, runs this, removes the image)
#
# Proves process start, the health route and a ready event loop with a working
# (memory) tracker poll — not dispatch, graceful stop or persistence; those are
# CORE-043/057/062 acceptances. The container and temp dir are always removed.
set -euo pipefail

IMAGE="${1:-itervox:smoke}"
NAME="itervox-smoke-$$"
TIMEOUT="${SMOKE_TIMEOUT:-120}"
WORK="$(mktemp -d)"

cleanup() {
  # -v: the image declares VOLUME /data; without it every run leaks a volume.
  docker rm -f -v "$NAME" >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT

log() { printf 'smoke: %s\n' "$*"; }

# ── throwaway project ───────────────────────────────────────────────────────
git init -q -b main "$WORK/src"
cat > "$WORK/src/WORKFLOW.md" <<'EOF'
---
itervox_schema_version: 2
tracker:
  kind: memory
  active_states: ["Todo", "In Progress"]
  terminal_states: ["Done"]
agent:
  command: claude
  max_concurrent_agents: 1
server:
  port: 8090
---

Smoke test for {{ issue.identifier }}.
EOF
git -C "$WORK/src" add WORKFLOW.md
git -C "$WORK/src" -c user.name=smoke -c user.email=smoke@example.invalid commit -qm smoke
git clone -q --bare "$WORK/src" "$WORK/proj.git"
chmod -R a+rX "$WORK/proj.git"

# Stand-in agent CLI: the daemon refuses to start its run loop without a
# runnable `claude`, and a smoke test must never reach a model. It answers
# --version only; with an empty memory tracker nothing is ever dispatched.
printf '%s\n' '#!/bin/sh' \
  'case "$*" in *--version*) echo "0.0.0 (smoke stub)"; exit 0 ;; esac' \
  'echo "smoke stub: no model calls" >&2' 'exit 1' > "$WORK/claude"
chmod 0755 "$WORK/claude"

# ── run ─────────────────────────────────────────────────────────────────────
log "starting $IMAGE as $NAME"
docker run -d --name "$NAME" \
  -p 127.0.0.1::8090 \
  -v "$WORK/proj.git:/src/proj.git:ro" \
  -v "$WORK/claude:/opt/agent/bin/claude:ro" \
  -e ITERVOX_REPO=file:///src/proj.git \
  -e ITERVOX_REPO_UPDATE=off \
  --stop-timeout 30 \
  "$IMAGE" >/dev/null

PORT="$(docker port "$NAME" 8090/tcp | head -n1 | sed 's/.*://')"
[[ -n "$PORT" ]] || { log "no published port"; docker logs "$NAME" 2>&1 | tail -40; exit 1; }
BASE="http://127.0.0.1:$PORT"

# wait_200 PATH — poll until 200; prints the final status line and body.
wait_200() {
  local path="$1" deadline=$(( $(date +%s) + TIMEOUT )) code body
  while :; do
    if [[ "$(docker inspect -f '{{.State.Running}}' "$NAME" 2>/dev/null)" != true ]]; then
      log "container exited"; docker logs "$NAME" 2>&1 | tail -40; return 1
    fi
    body="$(curl -sS --max-time 5 -w '\n%{http_code}' "$BASE$path" 2>/dev/null || true)"
    code="${body##*$'\n'}"
    if [[ "$code" == 200 ]]; then
      log "curl -fsS localhost:8090$path -> 200 ${body%$'\n'*}"
      return 0
    fi
    if (( $(date +%s) >= deadline )); then
      log "$path not 200 after ${TIMEOUT}s (last: ${code:-none} ${body%$'\n'*})"
      docker logs "$NAME" 2>&1 | tail -40
      return 1
    fi
    sleep 2
  done
}

wait_200 /api/v1/health
wait_200 /api/v1/ready
if docker logs "$NAME" 2>&1 | grep -q 'run returned with error'; then
  log "the daemon's run loop restarted during the smoke:"
  docker logs "$NAME" 2>&1 | grep 'run returned with error' | tail -3
  exit 1
fi
log "container health: $(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$NAME")"
log "PASS"
