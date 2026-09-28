package agent_test

// M1-B2 round 3 follow-ups from the security review of the stdin transport:
//
//   - m1: a remote login profile that enables xtrace (set -x) or verbose
//     (set -v) must not copy the remote script — which can carry secrets
//     from agent.command and the prompt — into stderr, and from there into
//     FailureText, slog, the log buffer and the dashboard.
//   - m2: a NUL byte in the prompt must fail the turn in Go with an error
//     naming the NUL, identically on the SSH and the local path, before any
//     process is started.
//   - m3: every ssh invocation must pass -T (never -t/-tt), so a
//     `RequestTTY force` in ssh_config cannot allocate a PTY that echoes the
//     stdin script (and corrupts the sublog tar stream).
//
// Every SSH case runs through the faithful fake (ssh_fake_test.go); no
// network is touched.

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent"
)

// Sentinels that must never reach stderr/FailureText: one stands for a
// secret in agent.command (`VAR=secret claude`), one for issue text.
const (
	traceCommandSecret = "itervox-cmd-secret-5f2a91"
	tracePromptSecret  = "itervox-prompt-secret-c3e07b"
	traceProfileProbe  = "itervox-profile-probe"
)

// installTracingLoginProfile makes every bash the fake remote side starts
// run `set <mode>` first: the login shell's ~/.bash_profile (read by the
// wrapper's `bash -l`) and BASH_ENV (read by the non-interactive outer
// `bash -c` the fake uses as the login shell, and by bash -l as well). The
// profile also runs `: itervox-profile-probe`, whose trace proves the mode
// really was on (the assertions are not vacuous).
func installTracingLoginProfile(t *testing.T, mode string) {
	t.Helper()
	home := t.TempDir()
	body := "set " + mode + "\n: " + traceProfileProbe + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(home, ".bash_profile"), []byte(body), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(home, "env.sh"), []byte(body), 0o644))
	t.Setenv("HOME", home)
	t.Setenv("BASH_ENV", filepath.Join(home, "env.sh"))
}

// TestSSHWrapperStderrUnderTracingProfile (m1): the wrapper's stderr under
// a tracing login profile holds the profile's own trace and the login
// shell's trace of the (secret-free) wrapper line, but no byte of the script.
func TestSSHWrapperStderrUnderTracingProfile(t *testing.T) {
	for _, mode := range []string{"-x", "-v"} {
		t.Run(mode, func(t *testing.T) {
			installFaithfulFakeSSH(t, "/bin/bash")
			installTracingLoginProfile(t, mode)
			script := "ITERVOX_TEST_SECRET=" + traceCommandSecret + "; printf '%s\\n' ran-ok"
			arg, payload := agent.RemoteBashInvocationForTest("-lc", script)
			cmd := exec.Command("ssh", "-T", "worker.example.test", arg)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			join, err := agent.StartWithRemoteStdinForTest(cmd, payload)
			require.NoError(t, err)
			require.NoError(t, cmd.Wait(), "stderr: %s", stderr.String())
			join()
			assert.Equal(t, "ran-ok\n", stdout.String())

			se := stderr.String()
			t.Logf("stderr under set %s:\n%s", mode, se)
			assert.Contains(t, se, traceProfileProbe, "the profile's trace must be on, or this test proves nothing")
			// The login shell tracing/echoing its own -c line is expected and
			// harmless: that line is the fixed wrapper, which holds no script byte.
			assert.Contains(t, se, "bash -lc '", "the login shell's own trace of the wrapper line")
			assert.NotContains(t, se, traceCommandSecret, "the script must not be traced into stderr")
			assert.NotContains(t, se, "ran-ok", "no script text (not even the printf) may be traced")
		})
	}
}

// TestSSHTurnTracingProfileKeepsSecretsOutOfFailureText (m1, end to end):
// a crashing remote agent under a tracing login profile — FailureText keeps
// the agent's stderr (and the profile's trace) but neither the command's
// secret nor the prompt.
func TestSSHTurnTracingProfileKeepsSecretsOutOfFailureText(t *testing.T) {
	for _, mode := range []string{"-x", "-v"} {
		for _, rc := range remoteRunnerCases() {
			t.Run(mode+"/"+rc.name, func(t *testing.T) {
				installFaithfulFakeSSH(t, "/bin/bash")
				installTracingLoginProfile(t, mode)
				dir := t.TempDir()
				exe := writeCrashingAgent(t, dir, rc.name, rc.firstLine)
				command := "ITERVOX_TEST_SECRET=" + traceCommandSecret + " " + exe
				result, _ := rc.runner.RunTurn(context.Background(), slog.Default(), nil,
					nil, "fix it "+tracePromptSecret, dir, command, "worker.example.test", "",
					30000, 60000, agent.PermissionBypass)
				t.Logf("FailureText: %s", result.FailureText)
				assert.True(t, result.Failed)
				assert.Contains(t, result.FailureText, "remote agent crashed")
				assert.Contains(t, result.FailureText, traceProfileProbe, "the profile's trace must be on, or this test proves nothing")
				assert.NotContains(t, result.FailureText, traceCommandSecret)
				assert.NotContains(t, result.FailureText, tracePromptSecret)
			})
		}
	}
}

// TestRunTurnRejectsNULInPrompt (m2): exec argv cannot carry NUL, so a
// prompt holding one can never run. Both paths must refuse it in Go, with
// an error naming the NUL, before starting any process (the SSH path used
// to fail remotely with exit 97 "does a login profile read stdin?"; the
// local path with an opaque "fork/exec: invalid argument").
func TestRunTurnRejectsNULInPrompt(t *testing.T) {
	for _, rc := range remoteRunnerCases() {
		for _, host := range []string{"worker.example.test", ""} {
			name := rc.name + "/local"
			if host != "" {
				name = rc.name + "/ssh"
			}
			t.Run(name, func(t *testing.T) {
				logPath := installRecordingFakeSSH(t)
				bin, out, ws := t.TempDir(), t.TempDir(), t.TempDir()
				exe := writeProbeAgent(t, bin, out, rc.firstLine)
				result, err := rc.runner.RunTurn(context.Background(), slog.Default(), nil,
					nil, "before\x00after", ws, exe, host, "",
					30000, 60000, agent.PermissionBypass)
				require.Error(t, err)
				assert.Contains(t, err.Error(), "NUL byte")
				assert.Contains(t, err.Error(), "offset 6")
				assert.True(t, result.Failed)
				assert.Contains(t, result.FailureText, "NUL byte")
				_, statErr := os.Stat(logPath)
				assert.True(t, os.IsNotExist(statErr), "ssh must not be invoked")
				_, statErr = os.Stat(filepath.Join(out, "started"))
				assert.True(t, os.IsNotExist(statErr), "the agent must not start")
			})
		}
	}
}

// TestSSHInvocationsPassNoPTYFlag (m3): every ssh call site passes -T and
// never -t/-tt, so ssh_config `RequestTTY force` cannot put the stdin script
// through a terminal (echo, CRLF) or corrupt the sublog tar stream.
func TestSSHInvocationsPassNoPTYFlag(t *testing.T) {
	sites := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"claude", func(t *testing.T) {
			_, _ = agent.NewClaudeRunner().RunTurn(context.Background(), slog.Default(), nil,
				nil, "hi", t.TempDir(), "claude", "worker.example.test", "", 30000, 60000, agent.PermissionBypass)
		}},
		{"codex", func(t *testing.T) {
			_, _ = agent.NewCodexRunner().RunTurn(context.Background(), slog.Default(), nil,
				nil, "hi", t.TempDir(), "codex", "worker.example.test", "", 30000, 60000, agent.PermissionBypass)
		}},
		{"logtailer", func(t *testing.T) {
			_, _ = agent.SSHSublogFetcher{Host: "worker.example.test"}.FetchSubLogs(context.Background(), "/remote/logs")
		}},
	}
	for _, s := range sites {
		t.Run(s.name, func(t *testing.T) {
			installRecordingFakeSSH(t)
			argvLog := filepath.Join(t.TempDir(), "argv.log")
			t.Setenv("FAKE_SSH_ARGV_LOG", argvLog)
			s.run(t)
			rec, err := os.ReadFile(argvLog)
			require.NoError(t, err, "ssh was not invoked")
			argv, _, found := strings.Cut(string(rec), "--END--\n")
			require.True(t, found)
			args := strings.Split(strings.TrimSuffix(argv, "\n"), "\n")
			hostAt := -1
			for i, a := range args {
				if a == "worker.example.test" {
					hostAt = i
					break
				}
			}
			require.GreaterOrEqual(t, hostAt, 0, "host not found in argv %q", args)
			opts := args[:hostAt]
			assert.Contains(t, opts, "-T", "argv %q", args)
			for _, bad := range []string{"-t", "-tt"} {
				assert.NotContains(t, opts, bad, "argv %q", args)
			}
		})
	}
}
