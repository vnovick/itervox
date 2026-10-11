#!/usr/bin/env bash
#
# Release check (#137): Itervox completes a real issue on a real repository.
#
# Runs the itervox binary under test against one labelled test issue of a
# GitHub repository (by default this one) with a real Claude agent, waits for
# the issue to reach the completion state with a pull request, and keeps the
# daemon's log and the agent sessions as the record.
#
# Only the test issue is touched: the workflow this script writes uses its own
# state labels (smoke:todo → smoke:in-progress → smoke:done), so no other
# issue of the repository is ever eligible.
#
# Usage:
#   deploy/acceptance/self-hosted-run.sh --issue <number> [--repo owner/repo]
#       [--itervox ./itervox] [--out DIR] [--timeout-min 60] [--dry-run]
#
# --dry-run writes <out>/project/WORKFLOW.md, checks the binary loads it
# (`itervox config show`), and stops: nothing on GitHub is touched and no
# token is needed.
#
# Needs: gh, git, jq, claude (Claude Code CLI) on PATH; GH_TOKEN (issues,
# pull requests and contents write on the repository); ANTHROPIC_API_KEY or
# CLAUDE_CODE_OAUTH_TOKEN for the agent. No secret is ever printed or put on a
# command line.
#
# Exit: 0 the issue reached smoke:done with a pull request; 1 it did not (timed
# out, the daemon died, or no pull request); 2 usage or setup error.
#
# Leaves behind, for review and cleanup by hand: the pull request and its
# branch, and the issue (labelled smoke:done).
set -euo pipefail

REPO="${GITHUB_REPOSITORY:-vnovick/itervox}"
ISSUE=""
ITERVOX="./itervox"
OUT="./acceptance-self-hosted"
TIMEOUT_MIN=60
DRY_RUN=false
POLL_SECONDS="${ITERVOX_SMOKE_POLL_SECONDS:-30}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --repo)        REPO="$2"; shift 2 ;;
    --issue)       ISSUE="$2"; shift 2 ;;
    --itervox)     ITERVOX="$2"; shift 2 ;;
    --out)         OUT="$2"; shift 2 ;;
    --timeout-min) TIMEOUT_MIN="$2"; shift 2 ;;
    --dry-run)     DRY_RUN=true; shift ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done

die() { echo "self-hosted-run: $*" >&2; exit 2; }

[[ "$ISSUE" =~ ^[0-9]+$ ]] || die "--issue <number> is required"
[[ "$REPO" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || die "--repo must be owner/repo"
[[ "$TIMEOUT_MIN" =~ ^[0-9]+$ ]] || die "--timeout-min must be a number"
[[ -x "$ITERVOX" ]] || die "$ITERVOX is not an executable itervox binary"
if ! $DRY_RUN; then
  for tool in gh git jq claude; do
    command -v "$tool" >/dev/null || die "$tool is not on PATH"
  done
  [[ -n "${GH_TOKEN:-}" ]] || die "GH_TOKEN is not set"
  [[ -n "${ANTHROPIC_API_KEY:-}${CLAUDE_CODE_OAUTH_TOKEN:-}" ]] || die "set ANTHROPIC_API_KEY or CLAUDE_CODE_OAUTH_TOKEN for the agent"
fi

ITERVOX="$(cd "$(dirname "$ITERVOX")" && pwd)/$(basename "$ITERVOX")"
mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"
PROJECT="$OUT/project"
LOGS="$OUT/logs"
mkdir -p "$PROJECT" "$LOGS"

TODO="smoke:todo"
WORKING="smoke:in-progress"
DONE="smoke:done"

echo "==> itervox under test: $("$ITERVOX" --version)"

# ── the workflow ────────────────────────────────────────────────────────────
cat > "$PROJECT/WORKFLOW.md" <<EOF
---
itervox_schema_version: 2
tracker:
  kind: github
  api_key: \$GH_TOKEN
  project_slug: $REPO
  active_states: ["$TODO", "$WORKING"]
  terminal_states: ["$DONE"]
  working_state: "$WORKING"
  completion_state: "$DONE"
polling:
  interval_ms: 30000
agent:
  command: claude
  max_concurrent_agents: 1
  max_retries: 1
  max_turns: 60
  turn_timeout_ms: 3000000
workspace:
  root: $OUT/workspaces
hooks:
  after_create: |
    gh repo clone $REPO .
server:
  host: 127.0.0.1
  port: 0
---

You are an engineer working on **{{ issue.identifier }}: {{ issue.title }}** in this repository.

{% if issue.description %}
{{ issue.description }}
{% endif %}

1. Read \`CLAUDE.md\` (or \`AGENTS.md\`) if the repository has one, and follow it.
2. Create the branch: \`git checkout -b itervox-smoke-{{ issue.identifier | remove: "#" }}\`.
3. Make the change the issue asks for. Keep it as small as the issue allows.
4. Run the checks that cover your change.
5. Commit, push and open a pull request:
   \`git push -u origin HEAD\`, then
   \`gh pr create --title "{{ issue.title }} ({{ issue.identifier }})" --body "Closes {{ issue.url }}"\`.

Never commit \`.env\` files or secrets.
EOF

if $DRY_RUN; then
  "$ITERVOX" config show --workflow "$PROJECT/WORKFLOW.md" >/dev/null \
    || die "itervox does not load the generated $PROJECT/WORKFLOW.md"
  echo "==> dry run: $PROJECT/WORKFLOW.md written and loads; nothing on GitHub was touched"
  exit 0
fi

# ── the test issue ──────────────────────────────────────────────────────────
state=$(gh issue view "$ISSUE" --repo "$REPO" --json state -q .state) || die "cannot read issue #$ISSUE of $REPO"
[[ "$state" == "OPEN" ]] || die "issue #$ISSUE is $state; use an open test issue"
gh label create "$TODO" --repo "$REPO" --color 0075ca --force >/dev/null
gh label create "$WORKING" --repo "$REPO" --color e4e669 --force >/dev/null
gh label create "$DONE" --repo "$REPO" --color 0e8a16 --force >/dev/null
gh issue edit "$ISSUE" --repo "$REPO" --remove-label "$WORKING" --remove-label "$DONE" >/dev/null 2>&1 || true
gh issue edit "$ISSUE" --repo "$REPO" --add-label "$TODO" >/dev/null
echo "==> https://github.com/$REPO/issues/$ISSUE labelled $TODO"

# git over HTTPS authenticates with GH_TOKEN through gh's credential helper,
# so neither the hook nor the agent ever sees the token on a command line.
gh auth setup-git >/dev/null

# ── run ─────────────────────────────────────────────────────────────────────
DAEMON_PID=""
stop_daemon() {
  if [[ -n "$DAEMON_PID" ]] && kill -0 "$DAEMON_PID" 2>/dev/null; then
    echo "==> stopping the daemon"
    kill -TERM "$DAEMON_PID" 2>/dev/null || true
    for _ in $(seq 1 120); do
      kill -0 "$DAEMON_PID" 2>/dev/null || break
      sleep 1
    done
    kill -KILL "$DAEMON_PID" 2>/dev/null || true
  fi
}
trap stop_daemon EXIT

echo "==> starting the daemon (log: $LOGS/itervox.log)"
(cd "$PROJECT" && exec setsid "$ITERVOX" --workflow WORKFLOW.md --logs-dir "$LOGS" --no-print-token \
  </dev/null >"$OUT/daemon.stdout" 2>"$OUT/daemon.stderr") &
DAEMON_PID=$!

started=$(date +%s)
deadline=$((started + TIMEOUT_MIN * 60))
pr_url=""
result="timed out after ${TIMEOUT_MIN}m"
while (( $(date +%s) < deadline )); do
  sleep "$POLL_SECONDS"
  if ! kill -0 "$DAEMON_PID" 2>/dev/null; then
    result="the daemon exited (see $OUT/daemon.stderr)"
    break
  fi
  labels=$(gh issue view "$ISSUE" --repo "$REPO" --json labels -q '[.labels[].name] | join(",")' 2>/dev/null || echo "?")
  echo "    $(( ($(date +%s) - started) / 60 ))m: labels=$labels"
  if [[ ",$labels," == *",$DONE,"* ]]; then
    pr_url=$(gh pr list --repo "$REPO" --state all --search "issues/$ISSUE in:body" \
      --json url,body -q "[.[] | select(.body | contains(\"/issues/$ISSUE\"))][0].url // empty")
    if [[ -n "$pr_url" ]]; then
      result="ok"
    else
      result="the issue reached $DONE without a pull request that closes it"
    fi
    break
  fi
done
stop_daemon
trap - EXIT

elapsed=$(( ($(date +%s) - started) / 60 ))
{
  echo "# Self-hosted release check"
  echo
  echo "- itervox: $("$ITERVOX" --version)"
  echo "- issue: https://github.com/$REPO/issues/$ISSUE"
  echo "- pull request: ${pr_url:-none}"
  echo "- result: $result (${elapsed}m)"
  echo "- record: logs/itervox.log, logs/sessions/, daemon.stderr"
} > "$OUT/result.md"
cat "$OUT/result.md"

[[ "$result" == "ok" ]] || exit 1
