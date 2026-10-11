#!/usr/bin/env bash
#
# #137 test for deploy/acceptance/self-hosted-run.sh. Run: make deploy-test.
# No network, no agent: gh, claude and itervox are stubs; the stub gh answers
# from files the test writes and logs every call.
#
# Proves:
#   - usage: no --issue, a missing GH_TOKEN or agent credential exit 2;
#   - the run labels only the test issue smoke:todo, starts the daemon on a
#     workflow whose states are the smoke labels, and exits 0 with the pull
#     request in result.md once the issue is smoke:done and a PR closes it;
#   - the daemon is stopped (SIGTERM) when the run ends;
#   - smoke:done without a pull request, and a daemon that exits, are failures
#     (exit 1) with the reason in result.md;
#   - GH_TOKEN never appears in the output or the files the run leaves.

# Stub bodies are single-quoted on purpose: they expand in the child process.
# `cond && pass || fail` is safe: pass only prints, so it never fails.
# shellcheck disable=SC2016,SC2015
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUN="$HERE/self-hosted-run.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

fails=0
pass() { printf 'ok   %s\n' "$*"; }
fail() { printf 'FAIL %s\n' "$*"; fails=$((fails + 1)); }

BIN="$WORK/bin"
mkdir -p "$BIN"
SECRET="ghp_fixture_secret_value_0123456789"

# gh: issue state, labels (one line consumed per poll from labels.txt), PR list.
cat > "$BIN/gh" <<'EOF'
#!/usr/bin/env bash
echo "gh $*" >> "$STUB_DIR/gh.log"
case "$1 $2" in
  "issue view")
    if [[ "$*" == *"-q .state"* ]]; then echo OPEN; exit 0; fi
    line=$(head -n1 "$STUB_DIR/labels.txt"); tail -n +2 "$STUB_DIR/labels.txt" > "$STUB_DIR/labels.next"; mv "$STUB_DIR/labels.next" "$STUB_DIR/labels.txt"
    [[ -n "$line" ]] || line=$(cat "$STUB_DIR/labels.last")
    echo "$line" > "$STUB_DIR/labels.last"; echo "$line" ;;
  "pr list") cat "$STUB_DIR/pr.txt" ;;
  *) exit 0 ;;
esac
EOF
printf '#!/usr/bin/env bash\nexit 0\n' > "$BIN/claude"
# itervox: --version, config show, or the daemon (sleeps until TERM unless
# STUB_DAEMON_EXIT is set).
cat > "$BIN/itervox" <<'EOF'
#!/usr/bin/env bash
case "$1" in
  --version) echo "itervox stub"; exit 0 ;;
  config) exit 0 ;;
esac
echo "$*" > "$STUB_DIR/daemon.args"
cp WORKFLOW.md "$STUB_DIR/daemon.workflow"
if [[ -n "${STUB_DAEMON_EXIT:-}" ]]; then exit 3; fi
trap 'echo term > "$STUB_DIR/daemon.term"; exit 0' TERM
while true; do sleep 0.1; done
EOF
chmod +x "$BIN"/*

# run <case> [args...]: runs the script with the stubs; output in $WORK/<case>.out.
run() {
  local name="$1"; shift
  local dir="$WORK/$name"
  mkdir -p "$dir/stub"
  : > "$dir/stub/gh.log"
  printf '%s\n' "${LABELS[@]}" > "$dir/stub/labels.txt"
  printf '%s' "$PR" > "$dir/stub/pr.txt"
  set +e
  STUB_DIR="$dir/stub" PATH="$BIN:$PATH" GH_TOKEN="$SECRET" ANTHROPIC_API_KEY=k \
    ITERVOX_SMOKE_POLL_SECONDS=0.2 "$@" bash "$RUN" --repo acme/widgets --issue 42 \
    --itervox "$BIN/itervox" --out "$dir/out" --timeout-min 1 > "$dir/run.out" 2>&1
  rc=$?
  set -e
  STUB="$dir/stub"; OUT="$dir/out"; LOG="$dir/run.out"
}

# ── usage ───────────────────────────────────────────────────────────────────
set +e
msg=$(bash "$RUN" --itervox "$BIN/itervox" 2>&1); rc=$?
set -e
[[ $rc -eq 2 && "$msg" == *"--issue <number> is required"* ]] && pass "no --issue: exit 2" || fail "no --issue: exit $rc, want 2"
set +e
msg=$(env -u GH_TOKEN PATH="$BIN:$PATH" ANTHROPIC_API_KEY=k bash "$RUN" --issue 1 --itervox "$BIN/itervox" --out "$WORK/u" 2>&1); rc=$?
set -e
[[ $rc -eq 2 && "$msg" == *"GH_TOKEN is not set"* ]] && pass "no GH_TOKEN: exit 2" || fail "no GH_TOKEN: exit $rc, want 2"
set +e
msg=$(env -u ANTHROPIC_API_KEY -u CLAUDE_CODE_OAUTH_TOKEN PATH="$BIN:$PATH" GH_TOKEN=x bash "$RUN" --issue 1 --itervox "$BIN/itervox" --out "$WORK/u" 2>&1); rc=$?
set -e
[[ $rc -eq 2 && "$msg" == *"ANTHROPIC_API_KEY or CLAUDE_CODE_OAUTH_TOKEN"* ]] && pass "no agent credential: exit 2" || fail "no agent credential: exit $rc, want 2"

# ── success ─────────────────────────────────────────────────────────────────
LABELS=("smoke:todo" "smoke:in-progress" "smoke:done")
PR="https://github.com/acme/widgets/pull/7"
run ok env
[[ $rc -eq 0 ]] && pass "done with a PR: exit 0" || { fail "done with a PR: exit $rc, want 0"; cat "$LOG"; }
grep -q "pull request: $PR" "$OUT/result.md" && pass "result.md names the PR" || fail "result.md lacks the PR"
grep -q "issue edit 42 --repo acme/widgets --add-label smoke:todo" "$STUB/gh.log" \
  && pass "only issue 42 is labelled smoke:todo" || fail "issue 42 was not labelled smoke:todo"
if grep "issue edit" "$STUB/gh.log" | grep -qv "issue edit 42 "; then fail "another issue was edited"; else pass "no other issue edited"; fi
grep -q 'active_states: \["smoke:todo", "smoke:in-progress"\]' "$STUB/daemon.workflow" \
  && grep -q 'completion_state: "smoke:done"' "$STUB/daemon.workflow" \
  && grep -q 'project_slug: acme/widgets' "$STUB/daemon.workflow" \
  && pass "the daemon runs on the smoke-label workflow" || fail "daemon workflow is not the smoke workflow"
grep -q -- "--logs-dir $OUT/logs" "$STUB/daemon.args" && pass "the daemon logs into <out>/logs" || fail "daemon args: $(cat "$STUB/daemon.args")"
[[ -f "$STUB/daemon.term" ]] && pass "the daemon is stopped with SIGTERM" || fail "the daemon was not sent SIGTERM"
if grep -rq "$SECRET" "$LOG" "$OUT" "$STUB/daemon.args"; then fail "GH_TOKEN leaked"; else pass "GH_TOKEN appears nowhere"; fi

# ── failures ────────────────────────────────────────────────────────────────
LABELS=("smoke:todo" "smoke:done")
PR=""
run nopr env
[[ $rc -eq 1 ]] && grep -q "without a pull request" "$OUT/result.md" \
  && pass "done without a PR: exit 1, reason recorded" || fail "done without a PR: exit $rc; $(cat "$OUT/result.md" 2>/dev/null)"

LABELS=("smoke:todo")
PR=""
run died env STUB_DAEMON_EXIT=1
[[ $rc -eq 1 ]] && grep -q "the daemon exited" "$OUT/result.md" \
  && pass "daemon exits: exit 1, reason recorded" || fail "daemon exits: exit $rc; $(cat "$OUT/result.md" 2>/dev/null)"

if [[ $fails -gt 0 ]]; then
  echo "self-hosted-run_test: $fails failure(s)"
  exit 1
fi
echo "self-hosted-run_test: PASS"
