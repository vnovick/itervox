#!/usr/bin/env bash
#
# CORE-107: prune stale itervox workspaces and old log files. Run by
# deploy/systemd/itervox-cleanup.timer as the service user; safe to run by hand.
#
# Usage: cleanup.sh [options]     (every option also reads an env var, so the
#                                  timer is configured in /etc/itervox/cleanup.env)
#
#   --workdir <dir>              checkout with .itervox/.env     ITERVOX_CLEANUP_WORKDIR
#   --workspaces <dir>           workspace base                  ITERVOX_CLEANUP_WORKSPACES   (default ~/.itervox/workspaces)
#   --logs <dir>                 daemon logs base                ITERVOX_CLEANUP_LOGS         (default ~/.itervox/logs)
#   --state-url <url>            GET /api/v1/state               ITERVOX_CLEANUP_STATE_URL    (default http://127.0.0.1:8090/api/v1/state)
#   --workspace-days <n>         remove workspaces idle > n days ITERVOX_CLEANUP_WORKSPACE_DAYS (default 14)
#   --log-days <n>               remove rotated/session logs > n days ITERVOX_CLEANUP_LOG_DAYS (default 30)
#   --grace-minutes <n>          never touch anything modified in the last n minutes
#                                                                ITERVOX_CLEANUP_GRACE_MINUTES (default 10)
#   --dry-run                    print what would be removed     ITERVOX_CLEANUP_DRY_RUN=1
#
# A workspace is <workspaces>/<project>/<id> or <workspaces>/<project>/worktrees/<id>.
# It is KEPT when any of these holds:
#   - its identifier is running, retrying, paused (incl. pausedWithPR),
#     input-required or queued in GET /api/v1/state;
#   - its identifier appears anywhere in a continuation ledger on disk
#     (paused.json, input_required.json — which also holds the pending input
#     resumes that /api/v1/state does not expose — pending_reviews.json,
#     automation_queue.json);
#   - anything at its top two levels changed within --workspace-days, or
#     within --grace-minutes (a dispatch racing the sweep).
# /api/v1/state is queried again immediately before each removal.
#
# Refusals (exit 1, nothing removed): the daemon answers /api/v1/health but the
# state query fails or answers 401/403 (fix the token: ITERVOX_API_TOKEN in the
# environment or in <workdir>/.itervox/.env); jq is missing; a ledger exists
# but cannot be read. When the daemon is not running at all, only the ledgers
# protect workspaces (nothing can be dispatching).
#
# Logs: removes rotated backups (itervox-*.log, itervox-*.log.gz) and files
# under sessions/ older than --log-days. The live itervox.log, the *.json
# ledgers and api-token are never touched.
set -euo pipefail

WORKDIR="${ITERVOX_CLEANUP_WORKDIR:-}"
WS_BASE="${ITERVOX_CLEANUP_WORKSPACES:-$HOME/.itervox/workspaces}"
LOGS_BASE="${ITERVOX_CLEANUP_LOGS:-$HOME/.itervox/logs}"
STATE_URL="${ITERVOX_CLEANUP_STATE_URL:-http://127.0.0.1:8090/api/v1/state}"
WS_DAYS="${ITERVOX_CLEANUP_WORKSPACE_DAYS:-14}"
LOG_DAYS="${ITERVOX_CLEANUP_LOG_DAYS:-30}"
GRACE_MIN="${ITERVOX_CLEANUP_GRACE_MINUTES:-10}"
DRY_RUN="${ITERVOX_CLEANUP_DRY_RUN:-0}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --workdir)        WORKDIR="${2:?}"; shift 2 ;;
    --workspaces)     WS_BASE="${2:?}"; shift 2 ;;
    --logs)           LOGS_BASE="${2:?}"; shift 2 ;;
    --state-url)      STATE_URL="${2:?}"; shift 2 ;;
    --workspace-days) WS_DAYS="${2:?}"; shift 2 ;;
    --log-days)       LOG_DAYS="${2:?}"; shift 2 ;;
    --grace-minutes)  GRACE_MIN="${2:?}"; shift 2 ;;
    --dry-run)        DRY_RUN=1; shift ;;
    *) echo "cleanup: unknown option: $1" >&2; exit 64 ;;
  esac
done

log() { printf 'cleanup: %s\n' "$*" >&2; }
die() { log "refusing: $*"; exit 1; }

for n in "$WS_DAYS" "$LOG_DAYS" "$GRACE_MIN"; do
  [[ "$n" =~ ^[0-9]+$ ]] || die "retention values must be whole numbers (got '$n')"
done
command -v jq >/dev/null 2>&1 || die "jq is required"

# ── token (never printed) ───────────────────────────────────────────────────
TOKEN="${ITERVOX_API_TOKEN:-}"
if [[ -z "$TOKEN" && -n "$WORKDIR" && -e "$WORKDIR/.itervox/.env" ]]; then
  [[ -r "$WORKDIR/.itervox/.env" ]] || die "$WORKDIR/.itervox/.env is not readable"
  TOKEN="$(sed -nE 's/^[[:space:]]*(export[[:space:]]+)?ITERVOX_API_TOKEN=["'\'']?([^"'\'']*)["'\'']?[[:space:]]*$/\2/p' \
    "$WORKDIR/.itervox/.env" | tail -n1)"
fi

base_url="${STATE_URL%/api/v1/state}"
DAEMON_UP=false
if curl -fsS --max-time 5 -o /dev/null "$base_url/api/v1/health" 2>/dev/null; then
  DAEMON_UP=true
fi

# live_ids prints one identifier per line from GET /api/v1/state, or fails.
live_ids() {
  local body code
  body="$(mktemp)"
  local args=(-sS --max-time 10 -o "$body" -w '%{http_code}')
  if [[ -n "$TOKEN" ]]; then
    args+=(-H "Authorization: Bearer $TOKEN")
  fi
  code="$(curl "${args[@]}" "$STATE_URL" 2>/dev/null || true)"
  if [[ "$code" != 200 ]]; then
    rm -f "$body"
    log "GET /api/v1/state answered ${code:-no response}"
    return 1
  fi
  if ! jq -r '
      [ (.running // [])[]?.identifier,
        (.retrying // [])[]?.identifier,
        (.paused // [])[]?,
        (.inputRequired // [])[]?.identifier,
        ((.pausedWithPR // {}) | keys[]),
        (.automationQueue // [])[]?.identifier ]
      | .[] | select(type == "string" and . != "")' "$body"; then
    rm -f "$body"
    log "GET /api/v1/state returned unparseable JSON"
    return 1
  fi
  rm -f "$body"
}

# ledger_ids prints every string (keys included) found in the continuation
# ledgers. Over-protecting is harmless; missing one is not.
ledger_ids() {
  local f
  while IFS= read -r -d '' f; do
    [[ -r "$f" ]] || die "cannot read ledger $f"
    jq -r '[.. | strings] + [.. | objects | keys[]] | .[]' "$f" 2>/dev/null \
      || die "cannot parse ledger $f"
  done < <(find "$LOGS_BASE" -type f \( -name paused.json -o -name input_required.json \
             -o -name pending_reviews.json -o -name automation_queue.json \) -print0 2>/dev/null)
}

# sanitize mirrors internal/workspace.SanitizeKey: [^A-Za-z0-9._-] -> _
sanitize() { LC_ALL=C sed -E 's/[^A-Za-z0-9._-]/_/g'; }

# protect_forms prints every protected id in BOTH directory forms the daemon
# uses (BH-M6-1): plain workspaces are <root>/<SanitizeKey(id)>
# (internal/workspace.WorkspacePath), but git worktrees are
# <root>/worktrees/<id> with the RAW id (internal/workspace/worktree.go
# worktreePath) — a GitHub "#12" worktree is literally "worktrees/#12".
protect_forms() {
  local ids
  ids="$(cat)"
  printf '%s\n' "$ids"
  printf '%s\n' "$ids" | sanitize
}

PROTECTED="$(mktemp)"
trap 'rm -f "$PROTECTED"' EXIT

refresh_protected() {
  local live=""
  if $DAEMON_UP; then
    live="$(live_ids)" || die "the daemon is up but its state could not be read (token in ITERVOX_API_TOKEN or <workdir>/.itervox/.env?)"
  fi
  { printf '%s\n' "$live"; ledger_ids; } | protect_forms | LC_ALL=C sort -u > "$PROTECTED"
}

# recent DIR MINUTES — true when DIR or anything one level below it changed
# within MINUTES (the directory mtime alone misses edits to existing files).
recent() { [[ -n "$(find "$1" -maxdepth 2 -mmin "-$2" -print -quit 2>/dev/null)" ]]; }

remove() {
  if [[ "$DRY_RUN" == 1 ]]; then
    log "[dry-run] would remove $1"
  else
    rm -rf -- "$1"
    log "removed $1"
  fi
}

# ── workspaces ──────────────────────────────────────────────────────────────
refresh_protected
removed=0 kept=0
if [[ -d "$WS_BASE" ]]; then
  while IFS= read -r -d '' ws; do
    id="$(basename "$ws")"
    case "$id" in .bare|worktrees|.*) continue ;; esac
    if grep -qxF -- "$id" "$PROTECTED"; then
      log "keep $ws (in use or retained for continuation)"; kept=$((kept + 1)); continue
    fi
    if recent "$ws" "$GRACE_MIN" || recent "$ws" $(( WS_DAYS * 1440 )); then
      kept=$((kept + 1)); continue
    fi
    # Re-query right before the removal: a dispatch may have started since.
    refresh_protected
    if grep -qxF -- "$id" "$PROTECTED" || recent "$ws" "$GRACE_MIN"; then
      log "keep $ws (became active during the sweep)"; kept=$((kept + 1)); continue
    fi
    remove "$ws"
    removed=$((removed + 1))
  done < <(find "$WS_BASE" -mindepth 2 -maxdepth 3 -type d \
             \( -path "$WS_BASE/*/worktrees/*" -o ! -path "$WS_BASE/*/*/*" \) -print0 2>/dev/null)

  # Drop git's records of worktrees whose directory is gone.
  if [[ "$DRY_RUN" != 1 && $removed -gt 0 ]]; then
    for g in "$WS_BASE"/*/.bare "$WS_BASE"/*/.git; do
      [[ -e "$g" ]] || continue
      git -C "$(dirname "$g")" worktree prune 2>/dev/null || true
    done
  fi
fi
log "workspaces: removed $removed, kept $kept"

# ── logs ────────────────────────────────────────────────────────────────────
logs_removed=0
if [[ -d "$LOGS_BASE" && "$LOG_DAYS" -gt 0 ]]; then
  while IFS= read -r -d '' f; do
    remove "$f"
    logs_removed=$((logs_removed + 1))
  done < <(find "$LOGS_BASE" -type f -mtime "+$LOG_DAYS" -mmin "+$GRACE_MIN" \
             \( -name 'itervox-*.log' -o -name 'itervox-*.log.gz' -o -path '*/sessions/*' \) -print0 2>/dev/null)
fi
log "logs: removed $logs_removed"
