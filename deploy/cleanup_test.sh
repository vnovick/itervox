#!/usr/bin/env bash
#
# CORE-107 test for deploy/cleanup.sh. Run: make deploy-test. No daemon, no
# network: curl is a stub that answers /api/v1/health and /api/v1/state from
# fixture files.
#
# Proves:
#   - with /api/v1/state listing one running, one paused, one input-required
#     issue and a pending input resume present only in input_required.json,
#     all four workspaces stay and a fifth stale one is removed;
#   - a stale worktree under <project>/worktrees/ is removed, a fresh one kept;
#   - state answering 401 while the daemon is up: exit 1, nothing removed;
#   - the bearer token comes from <workdir>/.itervox/.env and is never printed;
#   - a workspace touched inside the grace window is kept even when idle
#     retention is 0;
#   - old rotated logs and session files go; itervox.log, *.json and api-token stay.

# Stub bodies are single-quoted on purpose: they expand in the child process.
# shellcheck disable=SC2016
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CLEANUP="$HERE/cleanup.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

command -v jq >/dev/null 2>&1 || { echo "cleanup_test: SKIP (jq not installed)"; exit 0; }

fails=0
pass() { printf 'ok   %s\n' "$*"; }
fail() { printf 'FAIL %s\n' "$*"; fails=$((fails + 1)); }

mkdir -p "$WORK/bin"
mkstub() { printf '#!/usr/bin/env bash\n%s\n' "$2" > "$WORK/bin/$1"; chmod +x "$WORK/bin/$1"; }

# curl stub: /health -> $FIX/health_code (000 = connection refused, exit 7);
# /state -> $FIX/state_code + $FIX/state.json. Records the Authorization
# header it was given.
mkstub curl 'out=/dev/stdout; url=""; w=""; auth=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    -w) w="$2"; shift 2 ;;
    -H) auth="$2"; shift 2 ;;
    --max-time) shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
echo "$url|$auth" >> "$FIX/calls"
case "$url" in
  */api/v1/health)
    c="$(cat "$FIX/health_code")"; [[ "$c" == 000 ]] && exit 7
    [[ "$c" == 200 ]] || exit 22; exit 0 ;;
  */api/v1/state)
    c="$(cat "$FIX/state_code")"
    [[ "$c" == 200 ]] && cp "$FIX/state.json" "$out"
    [[ -n "$w" ]] && printf "%s" "$c"; exit 0 ;;
esac
exit 7'

old() { touch -t 202001010000 "$@"; }
names() { find "$1" -mindepth 1 -maxdepth 1 -exec basename {} \; | tr '\n' ' '; }

# setup NAME — fresh HOME-like tree with five workspaces, two worktrees, logs.
setup() {
  T="$WORK/$1"; FIX="$T/fix"; mkdir -p "$FIX"
  WS="$T/ws/proj-abc"; LOGS="$T/logs/linear/proj-abc"; WD="$T/repo"
  mkdir -p "$WS/worktrees" "$LOGS/sessions" "$WD/.itervox"
  for id in ENG-1 ENG-2 ENG-3 ENG-4 ENG-5; do
    mkdir -p "$WS/$id"; echo x > "$WS/$id/file"; old "$WS/$id/file" "$WS/$id"
  done
  mkdir -p "$WS/worktrees/ENG-6" "$WS/worktrees/ENG-7"
  echo x > "$WS/worktrees/ENG-6/f"; old "$WS/worktrees/ENG-6/f" "$WS/worktrees/ENG-6"
  echo x > "$WS/worktrees/ENG-7/f"   # fresh
  # Ledgers. ENG-4 exists ONLY as a pending input resume on disk.
  printf '{"version":1,"sha256":"x","payload":{"awaiting":{},"pending_resume":{"ENG-4":{"identifier":"ENG-4"}}}}\n' \
    > "$LOGS/input_required.json"
  printf '[]\n' > "$LOGS/paused.json"
  echo log > "$LOGS/itervox.log"; echo tok > "$LOGS/api-token"
  echo a > "$LOGS/itervox-2020-01-01T00-00-00.000.log.gz"; echo b > "$LOGS/sessions/old.jsonl"
  echo c > "$LOGS/sessions/new.jsonl"
  old "$LOGS/itervox.log" "$LOGS/api-token" "$LOGS/input_required.json" "$LOGS/paused.json" \
      "$LOGS/itervox-2020-01-01T00-00-00.000.log.gz" "$LOGS/sessions/old.jsonl"
  printf 'LINEAR_API_KEY=lin_x\nITERVOX_API_TOKEN="s3cr3t-token"\n' > "$WD/.itervox/.env"
  cat > "$FIX/state.json" <<'JSON'
{"running":[{"identifier":"ENG-1"}],"retrying":[],"paused":["ENG-2"],
 "inputRequired":[{"identifier":"ENG-3"}],"history":[{"identifier":"ENG-5"}]}
JSON
  echo 200 > "$FIX/health_code"; echo 200 > "$FIX/state_code"; : > "$FIX/calls"
}

run() {
  RC=0
  OUT="$(env -u ITERVOX_API_TOKEN PATH="$WORK/bin:$PATH" FIX="$FIX" HOME="$T" \
    bash "$CLEANUP" --workdir "$WD" --workspaces "$T/ws" --logs "$T/logs" "$@" 2>&1)" || RC=$?
}

# 1. retention of continuation workspaces
setup keep
run --workspace-days 14 --log-days 30 --grace-minutes 10
kept_all=true
for id in ENG-1 ENG-2 ENG-3 ENG-4; do [[ -d "$WS/$id" ]] || kept_all=false; done
if [[ $RC -eq 0 ]] && $kept_all && [[ ! -e "$WS/ENG-5" ]]; then
  pass "running/paused/inputRequired/pending-resume workspaces kept; stale ENG-5 removed"
else
  fail "retention: rc=$RC ls=$(names "$WS") out=$OUT"
fi
if [[ ! -e "$WS/worktrees/ENG-6" && -d "$WS/worktrees/ENG-7" ]]; then
  pass "stale worktree removed, fresh worktree kept"
else
  fail "worktrees: $(names "$WS/worktrees")"
fi
if grep -q 'Authorization: Bearer s3cr3t-token' "$FIX/calls" && ! grep -q 's3cr3t' <<<"$OUT"; then
  pass "token read from .itervox/.env, sent as bearer, never printed"
else
  fail "token: calls=$(cat "$FIX/calls") out=$OUT"
fi
states=$(grep -c '/api/v1/state' "$FIX/calls" || true)
if [[ "$states" -ge 3 ]]; then
  pass "state re-queried before each removal ($states state queries for 2 removals)"
else
  fail "requery: only $states state queries"
fi
if [[ ! -e "$LOGS/itervox-2020-01-01T00-00-00.000.log.gz" && ! -e "$LOGS/sessions/old.jsonl" \
      && -e "$LOGS/sessions/new.jsonl" && -e "$LOGS/itervox.log" && -e "$LOGS/api-token" \
      && -e "$LOGS/input_required.json" && -e "$LOGS/paused.json" ]]; then
  pass "old rotated log + session file removed; itervox.log, ledgers, api-token kept"
else
  fail "logs: $(find "$LOGS" -type f | tr '\n' ' ')"
fi

# 2. 401 while the daemon is up
setup unauth
echo 401 > "$FIX/state_code"
run --workspace-days 14
remaining=$(find "$WS" -mindepth 1 -maxdepth 2 -type d | wc -l | tr -d ' ')
if [[ $RC -eq 1 ]] && [[ "$remaining" == 8 ]] && [[ -e "$LOGS/sessions/old.jsonl" ]] && grep -q refusing <<<"$OUT"; then
  pass "state 401 while the daemon is up: exit 1, nothing removed"
else
  fail "401: rc=$RC remaining=$remaining out=$OUT"
fi

# 3. daemon down: ledgers still protect; nothing can be dispatching
setup down
echo 000 > "$FIX/health_code"
run --workspace-days 14
if [[ $RC -eq 0 && -d "$WS/ENG-4" && ! -e "$WS/ENG-1" ]]; then
  pass "daemon down: ledger-only protection (pending resume kept, others by age)"
else
  fail "down: rc=$RC ls=$(names "$WS") out=$OUT"
fi

# 4. grace window beats zero retention
setup grace
touch "$WS/ENG-5/file"
run --workspace-days 0 --grace-minutes 10
if [[ $RC -eq 0 && -d "$WS/ENG-5" ]]; then
  pass "workspace modified inside the grace window kept with --workspace-days 0"
else
  fail "grace: rc=$RC out=$OUT"
fi

# 5. dry run removes nothing
setup dry
run --dry-run
if [[ $RC -eq 0 && -d "$WS/ENG-5" && -e "$LOGS/sessions/old.jsonl" ]] && grep -q 'would remove' <<<"$OUT"; then
  pass "--dry-run lists removals and removes nothing"
else
  fail "dry: rc=$RC out=$OUT"
fi

# BH-M6-1: GitHub identifiers are "#N". Plain workspaces sanitize them
# ("_15", internal/workspace.WorkspacePath) but worktrees use the RAW id
# (internal/workspace/worktree.go worktreePath: <root>/worktrees/#12). A
# paused / input-required / pending-review GitHub worktree must be kept.
setup github
mkdir -p "$WS/worktrees/#12" "$WS/worktrees/#13" "$WS/worktrees/#14" "$WS/_15" "$WS/worktrees/#16"
for d in "$WS/worktrees/#12" "$WS/worktrees/#13" "$WS/worktrees/#14" "$WS/_15" "$WS/worktrees/#16"; do
  echo x > "$d/f"; old "$d/f" "$d"
done
printf '{"#14":{"issue_id":"i14","identifier":"#14","profile":"reviewer"}}\n' > "$LOGS/pending_reviews.json"
old "$LOGS/pending_reviews.json"
cat > "$FIX/state.json" <<'JSON'
{"running":[],"retrying":[],"paused":["#12"],"inputRequired":[{"identifier":"#13"}],
 "automationQueue":[{"identifier":"#15"}]}
JSON
run --workspace-days 14 --grace-minutes 10
if [[ $RC -eq 0 && -d "$WS/worktrees/#12" && -d "$WS/worktrees/#13" && -d "$WS/worktrees/#14" && -d "$WS/_15" \
      && ! -e "$WS/worktrees/#16" ]]; then
  pass "GitHub #N: paused/input-required/pending-review worktrees (raw id) and a sanitized workspace kept; unprotected #16 removed"
else
  fail "github #N: rc=$RC worktrees=$(names "$WS/worktrees") ws=$(names "$WS") out=$OUT"
fi

if [[ $fails -gt 0 ]]; then
  echo "cleanup_test: $fails FAILED"
  exit 1
fi
echo "cleanup_test: PASS"
