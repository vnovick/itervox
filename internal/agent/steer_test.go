package agent_test

// #178 step 2: operator messages reach a running Claude session mid-turn
// through its stream-json stdin (live steering), and wait for the next
// resumed turn otherwise.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent"
)

// writeStreamingClaude writes a fake claude that behaves like the real CLI
// in `--input-format stream-json` mode: it reads the prompt message, says
// "working", then BLOCKS until a second user message arrives on its stdin —
// the turn cannot finish without the operator's message — answers it,
// emits the result and reads stdin to EOF before exiting.
func writeStreamingClaude(t *testing.T, dir string) string {
	t.Helper()
	exe := filepath.Join(dir, "claude-streaming")
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$@" > %[1]s/argv
echo '{"type":"system","subtype":"init","session_id":"sess-live"}'
IFS= read -r first
printf '%%s\n' "$first" > %[1]s/first
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"working"}]},"session_id":"sess-live"}'
IFS= read -r second
printf '%%s\n' "$second" > %[1]s/second
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"operator message received"}]},"session_id":"sess-live"}'
echo '{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"sess-live"}'
cat > /dev/null
: > %[1]s/eof
`, dir)
	require.NoError(t, os.WriteFile(exe, []byte(script), 0o755))
	return exe
}

type userLine struct {
	Type    string `json:"type"`
	Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"message"`
}

func readUserLine(t *testing.T, path string) userLine {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var u userLine
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &u), "line %q", raw)
	return u
}

// runLiveSteeredTurn runs one live-steered claude turn with command and
// sends the operator message once the agent reports "working".
func runLiveSteeredTurn(t *testing.T, dir, command string) (agent.TurnResult, []agent.SteerMessage) {
	t.Helper()
	inbox := make(chan agent.SteerMessage, 4)
	var mu sync.Mutex
	var delivered []agent.SteerMessage
	steer := agent.NewSteering(inbox, true, func(m agent.SteerMessage) {
		mu.Lock()
		delivered = append(delivered, m)
		mu.Unlock()
	})
	var once sync.Once
	onProgress := func(r agent.TurnResult) {
		if r.LastText == "working" {
			once.Do(func() { inbox <- agent.SteerMessage{ID: "m1", Text: "please also update the docs"} })
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ctx = agent.WithSteering(ctx, steer)
	res, err := agent.NewClaudeRunner().RunTurn(ctx, testLogger(), onProgress, nil, "fix the bug", dir,
		command, "", "", 10_000, 0, agent.PermissionBypass)
	require.NoError(t, err)
	mu.Lock()
	defer mu.Unlock()
	return res, append([]agent.SteerMessage(nil), delivered...)
}

func testLogger() agent.Logger { return discardLogger{} }

type discardLogger struct{}

func (discardLogger) Info(string, ...any)  {}
func (discardLogger) Debug(string, ...any) {}
func (discardLogger) Warn(string, ...any)  {}

// TestOperatorMessageReachesRunningClaudeSessionMidTurn: with live steering
// the turn runs with `--input-format stream-json`, the prompt is the first
// user message, the operator's message is written into the live stdin while
// the turn runs (the fake cannot finish without it), and stdin is closed
// after the result so the CLI exits.
func TestOperatorMessageReachesRunningClaudeSessionMidTurn(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command func(exe string) string
	}{
		{"direct", func(exe string) string { return exe }},
		{"login_shell", func(exe string) string { return "/bin/sh " + exe }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			exe := writeStreamingClaude(t, dir)
			if tc.name == "login_shell" {
				t.Setenv("SHELL", "/bin/sh")
			}
			res, delivered := runLiveSteeredTurn(t, dir, tc.command(exe))

			assert.False(t, res.Failed, "failure: %s", res.FailureText)
			assert.Equal(t, "done", res.ResultText)
			assert.Equal(t, "sess-live", res.SessionID)
			assert.Contains(t, res.AllTextBlocks, "operator message received")

			argv, err := os.ReadFile(filepath.Join(dir, "argv"))
			require.NoError(t, err)
			assert.Contains(t, string(argv), "--input-format\nstream-json\n")

			first := readUserLine(t, filepath.Join(dir, "first"))
			assert.Equal(t, "user", first.Type)
			assert.Equal(t, "fix the bug", first.Message.Content)
			second := readUserLine(t, filepath.Join(dir, "second"))
			assert.Equal(t, "user", second.Type)
			assert.Equal(t, "user", second.Message.Role)
			assert.Equal(t, "please also update the docs", second.Message.Content)

			require.Len(t, delivered, 1)
			assert.Equal(t, "m1", delivered[0].ID)
			_, err = os.Stat(filepath.Join(dir, "eof"))
			assert.NoError(t, err, "stdin must reach EOF after the result so the CLI exits")
		})
	}
}

// TestSteeringWithoutLiveModeKeepsTheClassicPromptPath: with live steering
// off (the default) the turn is unchanged — the prompt file on stdin, no
// stream-json input — and a queued message stays for the next turn.
func TestSteeringWithoutLiveModeKeepsTheClassicPromptPath(t *testing.T) {
	dir := t.TempDir()
	out := t.TempDir()
	exe := writeLargePromptAgent(t, dir, out, `{"type":"result","subtype":"success","is_error":false,"result":"ok","session_id":"s1"}`)
	inbox := make(chan agent.SteerMessage, 1)
	inbox <- agent.SteerMessage{ID: "m1", Text: "later"}
	steer := agent.NewSteering(inbox, false, func(agent.SteerMessage) { t.Error("nothing is delivered live") })
	ctx := agent.WithSteering(context.Background(), steer)
	res, err := agent.NewClaudeRunner().RunTurn(ctx, testLogger(), nil, nil, "the prompt", dir,
		exe, "", "", 10_000, 0, agent.PermissionBypass)
	require.NoError(t, err)
	assert.Equal(t, "ok", res.ResultText)
	assertPromptByteExact(t, out, "the prompt")
	argv, err := os.ReadFile(filepath.Join(out, "argv"))
	require.NoError(t, err)
	assert.NotContains(t, string(argv), "--input-format")
	assert.Equal(t, []agent.SteerMessage{{ID: "m1", Text: "later"}}, steer.Drain())
	assert.Empty(t, steer.Drain())
}
