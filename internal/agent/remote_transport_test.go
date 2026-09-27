package agent_test

// M1-B2 fix round 2 (I1, I2): the remote script travels on ssh's stdin and
// the single ssh remote-command argument is a fixed wrapper, so no prompt
// byte is ever parsed by the remote login shell (csh/tcsh included) and the
// argument no longer grows with the prompt.

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent"
)

// codeLikePrompt returns n bytes of quote- and backslash-heavy source text,
// the shape of SOUL + INSTRUCTIONS + handoffs full of code.
func codeLikePrompt(n int) string {
	const unit = "fmt.Printf(\"it's %q\\n\", s) // don't `eval` $(x) \\ 'y' && z || !w; path='C:\\\\tmp'\n"
	return strings.Repeat(unit, n/len(unit)+1)[:n]
}

// adversarialPrompts is fix round 2's prompt matrix.
func adversarialPrompts() []struct{ name, prompt string } {
	var ctl strings.Builder
	for c := byte(1); c < 0x20; c++ {
		ctl.WriteByte(c)
	}
	ctl.WriteByte(0x7f)
	return []struct{ name, prompt string }{
		{"hazards", "it's \"q\" \\ \\\\ \\' $HOME ${USER} $(id) `id` !x !! * ? [a] ~ ; && || | > < & ( ) { } # %s\nline2\r\nline3\u2028é 日本 🚀 '' ''' end\\"},
		{"csh_newline_injection", "Please fix the bug.\ntouch PWNED_MARKER\nThanks"},
		{"control_bytes", "ctl:" + ctl.String() + ":end"},
		{"leading_dash", "-n -e --help"},
		{"empty", ""},
		{"200KiB_code_like", codeLikePrompt(200 << 10)},
		{"200KiB_single_quotes", strings.Repeat("'", 200<<10)},
		{"200KiB_backslashes", strings.Repeat("\\", 200<<10)},
	}
}

// pathologicalSizeRow reports the 200 KiB all-quote/all-backslash rows.
func pathologicalSizeRow(name string) bool {
	return name == "200KiB_single_quotes" || name == "200KiB_backslashes"
}

// TestSSHRemoteCommandRoundTripsThroughLoginShells runs the adversarial
// matrix through RunTurn → faithful fake ssh → each login shell → bash, and
// requires, per row: the agent started in the workspace with the marker in
// its environment (so every command of the script ran, not just the first —
// fix round 2 M2: this now fails with the split form), its prompt argument is
// byte-exact, and no prompt line was executed as a command.
func TestSSHRemoteCommandRoundTripsThroughLoginShells(t *testing.T) {
	requireSSHMatrix(t) // CORE-172: slow login-shell matrix
	// The runner logs every stdout line it cannot parse through the global
	// slog at Debug ("agent: raw line"). The probe agent prints exactly one
	// valid event, so ANY raw line means something other than the agent
	// wrote to the stream — e.g. the split form's `bash -lc set` variable
	// dump (fix round 2, M2: this is what makes the test fail when the
	// split form is restored, whatever the login shell). Not parallel:
	// slog.SetDefault is process-global.
	var global bytes.Buffer
	var globalMu sync.Mutex
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&lockedWriter{w: &global, mu: &globalMu}, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	for _, shell := range availableLoginShells() {
		for _, rc := range remoteRunnerCases() {
			for _, pc := range adversarialPrompts() {
				if pathologicalSizeRow(pc.name) && shell != "/bin/sh" && shell != "/bin/tcsh" {
					// The login shell never sees these bytes (they travel on
					// stdin), and bash's own evaluation of 200 KiB of ' or \
					// costs ~3.6s a row; one POSIX and one csh-family shell
					// keep the suite fast. Fix round 2's evidence run
					// (rounds.md) covered every shell.
					continue
				}
				t.Run(shellLabel(shell)+"/"+rc.name+"/"+pc.name, func(t *testing.T) {
					installFaithfulFakeSSH(t, shell)
					bin, out, ws := t.TempDir(), t.TempDir(), t.TempDir()
					exe := writeProbeAgent(t, bin, out, rc.firstLine)
					prompt := strings.ReplaceAll(pc.prompt, "PWNED_MARKER", filepath.Join(out, "PWNED"))

					globalMu.Lock()
					global.Reset()
					globalMu.Unlock()
					result, err := rc.runner.RunTurn(context.Background(), slog.Default(), nil,
						nil, prompt, ws, exe, "worker.example.test", "",
						30000, 60000, agent.PermissionBypass)
					require.NoError(t, err)
					require.False(t, result.Failed, "FailureText: %.300s", result.FailureText)
					globalMu.Lock()
					streamLog := global.String()
					globalMu.Unlock()
					assert.NotContains(t, streamLog, "agent: raw line", "the agent stream must carry only the agent's own output")

					_, pwned := os.Stat(filepath.Join(out, "PWNED"))
					assert.True(t, os.IsNotExist(pwned), "a prompt line was executed as a command on the worker")
					got, err := os.ReadFile(filepath.Join(out, "prompt"))
					require.NoError(t, err, "the agent never started")
					assert.True(t, prompt == string(got), "prompt on stdin differs: want %d bytes, got %d bytes", len(prompt), len(got))
					gotPwd, _ := os.ReadFile(filepath.Join(out, "pwd"))
					wantWs, _ := filepath.EvalSymlinks(ws)
					assert.Equal(t, wantWs, strings.TrimSpace(string(gotPwd)), "every command of the script must run (cd, then the agent)")
					env, _ := os.ReadFile(filepath.Join(out, "env"))
					assert.Contains(t, string(env), "ITERVOX_AGENT=1\n")
				})
			}
		}
	}
}

// TestSSHPromptNewlineCannotInjectUnderCsh is I1's named reproduction: under
// a csh/tcsh login shell a newline ends a single-quoted string, so with the
// prompt inside the ssh argument each later prompt line ran as a command on
// the worker and the agent never started.
func TestSSHPromptNewlineCannotInjectUnderCsh(t *testing.T) {
	for _, shell := range []string{"/bin/csh", "/bin/tcsh"} {
		if _, err := os.Stat(shell); err != nil {
			continue
		}
		for _, rc := range remoteRunnerCases() {
			t.Run(shellLabel(shell)+"/"+rc.name, func(t *testing.T) {
				installFaithfulFakeSSH(t, shell)
				bin, out, ws := t.TempDir(), t.TempDir(), t.TempDir()
				exe := writeProbeAgent(t, bin, out, rc.firstLine)
				pwned := filepath.Join(out, "PWNED")
				prompt := "Please fix the bug.\ntouch " + pwned + "\nThanks"

				_, _ = rc.runner.RunTurn(context.Background(), slog.Default(), nil,
					nil, prompt, ws, exe, "worker.example.test", "",
					30000, 60000, agent.PermissionBypass)

				_, injErr := os.Stat(pwned)
				_, startErr := os.Stat(filepath.Join(out, "started"))
				injected, started := injErr == nil, startErr == nil
				assert.False(t, injected, "injected=%v agentStarted=%v: a prompt line ran as a command on the worker", injected, started)
				assert.True(t, started, "injected=%v agentStarted=%v: the agent must start", injected, started)
			})
		}
	}
}

// TestSSHLargeQuoteHeavyPromptFits is I2's reproduction: 70 KiB of ' used
// to expand 16x across the two quoting layers and fail with "argument list
// too long". The prompt must reach the agent byte-exact.
func TestSSHLargeQuoteHeavyPromptFits(t *testing.T) {
	installFaithfulFakeSSH(t, "/bin/sh")
	for _, rc := range remoteRunnerCases() {
		t.Run(rc.name, func(t *testing.T) {
			bin, out, ws := t.TempDir(), t.TempDir(), t.TempDir()
			exe := writeProbeAgent(t, bin, out, rc.firstLine)
			prompt := strings.Repeat("'", 70<<10)

			result, err := rc.runner.RunTurn(context.Background(), slog.Default(), nil,
				nil, prompt, ws, exe, "worker.example.test", "",
				30000, 60000, agent.PermissionBypass)
			require.NoError(t, err)
			require.False(t, result.Failed, "FailureText: %.300s", result.FailureText)
			got, err := os.ReadFile(filepath.Join(out, "prompt"))
			require.NoError(t, err)
			assert.True(t, prompt == string(got), "prompt must arrive byte-exact (%d vs %d bytes)", len(prompt), len(got))
		})
	}
}

// safeRemoteArg is the shape of the one ssh remote-command argument: `bash`,
// flags, and a single-quoted wrapper with no ', \, !, or newline inside —
// the only characters whose meaning inside single quotes differs across sh,
// bash, zsh, dash, ksh, csh, tcsh and fish.
var safeRemoteArg = regexp.MustCompile(`^bash -l?c '[^'\\!\n\r]*'$`)

// TestSSHRemoteArgumentIsFixedSizeAndShellNeutral pins I1/I2 structurally:
// whatever the prompt, the argument is the fixed wrapper (well under 1 KiB)
// and contains no byte of the prompt.
func TestSSHRemoteArgumentIsFixedSizeAndShellNeutral(t *testing.T) {
	for _, rc := range remoteRunnerCases() {
		t.Run(rc.name, func(t *testing.T) {
			logPath := installRecordingFakeSSH(t)
			prompt := "SENTINEL-PROMPT " + codeLikePrompt(100<<10)
			_, _ = rc.runner.RunTurn(context.Background(), slog.Default(), nil,
				nil, prompt, t.TempDir(), "agent", "worker.example.test", "",
				30000, 60000, agent.PermissionBypass)
			rec, err := os.ReadFile(logPath)
			require.NoError(t, err)
			firstLine, stdin, _ := strings.Cut(string(rec), "\n")
			assert.Regexp(t, safeRemoteArg, firstLine)
			assert.Less(t, len(firstLine), 1024, "the ssh argument must not grow with the prompt")
			t.Logf("ssh remote-command argument: %d bytes for a %d-byte prompt (script on stdin: %d bytes)", len(firstLine), len(prompt), len(stdin))
			assert.NotContains(t, firstLine, "SENTINEL-PROMPT")
			assert.Contains(t, stdin, "SENTINEL-PROMPT", "the script (with the prompt) travels on stdin")
		})
	}
}

// TestSSHRemoteScriptTruncationFailsClosed: a remote login profile that
// reads stdin would eat the start of the script; the wrapper's byte-count
// check must refuse to run the remainder (which could otherwise start mid
// prompt and run prompt text as commands).
func TestSSHRemoteScriptTruncationFailsClosed(t *testing.T) {
	for _, rc := range remoteRunnerCases() {
		t.Run(rc.name, func(t *testing.T) {
			installFaithfulFakeSSH(t, "/bin/sh")
			t.Setenv("FAKE_SSH_EAT_STDIN_BYTES", "7")
			bin, out, ws := t.TempDir(), t.TempDir(), t.TempDir()
			exe := writeProbeAgent(t, bin, out, rc.firstLine)
			result, _ := rc.runner.RunTurn(context.Background(), slog.Default(), nil,
				nil, "hi", ws, exe, "worker.example.test", "",
				30000, 60000, agent.PermissionBypass)
			_, startErr := os.Stat(filepath.Join(out, "started"))
			assert.True(t, os.IsNotExist(startErr), "a truncated script must not run")
			assert.True(t, result.Failed)
			assert.Contains(t, result.FailureText, "arrived truncated")
		})
	}
}

// TestSSHPromptOverLinuxArgLimitNeedsNoCheck (CORE-156): the prompt never
// becomes an argument on the worker, so the wrapper no longer carries the
// Linux MAX_ARG_STRLEN refusal (exit 98) at, below or far above 131072
// bytes, and the argument stays the fixed shell-neutral wrapper.
func TestSSHPromptOverLinuxArgLimitNeedsNoCheck(t *testing.T) {
	for _, rc := range remoteRunnerCases() {
		for _, n := range []int{131071, 131072, 4 << 20} {
			t.Run(fmt.Sprintf("%s/%d", rc.name, n), func(t *testing.T) {
				logPath := installRecordingFakeSSH(t)
				_, _ = rc.runner.RunTurn(context.Background(), slog.Default(), nil,
					nil, strings.Repeat("x", n), t.TempDir(), "agent", "worker.example.test", "",
					30000, 60000, agent.PermissionBypass)
				rec, err := os.ReadFile(logPath)
				require.NoError(t, err)
				firstLine, _, _ := strings.Cut(string(rec), "\n")
				assert.Regexp(t, safeRemoteArg, firstLine)
				t.Logf("ssh remote-command argument: %d bytes for a %d-byte prompt", len(firstLine), n)
				assert.NotContains(t, firstLine, "MAX_ARG_STRLEN")
				assert.NotContains(t, firstLine, "exit 98")
			})
		}
	}
}

// TestSSHRemoteArgumentShapeForSublogFetch: the sublog fetch uses the same
// transport — a shell-neutral `bash -c` wrapper argument, script on stdin —
// even though its script holds no prompt (its directory path is operator
// input and is quoted inside the script only).
func TestSSHRemoteArgumentShapeForSublogFetch(t *testing.T) {
	logPath := installRecordingFakeSSH(t)
	_, _ = agent.SSHSublogFetcher{Host: "worker.example.test"}.FetchSubLogs(context.Background(), "/remote/it's logs")
	rec, err := os.ReadFile(logPath)
	require.NoError(t, err)
	lines := strings.SplitN(string(rec), "\n", 2)
	require.Len(t, lines, 2)
	assert.Regexp(t, safeRemoteArg, lines[0])
	assert.True(t, strings.HasPrefix(lines[0], "bash -c '"), "the sublog fetch runs bash without -l: %q", lines[0])
	assert.Contains(t, lines[1], "tar -cf - -C", "the fetch script travels on stdin")
}
