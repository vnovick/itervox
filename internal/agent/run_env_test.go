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
	"github.com/vnovick/itervox/internal/guardrails"
)

// #204: the run environment in a turn's context reaches the real CLI
// process, for both runners and both ways they start it: directly (a bare
// absolute command, no shell) and through the login shell (PATH prepended
// after the profile ran). Variables override the inherited environment.
func TestRunnersApplyRunEnv(t *testing.T) {
	t.Setenv("ITERVOX_API_TOKEN", "operator-token")
	for _, backend := range []string{"claude", "codex"} {
		for _, mode := range []string{"direct", "shell"} {
			t.Run(backend+"/"+mode, func(t *testing.T) {
				dir := t.TempDir()
				shim := filepath.Join(dir, "shim")
				require.NoError(t, os.MkdirAll(shim, 0o755))
				envFile := filepath.Join(dir, "env.out")
				script := "#!/bin/sh\nenv > " + envFile + "\n" +
					"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"session_id\":\"s1\"}'\n"
				exe := filepath.Join(shim, backend)
				require.NoError(t, os.WriteFile(exe, []byte(script), 0o755))
				command := exe
				if mode == "shell" {
					t.Setenv("SHELL", "/bin/sh")
					command = backend // found only through the prepended PATH
				}
				ctx := agent.WithRunEnv(context.Background(), agent.RunEnv{
					Vars:        []string{"GIT_CONFIG_PARAMETERS='core.hooksPath'='/x/hooks'", "ITERVOX_API_TOKEN="},
					PathPrepend: shim,
				})
				var runner agent.Runner = agent.NewClaudeRunner()
				if backend == "codex" {
					runner = agent.NewCodexRunner()
				}
				_, _ = runner.RunTurn(ctx, slog.Default(), nil, nil, "hi", dir, command, "", "", 30000, 60000, agent.PermissionBypass)
				data, err := os.ReadFile(envFile)
				require.NoError(t, err, "the CLI ran")
				env := map[string]string{}
				for _, line := range strings.Split(string(data), "\n") {
					if k, v, ok := strings.Cut(line, "="); ok {
						env[k] = v
					}
				}
				assert.True(t, strings.HasPrefix(env["PATH"], shim+":"), "PATH starts with the shims: %s", env["PATH"])
				assert.Equal(t, "'core.hooksPath'='/x/hooks'", env["GIT_CONFIG_PARAMETERS"])
				assert.Empty(t, env["ITERVOX_API_TOKEN"], "the operator's token is blanked")
				assert.Equal(t, "1", env["ITERVOX_AGENT"])
			})
		}
	}
}

// push: daemon: a patch-out run's agent CLI starts with no forge or tracker
// token in its environment, for both runners and both ways they start it,
// even when the daemon has them and the login profile exports one again;
// git sees no credential helper and gh and glab an empty configuration.
// The CLI is a fake script; no real agent runs.
func TestPatchOutAgentEnvHasNoForgeTokens(t *testing.T) {
	tokens := map[string]string{
		"GH_TOKEN": "forge-value-1", "GITHUB_TOKEN": "forge-value-2", "GITLAB_TOKEN": "forge-value-3",
		"LINEAR_API_KEY": "tracker-value", "MY_TRACKER_KEY": "tracker-value", "SSH_AUTH_SOCK": "/tmp/agent.sock",
	}
	for k, v := range tokens {
		t.Setenv(k, v)
	}
	t.Setenv("ITERVOX_API_TOKEN", "operator-token")
	home := t.TempDir()
	t.Setenv("HOME", home)
	// A login profile that hands a token back: `unset` runs after it.
	require.NoError(t, os.WriteFile(filepath.Join(home, ".profile"), []byte("export GH_TOKEN=from-profile\n"), 0o644))
	exe, err := os.Executable()
	require.NoError(t, err)
	for _, backend := range []string{"claude", "codex"} {
		for _, mode := range []string{"direct", "shell"} {
			t.Run(backend+"/"+mode, func(t *testing.T) {
				dir := t.TempDir()
				rt, err := guardrails.Prepare(context.Background(), guardrails.Options{
					ParentDir: dir, Executable: exe, Identifier: "ENG-1", IssueBranch: "eng-1",
					PatchOut: true, StripEnv: guardrails.EnvNamesWithValue(os.Environ(), "tracker-value"),
				})
				require.NoError(t, err)
				t.Cleanup(rt.Remove)
				envFile := filepath.Join(dir, "env.out")
				script := "#!/bin/sh\nenv > " + envFile + "\n" +
					"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"session_id\":\"s1\"}'\n"
				// The fake CLI lives in the shim directory, which the runner
				// puts first on PATH after the login profile ran: a bare
				// name can never resolve to an installed agent CLI.
				cli := filepath.Join(rt.BinDir(), backend)
				require.NoError(t, os.WriteFile(cli, []byte(script), 0o755))
				command := cli
				if mode == "shell" {
					t.Setenv("SHELL", "/bin/sh")
					command = backend
				}
				ctx := agent.WithRunEnv(context.Background(), rt.AgentEnv())
				var runner agent.Runner = agent.NewClaudeRunner()
				if backend == "codex" {
					runner = agent.NewCodexRunner()
				}
				res, runErr := runner.RunTurn(ctx, slog.Default(), nil, nil, "hi", dir, command, "", "", 30000, 60000, agent.PermissionBypass)
				data, err := os.ReadFile(envFile)
				require.NoError(t, err, "the CLI ran: %v %s", runErr, res.FailureText)
				env := map[string]string{}
				for _, line := range strings.Split(string(data), "\n") {
					if k, v, ok := strings.Cut(line, "="); ok {
						env[k] = v
					}
				}
				for name := range tokens {
					_, present := env[name]
					assert.False(t, present, "%s reached the agent", name)
				}
				for _, value := range []string{"forge-value", "tracker-value", "from-profile"} {
					// Not assert.NotContains: its failure message would print the whole environment.
					assert.False(t, strings.Contains(string(data), value), "%s reached the agent's environment", value)
				}
				assert.Empty(t, env["ITERVOX_API_TOKEN"])
				assert.Contains(t, env["GIT_CONFIG_PARAMETERS"], "'credential.helper'=''")
				assert.Equal(t, "0", env["GIT_TERMINAL_PROMPT"])
				assert.NotEmpty(t, env["GH_CONFIG_DIR"])
				assert.Equal(t, "1", env["ITERVOX_AGENT"])
			})
		}
	}
}
