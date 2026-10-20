#!/usr/bin/env bash
# check-no-bare-go.sh — CORE-008 panic-containment guard.
#
# Every production `go` statement under cmd/ and internal/ must either
#   (a) be a `go func(...) {` whose leading defers include
#       RecoverGoroutine(...) (one-shot task: recover, log, count, reconcile
#       via onPanic — internal/orchestrator/gosafe.go) or
#       failFastOnPanic(...) (long-lived loop: log through slog, restore the
#       terminal, re-panic — cmd/itervox/gosafe.go), or
#       recoverServerGoroutine(...) (internal/server's one-shot sibling of
#       RecoverGoroutine; server must not import orchestrator), or
#   (b) appear on the reasoned ALLOWLIST below, with the exact number of
#       occurrences expected in that file.
# goSafe(&o.<wg>, ...) launches are not `go` statements at their call sites;
# their single `go` lives in gosafe.go and satisfies (a).
#
# This guard covers panic containment only. WaitGroup tracking of
# event-loop goroutines is TestEventLoopGoroutinesAreWaitgroupTracked
# (internal/orchestrator/goroutine_convention_test.go); neither replaces the
# other.
#
# Exits 0 if clean, 1 on any violation.

set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd)
cd "$repo_root"

# path|statement prefix|max occurrences|reason
ALLOWLIST=$(cat <<'ALLOW'
cmd/itervox/main.go|go func() { orchDone <- orch.Run(ctx) }()|1|orchestrator event loop: never wrapped, fail-fast by design (CORE-008 invariant)
internal/orchestrator/automation.go|go o.runWorker(|1|runWorker defers its own recover (worker.go) and sends EventWorkerExited as its reconcile
internal/orchestrator/event_loop.go|go o.runWorker(|3|runWorker defers its own recover (worker.go) and sends EventWorkerExited as its reconcile
internal/orchestrator/dependency_refresh.go|go o.runDependencyRefresh(|1|runDependencyRefresh defers its own recover and still sends its result event
internal/depsanalysis/job.go|go m.execute(|1|execute recovers the analyzer run and records the job's terminal state
internal/logbuffer/diskwriter.go|go b.runWriter()|1|per-Buffer disk writer (M0-close fix-F): runWriter defers recoverWriter, which logs the panic with its stack, closes the writer and fails every queued/in-flight op so no caller waits forever; logbuffer must not import orchestrator
internal/agent/claude.go|go func() {|1|readLines stdout scanner: stdlib-only reads, no callbacks; file holds staged CORE-001 work
internal/agent/ssh.go|go func() {|1|remoteStdin payload writer (CORE-155): one io.WriteString into the ssh stdin pipe, no callbacks; joined by the caller after cmd.Wait, which closes that pipe and so unblocks it
internal/agent/testdata/fakesshd/main.go|go func() {|2|test-only fake sshd (built by internal/agent TestMain, never linked into itervox): the stdin relay and the stdout/stderr relays, each one io.Copy between pipes; the process exits when the remote command is done
internal/statusui/statusui.go|go func() {|2|bubbletea Program.Run catches panics and restores the terminal; the ctx->Quit forwarder has no failure mode; statusui must not import orchestrator
cmd/itervox/demo.go|go demoOpenWhenReady(|1|demoOpenWhenReady defers RecoverGoroutine itself; a panic only loses the browser opening
cmd/itervox/demo.go|go d.control(|1|demoRun.control defers RecoverGoroutine itself; a panic only stops the demo's stand-in operator
internal/tracker/github/client.go|go func(i int, it T) {|1|boundedDo fan-out joined by wg.Wait in the calling goroutine (event-loop tick); fail-fast like the event loop
ALLOW
)

files=$(find cmd internal -name '*.go' ! -name '*_test.go' -type f | sort)

# shellcheck disable=SC2086
violations=$(ALLOWLIST="$ALLOWLIST" awk '
BEGIN {
  n = split(ENVIRON["ALLOWLIST"], rows, "\n")
  for (i = 1; i <= n; i++) {
    if (rows[i] == "") continue
    split(rows[i], f, "|")
    key = f[1] SUBSEP f[2]
    allowMax[key] = f[3] + 0
    allowPath[key] = f[1]
    allowStmt[key] = f[2]
  }
}
FNR == 1 {
  # flush pending lookahead from previous file
  if (pending) { report(pendFile, pendLine, pendText) }
  pending = 0
}
function trim(s) { sub(/^[ \t]+/, "", s); sub(/[ \t]+$/, "", s); return s }
function report(file, line, text) { printf "%s:%d: %s\n", file, line, text }
{
  line = $0
  t = trim(line)
  if (pending) {
    if (t ~ /^defer[ \t]+(orchestrator\.)?(RecoverGoroutine|failFastOnPanic|recoverServerGoroutine)\(/) {
      pending = 0
    } else if (t == "" || t ~ /^\/\// || t ~ /^defer[ \t]/) {
      # keep scanning the leading defers / comments
    } else {
      report(pendFile, pendLine, pendText)
      pending = 0
    }
  }
  if (t ~ /^go[ \t]/) {
    for (key in allowPath) {
      if (FILENAME == allowPath[key] && index(t, allowStmt[key]) == 1) {
        seen[key]++
        if (seen[key] > allowMax[key]) {
          report(FILENAME, FNR, t " (allowlist exceeded: max " allowMax[key] ")")
        }
        next
      }
    }
    if (t ~ /^go[ \t]+func[ \t]*\(/ && t ~ /\{$/) {
      pending = 1; pendFile = FILENAME; pendLine = FNR; pendText = t
    } else {
      report(FILENAME, FNR, t)
    }
  }
}
END { if (pending) report(pendFile, pendLine, pendText) }
' $files)

if [ -n "$violations" ]; then
  echo "ERROR: go statement(s) without panic containment (CORE-008)." >&2
  echo "Defer orchestrator.RecoverGoroutine (one-shot, with an onPanic that sends" >&2
  echo "the task's terminal event) or failFastOnPanic (long-lived loop) at the top" >&2
  echo "of the goroutine, use goSafe, or add a reasoned ALLOWLIST entry in" >&2
  echo "scripts/check-no-bare-go.sh." >&2
  echo "" >&2
  echo "$violations" >&2
  exit 1
fi

echo "check-no-bare-go: clean (every production go statement is contained or allowlisted)"
