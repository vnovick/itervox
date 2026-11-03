//go:build unix

package agent

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
)

// Fake-CLI end-to-end tests for the OpenCode, Cursor and custom runners.
// No real agent CLI runs: each fake is a shell script that records its
// argv, environment and prompt and prints a scripted event stream.

// fakeBackendCLI is a fake agent CLI and where it records what it got.
type fakeBackendCLI struct {
	exe, out string
}

func (f fakeBackendCLI) read(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.out, name))
	require.NoError(t, err, "the fake CLI recorded no %s", name)
	return string(data)
}

// argv is the recorded argv, one argument per element.
func (f fakeBackendCLI) argv(t *testing.T) []string {
	t.Helper()
	return strings.Split(strings.TrimSuffix(f.read(t, "argv"), "\n"), "\n")
}

// writeFakeBackendCLI writes a fake CLI that records its argv, environment
// and prompt (its last argument when promptArg, else its stdin), prints
// stream to stdout, and exits with exit.
func writeFakeBackendCLI(t *testing.T, stream string, exit int, promptArg bool) fakeBackendCLI {
	t.Helper()
	dir, out := t.TempDir(), t.TempDir()
	streamFile := filepath.Join(dir, "stream.jsonl")
	require.NoError(t, os.WriteFile(streamFile, []byte(stream), 0o644))
	readPrompt := "cat > " + ShellQuote(filepath.Join(out, "prompt"))
	if promptArg {
		readPrompt = `for a in "$@"; do last=$a; done; printf %s "$last" > ` + ShellQuote(filepath.Join(out, "prompt"))
	}
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %s\nenv > %s\n%s\ncat %s\nexit %d\n",
		ShellQuote(filepath.Join(out, "argv")), ShellQuote(filepath.Join(out, "env")), readPrompt, ShellQuote(streamFile), exit)
	exe := filepath.Join(dir, "fake-cli")
	require.NoError(t, os.WriteFile(exe, []byte(script), 0o755))
	return fakeBackendCLI{exe: exe, out: out}
}

// backendScenario is one scripted turn of one backend.
type backendScenario struct {
	stream string
	exit   int
	check  func(t *testing.T, res TurnResult, err error)
}

// backendRunnerCase is one backend with its scenarios.
type backendRunnerCase struct {
	name      string
	runner    func() Runner
	promptArg bool
	// resumeCheck asserts how a continuation passed the session id.
	resumeCheck func(t *testing.T, f fakeBackendCLI)
	scenarios   map[string]backendScenario
}

func assertSucceeded(t *testing.T, res TurnResult, err error) {
	t.Helper()
	require.NoError(t, err)
	assert.False(t, res.Failed, res.FailureText)
}

func assertFailedWith(text string) func(t *testing.T, res TurnResult, err error) {
	return func(t *testing.T, res TurnResult, err error) {
		t.Helper()
		require.NoError(t, err, "a failed turn is a result, not a runner error")
		assert.True(t, res.Failed)
		assert.Contains(t, res.FailureText, text)
	}
}

func assertQuotaLimited(t *testing.T, res TurnResult, err error) {
	t.Helper()
	require.NoError(t, err)
	assert.True(t, res.Failed)
	require.NotNil(t, res.LastLimit, "the limit must reach TurnResult.LastLimit")
	assert.True(t, res.LastLimit.Terminal(), "a usage limit is a quota (terminal) signal")
}

func assertInputRequired(t *testing.T, res TurnResult, err error) {
	t.Helper()
	require.NoError(t, err)
	assert.False(t, res.Failed)
	assert.True(t, res.InputRequired)
}

func backendRunnerCases() []backendRunnerCase {
	const usageLimit = "You've hit your usage limit. Try again later."
	return []backendRunnerCase{
		{
			name: "opencode", runner: func() Runner { return NewOpenCodeRunner() },
			resumeCheck: func(t *testing.T, f fakeBackendCLI) {
				assert.Equal(t, []string{"run", "--format", "json", "--session", "ses_prev", "--auto"}, f.argv(t))
			},
			scenarios: map[string]backendScenario{
				"success": {stream: `{"type":"step_start","sessionID":"ses_1"}
{"type":"text","sessionID":"ses_1","part":{"type":"text","text":"all done"}}
{"type":"step_finish","sessionID":"ses_1","part":{"type":"step-finish","cost":0.5,"tokens":{"input":10,"output":5,"cache":{"read":2}}}}
`, check: func(t *testing.T, res TurnResult, err error) {
					assertSucceeded(t, res, err)
					assert.Equal(t, "ses_1", res.SessionID)
					assert.Equal(t, "all done", res.LastText)
					assert.Equal(t, 15, res.TotalTokens)
					require.NotNil(t, res.CostUSD)
					assert.InDelta(t, 0.5, *res.CostUSD, 1e-9)
				}},
				"question": {stream: `{"type":"step_start","sessionID":"ses_1"}
{"type":"text","sessionID":"ses_1","part":{"type":"text","text":"Which port?\n` + `<!-- itervox:needs-input -->"}}
`, check: assertInputRequired},
				"failure": {stream: `{"type":"error","sessionID":"ses_1","error":{"type":"unknown","message":"provider exploded"}}
`, exit: 1, check: assertFailedWith("provider exploded")},
				"rate_limit": {stream: `{"type":"error","sessionID":"ses_1","error":{"name":"APIError","data":{"message":"` + usageLimit + `","statusCode":429}}}
`, exit: 1, check: assertQuotaLimited},
			},
		},
		{
			name: "cursor", runner: func() Runner { return NewCursorRunner() }, promptArg: true,
			resumeCheck: func(t *testing.T, f fakeBackendCLI) {
				assert.Equal(t, []string{"-p", "--output-format", "stream-json", "--resume", "ses_prev", "--force", "--trust", "--", "fix it"}, f.argv(t))
			},
			scenarios: map[string]backendScenario{
				"success": {stream: `{"type":"system","subtype":"init","session_id":"cur-1"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"all done"}]},"session_id":"cur-1"}
{"type":"result","subtype":"success","is_error":false,"result":"all done","session_id":"cur-1"}
`, check: func(t *testing.T, res TurnResult, err error) {
					assertSucceeded(t, res, err)
					assert.Equal(t, "cur-1", res.SessionID)
					assert.Equal(t, "all done", res.ResultText)
				}},
				"question": {stream: `{"type":"system","subtype":"init","session_id":"cur-1"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Which port?\n` + `<!-- itervox:needs-input -->"}]},"session_id":"cur-1"}
{"type":"result","subtype":"success","is_error":false,"result":"Which port?","session_id":"cur-1"}
`, check: assertInputRequired},
				"failure": {stream: `{"type":"result","subtype":"error","is_error":true,"result":"workspace is not trusted","session_id":"cur-1"}
`, exit: 1, check: assertFailedWith("workspace is not trusted")},
				"rate_limit": {stream: `{"type":"result","subtype":"error","is_error":true,"result":"` + usageLimit + `","session_id":"cur-1"}
`, exit: 1, check: assertQuotaLimited},
			},
		},
		{
			name: "custom", runner: func() Runner { return NewCustomRunner() },
			resumeCheck: func(t *testing.T, f fakeBackendCLI) {
				env := f.read(t, "env")
				assert.Contains(t, env, "ITERVOX_SESSION_ID=ses_prev\n")
				assert.Contains(t, env, "ITERVOX_AGENT_PROTOCOL=1\n")
				assert.Contains(t, env, "ITERVOX_PERMISSION_MODE=bypass\n")
			},
			scenarios: map[string]backendScenario{
				"success": {stream: `{"type":"session","session_id":"cus-1"}
{"type":"text","text":"all done"}
{"type":"usage","input_tokens":10,"output_tokens":5,"cost_usd":0.5}
{"type":"done","result":"summary"}
`, check: func(t *testing.T, res TurnResult, err error) {
					assertSucceeded(t, res, err)
					assert.Equal(t, "cus-1", res.SessionID)
					assert.Equal(t, "summary", res.ResultText)
					require.NotNil(t, res.CostUSD)
					assert.InDelta(t, 0.5, *res.CostUSD, 1e-9)
				}},
				"question": {stream: `{"type":"session","session_id":"cus-1"}
{"type":"input_required","question":"Which port?"}
{"type":"done"}
`, check: func(t *testing.T, res TurnResult, err error) {
					assertInputRequired(t, res, err)
					assert.Equal(t, "Which port?", res.LastText, "the question is the input-required context")
				}},
				"failure": {stream: `{"type":"failed","reason":"wrapper gave up"}
`, exit: 1, check: assertFailedWith("wrapper gave up")},
				"rate_limit": {stream: `{"type":"rate_limit","kind":"quota","message":"monthly cap reached"}
{"type":"failed","reason":"monthly cap reached"}
`, exit: 1, check: assertQuotaLimited},
				"exit_75": {stream: `{"type":"text","text":"stopping"}
`, exit: 75, check: assertQuotaLimited},
			},
		},
	}
}

// TestBackendRunnersFakeCLIScenarios runs every scenario of every new
// backend through its runner, on the direct path and the login-shell path:
// success, a question, a failure and a rate limit, with the prompt reaching
// the CLI (stdin, or the last argument for Cursor).
func TestBackendRunnersFakeCLIScenarios(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no real login profile runs
	for _, bc := range backendRunnerCases() {
		for name, sc := range bc.scenarios {
			for _, path := range []string{"direct", "login_shell"} {
				t.Run(bc.name+"/"+name+"/"+path, func(t *testing.T) {
					f := writeFakeBackendCLI(t, sc.stream, sc.exit, bc.promptArg)
					command := f.exe
					if path == "login_shell" {
						t.Setenv("SHELL", "/bin/sh")
						command = "ITERVOX_TEST=1 " + f.exe
					}
					res, err := bc.runner().RunTurn(context.Background(), slog.Default(), nil, nil,
						"fix it", t.TempDir(), command, "", "", 10000, 30000, PermissionBypass)
					sc.check(t, res, err)
					assert.Equal(t, "fix it", f.read(t, "prompt"), "the prompt must reach the CLI")
					assert.Contains(t, f.read(t, "env"), "ITERVOX_AGENT=1\n")
				})
			}
		}
	}
}

// TestBackendRunnersResumeWithSessionID: a continuation passes the stored
// session id the way each CLI resumes (OpenCode --session, Cursor --resume,
// custom ITERVOX_SESSION_ID), and the result keeps that id when the stream
// does not repeat it.
func TestBackendRunnersResumeWithSessionID(t *testing.T) {
	for _, bc := range backendRunnerCases() {
		t.Run(bc.name, func(t *testing.T) {
			f := writeFakeBackendCLI(t, "", 0, bc.promptArg)
			sid := "ses_prev"
			res, err := bc.runner().RunTurn(context.Background(), slog.Default(), nil, &sid,
				"fix it", t.TempDir(), f.exe, "", "", 10000, 30000, PermissionBypass)
			assertSucceeded(t, res, err)
			bc.resumeCheck(t, f)
			assert.Equal(t, "ses_prev", res.SessionID)
		})
	}
}

// TestOpenCodeCommandFlagsBelongToRun: flags in the configured command are
// `opencode run` flags — "run" goes right after the program on both paths,
// and sandbox mode drops --auto.
func TestOpenCodeCommandFlagsBelongToRun(t *testing.T) {
	assert.Equal(t, "FOO=1 opencode run --model a/b --format json",
		openCodeTurn("FOO=1 opencode --model a/b", nil, PermissionSandbox, &openCodeStream{}).shellCmd(""))
	assert.Equal(t, "env -i opencode run --format json --auto",
		openCodeTurn("env -i opencode", nil, PermissionBypass, &openCodeStream{}).shellCmd(""))
	assert.Equal(t, "opencode run --format json --session 'a b' --auto",
		openCodeTurn("", ptr("a b"), PermissionBypass, &openCodeStream{}).shellCmd(""))

	f := writeFakeBackendCLI(t, "", 0, false)
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("HOME", t.TempDir())
	_, err := NewOpenCodeRunner().RunTurn(context.Background(), slog.Default(), nil, nil,
		"p", t.TempDir(), f.exe+" --model prov/m", "", "", 10000, 30000, PermissionSandbox)
	require.NoError(t, err)
	assert.Equal(t, []string{"run", "--model", "prov/m", "--format", "json"}, f.argv(t))
}

// TestCustomRunnerNeedsACommand: a custom backend with no command fails
// the turn before anything starts.
func TestCustomRunnerNeedsACommand(t *testing.T) {
	res, err := NewCustomRunner().RunTurn(context.Background(), slog.Default(), nil, nil,
		"p", t.TempDir(), "  ", "", "", 1000, 1000, PermissionBypass)
	require.ErrorIs(t, err, errCustomNoCommand)
	assert.True(t, res.Failed)
}

// TestCustomRunnerSandboxModeReachesTheAgent: the profile's permission mode
// is the agent's ITERVOX_PERMISSION_MODE, and a fresh turn has no
// ITERVOX_SESSION_ID.
func TestCustomRunnerSandboxModeReachesTheAgent(t *testing.T) {
	f := writeFakeBackendCLI(t, `{"type":"done"}`+"\n", 0, false)
	_, err := NewCustomRunner().RunTurn(context.Background(), slog.Default(), nil, nil,
		"p", t.TempDir(), f.exe, "", "", 10000, 30000, PermissionSandbox)
	require.NoError(t, err)
	env := f.read(t, "env")
	assert.Contains(t, env, "ITERVOX_PERMISSION_MODE=sandbox\n")
	assert.NotContains(t, env, "ITERVOX_SESSION_ID=")
}

// TestBackendCostIsCumulativePerSession: two turns of one session report
// the session's running cost, as Claude's total_cost_usd does (CORE-091).
func TestBackendCostIsCumulativePerSession(t *testing.T) {
	f := writeFakeBackendCLI(t, `{"type":"session","session_id":"s"}
{"type":"usage","cost_usd":0.25}
{"type":"done"}
`, 0, false)
	r := NewCustomRunner()
	var last float64
	for range 2 {
		res, err := r.RunTurn(context.Background(), slog.Default(), nil, nil,
			"p", t.TempDir(), f.exe, "", "", 10000, 30000, PermissionBypass)
		assertSucceeded(t, res, err)
		require.NotNil(t, res.CostUSD)
		last = *res.CostUSD
	}
	assert.InDelta(t, 0.5, last, 1e-9)
}

// TestCursorShellPromptArgHasLeadingDash: a prompt that starts with "-"
// reaches the Cursor CLI as the operand after "--", on both paths.
func TestCursorShellPromptArgHasLeadingDash(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SHELL", "/bin/sh")
	for _, command := range []func(string) string{
		func(exe string) string { return exe },
		func(exe string) string { return "ITERVOX_TEST=1 " + exe },
	} {
		f := writeFakeBackendCLI(t, `{"type":"result","is_error":false,"result":"ok","session_id":"c"}`+"\n", 0, true)
		res, err := NewCursorRunner().RunTurn(context.Background(), slog.Default(), nil, nil,
			"--help me", t.TempDir(), command(f.exe), "", "", 10000, 30000, PermissionBypass)
		assertSucceeded(t, res, err)
		argv := f.argv(t)
		assert.Equal(t, []string{"--", "--help me"}, argv[len(argv)-2:])
	}
}

// TestMultiRunnerRoutesNewBackends: a backend hint or the CLI's binary
// name routes to the right runner, and an unknown backend falls back to the
// default.
func TestMultiRunnerRoutesNewBackends(t *testing.T) {
	rec := map[string]*recordingRunner{}
	runners := map[string]Runner{}
	for _, b := range []string{"codex", "opencode", "cursor", "custom"} {
		rec[b] = &recordingRunner{}
		runners[b] = rec[b]
	}
	def := &recordingRunner{}
	m := NewMultiRunner(def, runners)
	cases := []struct{ command, want, gotCommand string }{
		{"opencode --model a/b", "opencode", "opencode --model a/b"},
		{"/usr/local/bin/cursor-agent", "cursor", "/usr/local/bin/cursor-agent"},
		{"@@itervox-backend=custom ./my-agent.sh --x", "custom", "./my-agent.sh --x"},
		{"@@itervox-backend=cursor agent", "cursor", "agent"},
		{"@@itervox-backend=nope ./x", "default", "./x"},
		{"claude", "default", "claude"},
	}
	for _, tc := range cases {
		_, _ = m.RunTurn(context.Background(), slog.Default(), nil, nil, "p", "", tc.command, "", "", 1, 1, PermissionBypass)
		got := "default"
		r := def
		for b, rr := range rec {
			if rr.command != "" {
				got, r = b, rr
			}
		}
		assert.Equal(t, tc.want, got, tc.command)
		assert.Equal(t, tc.gotCommand, r.command, tc.command)
		for _, rr := range rec {
			rr.command = ""
		}
		def.command = ""
	}
	assert.Len(t, BackendRunners(), 4, "every backend but claude has a runner")
}

type recordingRunner struct{ command string }

func (r *recordingRunner) RunTurn(_ context.Context, _ Logger, _ func(TurnResult), _ *string, _, _, command, _, _ string, _, _ int, _ PermissionMode) (TurnResult, error) {
	r.command = command
	return TurnResult{}, nil
}

func ptr(s string) *string { return &s }

// TestCustomExampleWrapperSpeaksProtocol runs the documented example
// wrapper (docs/examples/custom-agent.sh) through the custom runner with a
// stand-in tool: its events parse, a continuation keeps the session id, and
// a failing tool fails the turn.
func TestCustomExampleWrapperSpeaksProtocol(t *testing.T) {
	wrapper, err := filepath.Abs(filepath.Join("..", "..", "docs", "examples", "custom-agent.sh"))
	require.NoError(t, err)
	t.Setenv("CUSTOM_AGENT_TOOL", "tr a-z A-Z")
	res, err := NewCustomRunner().RunTurn(context.Background(), slog.Default(), nil, nil,
		"say \"hi\"\tthere\nline two", t.TempDir(), wrapper, "", "", 10000, 30000, PermissionBypass)
	assertSucceeded(t, res, err)
	assert.Equal(t, []string{"SAY \"HI\"\tTHERE", "LINE TWO"}, res.AllTextBlocks)
	assert.Equal(t, 1, res.ToolCalls)
	assert.NotEmpty(t, res.SessionID)

	sid := res.SessionID
	res, err = NewCustomRunner().RunTurn(context.Background(), slog.Default(), nil, &sid,
		"again", t.TempDir(), wrapper, "", "", 10000, 30000, PermissionBypass)
	assertSucceeded(t, res, err)
	assert.Equal(t, sid, res.SessionID, "a continuation keeps the session")

	t.Setenv("CUSTOM_AGENT_TOOL", "exit 3")
	res, err = NewCustomRunner().RunTurn(context.Background(), slog.Default(), nil, nil,
		"x", t.TempDir(), wrapper, "", "", 10000, 30000, PermissionBypass)
	require.NoError(t, err)
	assert.True(t, res.Failed)
	assert.Contains(t, res.FailureText, "exit 3 exited 3")
}

// TestListOpenCodeModelsFromCLI: `opencode models` output (provider/model
// per line) becomes the model list, sorted and de-duplicated; a missing or
// failing CLI gives none.
func TestListOpenCodeModelsFromCLI(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\n[ \"$1\" = models ] || exit 2\nprintf 'openai/gpt-5\\nanthropic/claude-sonnet-4-5\\n\\nWarning: banner line\\nopenai/gpt-5\\n'\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "opencode"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	assert.Equal(t, []ModelOption{
		{ID: "anthropic/claude-sonnet-4-5", Label: "anthropic/claude-sonnet-4-5"},
		{ID: "openai/gpt-5", Label: "openai/gpt-5"},
	}, ListOpenCodeModels())

	require.NoError(t, os.WriteFile(filepath.Join(dir, "opencode"), []byte("#!/bin/sh\nexit 1\n"), 0o755))
	assert.Nil(t, ListOpenCodeModels())
	t.Setenv("PATH", t.TempDir())
	assert.Nil(t, ListOpenCodeModels())
}
