package agent_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
)

// A profile command that is not a simple command — a brace group, a
// subshell, a `.`/`source … &&` list — gets the runner's flags after its
// final command, inside the group, not after the whole command line
// (`{ …; } --output-format …`, a bash syntax error). Every runner, through
// the local login shell and on an SSH worker (the faithful fake ssh), must
// hand the fake CLI exactly the argv and prompt the plain command gets, and
// the setup must have run first in the same shell. The CLI is a fake
// script; no real agent runs.
func TestCompoundCommandsGetTheRunnerFlags(t *testing.T) {
	type backend struct {
		name      string
		runner    func() agent.Runner
		promptArg bool
		stream    string
	}
	backends := []backend{
		{"claude", func() agent.Runner { return agent.NewClaudeRunner() }, false,
			`{"type":"result","subtype":"success","session_id":"s1","result":"ok"}`},
		{"codex", func() agent.Runner { return agent.NewCodexRunner() }, false,
			`{"type":"thread.started","thread_id":"t1"}`},
		{"opencode", func() agent.Runner { return agent.NewOpenCodeRunner() }, false,
			`{"type":"step_start","sessionID":"ses_r"}`},
		{"cursor", func() agent.Runner { return agent.NewCursorRunner() }, true,
			`{"type":"result","is_error":false,"result":"ok","session_id":"cur_r"}`},
	}
	home := t.TempDir()
	setup := filepath.Join(home, "setup.sh")
	require.NoError(t, os.WriteFile(setup, []byte("SETUP_RAN=1; export SETUP_RAN\n"), 0o644))
	sub := filepath.Join(home, "sub")
	require.NoError(t, os.MkdirAll(sub, 0o755))

	type shape struct {
		name, shell string
		wrap        func(probe string) string
		setupRuns   bool
	}
	shapes := []shape{
		{"brace-group", "/bin/sh", func(p string) string { return "{ . " + setup + "; " + p + "; }" }, true},
		{"subshell", "/bin/sh", func(p string) string { return "(cd " + sub + " && . " + setup + " && " + p + ")" }, true},
		{"dot-and", "/bin/sh", func(p string) string { return ". " + setup + " && " + p }, true},
		{"trailing-semicolon", "/bin/sh", func(p string) string { return p + ";" }, false},
		{"bash-source-group", "/bin/bash", func(p string) string { return "{ source " + setup + "; " + p + "; }" }, true},
	}
	run := func(t *testing.T, b backend, command, shell, workerHost, ws string) (argv, prompt, env string) {
		t.Helper()
		t.Setenv("HOME", home)
		t.Setenv("SHELL", shell)
		bin, out := t.TempDir(), t.TempDir()
		probe := writeBackendProbe(t, bin, out, b.stream, b.promptArg)
		sid := "sid-1"
		_, _ = b.runner().RunTurn(context.Background(), slog.Default(), nil, &sid, "the prompt", ws,
			strings.ReplaceAll(command, "@PROBE@", probe), workerHost, "", 30000, 60000, agent.PermissionBypass)
		read := func(name string) string {
			data, err := os.ReadFile(filepath.Join(out, name))
			require.NoError(t, err, "the fake CLI never started for %q", command)
			return string(data)
		}
		return read("argv"), read("prompt"), read("env")
	}
	for _, mode := range []string{"local", "ssh"} {
		for _, b := range backends {
			for _, s := range shapes {
				if _, err := os.Stat(s.shell); err != nil {
					continue
				}
				t.Run(mode+"/"+b.name+"/"+s.name, func(t *testing.T) {
					host := ""
					if mode == "ssh" {
						installFaithfulFakeSSH(t, "/bin/bash")
						host = "worker.example.test"
					}
					ws := t.TempDir()
					// "@PROBE@" is replaced by the probe's path; the plain
					// form goes through the shell too (it is not a lone
					// absolute path, which would run directly).
					plainArgv, plainPrompt, _ := run(t, b, "@PROBE@ --model m", s.shell, host, ws)
					argv, prompt, env := run(t, b, s.wrap("@PROBE@ --model m"), s.shell, host, ws)
					assert.Equal(t, plainArgv, argv, "the compound command's CLI gets the plain command's argv")
					assert.Equal(t, plainPrompt, prompt, "the prompt reaches the CLI")
					assert.Contains(t, argv, "--model\nm\n", "the command's own flags are kept")
					if s.setupRuns {
						assert.Contains(t, env, "SETUP_RAN=1\n", "the setup ran first, in the same shell")
					}
				})
			}
		}
	}
}
