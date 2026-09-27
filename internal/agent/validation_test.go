package agent_test

import (
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

func TestValidateCLI_NotFound(t *testing.T) {
	// Disable the interactive-shell fallback so PATH manipulation alone
	// determines the outcome; otherwise the user's real ~/.zshrc (sourced
	// by the fallback) would re-add the tool to PATH and mask the failure.
	prev := agent.SetValidateCLIShellFallback(false)
	defer agent.SetValidateCLIShellFallback(prev)

	cases := []struct {
		name     string
		validate func() error
		wantMsg  string
	}{
		{"claude", agent.ValidateClaudeCLI, "claude CLI not available"},
		{"codex", agent.ValidateCodexCLI, "codex CLI not available"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			orig := os.Getenv("PATH")
			defer func() { _ = os.Setenv("PATH", orig) }()
			require.NoError(t, os.Setenv("PATH", t.TempDir()))
			err := tc.validate()
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantMsg)
		})
	}
}

func TestValidateCLI_FakeBinary(t *testing.T) {
	cases := []struct {
		binName  string
		validate func() error
	}{
		{"claude", agent.ValidateClaudeCLI},
		{"codex", agent.ValidateCodexCLI},
	}
	for _, tc := range cases {
		t.Run(tc.binName, func(t *testing.T) {
			orig := os.Getenv("PATH")
			defer func() { _ = os.Setenv("PATH", orig) }()
			tmpDir := t.TempDir()
			script := "#!/bin/sh\necho 'version 1.0.0'"
			require.NoError(t, os.WriteFile(filepath.Join(tmpDir, tc.binName), []byte(script), 0o755))
			require.NoError(t, os.Setenv("PATH", tmpDir))
			assert.NoError(t, tc.validate())
		})
	}
}

func TestValidateCodexCLITimeout(t *testing.T) {
	// Save original PATH
	origPath := os.Getenv("PATH")
	defer func() { _ = os.Setenv("PATH", origPath) }()

	// Create a fake codex binary that hangs
	tmpDir := t.TempDir()
	fakeCodex := filepath.Join(tmpDir, "codex")
	script := "#!/bin/sh\nsleep 10" // Will timeout after 5s
	require.NoError(t, os.WriteFile(fakeCodex, []byte(script), 0o755))

	require.NoError(t, os.Setenv("PATH", tmpDir+string(os.PathListSeparator)+origPath))
	err := agent.ValidateCodexCLI()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
}

// TestValidateCLIShellFallbackIsBoundedWhenRCHoldsStderr is fix round 1's
// M7: an rc file that leaves a background process holding the fallback
// login shell's stderr used to block validateCLI (and daemon startup) until
// that process exited, because Wait also waits for the stderr copy and the
// command had no WaitDelay or group kill.
func TestValidateCLIShellFallbackIsBoundedWhenRCHoldsStderr(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "bg.pid")
	fakeShell := filepath.Join(dir, "fake-login-shell")
	// CORE-172: the background child sleeps for 300 s so the failure mode
	// (validateCLI waiting for the stderr holder to exit) is unmistakable,
	// and the bound below sits far from both ends: the fixed path returns
	// after at most validationWaitDelay (2 s) plus process start-up, even on
	// a machine running the whole -race suite concurrently (it once took
	// just over the old 5-6 s bound there).
	script := "#!/bin/sh\necho 'rc: loading profile' >&2\n/bin/sleep 300 &\necho $! > " + pidFile + "\nexit 1\n"
	require.NoError(t, os.WriteFile(fakeShell, []byte(script), 0o755))
	t.Setenv("SHELL", fakeShell)
	t.Cleanup(func() {
		if b, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	prev := agent.SetValidateCLIShellFallback(true)
	t.Cleanup(func() { agent.SetValidateCLIShellFallback(prev) })

	start := time.Now()
	err := agent.ValidateClaudeCLICommand("itervox-definitely-missing-cli-7f3a")
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "rc: loading profile", "the fallback shell's stderr must reach the error")
	assert.Less(t, elapsed, 60*time.Second, "validateCLI must not wait on a background process holding stderr (it would take ~300s); took %s", elapsed)
}

// TestValidateCLIShellFallbackKillsHungShellGroup is M7's deadline half: a
// login shell that never returns (an rc file blocking on a child) must be
// killed with its whole group at the 5s deadline — with Setsid the shell is
// its group's leader, so procgroup's group kill reaches the child that
// holds stderr — and validateCLI must report the timeout promptly.
func TestValidateCLIShellFallbackKillsHungShellGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	fakeShell := filepath.Join(dir, "fake-login-shell")
	script := "#!/bin/sh\n/bin/sleep 30 &\necho $! > " + pidFile + "\nwait\n"
	require.NoError(t, os.WriteFile(fakeShell, []byte(script), 0o755))
	t.Setenv("SHELL", fakeShell)
	prev := agent.SetValidateCLIShellFallback(true)
	t.Cleanup(func() { agent.SetValidateCLIShellFallback(prev) })

	start := time.Now()
	err := agent.ValidateClaudeCLICommand("itervox-definitely-missing-cli-7f3a")
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
	assert.Less(t, elapsed, 9*time.Second, "the hung shell's group must be killed at the 5s deadline; took %s", elapsed)
	b, readErr := os.ReadFile(pidFile)
	require.NoError(t, readErr)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	deadline := time.Now().Add(2 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("the rc file's child %d survived the group kill", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
