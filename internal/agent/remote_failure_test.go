package agent_test

// M1-B2 regression tests for the SSH-hosted agent and sublog paths:
//
//   - CORE-139: a remote agent that exits non-zero must be reported as a
//     failed turn even when its stdout is piped through `tee` into the
//     remote session log.
//   - CORE-126: an ssh transport failure (unreachable host, auth refusal)
//     during a sublog fetch must surface as an error, not as "no logs".
//   - CORE-135: a sublog fetch that abandons a malformed tar stream must not
//     block in cmd.Wait() until the fetch's context expires.
//   - CORE-127: a per-file read error on the local sublog path must keep the
//     entries already parsed from that file and surface the failure.
//   - CORE-028: a child that floods stderr must yield a bounded FailureText
//     that keeps the tail of the stream.
//
// Every SSH case substitutes a fake `ssh` on PATH; no network is touched.

import (
	"context"
	"fmt"
	"log/slog"
	"os"
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

// writeCrashingAgent writes a fake agent that emits one parser-valid,
// non-terminal event, a diagnostic on stderr, and exits 7 — the shape of a
// real agent dying of an auth failure or a panic without a terminal event.
func writeCrashingAgent(t *testing.T, dir, name, firstLine string) string {
	t.Helper()
	exe := filepath.Join(dir, name)
	script := fmt.Sprintf("#!/bin/sh\necho '%s'\necho 'fatal: remote agent crashed' >&2\nexit 7\n", firstLine)
	require.NoError(t, os.WriteFile(exe, []byte(script), 0o755))
	return exe
}

// TestSSHTurnReportsRemoteAgentCrash is CORE-139's reproduction: the Codex
// SSH path pipes the agent into `tee <logfile>` whenever a log dir is set,
// and a bash pipeline's status is its last command's, so a crashed remote
// agent used to come back as ssh exit 0 and a successful 0-token turn. Every
// row runs through the faithful fake ssh (ssh_fake_test.go) under each
// available remote login shell, because the remote command is re-parsed by
// that shell (fix round 1, C1).
func TestSSHTurnReportsRemoteAgentCrash(t *testing.T) {
	cases := []struct {
		name      string
		runner    agent.Runner
		exeName   string
		firstLine string
		withLog   bool
	}{
		{"codex_with_log_dir_tee_pipeline", agent.NewCodexRunner(), "codex", `{"type":"thread.started","thread_id":"t1"}`, true},
		{"codex_without_log_dir", agent.NewCodexRunner(), "codex", `{"type":"thread.started","thread_id":"t1"}`, false},
		{"claude_with_log_dir", agent.NewClaudeRunner(), "claude", `{"type":"system","session_id":"s1"}`, true},
	}
	for _, shell := range availableLoginShells() {
		for _, tc := range cases {
			t.Run(shellLabel(shell)+"/"+tc.name, func(t *testing.T) {
				runRemoteCrashRow(t, shell, tc.runner, tc.exeName, tc.firstLine, tc.withLog)
			})
		}
	}
}

func runRemoteCrashRow(t *testing.T, shell string, runner agent.Runner, exeName, firstLine string, withLog bool) {
	installFaithfulFakeSSH(t, shell)
	dir := t.TempDir()
	exe := writeCrashingAgent(t, dir, exeName, firstLine)
	logDir := ""
	if withLog {
		logDir = filepath.Join(dir, "logs")
	}
	result, _ := runner.RunTurn(
		context.Background(), slog.Default(), nil,
		nil, "hi", dir, exe, "worker.example.test", logDir,
		30000, 60000,
		agent.PermissionBypass)

	assert.True(t, result.Failed, "a remote agent that exited 7 must be reported as a failed turn")
	assert.Contains(t, result.FailureText, "remote agent crashed",
		"the remote agent's stderr must reach FailureText")
	if withLog && exeName == "codex" {
		// The tee still captured the transcript: pipefail changes the
		// status only, not what reaches the session log.
		files, err := filepath.Glob(filepath.Join(logDir, "codex-*.jsonl"))
		require.NoError(t, err)
		require.Len(t, files, 1, "the remote session log must still be written")
		got, err := os.ReadFile(files[0])
		require.NoError(t, err)
		assert.Contains(t, string(got), "thread.started")
	}
}

// TestSSHSublogFetchReportsTransportFailure is CORE-126's reproduction: ssh
// exiting 255 with a diagnostic on stderr (the shape of an unresolvable host
// or a BatchMode auth refusal) used to come back as (nil, nil).
func TestSSHSublogFetchReportsTransportFailure(t *testing.T) {
	installFakeSSHTransport(t, "echo 'ssh: Could not resolve hostname nope.invalid: nodename nor servname provided' >&2\nexit 255\n")

	entries, err := agent.SSHSublogFetcher{Host: "nope.invalid"}.FetchSubLogs(context.Background(), "/remote/logs")
	require.Error(t, err, "an ssh transport failure must surface as an error, not as an empty log")
	assert.Contains(t, err.Error(), "Could not resolve hostname", "ssh's own diagnostic must be in the error")
	assert.Empty(t, entries)
}

// TestSSHSublogFetchHappyPathAndAbsentDir is the control for the fetch
// fixes: a reachable host with logs returns them with a nil error, and an
// absent remote directory is still "no logs", not an error.
func TestSSHSublogFetchHappyPathAndAbsentDir(t *testing.T) {
	installFaithfulFakeSSH(t, "")

	dir := t.TempDir()
	claude := `{"type":"assistant","message":{"content":[{"type":"text","text":"hello over ssh"}]}}` + "\n"
	codex := `{"type":"item.completed","item":{"id":"i1","type":"agent_message","text":"codex over ssh"}}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sess-a.jsonl"), []byte(claude), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "codex-1.jsonl"), []byte(codex), 0o644))

	entries, err := agent.SSHSublogFetcher{Host: "worker.example.test"}.FetchSubLogs(context.Background(), dir)
	require.NoError(t, err)
	var msgs []string
	for _, e := range entries {
		msgs = append(msgs, e.SessionID+":"+e.Message)
	}
	assert.ElementsMatch(t, []string{"sess-a:hello over ssh", "codex-1:codex over ssh"}, msgs)

	entries, err = agent.SSHSublogFetcher{Host: "worker.example.test"}.FetchSubLogs(context.Background(), filepath.Join(dir, "absent"))
	require.NoError(t, err, "an absent remote log dir is not a failure")
	assert.Empty(t, entries)
}

// TestSSHSublogFetchDrainsAbandonedStream is CORE-135's reproduction: the
// fake remote sends a malformed tar header followed by 4 MiB (far more than
// a pipe buffer). The parser abandons the stream at the bad header; unless
// the rest is drained, ssh blocks writing and cmd.Wait() blocks on ssh until
// the fetch's context expires (10s here, 30s in production).
func TestSSHSublogFetchDrainsAbandonedStream(t *testing.T) {
	installFakeSSHTransport(t, "printf 'this is not a tar header'\nhead -c 4194304 /dev/zero | tr '\\0' 'x'\nexit 0\n")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	_, _ = agent.SSHSublogFetcher{Host: "worker.example.test"}.FetchSubLogs(ctx, "/remote/logs")
	elapsed := time.Since(start)
	assert.Less(t, elapsed, 5*time.Second,
		"an abandoned tar stream must be drained so cmd.Wait() returns promptly; took %s", elapsed)
}

// TestLocalSublogFetchKeepsPartialEntriesOnReadError is CORE-127's
// reproduction: a line over the 1 MiB scanner cap makes the file's scanner
// fail after two good entries. Those entries must survive, and the failure
// must be visible in the returned timeline rather than silently dropped.
func TestLocalSublogFetchKeepsPartialEntriesOnReadError(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString(`{"type":"assistant","message":{"content":[{"type":"text","text":"first"}]}}` + "\n")
	b.WriteString(`{"type":"assistant","message":{"content":[{"type":"text","text":"second"}]}}` + "\n")
	b.WriteString(`{"type":"assistant","message":{"content":[{"type":"text","text":"` + strings.Repeat("y", 2<<20) + `"}]}}` + "\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sess-big.jsonl"), []byte(b.String()), 0o644))
	good := `{"type":"assistant","message":{"content":[{"type":"text","text":"other file"}]}}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sess-ok.jsonl"), []byte(good), 0o644))

	entries, err := agent.LocalSublogFetcher{}.FetchSubLogs(context.Background(), dir)
	require.NoError(t, err)

	var texts []string
	var readErrEntry *string
	for _, e := range entries {
		switch e.Event {
		case "text":
			texts = append(texts, e.SessionID+":"+e.Message)
		case "error":
			if e.SessionID == "sess-big" {
				m := e.Message
				readErrEntry = &m
			}
		}
	}
	assert.Equal(t, []string{"sess-big:first", "sess-big:second", "sess-ok:other file"}, texts,
		"entries parsed before the read error must be kept, and other files must still load")
	require.NotNil(t, readErrEntry, "the per-file read failure must be surfaced in the timeline")
	assert.Contains(t, *readErrEntry, "token too long")
}

// TestRunnerStderrCaptureIsTailBounded is CORE-028's end-to-end acceptance:
// a child that writes more than 1 MiB to stderr and then fails yields a
// FailureText of at most 64 KiB plus a truncation marker, with the tail of
// the stderr stream preserved.
func TestRunnerStderrCaptureIsTailBounded(t *testing.T) {
	const tailSentinel = "FINAL-STDERR-LINE-THE-ROOT-CAUSE"
	cases := []struct {
		name      string
		runner    agent.Runner
		firstLine string
	}{
		{"claude", agent.NewClaudeRunner(), `{"type":"system","session_id":"s1"}`},
		{"codex", agent.NewCodexRunner(), `{"type":"thread.started","thread_id":"t1"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			exe := filepath.Join(dir, "agent")
			// 2 MiB of stderr noise, then the line that actually matters.
			script := fmt.Sprintf("#!/bin/sh\necho '%s'\nhead -c 2097152 /dev/zero | tr '\\0' 'e' >&2\necho >&2\necho '%s' >&2\nexit 3\n",
				tc.firstLine, tailSentinel)
			require.NoError(t, os.WriteFile(exe, []byte(script), 0o755))

			result, _ := tc.runner.RunTurn(
				context.Background(), slog.Default(), nil,
				nil, "hi", dir, exe, "", "",
				30000, 60000,
				agent.PermissionBypass)

			require.True(t, result.Failed)
			assert.LessOrEqual(t, len(result.FailureText), 64<<10+len(agent.FailureTextTruncationMarker),
				"FailureText must be bounded to 64 KiB plus the truncation marker; got %d bytes", len(result.FailureText))
			assert.Contains(t, result.FailureText, agent.FailureTextTruncationMarker, "a truncated FailureText must say so")
			assert.True(t, strings.HasSuffix(result.FailureText, tailSentinel),
				"the tail of stderr (the root cause) must be preserved")
		})
	}
}

// TestReadTimeoutTurnFoldsStderrIntoFailureText pins the agent half of
// CORE-029, which CORE-001's resolveFailureText already delivers: limit text
// printed only on stderr during a turn that then ends by idle read timeout
// reaches FailureText (alongside the read error's own text), so the runner
// never hands the worker an empty FailureText for a read-error turn.
func TestReadTimeoutTurnFoldsStderrIntoFailureText(t *testing.T) {
	cases := []struct {
		name      string
		runner    agent.Runner
		firstLine string
	}{
		{"claude", agent.NewClaudeRunner(), `{"type":"system","session_id":"s1"}`},
		{"codex", agent.NewCodexRunner(), `{"type":"thread.started","thread_id":"t1"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			exe := filepath.Join(dir, "agent")
			script := fmt.Sprintf("#!/bin/sh\necho '%s'\necho 'Claude AI usage limit reached|1700000000' >&2\nexec /bin/sleep 30\n", tc.firstLine)
			require.NoError(t, os.WriteFile(exe, []byte(script), 0o755))

			result, err := tc.runner.RunTurn(
				context.Background(), slog.Default(), nil,
				nil, "hi", dir, exe, "", "",
				1500, 0,
				agent.PermissionBypass)

			require.Error(t, err, "an idle read timeout returns the read error")
			assert.True(t, result.Failed)
			assert.Contains(t, result.FailureText, "usage limit reached", "stderr-only limit text must reach FailureText on the read-error path")
			assert.Contains(t, result.FailureText, err.Error(), "the read error's own text leads FailureText")
		})
	}
}

// TestSSHSublogFetchBoundedWhenDescendantHoldsStderr pins the bound added
// with CORE-126's stderr capture: once cmd.Stderr is a non-file writer,
// cmd.Wait also waits for its copy goroutine, so a descendant of ssh that
// outlives it while holding the stderr pipe (a ProxyCommand, say) used to be
// able to hold the fetch open until that descendant exited. The ssh command
// now carries procgroup's WaitDelay, so the fetch returns within it.
func TestSSHSublogFetchBoundedWhenDescendantHoldsStderr(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "lingerer.pid")
	installFakeSSHTransport(t, fmt.Sprintf("/bin/sleep 30 >/dev/null &\necho $! > %s\necho 'ssh: connect to host h port 22: Connection refused' >&2\nexit 255\n", pidFile))
	t.Cleanup(func() {
		if b, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	start := time.Now()
	_, err := agent.SSHSublogFetcher{Host: "h"}.FetchSubLogs(ctx, "/remote/logs")
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "Connection refused")
	assert.Less(t, elapsed, 8*time.Second,
		"a descendant holding ssh's stderr must not hold the fetch open past WaitDelay; took %s", elapsed)
}

// writeSublogFixture writes one good Claude and one good Codex session log.
func writeSublogFixture(t *testing.T, dir string) {
	t.Helper()
	claude := `{"type":"assistant","message":{"content":[{"type":"text","text":"hello over ssh"}]}}` + "\n"
	codex := `{"type":"item.completed","item":{"id":"i1","type":"agent_message","text":"codex over ssh"}}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sess-a.jsonl"), []byte(claude), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "codex-1.jsonl"), []byte(codex), 0o644))
}

// TestSSHSublogFetchPartialFailureKeepsEntries is fix round 1's M5: when only
// the Codex fetch fails, the Claude entries must still be returned (with an
// in-band ERROR entry naming the failure) and the error must be nil — every
// server caller discards entries on error and ends the sublog stream.
func TestSSHSublogFetchPartialFailureKeepsEntries(t *testing.T) {
	installFaithfulFakeSSH(t, "")
	// Only the Codex fetch's find has "-maxdepth 1 -name 'codex-"; the
	// Claude fetch's reads "-maxdepth 1 -name '*.jsonl" (the script travels
	// verbatim on ssh's stdin since fix round 2).
	t.Setenv("FAKE_SSH_FAIL_IF_CONTAINS", `-maxdepth 1 -name 'codex-`)
	dir := t.TempDir()
	writeSublogFixture(t, dir)

	entries, err := agent.SSHSublogFetcher{Host: "worker.example.test"}.FetchSubLogs(context.Background(), dir)
	require.NoError(t, err, "a partial success must not be reported as a failed fetch")
	var texts, errs []string
	for _, e := range entries {
		switch e.Event {
		case "text":
			texts = append(texts, e.SessionID+":"+e.Message)
		case "error":
			errs = append(errs, e.Message)
		}
	}
	assert.Equal(t, []string{"sess-a:hello over ssh"}, texts, "the Claude entries must survive a Codex fetch failure")
	require.Len(t, errs, 1, "the Codex failure must be surfaced in-band")
	assert.Contains(t, errs[0], "fake transport failure")
}

// TestSSHSublogFetchTotalFailureIsAnError is M5's other half: when both
// fetches fail, the fetch is a failure and returns an error.
func TestSSHSublogFetchTotalFailureIsAnError(t *testing.T) {
	installFaithfulFakeSSH(t, "")
	t.Setenv("FAKE_SSH_FAIL_IF_CONTAINS", "jsonl")
	dir := t.TempDir()
	writeSublogFixture(t, dir)

	_, err := agent.SSHSublogFetcher{Host: "worker.example.test"}.FetchSubLogs(context.Background(), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fake transport failure")
}

// oversizedSessionLog is a session log whose second line exceeds the 1 MiB
// scanner cap, so reading it fails after one entry.
func oversizedSessionLog() string {
	return `{"type":"assistant","message":{"content":[{"type":"text","text":"before"}]}}` + "\n" +
		`{"type":"assistant","message":{"content":[{"type":"text","text":"` + strings.Repeat("y", 2<<20) + `"}]}}` + "\n"
}

// TestLocalSublogFetchAllFilesFailedIsAnError is fix round 1's M4: when every
// session file fails to read, the fetch must fail (so the server reaches
// fetch_failed) instead of succeeding with only ERROR entries.
func TestLocalSublogFetchAllFilesFailedIsAnError(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sess-1.jsonl"), []byte(oversizedSessionLog()), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "codex-2.jsonl"), []byte(oversizedSessionLog()), 0o644))

	_, err := agent.LocalSublogFetcher{}.FetchSubLogs(context.Background(), dir)
	require.Error(t, err, "every file failing is a failed fetch")
	assert.Contains(t, err.Error(), "token too long")
}

// TestLocalSublogFetchReadErrorEntrySurvivesTailTrim is M4's trim half: the
// in-band ERROR entry for an early file must not be cut by the
// maxSubLogLines (5000) tail trim when later files are large.
func TestLocalSublogFetchReadErrorEntrySurvivesTailTrim(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a-early.jsonl"), []byte(oversizedSessionLog()), 0o644))
	line := `{"type":"assistant","message":{"content":[{"type":"text","text":"later"}]}}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b-late.jsonl"), []byte(strings.Repeat(line, 6000)), 0o644))

	entries, err := agent.LocalSublogFetcher{}.FetchSubLogs(context.Background(), dir)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(entries), 5000)
	found := false
	for _, e := range entries {
		if e.Event == "error" && e.SessionID == "a-early" {
			found = true
		}
	}
	assert.True(t, found, "the read-error entry for an early file must survive the tail trim")
}
