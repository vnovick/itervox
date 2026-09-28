package agent_test

// M1-B2 fix round 1 (C1, CORE-154): the remote command must reach the remote
// host as ONE ssh argument that the remote login shell parses back into
// exactly the intended `bash -lc <script>`. Every row runs through the
// faithful fake ssh (ssh_fake_test.go), under each available login shell.

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent"
)

type remoteRunnerCase struct {
	name      string
	runner    agent.Runner
	firstLine string
}

func remoteRunnerCases() []remoteRunnerCase {
	return []remoteRunnerCase{
		{"claude", agent.NewClaudeRunner(), `{"type":"system","session_id":"s1"}`},
		{"codex", agent.NewCodexRunner(), `{"type":"thread.started","thread_id":"t1"}`},
	}
}

// writeProbeAgent writes a fake agent that records its working directory,
// its environment, and its stdin (the prompt, CORE-156) into outDir — then
// "eof" once that stdin ended, which it never would if stdin were the ssh
// channel — touches a "started" marker, emits one non-terminal event, and
// exits 0.
func writeProbeAgent(t *testing.T, dir, outDir, firstLine string) string {
	t.Helper()
	exe := filepath.Join(dir, "probe-agent")
	script := fmt.Sprintf(`#!/bin/sh
: > %[1]s/started
pwd -P > %[1]s/pwd
env > %[1]s/env
cat > %[1]s/prompt
echo eof > %[1]s/stdin
echo '%[2]s'
`, outDir, firstLine)
	require.NoError(t, os.WriteFile(exe, []byte(script), 0o755))
	return exe
}

// TestSSHTurnStartsAgentInWorkspace: the remote agent must run in the
// workspace with the ITERVOX_AGENT marker exported. Before fix round 1 the
// words were split, so `bash -lc cd` ran a bare cd and the agent ran in the
// remote HOME (CORE-154's broader form).
func TestSSHTurnStartsAgentInWorkspace(t *testing.T) {
	for _, shell := range availableLoginShells() {
		for _, rc := range remoteRunnerCases() {
			t.Run(shellLabel(shell)+"/"+rc.name, func(t *testing.T) {
				installFaithfulFakeSSH(t, shell)
				bin, out, ws := t.TempDir(), t.TempDir(), t.TempDir()
				exe := writeProbeAgent(t, bin, out, rc.firstLine)

				result, err := rc.runner.RunTurn(context.Background(), slog.Default(), nil,
					nil, "hi", ws, exe, "worker.example.test", filepath.Join(bin, "logs"),
					30000, 60000, agent.PermissionBypass)
				require.NoError(t, err)
				assert.False(t, result.Failed, "FailureText: %s", result.FailureText)

				gotPwd, err := os.ReadFile(filepath.Join(out, "pwd"))
				require.NoError(t, err, "the agent never started")
				wantWs, _ := filepath.EvalSymlinks(ws)
				assert.Equal(t, wantWs, strings.TrimSpace(string(gotPwd)), "the remote agent must start in the workspace")
				env, err := os.ReadFile(filepath.Join(out, "env"))
				require.NoError(t, err)
				assert.Contains(t, string(env), "ITERVOX_AGENT=1\n", "the marker must reach the remote agent's environment")
				stdin, err := os.ReadFile(filepath.Join(out, "stdin"))
				require.NoError(t, err)
				assert.Equal(t, "eof\n", string(stdin), "the remote agent's stdin must be the prompt and end at EOF, not the ssh channel that carried the script")
				prompt, err := os.ReadFile(filepath.Join(out, "prompt"))
				require.NoError(t, err)
				assert.Equal(t, "hi", string(prompt), "the prompt must arrive on the agent's stdin (CORE-156)")
			})
		}
	}
}

// TestSSHTurnMissingWorkspaceFailsBeforeAgentStarts is CORE-154: a remote
// workspace that does not exist must abort before the agent starts, and the
// turn must be reported failed with the shell's diagnostic.
func TestSSHTurnMissingWorkspaceFailsBeforeAgentStarts(t *testing.T) {
	for _, shell := range availableLoginShells() {
		for _, rc := range remoteRunnerCases() {
			t.Run(shellLabel(shell)+"/"+rc.name, func(t *testing.T) {
				installFaithfulFakeSSH(t, shell)
				bin, out := t.TempDir(), t.TempDir()
				exe := writeProbeAgent(t, bin, out, rc.firstLine)
				missing := filepath.Join(bin, "no-such-workspace")

				result, _ := rc.runner.RunTurn(context.Background(), slog.Default(), nil,
					nil, "hi", missing, exe, "worker.example.test", "",
					30000, 60000, agent.PermissionBypass)

				_, statErr := os.Stat(filepath.Join(out, "started"))
				assert.True(t, os.IsNotExist(statErr), "the agent must NOT start when cd into the workspace fails")
				assert.True(t, result.Failed, "a failed cd must fail the turn")
				assert.Contains(t, result.FailureText, "no-such-workspace", "the cd diagnostic must reach FailureText")
			})
		}
	}
}

// allLevelsLogger records every message (Debug included, unlike codex_test.go's loggers) and argument a runner logs.
type allLevelsLogger struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (c *allLevelsLogger) add(msg string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fmt.Fprintln(&c.buf, msg, args)
}
func (c *allLevelsLogger) Info(msg string, args ...any)  { c.add(msg, args...) }
func (c *allLevelsLogger) Debug(msg string, args ...any) { c.add(msg, args...) }
func (c *allLevelsLogger) Warn(msg string, args ...any)  { c.add(msg, args...) }
func (c *allLevelsLogger) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

// TestSSHTurnNeverDumpsRemoteShellVariables is C1's secret-leak regression:
// with the words split, `bash -lc set` / `bash -lc export` printed every
// remote variable to the agent stream, which the runners log. A sentinel in
// the fake remote environment must never reach the logger, the result, or
// FailureText.
func TestSSHTurnNeverDumpsRemoteShellVariables(t *testing.T) {
	requireSSHMatrix(t) // CORE-172: slow login-shell matrix
	// The readers log unparseable stream lines through the global slog at
	// Debug ("agent: raw line"), not through the per-run Logger, so capture
	// both. Not parallel: slog.SetDefault is process-global.
	var global bytes.Buffer
	var globalMu sync.Mutex
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&lockedWriter{w: &global, mu: &globalMu}, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	for _, shell := range availableLoginShells() {
		for _, rc := range remoteRunnerCases() {
			for _, withWs := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/workspace=%v", shellLabel(shell), rc.name, withWs), func(t *testing.T) {
					installFaithfulFakeSSH(t, shell)
					bin, out := t.TempDir(), t.TempDir()
					exe := writeProbeAgent(t, bin, out, rc.firstLine)
					ws := ""
					if withWs {
						ws = t.TempDir()
					}
					logger := &allLevelsLogger{}
					// Fix round 2, M1: the progress sink is a third output
					// path (dashboard live state); the dump must not reach it.
					var progressMu sync.Mutex
					var progress strings.Builder
					onProgress := func(r agent.TurnResult) {
						progressMu.Lock()
						defer progressMu.Unlock()
						fmt.Fprintf(&progress, "%+v\n", r)
					}
					result, _ := rc.runner.RunTurn(context.Background(), logger, onProgress,
						nil, "hi", ws, exe, "worker.example.test", filepath.Join(bin, "logs"),
						30000, 60000, agent.PermissionBypass)

					globalMu.Lock()
					globalLog := global.String()
					global.Reset()
					globalMu.Unlock()
					progressMu.Lock()
					progressText := progress.String()
					progressMu.Unlock()
					assert.NotEmpty(t, progressText, "the progress sink must have been called (positive control)")
					everything := globalLog + progressText + logger.String() + result.FailureText + result.ResultText + result.LastText + strings.Join(result.AllTextBlocks, "\n")
					assert.NotContains(t, everything, fakeRemoteSecret, "a remote shell-variable dump reached the agent stream")
					if files, _ := filepath.Glob(filepath.Join(bin, "logs", "*.jsonl")); len(files) > 0 {
						for _, f := range files {
							b, _ := os.ReadFile(f)
							assert.NotContains(t, string(b), fakeRemoteSecret, "a remote shell-variable dump reached the session log")
						}
					}
				})
			}
		}
	}
}

// lockedWriter serialises writes from concurrent slog calls into a buffer.
type lockedWriter struct {
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
