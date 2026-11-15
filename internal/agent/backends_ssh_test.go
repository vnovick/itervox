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

// writeBackendProbe writes a fake agent CLI for the SSH tests: it records
// its working directory, environment, argv and prompt (its last argument
// when promptArg, else its stdin) into outDir and prints stream.
func writeBackendProbe(t *testing.T, dir, outDir, stream string, promptArg bool) string {
	t.Helper()
	exe := filepath.Join(dir, "backend-probe")
	readPrompt := fmt.Sprintf("cat > %s/prompt", outDir)
	if promptArg {
		readPrompt = fmt.Sprintf(`for a in "$@"; do last=$a; done; printf %%s "$last" > %s/prompt`, outDir)
	}
	script := fmt.Sprintf("#!/bin/sh\npwd -P > %[1]s/pwd\nenv > %[1]s/env\nprintf '%%s\\n' \"$@\" > %[1]s/argv\n%[2]s\ncat <<'EOF'\n%[3]s\nEOF\n",
		outDir, readPrompt, stream)
	require.NoError(t, os.WriteFile(exe, []byte(script), 0o755))
	return exe
}

// TestBackendRunnersSSHLaunchPath: an OpenCode, Cursor or custom turn on an
// SSH worker runs through the same remote wrapper as claude/codex: in the
// workspace, with the ITERVOX_AGENT marker (and the custom protocol's
// variables) exported, the prompt delivered (stdin, or Cursor's last
// argument), and a continuation's session id passed.
func TestBackendRunnersSSHLaunchPath(t *testing.T) {
	cases := []struct {
		name      string
		runner    agent.Runner
		promptArg bool
		stream    string
		wantArgv  []string
		wantEnv   []string
	}{
		{"opencode", agent.NewOpenCodeRunner(), false,
			`{"type":"step_start","sessionID":"ses_r"}` + "\n" + `{"type":"text","sessionID":"ses_r","part":{"text":"remote ok"}}`,
			[]string{"run", "--format", "json", "--session", "sid-1", "--auto"}, nil},
		{"cursor", agent.NewCursorRunner(), true,
			`{"type":"result","is_error":false,"result":"remote ok","session_id":"cur_r"}`,
			[]string{"-p", "--output-format", "stream-json", "--resume", "sid-1", "--force", "--trust", "--", "it's a \"prompt\"\nline 2"}, nil},
		{"custom", agent.NewCustomRunner(), false,
			`{"type":"session","session_id":"cus_r"}` + "\n" + `{"type":"done","result":"remote ok"}`,
			nil, []string{"ITERVOX_AGENT_PROTOCOL=1", "ITERVOX_SESSION_ID=sid-1", "ITERVOX_PERMISSION_MODE=bypass"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			installFaithfulFakeSSH(t, "/bin/bash")
			bin, out, ws := t.TempDir(), t.TempDir(), t.TempDir()
			exe := writeBackendProbe(t, bin, out, tc.stream, tc.promptArg)
			sid := "sid-1"
			prompt := "it's a \"prompt\"\nline 2"

			res, err := tc.runner.RunTurn(context.Background(), slog.Default(), nil, &sid, prompt, ws, exe,
				"worker.example.test", "", 30000, 60000, agent.PermissionBypass)
			require.NoError(t, err)
			require.False(t, res.Failed, res.FailureText)

			read := func(name string) string {
				data, err := os.ReadFile(filepath.Join(out, name))
				require.NoError(t, err, "the remote agent never started")
				return string(data)
			}
			wantWs, _ := filepath.EvalSymlinks(ws)
			assert.Equal(t, wantWs, strings.TrimSpace(read("pwd")), "the remote agent runs in the workspace")
			assert.Equal(t, prompt, read("prompt"), "the prompt reaches the remote agent byte-exact")
			env := read("env")
			assert.Contains(t, env, "ITERVOX_AGENT=1\n")
			for _, kv := range tc.wantEnv {
				assert.Contains(t, env, kv+"\n")
			}
			if tc.wantArgv != nil {
				assert.Equal(t, strings.Join(tc.wantArgv, "\n")+"\n", read("argv"))
			}
			assert.Contains(t, res.LastText+res.ResultText, "remote ok")
		})
	}
}
