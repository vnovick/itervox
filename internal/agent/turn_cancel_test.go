package agent_test

// CORE-001: on a read error (idle read timeout, or a scanner error from an
// oversized line) RunTurn must cancel the turn context before cmd.Wait() so
// the subprocess (its whole process group) is killed promptly instead of
// running unobserved until the caller's context or the hard turn timeout
// eventually fires. These tests drive a real subprocess (a fake agent shell
// script) through the real ClaudeRunner/CodexRunner so they exercise the
// actual cmd.Cancel/WaitDelay wiring, not a mock.
//
// Fix round 1 additions (review findings):
//   - The process-group-death assertion now has a positive control: before
//     RunTurn is allowed to kill anything, the test asserts the fake agent
//     really is its own process group leader and really is alive, so a
//     `kill(-pid, 0)` == ESRCH after RunTurn returns is meaningful evidence
//     of a kill rather than a vacuous pass (e.g. if the command ever routed
//     through the login-shell branch instead of the direct-exec branch,
//     $$ would be a grandchild pid and the ESRCH check would pass for free).
//   - Two extra rows pin the FailureText carve-out for context.Canceled vs
//     context.DeadlineExceeded (see failure_text.go's resolveFailureText):
//     an externally-cancelled parent context must keep the pre-CORE-001
//     empty FailureText, while a turn_timeout_ms expiry must report a real
//     failure with deadline-mentioning text.
//
// Fix round 2 addition (finding G3 — see discoverPid's doc comment): pid
// discovery no longer depends on the fake agent executing any of its own
// instructions. A self-reported pid file (round 1) can still race a fully
// starved child under extreme concurrent load, because writing that file
// IS an instruction the child must be scheduled to run. discoverPid instead
// polls the OS process table, which is populated by fork()/exec() the
// instant cmd.Start() returns — before the child needs any CPU quantum of
// its own.
//
// Fix round 4 additions (finding G6 — the scanner_error rows were flaky under
// concurrent load, silently degrading into duplicates of the idle_read_timeout
// rows): the over-cap payload is pre-generated in Go at setup time rather than
// by a shell pipeline at run time; the fake agent's first exec is warmed up so
// its cost is paid outside the window that competes with readLines' idle timer
// (warmFakeAgent); the scanner_error rows park on a FIFO barrier so the
// process-group positive controls have something to observe even though the
// turn ends ~10ms after release (makeTurnCancelBarrier); and an attempt in
// which the child never reached the barrier is classified as inconclusive and
// retried rather than asserted on (schedulingMarkerPath). No timeout, budget
// or scanner buffer was raised.
import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent"
)

const (
	// turnCancelReadTimeoutMs is deliberately above the 300-500ms suggested
	// floor: under a loaded test machine (go test -race spawning many
	// subprocesses back to back across this package's suite) a fresh child
	// process can occasionally wait longer than 500ms for its first CPU
	// quantum. 1500ms keeps the suite fast (each row still completes in a
	// couple of seconds, not the multi-hour production default) while
	// giving realistic scheduling headroom.
	turnCancelReadTimeoutMs = 1500
	// turnCancelWaitDelay mirrors setProcessGroup's cmd.WaitDelay.
	turnCancelWaitDelay = 5 * time.Second
	// turnCancelBudgetSlack is the "+ 2s" headroom the acceptance criterion allows.
	turnCancelBudgetSlack = 2 * time.Second
	// turnCancelParentTimeout bounds the parent ctx passed to RunTurn. It must
	// be comfortably above turnCancelBudget() so that, if the fix regresses,
	// RunTurn only unblocks when THIS parent context expires (proving the
	// hang) rather than by coincidence within budget.
	turnCancelParentTimeout = 13 * time.Second
	// turnCancelPidDiscoveryTimeout bounds how long discoverPid polls the OS
	// process table, while the turn is still in flight, for the fake
	// agent's pid. Generous on purpose — it never weakens any elapsed-time
	// assertion, all of which are checked separately after RunTurn returns.
	turnCancelPidDiscoveryTimeout = 8 * time.Second
	// turnCancelExternalCancelDelay is how long the "parent context cancelled"
	// row waits before cancelling — long enough that pid discovery and the
	// positive liveness control complete well before cancellation, short
	// enough to keep the row fast.
	turnCancelExternalCancelDelay = 500 * time.Millisecond
	// turnCancelLargeReadTimeoutMs is used by the two new rows (parent-cancel
	// and turn-timeout-expiry) so the idle read timer never fires first —
	// only the mechanism under test (external cancel / hard turn deadline)
	// should be what unblocks RunTurn.
	turnCancelLargeReadTimeoutMs = 5000
	// turnCancelSmallTurnTimeoutMs is the hard turn deadline exercised by the
	// turn-timeout-expiry row. Matches turnCancelReadTimeoutMs's reasoning:
	// large enough to survive scheduling jitter under a loaded test machine,
	// small enough to keep the row fast relative to the multi-hour
	// production default.
	turnCancelSmallTurnTimeoutMs = 1500
)

// claudeInnocuousLine / codexInnocuousLine are the one-line, parser-valid,
// non-terminal stream-json events the fake agent emits right after
// starting (see writeFakeHangingAgent). A "system" event (Claude) and a
// "thread.started" event (Codex) both parse to EventSystem — they only
// stamp a session id (see ApplyEvent's EventSystem case) and never set
// Failed or end the turn, unlike a "result" / "turn.completed" /
// "turn.failed" event. Emitting this line resets readLines' idle timer
// exactly once, so the rows that are supposed to fail via a SECOND idle
// timeout (or a scanner error, or an external cancel/deadline) still do —
// see writeFakeHangingAgent's doc comment for why this line exists at all.
const (
	claudeInnocuousLine = `{"type":"system","session_id":"fake-warmup"}`
	codexInnocuousLine  = `{"type":"thread.started","thread_id":"fake-warmup"}`
)

// turnCancelBudget is the maximum time RunTurn may take to return once the
// fake agent stops producing output: read_timeout_ms + WaitDelay + 2s. The
// innocuous first line (see writeFakeHangingAgent) resets readLines' idle
// timer once near t=0, so the SECOND (real) idle timeout still fires at
// approximately read_timeout_ms after start — this budget is unchanged by
// that line's addition, only made slightly more conservative in practice
// since the first line's arrival is itself subject to scheduling.
func turnCancelBudget() time.Duration {
	return time.Duration(turnCancelReadTimeoutMs)*time.Millisecond + turnCancelWaitDelay + turnCancelBudgetSlack
}

// oversizedPayloadBytes is the size of the pre-generated over-cap payload's
// padding. It is just over readLines' documented 16 MiB hard line cap
// (claude.go: maxStreamLineBytes) — the one remaining stream READ-ERROR path
// after CORE-002 made oversized lines under the cap survivable. The payload
// is shaped as a terminal event on purpose: under CORE-002's fail-closed
// policy a terminal-shaped over-cap line is a read error like any other.
// Kept only 1 KiB over the cap: the less there is to push through the pipe,
// the less the row depends on the child getting sustained CPU time (CORE-001
// fix round 4, finding G6; CORE-002 fix round 1 moved the cap from 1 MiB).
const oversizedPayloadBytes = 16<<20 + 1024

// writeOversizedPayload materialises the over-cap payload as a file in dir,
// in Go, at test-setup time — before the fake agent process is ever started.
//
// Round 3 generated this payload at runtime inside the shell script
// (`head -c 2000000 /dev/zero | tr '\0' 'a'`). That pipeline is a workload:
// it spawns two more processes and streams 2MB through a pipe, and under
// concurrent load it can take longer than the row's idle window. When that
// happened the idle timer won the race, readLines returned
// "agent: read timeout after 1500ms idle", and the scanner_error row silently
// degraded into a duplicate of the idle_read_timeout row (finding G6).
// Pre-generating removes the generation cost from the racing window entirely:
// at run time the fake agent only has to `cat` bytes that already exist.
func writeOversizedPayload(t *testing.T, dir string) string {
	t.Helper()
	payload := filepath.Join(dir, "oversized-payload")
	// One terminal-shaped line whose string value alone exceeds the 16 MiB
	// cap. readLines fails closed as soon as the cap is crossed, so the
	// trailing newline is never needed to trigger the error; it is present so
	// the line is well-formed.
	var b bytes.Buffer
	b.WriteString(`{"type":"result","is_error":false,"result":"`)
	b.Write(bytes.Repeat([]byte("a"), oversizedPayloadBytes))
	b.WriteString("\"}\n")
	require.NoError(t, os.WriteFile(payload, b.Bytes(), 0o600))
	return payload
}

// writeFakeHangingAgent writes a shell script whose stdout ordering is chosen
// per row, then sleeps well past this suite's budget — so a passing test
// proves the caller killed it, not that it exited on its own.
//
// idle_read_timeout rows (oversizedLine == false):
//  1. emit ONE innocuous, parser-valid stream-json line (see
//     claudeInnocuousLine / codexInnocuousLine) to break the silence and reset
//     readLines' idle timer once;
//  2. sleep — the SECOND idle window is what must expire.
//
// The innocuous line must be one the real parser (ParseLine / ParseCodexLine)
// accepts and that does not set Failed or terminate the turn — a "result"
// (Claude) or "turn.completed"/"turn.failed" (Codex) event would end the
// turn as succeeded/failed and defeat the row under test.
//
// scanner_error rows (oversizedLine == true) invert that ordering, and this is
// load-bearing (finding G6):
//  1. block on the FIFO barrier (see makeTurnCancelBarrier) — no stdout, no
//     sleep, no CPU — until the test has observed the process alive;
//  2. emit the pre-generated over-cap payload as the FIRST and only stdout
//     write, immediately on release, so readLines' 16 MiB line cap is tripped by
//     construction rather than by winning a race against the idle timer;
//  3. sleep, which the turn never reaches.
//
// These rows deliberately emit no innocuous line at all — there is nothing for
// it to do, since the scanner errors out before any complete line is ever
// delivered, and emitting it first is exactly the ordering that let the idle
// timer win in round 3.
//
// Every shape begins with two things that are NOT stdout writes:
//   - a warm-up guard (see turnCancelWarmupArg / warmFakeAgent) that makes the
//     script exit immediately when invoked with the sentinel argument, so the
//     first-exec cost of a freshly written script can be paid before the turn
//     under test begins;
//   - `: > <marker>`, a shell builtin (no fork, no exec) recording that the
//     shell got that far — see schedulingMarkerPath and runTurnCancelRow.
//
// This no longer writes a self-reported pid file (round 1's approach): pid
// discovery is done externally via discoverPid, which reads the OS process
// table instead of depending on the child executing any of its own
// instructions — see discoverPid's doc comment for why (finding G3). The
// marker here is NOT a return to pid-file discovery: nothing waits on it, and
// no assertion depends on it appearing. It is read once, after the turn is
// over, purely to classify the attempt.
func writeFakeHangingAgent(t *testing.T, dir, innocuousLine string, oversizedLine bool) string {
	t.Helper()
	exe := filepath.Join(dir, "fake-agent")
	payload := ""
	if oversizedLine {
		payload = writeOversizedPayload(t, dir)
	}
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	// Warm-up mode: touch everything the real run will touch, write nothing to
	// stdout, and exit. RunTurn never passes this argument (see
	// buildDirectArgs), so only warmFakeAgent can reach this branch.
	b.WriteString("if [ \"$1\" = " + shellLiteral(turnCancelWarmupArg) + " ]; then\n")
	if payload != "" {
		b.WriteString("\tcat " + shellLiteral(payload) + " > /dev/null\n")
	}
	b.WriteString("\texit 0\n")
	b.WriteString("fi\n")
	if payload != "" {
		// Barrier: block (no stdout, no CPU, no sleep) until the test has
		// observed this process alive — see makeTurnCancelBarrier.
		b.WriteString("read itervox_barrier < " + shellLiteral(makeTurnCancelBarrier(t, dir)) + "\n")
	}
	b.WriteString(": > " + shellLiteral(schedulingMarkerPath(dir)) + "\n")
	if payload != "" {
		b.WriteString("cat " + shellLiteral(payload) + "\n")
	} else {
		b.WriteString("echo " + shellLiteral(innocuousLine) + "\n")
	}
	b.WriteString("sleep 30\n")
	require.NoError(t, os.WriteFile(exe, []byte(b.String()), 0o755))
	return exe
}

// turnCancelWarmupArg is the sentinel argv[1] that puts the fake agent into
// warm-up mode. It is deliberately unlike anything buildDirectArgs passes.
const turnCancelWarmupArg = "__itervox_turn_cancel_warmup__"

// makeTurnCancelBarrier creates the FIFO the scanner_error rows block on
// before writing their over-cap payload, and returns its path.
//
// Why the scanner_error rows need one, when no other row does: every other row
// keeps its fake agent alive for at least half a second by construction (a
// second idle window, an external cancel delay, a turn deadline), which is
// what gives discoverPid and the G1 positive controls
// (assertOwnProcessGroupLeader / assertProcessGroupAlive) something to
// observe. The scanner_error row is the opposite: its whole point is that the
// turn ends the instant the over-cap line lands. Measured post-warm-up, that
// is ~10ms from cmd.Start() to SIGKILL — far too narrow for an external
// `pgrep` poll to land in, and the round-4 warm-up fix made it narrower still
// (before the barrier existed, every scanner_error row failed with
// `pgrep ... never found the fake agent's pid within 8s`).
//
// Rather than trade one race for another, the child blocks on a FIFO read
// until the test has finished observing it. This is not a sleep and not a
// timing margin: `read x < fifo` parks the shell in the kernel with no stdout
// written and no CPU consumed, and it resumes exactly when
// releaseTurnCancelBarrier opens the write end — after the positive controls
// have run. The payload is still the first thing the fake agent writes to
// stdout, and it is written immediately on release.
func makeTurnCancelBarrier(t *testing.T, dir string) string {
	t.Helper()
	fifo := turnCancelBarrierPath(dir)
	require.NoError(t, syscall.Mkfifo(fifo, 0o600), "barrier fifo")
	return fifo
}

// turnCancelBarrierPath is the FIFO path for dir's fake agent.
func turnCancelBarrierPath(dir string) string {
	return filepath.Join(dir, "barrier.fifo")
}

// turnCancelBarrierReleaseTimeout bounds how long releaseTurnCancelBarrier
// waits for the fake agent to attach to the read end. Comfortably below
// turnCancelReadTimeoutMs so that a child which never arrives is classified as
// an inconclusive attempt rather than silently eating the whole idle window.
const turnCancelBarrierReleaseTimeout = 750 * time.Millisecond

// releaseTurnCancelBarrier unblocks the fake agent's `read ... < fifo` by
// opening the write end and writing a line.
//
// The open is non-blocking: opening a FIFO for writing with O_NONBLOCK fails
// with ENXIO until a reader has it open, so this polls instead of parking the
// test goroutine forever on a child that never made it to the barrier. Failing
// to release within the timeout is NOT an assertion failure — it means the
// child never got that far, which runTurnCancelRow classifies (via the absent
// scheduling marker) as an inconclusive attempt and retries.
func releaseTurnCancelBarrier(t *testing.T, fifo string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		f, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			_, writeErr := f.WriteString("go\n")
			closeErr := f.Close()
			require.NoError(t, writeErr, "releasing the barrier must succeed once the reader is attached")
			require.NoError(t, closeErr, "closing the barrier write end must succeed")
			return
		}
		if time.Now().After(deadline) {
			t.Logf("barrier was never opened by the fake agent within %s (last err: %v); the attempt will be discarded as inconclusive", timeout, err)
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// warmFakeAgent execs the fake agent once, in warm-up mode, and waits for it
// to exit — before the turn under test starts.
//
// This is what makes the scanner_error rows deterministic rather than
// timing-lucky (finding G6). The expensive part of the fake agent's first
// output is not generating or writing the payload; it is the FIRST exec of a
// freshly created script path. An out-of-tree probe of this exact script
// shape, run concurrently with a full `go test -race ./cmd/... ./internal/...`,
// measured time-from-cmd.Start()-to-1MiB-delivered as:
//
//	fresh script, no warm-up:  p50 579ms  p90 910ms  max 3.39s  (2/30 over 1500ms)
//	fresh script + read-back:  p50 602ms  p90 915ms  max 2.68s  (1/30 over 1500ms)
//	fresh script + warm-up:    p50 8.3ms  p90 10.9ms max 12.3ms (0/30 over 1500ms)
//
// Reading the files back inside the test process does NOT help, so this is not
// a page-cache effect — it is the exec of a never-yet-executed path. Paying it
// here moves it out of the window that competes with readLines' idle timer,
// leaving the measured window two orders of magnitude inside
// turnCancelReadTimeoutMs instead of hovering at the cliff. No timeout, budget
// or scanner buffer is raised to achieve this.
func warmFakeAgent(t *testing.T, fakeExe string) {
	t.Helper()
	require.NoError(t, exec.Command(fakeExe, turnCancelWarmupArg).Run(), "fake agent warm-up run must succeed")
}

// schedulingMarkerPath is the file the fake agent creates, via a shell
// builtin, as its very first action — before it writes anything to stdout.
//
// It distinguishes the two ways a scanner_error row can come back reporting
// an idle read timeout instead of the over-cap read error:
//
//   - marker ABSENT — the child never made it past the barrier within the
//     row's idle window, so it never wrote the payload. This is a host
//     scheduling property, not a property of the code under test, and the
//     attempt is inconclusive and retried. The warm-up (see warmFakeAgent)
//     makes this rare: across three full concurrent-load reproductions (60
//     scanner_error subtests) it did not occur once. It is kept as a backstop
//     because the underlying cost is unbounded — an out-of-tree probe of this
//     script shape, run against a full concurrent `go test -race ./cmd/...
//     ./internal/...`, measured a p50 time-to-first-output of 579ms with a MAX
//     of 3.39s when the first exec was NOT warmed up.
//
//   - marker PRESENT — the child ran past the barrier, and the very next thing
//     it did was write the over-cap payload. If the FailureText still reports
//     an idle timeout, the scanner path genuinely did not trip (e.g. the
//     scanner buffer was raised, or CORE-002 replaced bufio.Scanner with
//     bounded line handling) and the row must FAIL. This is what keeps the
//     wantFailureSubstr assertion in force instead of retrying a real
//     regression away: verified by raising the scanner buffer to 8MiB in an
//     out-of-tree copy, where all four scanner_error rows failed on the
//     substring assertion with zero retries.
func schedulingMarkerPath(dir string) string {
	return filepath.Join(dir, "fake-agent-scheduled")
}

// discoverPid polls the OS process table (via `pgrep -f <fakeExe>`) for the
// forked fake-agent process's pid, instead of relying on the child to
// self-report it (round 1's pid-file approach).
//
// Why: a self-reported pid file requires the child to be scheduled and run
// at least one instruction (the write). Under severe concurrent load — this
// suite's own -count=5 run alongside a full `go test -race ./cmd/...
// ./internal/...` in another process, reproducing finding G3 — a freshly
// forked child can go long enough without ANY CPU quantum that RunTurn's
// FIRST idle-timeout window (or the external-cancel/turn-timeout deadline
// in the two extra rows) can elapse before the child ever runs a single
// instruction, regardless of what that first instruction is. Reordering the
// script (emitting an innocuous line before anything else) fixes the
// SECOND-idle-timeout race this suite depends on for its normal rows, but
// does not by itself fix pid *discovery* under that total-starvation case,
// because discovery still depended on the same child executing code.
//
// pgrep instead reads the kernel's process table directly. A process is
// visible there — with its exec'd argv[0] already set to our fakeExe path —
// the instant fork()/exec() completes, which by definition has already
// happened by the time cmd.Start() returns inside RunTurn, independent of
// whether the OS has since given the child any CPU time to execute its own
// instructions. pgrep itself is still a freshly-spawned process subject to
// the same scheduling contention, but it only needs ONE opportunity to run,
// at ANY point while the target process still exists (from fork() through
// to cmd.Wait() reaping it) — a far wider window than the target's own
// narrow first-instruction race, and one this function's poll loop keeps
// retrying into for up to timeout.
func discoverPid(t *testing.T, fakeExe string, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		out, err := exec.Command("pgrep", "-f", fakeExe).Output()
		if err == nil {
			if fields := strings.Fields(string(out)); len(fields) > 0 {
				pid, convErr := strconv.Atoi(fields[0])
				require.NoError(t, convErr, "pgrep output must be a pid: %q", out)
				return pid
			}
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			t.Fatalf("pgrep -f %q never found the fake agent's pid within %s (last err: %v)", fakeExe, timeout, lastErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// assertOwnProcessGroupLeader confirms the fake agent's pid is its own
// process group leader (pgid == pid). setProcessGroup sets Setpgid: true on
// the direct-exec branch (claude.go / codex.go, when the command is an
// absolute path with no spaces — true for our t.TempDir() fake agent),
// making the discovered pid the actual Setpgid child. If RunTurn ever
// routed this command through the login-shell branch instead, the
// discovered pid could be a shell wrapper whose child is the real leader,
// and `kill(-pid, 0)` would return ESRCH immediately regardless of whether
// anything was ever killed — this assertion is what makes the later ESRCH
// assertion meaningful rather than vacuous.
func assertOwnProcessGroupLeader(t *testing.T, pid int) {
	t.Helper()
	pgid, err := syscall.Getpgid(pid)
	require.NoError(t, err, "process group lookup must succeed while the turn is in flight")
	require.Equal(t, pid, pgid, "fake agent must be its own process group leader (Setpgid) for -pid kill semantics to be meaningful")
}

// assertProcessGroupAlive is the positive control: it proves the process
// group actually exists and is signalable BEFORE RunTurn has had a chance
// to kill it, so the later "gone" assertion demonstrates a real kill.
func assertProcessGroupAlive(t *testing.T, pgid int) {
	t.Helper()
	require.NoError(t, syscall.Kill(-pgid, 0), "positive control: process group must be alive while the turn is still in flight")
	t.Logf("positive control: process group %d confirmed alive (kill -0 succeeded) while RunTurn was still in flight", pgid)
}

// assertProcessGroupGone polls syscall.Kill(-pgid, 0) until it reports ESRCH
// (no process left in the group) or fails the test after timeout. setProcessGroup
// makes the fake agent its own process group leader (Setpgid: true), so pgid
// equals its own pid, and this proves the WHOLE group (script + any children
// such as `sleep`) was killed — not just that RunTurn happened to return.
func assertProcessGroupGone(t *testing.T, pgid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		lastErr = syscall.Kill(-pgid, 0)
		if lastErr == syscall.ESRCH {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("process group %d still alive after %s (kill -0 err=%v)", pgid, timeout, lastErr)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// turnRunOutcome carries a RunTurn call's result off its goroutine so the
// test can perform the positive liveness control while the turn is still
// in flight, before waiting for RunTurn to return.
type turnRunOutcome struct {
	result agent.TurnResult
	err    error
}

// runTurnAsync starts runner.RunTurn against fakeExe in a goroutine and
// returns a channel that receives its outcome. Only the parameters this
// suite varies are exposed.
func runTurnAsync(runner agent.Runner, parentCtx context.Context, dir, fakeExe string, readTimeoutMs, turnTimeoutMs int) <-chan turnRunOutcome {
	ch := make(chan turnRunOutcome, 1)
	go func() {
		result, err := runner.RunTurn(
			parentCtx, slog.Default(), nil,
			nil, "hello", dir, fakeExe, "", "",
			readTimeoutMs, turnTimeoutMs,
			agent.PermissionBypass)
		ch <- turnRunOutcome{result, err}
	}()
	return ch
}

type turnCancelTurnTimeoutCase struct {
	name          string
	turnTimeoutMs int
}

var turnCancelTurnTimeouts = []turnCancelTurnTimeoutCase{
	{"turn_timeout_disabled", 0},
	{"turn_timeout_1h", 3600000},
}

type turnCancelFailureKindCase struct {
	name      string
	oversized bool
	// wantFailureSubstr is a stable substring of the FailureText this
	// specific failure kind must produce, so each row proves WHICH path it
	// took instead of only asserting "some failure occurred" — otherwise
	// the scanner_error row could silently degrade into a second copy of
	// idle_read_timeout (e.g. if the scanner buffer is raised, the fake
	// agent's oversized-line pipeline changes, or CORE-002 replaces the
	// Scanner with bounded line handling) and still pass (CORE-001 fix
	// round 3, finding G5).
	wantFailureSubstr string
}

var turnCancelFailureKinds = []turnCancelFailureKindCase{
	// readLines' idle-timeout error: fmt.Errorf("agent: read timeout after
	// %dms idle", readTimeoutMs) — assert on the stable prefix, not the
	// formatted duration.
	{"idle_read_timeout", false, "read timeout"},
	// readLines' fail-closed over-cap read error (claude.go:
	// errStreamLineTooLong, "agent: stream line exceeds 16 MiB cap"). Was
	// "bufio.Scanner: token too long" until CORE-002 replaced the 1 MiB
	// Scanner with the 16 MiB bounded reader; the row name is kept.
	{"scanner_error", true, "stream line exceeds 16 MiB cap"},
}

// turnCancelScannerAttempts bounds how many times a scanner_error row may be
// re-run after an inconclusive attempt — one in which the forked fake agent
// never got a CPU quantum at all, so it never wrote the over-cap payload (see
// schedulingMarkerPath). Only that specific, externally-observable condition
// is retried; an attempt in which the child DID run is always asserted on, so
// a genuine regression fails on its first attempt rather than being retried
// away. Four attempts is a wide margin: the measured per-attempt probability
// of a starved child is a few percent even under a full concurrent
// `go test -race ./cmd/... ./internal/...`.
const turnCancelScannerAttempts = 4

// turnCancelRowAttempt is the outcome of one execution of a table row.
type turnCancelRowAttempt struct {
	outcome turnRunOutcome
	elapsed time.Duration
	// scheduled reports whether the fake agent's scheduling marker appeared,
	// i.e. whether the child ran at all before RunTurn killed it.
	scheduled bool
}

// runTurnCancelRow executes one attempt of a turnCancelTurnTimeouts ×
// turnCancelFailureKinds row against the given runner, including the
// in-flight process-group positive controls and the post-return
// process-group-death check. It performs no outcome assertions — the caller
// does that, once it has an attempt worth asserting on.
func runTurnCancelRow(t *testing.T, runner agent.Runner, innocuousLine string, fk turnCancelFailureKindCase, turnTimeoutMs int) turnCancelRowAttempt {
	t.Helper()
	dir := t.TempDir()
	fakeExe := writeFakeHangingAgent(t, dir, innocuousLine, fk.oversized)
	warmFakeAgent(t, fakeExe)

	parentCtx, cancelParent := context.WithTimeout(context.Background(), turnCancelParentTimeout)
	defer cancelParent()

	start := time.Now()
	outcomeCh := runTurnAsync(runner, parentCtx, dir, fakeExe, turnCancelReadTimeoutMs, turnTimeoutMs)

	// Positive control: the fake agent must be observed alive, and as its own
	// process group leader, while RunTurn is still in flight — otherwise the
	// later "gone" check would be vacuous.
	pid := discoverPid(t, fakeExe, turnCancelPidDiscoveryTimeout)
	assertOwnProcessGroupLeader(t, pid)
	assertProcessGroupAlive(t, pid)

	// The scanner_error rows park at a FIFO barrier until this point, so the
	// positive controls above had something to observe; releasing it lets the
	// fake agent write its over-cap payload (see makeTurnCancelBarrier).
	if fk.oversized {
		releaseTurnCancelBarrier(t, turnCancelBarrierPath(dir), turnCancelBarrierReleaseTimeout)
	}

	var outcome turnRunOutcome
	select {
	case outcome = <-outcomeCh:
	case <-time.After(turnCancelParentTimeout + 2*time.Second):
		t.Fatal("RunTurn did not return within the parent timeout + 2s")
	}
	elapsed := time.Since(start)

	assertProcessGroupGone(t, pid, turnCancelWaitDelay+2*time.Second)

	_, statErr := os.Stat(schedulingMarkerPath(dir))
	return turnCancelRowAttempt{outcome: outcome, elapsed: elapsed, scheduled: statErr == nil}
}

// assertTurnCancelRow runs one table row and asserts the CORE-001 acceptance
// criteria on it: RunTurn returns within read_timeout_ms + WaitDelay + 2s,
// classifies the turn as failed, and reports a FailureText that identifies
// which failure path was taken.
//
// A scanner_error row whose fake agent never got CPU (see
// schedulingMarkerPath) produced no over-cap line for bufio.Scanner to choke
// on, so there is nothing for this row to assert about the scanner path; that
// attempt is discarded and re-run. Every attempt in which the child DID run is
// asserted on unconditionally.
func assertTurnCancelRow(t *testing.T, runner agent.Runner, innocuousLine string, fk turnCancelFailureKindCase, turnTimeoutMs int) {
	t.Helper()
	att := runTurnCancelRow(t, runner, innocuousLine, fk, turnTimeoutMs)
	for i := 2; fk.oversized && !att.scheduled; i++ {
		if i > turnCancelScannerAttempts {
			t.Fatalf("the fake agent never got a CPU quantum within %dms in %d attempts; this host is too loaded to exercise the scanner path at all",
				turnCancelReadTimeoutMs, turnCancelScannerAttempts)
		}
		t.Logf("attempt %d/%d discarded: the fake agent was never scheduled, so it never wrote the over-cap payload (host starvation, not a code defect); retrying",
			i-1, turnCancelScannerAttempts)
		att = runTurnCancelRow(t, runner, innocuousLine, fk, turnTimeoutMs)
	}

	require.Error(t, att.outcome.err, "a read error must be returned")
	assert.LessOrEqualf(t, att.elapsed, turnCancelBudget(),
		"RunTurn must return within read_timeout_ms + WaitDelay + 2s (%s); took %s", turnCancelBudget(), att.elapsed)
	assert.True(t, att.outcome.result.Failed, "a read error must classify the turn as failed")
	assert.NotEmpty(t, att.outcome.result.FailureText, "a read error must produce a non-empty FailureText (worker.go:577 must not classify this as a clean session end)")
	assert.Containsf(t, att.outcome.result.FailureText, fk.wantFailureSubstr,
		"FailureText must identify the %s failure path (not merely be non-empty); got %q", fk.name, att.outcome.result.FailureText)
}

// TestRunTurnCancelsOnReadTimeout is the named Claude test from the CORE-001
// acceptance criteria.
func TestRunTurnCancelsOnReadTimeout(t *testing.T) {
	for _, tt := range turnCancelTurnTimeouts {
		for _, fk := range turnCancelFailureKinds {
			t.Run(tt.name+"/"+fk.name, func(t *testing.T) {
				assertTurnCancelRow(t, agent.NewClaudeRunner(), claudeInnocuousLine, fk, tt.turnTimeoutMs)
			})
		}
	}

	t.Run("parent_context_cancelled_keeps_empty_failure_text", func(t *testing.T) {
		dir := t.TempDir()
		fakeExe := writeFakeHangingAgent(t, dir, claudeInnocuousLine, false)
		warmFakeAgent(t, fakeExe)

		parentCtx, cancelParent := context.WithCancel(context.Background())
		defer cancelParent()
		go func() {
			time.Sleep(turnCancelExternalCancelDelay)
			cancelParent()
		}()

		start := time.Now()
		outcomeCh := runTurnAsync(agent.NewClaudeRunner(), parentCtx, dir, fakeExe, turnCancelLargeReadTimeoutMs, 0)

		pid := discoverPid(t, fakeExe, turnCancelPidDiscoveryTimeout)
		assertOwnProcessGroupLeader(t, pid)
		assertProcessGroupAlive(t, pid)

		var outcome turnRunOutcome
		budget := turnCancelExternalCancelDelay + turnCancelWaitDelay + turnCancelBudgetSlack
		select {
		case outcome = <-outcomeCh:
		case <-time.After(budget + 3*time.Second):
			t.Fatal("RunTurn did not return after the parent context was cancelled")
		}
		elapsed := time.Since(start)

		require.Error(t, outcome.err, "a cancelled parent context must surface as an error")
		assert.True(t, errors.Is(outcome.err, context.Canceled), "expected context.Canceled, got %v", outcome.err)
		assert.LessOrEqualf(t, elapsed, budget,
			"RunTurn must return promptly after the parent context is cancelled (%s); took %s", budget, elapsed)
		assert.True(t, outcome.result.Failed, "a cancelled turn is still classified as failed")
		assert.Empty(t, outcome.result.FailureText, "operator pause / daemon shutdown must keep the pre-CORE-001 behavior: FailureText stays empty for context.Canceled")

		assertProcessGroupGone(t, pid, turnCancelWaitDelay+2*time.Second)
	})

	t.Run("turn_timeout_expiry_reports_failure_with_deadline_text", func(t *testing.T) {
		dir := t.TempDir()
		fakeExe := writeFakeHangingAgent(t, dir, claudeInnocuousLine, false)
		warmFakeAgent(t, fakeExe)

		parentCtx, cancelParent := context.WithTimeout(context.Background(), turnCancelParentTimeout)
		defer cancelParent()

		start := time.Now()
		outcomeCh := runTurnAsync(agent.NewClaudeRunner(), parentCtx, dir, fakeExe, turnCancelLargeReadTimeoutMs, turnCancelSmallTurnTimeoutMs)

		pid := discoverPid(t, fakeExe, turnCancelPidDiscoveryTimeout)
		assertOwnProcessGroupLeader(t, pid)
		assertProcessGroupAlive(t, pid)

		var outcome turnRunOutcome
		budget := time.Duration(turnCancelSmallTurnTimeoutMs)*time.Millisecond + turnCancelWaitDelay + turnCancelBudgetSlack
		select {
		case outcome = <-outcomeCh:
		case <-time.After(budget + 3*time.Second):
			t.Fatal("RunTurn did not return after turn_timeout_ms expired")
		}
		elapsed := time.Since(start)

		require.Error(t, outcome.err, "a turn-timeout expiry must surface as an error")
		assert.True(t, errors.Is(outcome.err, context.DeadlineExceeded), "expected context.DeadlineExceeded, got %v", outcome.err)
		assert.LessOrEqualf(t, elapsed, budget,
			"RunTurn must return within turn_timeout_ms + WaitDelay + 2s (%s); took %s", budget, elapsed)
		assert.True(t, outcome.result.Failed, "a turn-timeout expiry must classify the turn as failed")
		assert.NotEmpty(t, outcome.result.FailureText, "a turn-timeout expiry must not be misreported as a clean session end")
		assert.Contains(t, outcome.result.FailureText, "deadline", "FailureText should mention the deadline that was exceeded")

		assertProcessGroupGone(t, pid, turnCancelWaitDelay+2*time.Second)
	})
}

// TestCodexRunTurnCancelsOnReadTimeout is the named Codex test from the
// CORE-001 acceptance criteria — same table, same assertions, CodexRunner.
func TestCodexRunTurnCancelsOnReadTimeout(t *testing.T) {
	for _, tt := range turnCancelTurnTimeouts {
		for _, fk := range turnCancelFailureKinds {
			t.Run(tt.name+"/"+fk.name, func(t *testing.T) {
				assertTurnCancelRow(t, agent.NewCodexRunner(), codexInnocuousLine, fk, tt.turnTimeoutMs)
			})
		}
	}

	t.Run("parent_context_cancelled_keeps_empty_failure_text", func(t *testing.T) {
		dir := t.TempDir()
		fakeExe := writeFakeHangingAgent(t, dir, codexInnocuousLine, false)
		warmFakeAgent(t, fakeExe)

		parentCtx, cancelParent := context.WithCancel(context.Background())
		defer cancelParent()
		go func() {
			time.Sleep(turnCancelExternalCancelDelay)
			cancelParent()
		}()

		start := time.Now()
		outcomeCh := runTurnAsync(agent.NewCodexRunner(), parentCtx, dir, fakeExe, turnCancelLargeReadTimeoutMs, 0)

		pid := discoverPid(t, fakeExe, turnCancelPidDiscoveryTimeout)
		assertOwnProcessGroupLeader(t, pid)
		assertProcessGroupAlive(t, pid)

		var outcome turnRunOutcome
		budget := turnCancelExternalCancelDelay + turnCancelWaitDelay + turnCancelBudgetSlack
		select {
		case outcome = <-outcomeCh:
		case <-time.After(budget + 3*time.Second):
			t.Fatal("RunTurn did not return after the parent context was cancelled")
		}
		elapsed := time.Since(start)

		require.Error(t, outcome.err, "a cancelled parent context must surface as an error")
		assert.True(t, errors.Is(outcome.err, context.Canceled), "expected context.Canceled, got %v", outcome.err)
		assert.LessOrEqualf(t, elapsed, budget,
			"RunTurn must return promptly after the parent context is cancelled (%s); took %s", budget, elapsed)
		assert.True(t, outcome.result.Failed, "a cancelled turn is still classified as failed")
		assert.Empty(t, outcome.result.FailureText, "operator pause / daemon shutdown must keep the pre-CORE-001 behavior: FailureText stays empty for context.Canceled")

		assertProcessGroupGone(t, pid, turnCancelWaitDelay+2*time.Second)
	})

	t.Run("turn_timeout_expiry_reports_failure_with_deadline_text", func(t *testing.T) {
		dir := t.TempDir()
		fakeExe := writeFakeHangingAgent(t, dir, codexInnocuousLine, false)
		warmFakeAgent(t, fakeExe)

		parentCtx, cancelParent := context.WithTimeout(context.Background(), turnCancelParentTimeout)
		defer cancelParent()

		start := time.Now()
		outcomeCh := runTurnAsync(agent.NewCodexRunner(), parentCtx, dir, fakeExe, turnCancelLargeReadTimeoutMs, turnCancelSmallTurnTimeoutMs)

		pid := discoverPid(t, fakeExe, turnCancelPidDiscoveryTimeout)
		assertOwnProcessGroupLeader(t, pid)
		assertProcessGroupAlive(t, pid)

		var outcome turnRunOutcome
		budget := time.Duration(turnCancelSmallTurnTimeoutMs)*time.Millisecond + turnCancelWaitDelay + turnCancelBudgetSlack
		select {
		case outcome = <-outcomeCh:
		case <-time.After(budget + 3*time.Second):
			t.Fatal("RunTurn did not return after turn_timeout_ms expired")
		}
		elapsed := time.Since(start)

		require.Error(t, outcome.err, "a turn-timeout expiry must surface as an error")
		assert.True(t, errors.Is(outcome.err, context.DeadlineExceeded), "expected context.DeadlineExceeded, got %v", outcome.err)
		assert.LessOrEqualf(t, elapsed, budget,
			"RunTurn must return within turn_timeout_ms + WaitDelay + 2s (%s); took %s", budget, elapsed)
		assert.True(t, outcome.result.Failed, "a turn-timeout expiry must classify the turn as failed")
		assert.NotEmpty(t, outcome.result.FailureText, "a turn-timeout expiry must not be misreported as a clean session end")
		assert.Contains(t, outcome.result.FailureText, "deadline", "FailureText should mention the deadline that was exceeded")

		assertProcessGroupGone(t, pid, turnCancelWaitDelay+2*time.Second)
	})
}
