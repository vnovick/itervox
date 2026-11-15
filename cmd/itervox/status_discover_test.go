//go:build linux

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sleepChildEnv makes the test binary sleep instead of running tests, so a
// test can start a process with any argv (exec.Cmd.Args[0] included) and a
// chosen working directory.
const sleepChildEnv = "ITERVOX_TEST_SLEEP_CHILD"

func init() {
	if os.Getenv(sleepChildEnv) == "1" {
		time.Sleep(60 * time.Second)
		os.Exit(0)
	}
}

// startArgvProcess starts the test binary with argv (argv[0] as given) in dir
// and returns its pid; the process is killed when the test ends.
func startArgvProcess(t *testing.T, dir string, argv ...string) int {
	t.Helper()
	cmd := &exec.Cmd{Path: os.Args[0], Args: argv, Dir: dir, Env: append(os.Environ(), sleepChildEnv+"=1")}
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd.Process.Pid
}

// TestStatusIgnoresGuardrailShimsAndTmuxSessions: `itervox status` (and
// `itervox stop`) find a project's daemons by scanning for processes whose
// command line mentions itervox and whose working directory is the project.
// A tmux session started from Sessions → New session runs in the project
// with an itervox-named session and guardrail shims (the itervox binary as
// `guardrail-shim ... git`) on PATH; none of those is a daemon. A daemon
// started without a pid file still counts.
func TestStatusIgnoresGuardrailShimsAndTmuxSessions(t *testing.T) {
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("pgrep is required for the process scan")
	}
	project := t.TempDir()
	shims := filepath.Join(project, ".itervox", "run", "guardrails", "itervox-abc123", "bin")
	exe := filepath.Join(t.TempDir(), "itervox")

	daemon := startArgvProcess(t, project, exe, "--workflow", filepath.Join(project, "WORKFLOW.md"))
	decoys := map[string]int{
		"guardrail shim": startArgvProcess(t, project, exe, "guardrail-shim", filepath.Join(shims, "..", "policy.json"), "git", "push"),
		"pre-push hook":  startArgvProcess(t, project, exe, "guardrail-hook", filepath.Join(shims, "..", "policy.json"), "pre-push", "origin"),
		"tmux session":   startArgvProcess(t, project, "tmux", "new-session", "-d", "-s", "itervox-0a1b2c", "-c", project, "/bin/sh", "-c", "PATH="+shims+":$PATH claude"),
		"session shell":  startArgvProcess(t, project, "/bin/sh", "-c", "PATH="+shims+":$PATH exec claude"),
		"watch client":   startArgvProcess(t, project, exe, "watch", "--workflow", "WORKFLOW.md"),
	}

	var got []int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got = discoverDaemonsByCwd(project); slices.Contains(got, daemon) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	assert.Contains(t, got, daemon, "a daemon in the project counts")
	for name, pid := range decoys {
		assert.NotContains(t, got, pid, "%s (pid %d) is not a daemon", name, pid)
	}
	all := discoverAllDaemons()
	assert.Contains(t, all, daemon)
	for name, pid := range decoys {
		assert.NotContains(t, all, pid, "status --all: %s (pid %d) is not a daemon", name, pid)
	}
}
