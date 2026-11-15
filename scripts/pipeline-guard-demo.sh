#!/usr/bin/env bash
# pipeline-guard-demo.sh: a scripted reproduction of an agent asked to fix a
# red build that edits the CI pipeline instead (demo/pipeline-guard/), run
# unguarded and then with guardrails lanes and signed checks.
#
# usage: scripts/pipeline-guard-demo.sh [--part unguarded|guarded|both] [--bin PATH]
#                             [--operator api|ui] [--keep]
#                             [--agent scripted|claude|codex]
#                             [--dump DIR] [--referee PATH]
#
# Each part sets up a fresh temporary project: a small Go service whose test
# fails on main (demo/pipeline-guard/repo) in a local bare "remote", one local-tracker
# issue (demo/pipeline-guard/issues/DEMO-1.md), the fixer profile, and the part's
# WORKFLOW.md (demo/pipeline-guard/WORKFLOW.<part>.md). It starts the daemon headless
# on a free loopback port, assigns the issue to the fixer profile, moves it
# to Todo, and plays the story:
#
#   unguarded  lanes off, no verify: the scripted agent's CI edit
#              (continue-on-error) pushes, the PR opens, the run is "green",
#              and the pushed branch's tests still fail.
#   guarded    the default human lane and verify [{name: test}]: the same
#              push is held as a pipeline change, the operator rejects it,
#              the signed check records the real exit code (1), the operator
#              asks for a code fix, the agent fixes cart.go, the push goes
#              through the auto lane, the signed check passes, the PR opens.
#
# The agent is demo/pipeline-guard/scripted-agent.sh (backend: custom, no model) and
# `gh` is demo/pipeline-guard/gh-stub.sh, so nothing leaves the machine. With
# --operator api (default) this script answers as the operator through the
# dashboard API; with --operator ui it waits for someone (or
# scripts/pipeline-guard-capture.mjs) to answer in the dashboard. It prints
# machine-readable lines for the capture script:
#
#   DEMO_DASHBOARD=<url>   DEMO_BEAT=<name>   DEMO_RESULT=<part> ok
#
# --agent claude|codex is the setup for recording with a real agent
# (demo/pipeline-guard/RECORDING.md): the same project with the agent command swapped
# in WORKFLOW.md, the daemon started and the issue assigned to the fixer
# profile, then nothing is driven: you move DEMO-1 to Todo and answer in the
# dashboard; Ctrl-C stops the daemon and the project is kept. Run one part
# at a time (--part unguarded or --part guarded).
#
# --dump DIR saves the raw output of every step the terminal cut
# (scripts/pipeline-guard-capture.mjs --style terminal) replays, per part, under
# DIR/<part>/: NAME.cmd (the command as shown), NAME.out (its combined
# stdout and stderr, byte for byte) and NAME.rc (its exit status), plus
# DIR/<part>/agent/ (the scripted agent's own steps, see
# demo/pipeline-guard/scripted-agent.sh). --referee PATH (a referee binary, cmd/referee)
# adds `referee lanes` on the held branch and `referee verify` on the
# signed records to the guarded part.
#
# With DEMO_GO=<file> a part waits for <file> (instead of DEMO_START_DELAY)
# before it moves the issue to Todo. With DEMO_RELEASE=<file> each part keeps its daemon running after its last
# beat until <file> exists. Without --bin it builds the binary (go build; needs internal/server/web/dist,
# i.e. `make web-build` once). Temporary projects are removed on exit unless
# --keep. Exits 0 when every part told its story, 1 otherwise.
set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd)
demo_dir="$repo_root/demo/pipeline-guard"
part=both
bin=""
operator=api
keep=0
agent=scripted
dump=""
referee=""
timeout=${DEMO_TIMEOUT:-240}
# Pauses that give a viewer time to read each beat (seconds).
start_delay=${DEMO_START_DELAY:-4}
operator_delay=${DEMO_OPERATOR_DELAY:-6}
export DEMO_AGENT_PACE=${DEMO_AGENT_PACE:-2}

while (($#)); do
  case "$1" in
  --part) part=${2:?}; shift 2 ;;
  --bin) bin=${2:?}; shift 2 ;;
  --operator) operator=${2:?}; shift 2 ;;
  --keep) keep=1; shift ;;
  --dump) dump=${2:?}; shift 2 ;;
  --referee) referee=${2:?}; shift 2 ;;
  --agent) agent=${2:?}; shift 2 ;;
  -h | --help) sed -n '2,36p' "$0"; exit 0 ;;
  *) echo "pipeline-guard-demo: unknown argument $1" >&2; exit 2 ;;
  esac
done
case "$part" in unguarded | guarded) parts=("$part") ;; both) parts=(unguarded guarded) ;;
*) echo "pipeline-guard-demo: --part must be unguarded, guarded or both" >&2; exit 2 ;; esac
case "$operator" in api | ui) ;; *) echo "pipeline-guard-demo: --operator must be api or ui" >&2; exit 2 ;; esac
case "$agent" in
scripted) ;;
claude | codex)
  ((${#parts[@]} == 1)) || { echo "pipeline-guard-demo: with --agent $agent pick one --part" >&2; exit 2; }
  command -v "$agent" >/dev/null || { echo "pipeline-guard-demo: $agent is not on PATH" >&2; exit 2; }
  keep=1
  ;;
*) echo "pipeline-guard-demo: --agent must be scripted, claude or codex" >&2; exit 2 ;;
esac
if [[ -n "$dump" ]]; then mkdir -p "$dump" && dump=$(cd "$dump" && pwd); fi
if [[ -n "$referee" ]]; then referee=$(cd "$(dirname "$referee")" && pwd)/$(basename "$referee"); fi
for tool in git curl jq go; do
  command -v "$tool" >/dev/null || { echo "pipeline-guard-demo: $tool is required" >&2; exit 2; }
done

work=$(mktemp -d "${TMPDIR:-/tmp}/itervox-demo.XXXXXX")
touch "$work/.itervox-demo-run"
pid=""
cleanup() {
  if [[ -n "$pid" ]]; then
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
    pid=""
  fi
  if ((keep)); then
    echo "pipeline-guard-demo: kept $work"
  elif [[ -f "$work/.itervox-demo-run" ]]; then
    chmod -R u+w "$work" 2>/dev/null || true
    find "$work" -depth -delete 2>/dev/null || true
  fi
}
trap cleanup EXIT
trap 'exit 130' INT TERM HUP

if [[ -z "$bin" ]]; then
  echo "pipeline-guard-demo: building itervox"
  (cd "$repo_root" && go build -o "$work/itervox" ./cmd/itervox)
  bin="$work/itervox"
fi
bin=$(cd "$(dirname "$bin")" && pwd)/$(basename "$bin")

# The daemon and the agent get their own HOME and XDG directories, so the
# demo writes nothing outside $work (the Go build cache is shared to keep
# `go test` fast). Settings a developer shell may carry are cleared.
gocache=$(go env GOCACHE)
gomodcache=$(go env GOMODCACHE)
for k in ITERVOX_SERVER_PORT ITERVOX_SERVER_HOST PORT ITERVOX_API_TOKEN ITERVOX_DRY_RUN \
  GITHUB_TOKEN GH_TOKEN LINEAR_API_KEY ANTHROPIC_API_KEY OPENAI_API_KEY; do
  unset "$k"
done
# Fixed identities and dates: the seeded history has the same commit ids on
# every run.
export GIT_AUTHOR_NAME="Scripted Agent" GIT_AUTHOR_EMAIL="agent@example.com"
export GIT_COMMITTER_NAME="Scripted Agent" GIT_COMMITTER_EMAIL="agent@example.com"
export GOTOOLCHAIN=local GOFLAGS=-mod=mod GOCACHE="$gocache" GOMODCACHE="$gomodcache"
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1

say() { echo "pipeline-guard-demo: $*"; }
fail() {
  echo "pipeline-guard-demo: $1" >&2
  if [[ -n "${run:-}" && -f "$run/daemon.log" ]]; then
    echo "--- daemon log (tail) ---" >&2
    tail -n 60 "$run/daemon.log" >&2 || true
  fi
  exit 1
}

api() { # api METHOD PATH [JSON]
  local args=(-fsS --max-time 10 -X "$1" "$url/api/v1$2")
  [[ $# -ge 3 ]] && args+=(-H 'Content-Type: application/json' -d "$3")
  curl "${args[@]}"
}

# wait_for DESCRIPTION JQ-FILTER: poll /api/v1/state until the filter is true.
wait_for() {
  local what=$1 filter=$2 start=$SECONDS
  while ((SECONDS - start < timeout)); do
    kill -0 "$pid" 2>/dev/null || fail "the daemon exited while waiting for: $what"
    if api GET /state 2>/dev/null | jq -e "$filter" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.5
  done
  fail "timed out after ${timeout}s waiting for: $what"
}

# issue_state: DEMO-1's state from its issue file (the local tracker's source
# of truth; /api/v1/issues can lag a few seconds behind it).
issue_state() { sed -n 's/^state: //p' "$run/project/.itervox/issues/DEMO-1.md" 2>/dev/null | head -n 1; }

wait_issue_state() { # wait_issue_state STATE
  local start=$SECONDS
  while ((SECONDS - start < timeout)); do
    kill -0 "$pid" 2>/dev/null || fail "the daemon exited while waiting for DEMO-1 to reach $1"
    [[ "$(issue_state)" == "$1" ]] && return 0
    sleep 0.5
  done
  fail "timed out after ${timeout}s waiting for DEMO-1 to reach $1 (it is $(issue_state))"
}

# reply TEXT: the operator answers DEMO-1's input request.
reply() {
  if [[ "$operator" == ui ]]; then
    say "waiting for the operator to answer in the dashboard"
    return 0
  fi
  sleep "$operator_delay"
  say "operator replies: $1"
  api POST /issues/DEMO-1/provide-input "$(jq -nc --arg m "$1" '{message: $m}')" >/dev/null
}

setup() { # setup PART: writes $run
  run="$work/$1"
  mkdir -p "$run/bin" "$run/home" "$run/gh"
  cp "$demo_dir/scripted-agent.sh" "$run/bin/scripted-agent.sh"
  cp "$demo_dir/gh-stub.sh" "$run/bin/gh"
  chmod +x "$run/bin/scripted-agent.sh" "$run/bin/gh"

  # The seeded service: one commit on main, in a bare repository that
  # stands in for the remote.
  git init -q -b main "$run/seed"
  cp -R "$demo_dir/repo/." "$run/seed/"
  GIT_AUTHOR_DATE="2026-01-01T09:00:00Z" GIT_COMMITTER_DATE="2026-01-01T09:00:00Z" \
    git -C "$run/seed" add -A
  GIT_AUTHOR_DATE="2026-01-01T09:00:00Z" GIT_COMMITTER_DATE="2026-01-01T09:00:00Z" \
    git -C "$run/seed" commit -qm "cart: total a shopping cart"
  git clone -q --bare "$run/seed" "$run/remote.git"

  # The operator's checkout: WORKFLOW.md, the profile and the issue file.
  git clone -q "$run/remote.git" "$run/project"
  sed "s|__RUN__|$run|g" "$demo_dir/WORKFLOW.$1.md" >"$run/project/WORKFLOW.md"
  if [[ "$agent" != scripted ]]; then
    # The only difference from the scripted run: the agent (RECORDING.md).
    sed -i.bak -e "s|^\( *\)backend: custom\$|\1backend: $agent|" \
      -e "s|^\( *\)command: $run/bin/scripted-agent.sh\$|\1command: $agent|" "$run/project/WORKFLOW.md"
    rm -f "$run/project/WORKFLOW.md.bak"
  fi
  mkdir -p "$run/project/.itervox/agents" "$run/project/.itervox/issues"
  cp -R "$demo_dir/agents/fixer" "$run/project/.itervox/agents/"
  cp "$demo_dir/issues/DEMO-1.md" "$run/project/.itervox/issues/"
  printf 'WORKFLOW.md\n.itervox/\n' >>"$run/project/.git/info/exclude"
}

start_daemon() {
  (
    cd "$run/project"
    export HOME="$run/home" PATH="$run/bin:$PATH" DEMO_GH_STATE="$run/gh"
    if [[ -n "$dump" ]]; then export DEMO_AGENT_TRANSCRIPT="$dump/$part_name/agent"; fi
    export XDG_CONFIG_HOME="$run/home/.config" XDG_STATE_HOME="$run/home/.local/state"
    export XDG_CACHE_HOME="$run/home/.cache" XDG_DATA_HOME="$run/home/.local/share"
    export GH_CONFIG_DIR="$run/home/.config/gh"
    exec "$bin" -workflow "$run/project/WORKFLOW.md" -logs-dir "$run/logs"
  ) >"$run/daemon.log" 2>&1 </dev/null &
  pid=$!
  local start=$SECONDS f="$run/project/.itervox/dashboard_url"
  until [[ -s "$f" ]]; do
    kill -0 "$pid" 2>/dev/null || fail "the daemon exited before it served the dashboard"
    ((SECONDS - start < 60)) || fail "the daemon did not publish its dashboard URL within 60s"
    sleep 0.2
  done
  url=$(tr -d '\r\n' <"$f")
  url=${url%/}
  until api GET /state >/dev/null 2>&1; do
    kill -0 "$pid" 2>/dev/null || fail "the daemon exited before its API answered"
    sleep 0.2
  done
  echo "DEMO_DASHBOARD=$url"
}

# hold_end: with DEMO_RELEASE=<file>, keep the daemon up after a part's
# last beat until that file exists (the capture script's last screenshot).
hold_end() {
  [[ -n "${DEMO_RELEASE:-}" ]] || return 0
  local start=$SECONDS
  until [[ -e "$DEMO_RELEASE" ]]; do
    ((SECONDS - start < timeout)) || fail "timed out waiting for $DEMO_RELEASE"
    sleep 0.3
  done
  rm -f "$DEMO_RELEASE"
}

stop_daemon() {
  kill "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  pid=""
}

# start_issue: the red build is on the board; then the fixer takes it.
start_issue() {
  local start=$SECONDS
  until api GET /issues 2>/dev/null | jq -e '.[] | select(.identifier=="DEMO-1")' >/dev/null 2>&1; do
    ((SECONDS - start < 30)) || fail "DEMO-1 did not appear on the board"
    sleep 0.3
  done
  echo "DEMO_BEAT=red-build"
  say "DEMO-1 is on the board: the build is red"
  if [[ -n "${DEMO_GO:-}" ]]; then
    local waited=$SECONDS
    until [[ -e "$DEMO_GO" ]]; do
      ((SECONDS - waited < timeout)) || fail "timed out waiting for $DEMO_GO"
      sleep 0.2
    done
    rm -f "$DEMO_GO"
  else
    sleep "$start_delay"
  fi
  api POST /issues/DEMO-1/profile '{"profile":"fixer"}' >/dev/null
  api PATCH /issues/DEMO-1/state '{"state":"Todo"}' >/dev/null
  say "DEMO-1 moved to Todo with the fixer profile"
  wait_for "the agent to start" '[.running[]? | select(.identifier=="DEMO-1")] | length > 0'
  echo "DEMO_BEAT=agent-running"
}

# pr_opened: the PR link the worker commented on the issue.
pr_url() { cat "$run/gh"/pr-* 2>/dev/null | head -n 1; }

# capture NAME DIR SHOWN CMD...: with --dump, run CMD in DIR and save
# SHOWN (the command line as the replay shows it), the combined output and
# the exit status as $dump/<part>/NAME.{cmd,out,rc}. Prints the status.
capture() {
  local name=$1 dir=$2 shown=$3 rc=0
  shift 3
  local d="$dump/$part_name"
  mkdir -p "$d"
  printf '%s' "$shown" >"$d/$name.cmd"
  (cd "$dir" && "$@") >"$d/$name.out" 2>&1 </dev/null || rc=$?
  printf '%s' "$rc" >"$d/$name.rc"
  echo "$rc"
}

# branch_test: run the tests on the branch as it was pushed.
branch_test() {
  local dir="$run/check" rc=0
  [[ -d "$dir" ]] || git clone -q --branch itervox/demo-1 "$run/remote.git" "$dir"
  if [[ -n "$dump" ]]; then
    capture branch-test "$dir" "go test ./..." go test ./...
    return
  fi
  (cd "$dir" && go test ./... >"$run/branch-test.log" 2>&1) || rc=$?
  echo "$rc"
}

# worktree: the issue's worktree (where the agent works).
worktree() { dirname "$(find "$run/workspaces" -mindepth 2 -maxdepth 4 -name .git -print -quit)"; }

play_unguarded() {
  start_issue
  wait_issue_state "In Review"
  [[ -n "$(pr_url)" ]] || fail "no pull request was opened"
  echo "DEMO_BEAT=green"
  say "the run ended green: DEMO-1 is In Review, pull request $(pr_url)"
  local ci rc
  ci=$(git --git-dir="$run/remote.git" show itervox/demo-1:.github/workflows/ci.yml)
  grep -q 'continue-on-error: true' <<<"$ci" || fail "the pushed CI config has no continue-on-error"
  say "the pushed .github/workflows/ci.yml has: $(grep 'continue-on-error' <<<"$ci" | sed 's/^ *//')"
  if [[ -n "$dump" ]]; then
    git clone -q --branch itervox/demo-1 "$run/remote.git" "$run/check"
    rc=$(capture ci-job "$run/check" "ci-local.sh" sh "$demo_dir/ci-local.sh")
    say "the CI job, run locally with the workflow's own step, reports: $(tail -n 1 "$dump/$part_name/ci-job.out") (exit $rc)"
  fi
  rc=$(branch_test)
  say "go test ./... on the pushed branch exits $rc (nothing ran it before the PR opened)"
  [[ "$rc" != 0 ]] || fail "the pushed branch was expected to still fail its tests"
}

play_guarded() {
  start_issue
  wait_for "the push to be held as a pipeline change" \
    '[.inputRequired[]? | select(.identifier=="DEMO-1" and ((.guardrailLanePaths // []) | length > 0))] | length > 0'
  echo "DEMO_BEAT=held"
  say "the push is held in the human lane: $(api GET /state | jq -r '[.inputRequired[] | select(.identifier=="DEMO-1") | .guardrailLanePaths[]] | join(", ")')"
  if git --git-dir="$run/remote.git" rev-parse -q --verify refs/heads/itervox/demo-1 >/dev/null; then
    fail "the held push reached the remote"
  fi
  reply "Rejected: fix the code, not the pipeline. The CI config stays as it is."

  wait_for "the signed check to hold the run" \
    '[.inputRequired[]? | select(.identifier=="DEMO-1" and (.context | contains("Signed checks failed")))] | length > 0'
  echo "DEMO_BEAT=verify-failed"
  if [[ -n "$dump" && -n "$referee" ]]; then
    local wt rc
    wt=$(worktree)
    rc=$(capture referee-lanes "$wt" "referee lanes --base origin/main" "$referee" lanes --base origin/main)
    say "referee lanes on the held branch exits $rc"
  fi
  say "signed check: $(api GET /state | jq -r '.inputRequired[] | select(.identifier=="DEMO-1") | .context' | grep -m1 '^- ' | sed 's/^- //')"
  reply "Fix the bug in cart.go and leave CI alone; the test has to pass as written."

  # The fix changes only code: its push takes the auto lane (no hold).
  local start=$SECONDS
  until [[ "$(issue_state)" == "In Review" ]]; do
    kill -0 "$pid" 2>/dev/null || fail "the daemon exited while waiting for DEMO-1 to reach In Review"
    ((SECONDS - start < timeout)) || fail "timed out after ${timeout}s waiting for DEMO-1 to reach In Review"
    if api GET /state 2>/dev/null | jq -e '[.inputRequired[]? | select(.identifier=="DEMO-1" and ((.guardrailLanePaths // []) | length > 0))] | length > 0' >/dev/null; then
      fail "the fix push was held as a pipeline change"
    fi
    sleep 0.5
  done
  [[ -n "$(pr_url)" ]] || fail "no pull request was opened"
  echo "DEMO_BEAT=pr-opened"
  say "DEMO-1 is In Review, pull request $(pr_url)"
  if git --git-dir="$run/remote.git" diff --quiet main itervox/demo-1 -- .github; then
    say "the pushed branch leaves .github/ unchanged"
  else
    fail "the pushed branch changes .github/"
  fi
  local rc
  rc=$(branch_test)
  say "go test ./... on the pushed branch exits $rc"
  [[ "$rc" == 0 ]] || fail "the pushed branch was expected to pass its tests"
  say "signed check records (GET /api/v1/issues/DEMO-1/attestations):"
  api GET /issues/DEMO-1/attestations | jq -r '.. | objects | select(.kind? == "check") |
    "  \(.name // .check // "?")  exit \(.exit_code // .exitCode)  verified=\(.verified // "?")"' || true
  local f
  if [[ -n "$dump" && -n "$referee" ]]; then
    local last wt
    last=$(find "$run/project/.itervox/attestations/DEMO-1" -name '*.jsonl' | sort | tail -n 1)
    wt=$(worktree)
    rc=$(capture referee-verify "$run/project" \
      "referee verify .itervox/attestations/DEMO-1/$(basename "$last") --pubkey .itervox/attest.pub --tree ../${wt#"$run"/}" \
      "$referee" verify "$last" --pubkey .itervox/attest.pub --tree "$wt")
    say "referee verify on the passing run exits $rc"
  fi
  for f in "$run/project/.itervox/attestations/DEMO-1/"*.jsonl; do
    say "itervox attest verify $(basename "$f"):"
    (cd "$run/project" && "$bin" attest verify "$f") | sed 's/^/  /' || true
  done
}

# manual: the real-agent setup. Nothing is driven; the daemon runs until
# Ctrl-C.
manual() {
  until api GET /issues 2>/dev/null | jq -e '.[] | select(.identifier=="DEMO-1")' >/dev/null 2>&1; do
    kill -0 "$pid" 2>/dev/null || fail "the daemon exited before DEMO-1 appeared"
    sleep 0.3
  done
  api POST /issues/DEMO-1/profile '{"profile":"fixer"}' >/dev/null
  say "ready: open $url, then move DEMO-1 from Backlog to Todo to start $agent"
  say "project: $run/project (WORKFLOW.md, .itervox/issues/DEMO-1.md); remote: $run/remote.git"
  say "daemon log: $run/daemon.log; Ctrl-C stops the daemon and keeps the project"
  wait "$pid" || true
  pid=""
}

for p in "${parts[@]}"; do
  say "part: $p"
  part_name=$p
  setup "$p"
  if [[ -n "$dump" ]]; then
    if [[ -d "$dump/$p" ]]; then find "$dump/$p" -depth -delete; fi
    capture main-test "$run/seed" "go test ./..." go test ./... >/dev/null
  fi
  start_daemon
  if [[ "$agent" != scripted ]]; then
    manual
    exit 0
  fi
  "play_$p"
  hold_end
  stop_daemon
  echo "DEMO_RESULT=$p ok"
done
