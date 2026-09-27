package agent_test

// The one fake `ssh` for every SSH test in internal/agent (M1-B2 fix round 1).
// Its sshd half is the separate program testdata/fakesshd.
//
// Real OpenSSH does NOT pass its remote-command arguments through as argv:
// it joins every argument after the host with single spaces into ONE string,
// and sshd hands that string to the remote user's login shell as
// `$SHELL -c "<joined>"`, which parses it again. A fake that re-uses the
// separate words (the round-0 `exec /bin/bash -c "$1"` fake) hides exactly
// the bug class where `workerHost, "bash", "-lc", cmd` makes bash run only
// cmd's first word. installFaithfulFakeSSH models the join and the re-parse;
// installFakeSSHTransport models only the local ssh client failing or
// streaming raw bytes, where no remote command runs at all.
//
// sshd model (M1-B9, CORE-155). The remote side does NOT run inside the
// fake client's process: the client execs a fake sshd
// (testdata/fakesshd, built by TestMain), which starts `$LOGIN_SHELL -c "<joined>"` in
// a NEW SESSION (setsid) and relays the three streams over pipes, exactly
// the shape of sshd's session channel:
//   - the remote command is outside the local ssh client's process group,
//     so the CORE-001 group kill of the client does not touch it (on a real
//     worker it is on another machine);
//   - when the client process dies, the relay dies with it: the remote
//     stdin pipe's only writer closes (the remote reads EOF) and the remote
//     stdout/stderr pipes lose their only reader (the next remote write gets
//     EPIPE/SIGPIPE) — sshd closing the channel of a dead connection;
//   - no PTY, so no SIGHUP reaches the remote command (ssh -T);
//   - the client exits when the remote command has exited AND its stdout and
//     stderr have been drained to EOF (sshd keeps the channel open while a
//     remote descendant still holds them), whether or not the client's own
//     stdin is still open; its status is the remote status, or 255 when the
//     remote command died of a signal, like OpenSSH.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

// fakeRemoteSecret is exported into the fake remote environment. Any test
// that sees it in agent output has proven a remote shell-variable dump.
const fakeRemoteSecret = "itervox-fake-remote-secret-7c1d9e"

// faithfulFakeSSH drops ssh options and the host, joins the remaining
// arguments with single spaces exactly like OpenSSH, and hands the joined
// string to the fake sshd (testdata/fakesshd), which runs it under the configured
// "remote login shell" with -c, exactly like sshd. FAKE_SSH_REMOTE_LOG, when
// set, records each joined remote command. FAKE_SSH_NO_EXEC=1 records (the
// joined command, then the stdin payload up to its NUL terminator) and exits
// 0 without running anything. FAKE_SSH_EAT_STDIN_BYTES=<n> discards the
// first n bytes of stdin before the remote shell runs (a profile that reads
// stdin). FAKE_SSH_FAIL_IF_CONTAINS=<text> makes a connection whose joined
// remote command or stdin payload contains <text> fail like an ssh
// transport error (exit 255). FAKE_SSH_ARGV_LOG, when set, records the
// client's FULL argv (options included), one argument per line, then a
// "--END--" line (round 3, m3). FAKE_SSHD_SESSION_LOG, when set, records the
// pid of each remote session leader (the login shell; its pgid and sid).
// FAKE_SSHD_MODEL=dropbear switches to the Dropbear process model (M1-B9
// round 2, V2): one setsid'd connection-server process whose session
// channels share its process group, plus a sibling session process; the
// server writes "<server pid> <sibling pid>" to FAKE_SSHD_DROPBEAR_LOG.
const faithfulFakeSSH = `#!/bin/sh
if [ -n "$FAKE_SSH_ARGV_LOG" ]; then
  for a in "$@"; do printf '%s\n' "$a"; done >> "$FAKE_SSH_ARGV_LOG"
  printf '%s\n' --END-- >> "$FAKE_SSH_ARGV_LOG"
fi
while [ $# -gt 0 ]; do
  case "$1" in
    -o|-p|-i|-l|-F) shift 2 ;;
    -*) shift ;;
    *) break ;;
  esac
done
shift # host
if [ -n "$FAKE_SSH_REMOTE_LOG" ]; then printf '%s\n' "$*" >> "$FAKE_SSH_REMOTE_LOG"; fi
if [ -n "$FAKE_SSH_EAT_STDIN_BYTES" ]; then
  # Model a remote login profile that reads from stdin before the command
  # runs, consuming the start of whatever the client sent.
  dd bs=1 count="$FAKE_SSH_EAT_STDIN_BYTES" of=/dev/null 2>/dev/null
fi
ITERVOX_FAKE_REMOTE_SECRET=` + fakeRemoteSecret + `
export ITERVOX_FAKE_REMOTE_SECRET
ITERVOX_FAKE_SSHD=1 exec "$FAKE_SSHD_BIN" "$*"
`

// fakeSSHDBin is the fake sshd (testdata/fakesshd), built by TestMain.
var fakeSSHDBin string

// TestMain builds the fake sshd once, without -race (a race-instrumented
// binary takes about a second to start on macOS, and every fake ssh
// connection starts one), then runs the tests.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "itervox-fakesshd-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakesshd:", err)
		os.Exit(1)
	}
	fakeSSHDBin = filepath.Join(dir, "fakesshd")
	// `go test` runs with the go tool on PATH (the Makefile also pins
	// GOTOOLCHAIN, which this build inherits).
	build := exec.Command("go", "build", "-o", fakeSSHDBin, "./testdata/fakesshd")
	build.Env = append(os.Environ(), "GOFLAGS=", "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "fakesshd: build failed: %v\n%s", err, out)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	// CORE-112: fail the run when a goroutine outlives every test. The
	// fakesshd directory is removed in goleak's cleanup, which then exits.
	goleak.VerifyTestMain(m, goleak.Cleanup(func(code int) {
		_ = os.RemoveAll(dir)
		os.Exit(code)
	}))
}

// installFaithfulFakeSSH puts the faithful fake first on PATH, using
// loginShell as the remote user's login shell ("" means /bin/bash). It
// returns the path the joined remote commands are recorded to.
func installFaithfulFakeSSH(t *testing.T, loginShell string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ssh"), []byte(faithfulFakeSSH), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_SSHD_BIN", fakeSSHDBin)
	if loginShell == "" {
		loginShell = "/bin/bash"
	}
	t.Setenv("FAKE_SSH_LOGIN_SHELL", loginShell)
	logPath := filepath.Join(dir, "remote_commands.log")
	t.Setenv("FAKE_SSH_REMOTE_LOG", logPath)
	return logPath
}

// installRecordingFakeSSH is the faithful fake in record-only mode: it logs
// the joined remote command and exits 0 without executing it.
func installRecordingFakeSSH(t *testing.T) string {
	t.Helper()
	logPath := installFaithfulFakeSSH(t, "")
	t.Setenv("FAKE_SSH_NO_EXEC", "1")
	return logPath
}

// installFakeSSHTransport installs an `ssh` whose body is a local ssh client
// behaviour (a transport failure, or a raw byte stream) — no remote command
// is modelled, so no remote parsing is involved.
func installFakeSSHTransport(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ssh"), []byte("#!/bin/sh\n"+body), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// availableLoginShells lists the remote login shells present on this host
// that the round-trip rows run under.
func availableLoginShells() []string {
	var out []string
	for _, sh := range []string{
		"/bin/sh", "/bin/bash", "/opt/homebrew/bin/bash", "/bin/zsh", "/bin/dash", "/bin/ksh",
		"/bin/csh", "/bin/tcsh", // csh family: a newline ends a single-quoted string (fix round 2, I1)
		"/usr/bin/fish", "/opt/homebrew/bin/fish", "/usr/local/bin/fish",
	} {
		if _, err := os.Stat(sh); err == nil {
			out = append(out, sh)
		}
	}
	return out
}

// shellLabel turns a shell path into a unique subtest name
// ("bin_zsh", "opt_homebrew_bin_bash").
func shellLabel(shell string) string {
	return strings.ReplaceAll(strings.TrimPrefix(shell, "/"), "/", "_")
}
