package agent_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent"
)

func TestItervoxAgentEnvSetsMarker(t *testing.T) {
	env := agent.ItervoxAgentEnv()

	assert.Contains(t, env, "ITERVOX_AGENT=1")
	// The daemon's own environment must survive — PATH in particular, or the
	// agent binary stops resolving.
	var sawPath bool
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			sawPath = true
			break
		}
	}
	assert.True(t, sawPath, "os.Environ() must be preserved, not replaced")
}

func TestItervoxAgentEnvKeepsExtras(t *testing.T) {
	env := agent.ItervoxAgentEnv("CLAUDE_CODE_LOG_DIR=/tmp/logs")

	assert.Contains(t, env, "ITERVOX_AGENT=1")
	assert.Contains(t, env, "CLAUDE_CODE_LOG_DIR=/tmp/logs")
}

func TestItervoxAgentEnvIgnoresEmptyExtras(t *testing.T) {
	// Callers pass a conditionally-empty log dir; an empty string must not
	// become a bare "=" entry in the environment.
	env := agent.ItervoxAgentEnv("")

	assert.Contains(t, env, "ITERVOX_AGENT=1")
	assert.NotContains(t, env, "")
	for _, kv := range env {
		require.NotEqual(t, "", kv, "empty extras must be dropped, not passed through")
	}
}

func TestItervoxAgentExportPrefixIsShellSafe(t *testing.T) {
	got := agent.ItervoxAgentExportPrefix()

	assert.Contains(t, got, "ITERVOX_AGENT=1")
	assert.True(t, strings.HasSuffix(got, "; "),
		"the prefix must terminate so the real command follows cleanly, got %q", got)
	// No shell metacharacter can appear — the value is a literal 1.
	assert.NotContains(t, got, "$")
	assert.NotContains(t, got, "`")
}

// CORE-133: TestClaudeLocalTurnSetsMarkerWithoutLogDir used to live here. It
// only re-tested itervoxAgentEnv's no-log-dir shape in isolation and never
// touched claude.go's cmd.Env, so it could not have caught a regression in
// the local RunTurn path (e.g. cmd.Env only being set conditionally again).
// Deleted rather than duplicated: TestClaudeDirectBinaryTurnCarriesMarkerInRealEnv
// below already exercises the exact trap this test named — the real local
// RunTurn path (workerHost="", logDir="") with a fake claude executable that
// dumps its own environment — and is the test that actually asserts the
// marker landed in cmd.Env.

// ---------------------------------------------------------------------------
// Behavioral wiring tests — real subprocess, not source text.
//
// The plan's brief offered a source-text test ("does claude.go/codex.go
// contain the string itervoxAgentEnv(") as a fallback because there was
// apparently no way to observe five of the six sites without spawning a real
// claude/codex binary. But this package already spawns FAKE stand-in
// binaries via RunTurn in codex_test.go (e.g. TestCodexRunnerFreshTurn) —
// that idiom extends directly to inspecting the real *exec.Cmd's actual
// environment and the real assembled SSH shellCmd, which is strictly better
// than grepping source text (it would catch e.g. a call to itervoxAgentEnv
// whose result is discarded, or a typo'd env var name). So the source-text
// test from the brief is intentionally NOT included here.
// ---------------------------------------------------------------------------

func TestClaudeDirectBinaryTurnCarriesMarkerInRealEnv(t *testing.T) {
	// workerHost="" and logDir="" — the exact shape of the trap.
	dir := t.TempDir()
	envFile := filepath.Join(dir, "env.out")
	fakeExe := filepath.Join(dir, "claude")
	script := fmt.Sprintf("#!/bin/sh\nenv > %s\nprintf '%%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"session_id\":\"s1\"}'\n", envFile)
	require.NoError(t, os.WriteFile(fakeExe, []byte(script), 0o755))

	runner := agent.NewClaudeRunner()
	_, err := runner.RunTurn(
		context.Background(), slog.Default(), nil,
		nil, "hi", dir, fakeExe, "", "",
		30000, 60000,
		agent.PermissionBypass)
	require.NoError(t, err)

	got, err := os.ReadFile(envFile)
	require.NoError(t, err)
	assert.Contains(t, string(got), "ITERVOX_AGENT=1\n",
		"the real subprocess environment must contain the marker even with no log dir configured")
}

func TestCodexDirectBinaryTurnCarriesMarkerInRealEnv(t *testing.T) {
	// codex.go set cmd.Env nowhere before this change — this is the
	// regression guard for that gap.
	dir := t.TempDir()
	envFile := filepath.Join(dir, "env.out")
	fakeExe := filepath.Join(dir, "codex")
	script := fmt.Sprintf("#!/bin/sh\nenv > %s\nprintf '%%s\\n' '{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}'\n", envFile)
	require.NoError(t, os.WriteFile(fakeExe, []byte(script), 0o755))

	runner := agent.NewCodexRunner()
	_, err := runner.RunTurn(
		context.Background(), slog.Default(), nil,
		nil, "hi", dir, fakeExe, "", "",
		30000, 60000,
		agent.PermissionBypass)
	require.NoError(t, err)

	got, err := os.ReadFile(envFile)
	require.NoError(t, err)
	assert.Contains(t, string(got), "ITERVOX_AGENT=1\n",
		"the real subprocess environment must contain the marker")
}

func TestClaudeSSHTurnCarriesMarkerAcrossBoundary(t *testing.T) {
	// cmd.Env does not cross the ssh boundary — the marker must instead be
	// baked into the shell command ssh hands to the remote `bash -lc`. We
	// substitute a fake "ssh" binary on PATH to capture the real argv
	// RunTurn builds, without touching a network or a real host.
	argsFile := installRecordingFakeSSH(t)

	runner := agent.NewClaudeRunner()
	_, _ = runner.RunTurn(
		context.Background(), slog.Default(), nil,
		nil, "hi", "", "claude", "worker.example.test", "",
		30000, 60000,
		agent.PermissionBypass)

	got, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	assert.Contains(t, string(got), "export ITERVOX_AGENT=1; ",
		"the shell command handed to the remote host over ssh must carry the marker export")
}

func TestCodexSSHTurnCarriesMarkerAcrossBoundary(t *testing.T) {
	argsFile := installRecordingFakeSSH(t)

	runner := agent.NewCodexRunner()
	_, _ = runner.RunTurn(
		context.Background(), slog.Default(), nil,
		nil, "hi", "", "codex", "worker.example.test", "",
		30000, 60000,
		agent.PermissionBypass)

	got, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	assert.Contains(t, string(got), "export ITERVOX_AGENT=1; ",
		"the shell command handed to the remote host over ssh must carry the marker export")
}
