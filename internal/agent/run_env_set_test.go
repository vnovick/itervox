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

// A profile's model_server reaches the real CLI process through
// RunEnv.Set, for both runners and both ways they start it, over the
// daemon's own value and over a login profile that exports the same name;
// only the named variable is set. The CLI is a fake script; no real agent
// runs and no model server is contacted.
func TestRunnersApplyRunEnvSet(t *testing.T) {
	t.Setenv("ANTHROPIC_BASE_URL", "http://daemon-value")
	t.Setenv("OPENAI_BASE_URL", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.WriteFile(filepath.Join(home, ".profile"),
		[]byte("export ANTHROPIC_BASE_URL=http://from-profile\nexport OPENAI_BASE_URL=http://from-profile\n"), 0o644))
	for backend, want := range map[string]string{
		"claude": "ANTHROPIC_BASE_URL=http://127.0.0.1:8000",
		"codex":  "OPENAI_BASE_URL=http://127.0.0.1:8000/v1",
	} {
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
					command = backend
				}
				ctx := agent.WithRunEnv(context.Background(), agent.RunEnv{PathPrepend: shim})
				ctx = agent.WithSetEnv(ctx, want)
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
				name, value, _ := strings.Cut(want, "=")
				assert.Equal(t, value, env[name], "the model server wins over the daemon's and the profile's value")
				if backend == "codex" && mode == "direct" {
					assert.Equal(t, "http://daemon-value", env["ANTHROPIC_BASE_URL"], "the other backend's variable is left as inherited")
				}
				if backend == "claude" && mode == "direct" {
					assert.Empty(t, env["OPENAI_BASE_URL"], "the other backend's variable is not set")
				}
			})
		}
	}
}

// TestWithSetEnv: an empty assignment changes nothing; entries accumulate
// without touching the context's other run environment.
func TestWithSetEnv(t *testing.T) {
	ctx := agent.WithRunEnv(context.Background(), agent.RunEnv{Vars: []string{"A=1"}})
	assert.Equal(t, ctx, agent.WithSetEnv(ctx, ""))
	ctx = agent.WithSetEnv(ctx, "ANTHROPIC_BASE_URL=http://x")
	env := agent.RunEnvFrom(ctx)
	assert.Equal(t, []string{"A=1"}, env.Vars)
	assert.Equal(t, []string{"ANTHROPIC_BASE_URL=http://x"}, env.Set)
}

// A profile command that is not a single external program (a sourced
// setup file, a cd, exec, a shell function) still gets the run
// environment through the login shell: the model server and the shim PATH
// are exported, and Unset names removed, as statements before the command
// line, not as `env` in front of its first command (which failed with
// "env: 'source': No such file or directory", or bound the variable to
// `source` alone). The login profile exports values of its own for every
// name. The CLI is a fake script; no real agent runs.
func TestRunEnvReachesCompoundShellCommands(t *testing.T) {
	t.Setenv("ANTHROPIC_BASE_URL", "http://daemon-value")
	t.Setenv("GH_TOKEN", "daemon-token")
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.WriteFile(filepath.Join(home, ".profile"),
		[]byte("export ANTHROPIC_BASE_URL=http://from-profile\nexport GH_TOKEN=from-profile\n"), 0o644))
	setup := filepath.Join(home, "setup.sh")
	require.NoError(t, os.WriteFile(setup, []byte("SETUP_RAN=1; export SETUP_RAN\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(home, "sub"), 0o755))
	type tc struct{ name, shell, command string }
	cases := []tc{
		{"dot", "/bin/sh", ". " + setup + " && claude"},
		{"cd", "/bin/sh", "cd " + filepath.Join(home, "sub") + " && claude"},
		{"exec", "/bin/sh", "exec claude"},
		{"function", "/bin/sh", `run() { claude "$@"; }; run`},
	}
	if _, err := os.Stat("/bin/bash"); err == nil {
		cases = append(cases, tc{"bash-source", "/bin/bash", "source " + setup + " && claude"})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("SHELL", c.shell)
			dir := t.TempDir()
			shim := filepath.Join(dir, "shim")
			require.NoError(t, os.MkdirAll(shim, 0o755))
			envFile := filepath.Join(dir, "env.out")
			script := "#!/bin/sh\nenv > " + envFile + "\n" +
				"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"session_id\":\"s1\"}'\n"
			require.NoError(t, os.WriteFile(filepath.Join(shim, "claude"), []byte(script), 0o755))
			ctx := agent.WithRunEnv(context.Background(), agent.RunEnv{PathPrepend: shim, Unset: []string{"GH_TOKEN"}})
			ctx = agent.WithSetEnv(ctx, "ANTHROPIC_BASE_URL=http://127.0.0.1:8000")
			res, runErr := agent.NewClaudeRunner().RunTurn(ctx, slog.Default(), nil, nil, "hi", dir, c.command, "", "", 30000, 60000, agent.PermissionBypass)
			data, err := os.ReadFile(envFile)
			require.NoError(t, err, "the CLI ran: %v %s", runErr, res.FailureText)
			env := map[string]string{}
			for _, line := range strings.Split(string(data), "\n") {
				if k, v, ok := strings.Cut(line, "="); ok {
					env[k] = v
				}
			}
			assert.Equal(t, "http://127.0.0.1:8000", env["ANTHROPIC_BASE_URL"])
			_, hasToken := env["GH_TOKEN"]
			assert.False(t, hasToken, "an Unset name reached the CLI")
			assert.True(t, strings.HasPrefix(env["PATH"], shim+":"), "PATH starts with the shims: %s", env["PATH"])
		})
	}
}

// TestExportStatement: a statement that quotes the value and ends the
// command line when the variable cannot be set.
func TestExportStatement(t *testing.T) {
	assert.Equal(t, `export ANTHROPIC_BASE_URL='http://h:1/a b'\''c' || exit 1; `,
		agent.ExportStatement("ANTHROPIC_BASE_URL=http://h:1/a b'c"))
}
