package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
)

// fakeSandbox is a recording sandbox for the tmux launch path (#258): it
// wraps argv in a script that logs what it was asked to run, then runs it.
type fakeSandbox struct{ script string }

func newFakeSandbox(t *testing.T, dir, log string) fakeSandbox {
	t.Helper()
	script := filepath.Join(dir, "fake-sandbox")
	body := fmt.Sprintf("#!/bin/sh\n{ printf '%%s\\n' \"$@\"; echo --END--; } >> %s\n"+
		"while [ \"$1\" != -- ]; do shift; done\nshift\nexec \"$@\"\n", shellQuote(log))
	require.NoError(t, os.WriteFile(script, []byte(body), 0o755))
	return fakeSandbox{script: script}
}

func (fakeSandbox) Name() string     { return "recorder" }
func (fakeSandbox) Available() error { return nil }
func (f fakeSandbox) Wrap(argv []string, _ agent.IsolationPolicy) ([]string, error) {
	return append([]string{f.script, "--"}, argv...), nil
}

// TestIsolationNoneLeavesCommandUnchanged (tmux path): without isolation
// a session's command line is exactly what interactiveCommand builds, for
// claude and codex, and no sandbox is probed.
func TestIsolationNoneLeavesCommandUnchanged(t *testing.T) {
	ts := &tmuxSessions{projectRoot: t.TempDir(), isolation: config.IsolationNone,
		isolationDetect: func() (agent.IsolationMechanism, error) {
			t.Fatal("isolation none must not probe for a sandbox")
			return nil, nil
		}}
	look := func(string) (string, error) { return "/usr/bin/x", nil }
	for _, backend := range []string{"claude", "codex"} {
		want, err := interactiveCommand(backend, backend, agent.PermissionSandbox, "fix 'it'", look)
		require.NoError(t, err)
		for _, profile := range []config.AgentProfile{{}, {Isolation: config.IsolationNone}} {
			iso, err := ts.sessionIsolation(profile, ts.projectRoot)
			require.NoError(t, err)
			assert.Nil(t, iso)
			got, err := agent.IsolateShellCommand(iso, want)
			require.NoError(t, err)
			assert.Equal(t, want, got, "%s: the tmux command must be byte-identical", backend)
		}
	}
}

// TestIsolationProcessWrapsEveryLaunchPath (tmux path): with
// agent.isolation: process a new session's whole command line runs inside
// the sandbox, for claude and codex, and the CLI still starts there.
func TestIsolationProcessWrapsEveryLaunchPath(t *testing.T) {
	r := newTmuxRig(t, "agent:\n  isolation: process\n", true)
	out := filepath.Join(filepath.Dir(r.bin), "out")
	log := filepath.Join(r.bin, "sandbox.log")
	fake := newFakeSandbox(t, r.bin, log)
	r.ts.isolation = config.IsolationProcess
	r.ts.isolationDetect = func() (agent.IsolationMechanism, error) { return fake, nil }

	for _, backend := range []string{"claude", "codex"} {
		w := r.do(http.MethodPost, "/api/v1/sessions", `{"backend":"`+backend+`"}`)
		require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
		readLines(t, filepath.Join(out, backend+".args")) // waits: the CLI ran inside the sandbox
	}
	rec := readLines(t, log)
	var runs [][]string
	for start := 0; start < len(rec); {
		end := slices.Index(rec[start:], "--END--")
		require.GreaterOrEqual(t, end, 0)
		runs = append(runs, rec[start:start+end])
		start += end + 1
	}
	require.Len(t, runs, 2, "one sandboxed launch per session: %q", rec)
	for i, backend := range []string{"claude", "codex"} {
		run := runs[i]
		require.GreaterOrEqual(t, len(run), 4)
		assert.Equal(t, []string{"--", "/bin/sh", "-c"}, run[:3])
		first, _, _ := strings.Cut(run[3], " ")
		assert.Equal(t, backend, filepath.Base(first),
			"%s: the sandbox runs the session's command line: %q", backend, run[3])
	}
}

// TestIsolationTmuxSessionRefusedWhenUnavailable: a session asking for
// process isolation on a machine with no sandbox is refused with the
// reason, not started unconfined.
func TestIsolationTmuxSessionRefusedWhenUnavailable(t *testing.T) {
	r := newTmuxRig(t, "agent:\n  isolation: process\n", true)
	r.ts.isolation = config.IsolationProcess
	r.ts.isolationDetect = func() (agent.IsolationMechanism, error) {
		return nil, fmt.Errorf("%w: bubblewrap: bwrap is not installed", agent.ErrIsolationUnavailable)
	}
	w := r.do(http.MethodPost, "/api/v1/sessions", `{"backend":"claude"}`)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "process isolation unavailable")
	assert.NoFileExists(t, filepath.Join(filepath.Dir(r.bin), "out", "claude.args"))
	assert.Empty(t, r.list().Sessions, "no session is left reserved")
}
