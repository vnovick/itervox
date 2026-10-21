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
