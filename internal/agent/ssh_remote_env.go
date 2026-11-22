package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/procgroup"
)

// RemoteEnv is the run environment of an SSH worker's turns (#204 on SSH
// hosts): the local cmd.Env never reaches the remote shell, so the runner
// carries it in the remote script (exported after the login profile ran,
// like ITERVOX_AGENT) and, for the daemon callback, as a reverse forward on
// the turn's ssh.
//
// Nothing here travels on a command line: the script reaches the remote
// bash on ssh's stdin (remoteBashInvocation), so an exported value (the
// guardrail token) is never in any process's argv on either host.
type RemoteEnv struct {
	// Exports are NAME=value entries exported in the remote script. Names
	// must be shell identifiers; others are ignored.
	Exports []string
	// PathPrepend is a remote directory put first on PATH.
	PathPrepend string
	// ForwardDir, ForwardTarget and ForwardEnv: when all are set, each turn
	// reverse-forwards a fresh Unix socket in the remote directory
	// ForwardDir to the local TCP address ForwardTarget (host:port) and
	// exports its path as ForwardEnv. A fresh name per turn: sshd does not
	// unlink a forwarded socket when the connection ends, and refuses to
	// bind over one unless its StreamLocalBindUnlink is on.
	ForwardDir    string
	ForwardTarget string
	ForwardEnv    string
}

// remoteTurn returns the extra ssh options and the script prefix of one
// SSH turn under e.Remote ("" and nil without it).
func (e RunEnv) remoteTurn() ([]string, string) {
	r := e.Remote
	if r == nil {
		return nil, ""
	}
	var b strings.Builder
	exports := r.Exports
	var args []string
	if r.ForwardDir != "" && r.ForwardTarget != "" && isEnvName(r.ForwardEnv) {
		sock := strings.TrimRight(r.ForwardDir, "/") + "/cb-" + randomHex(4) + ".sock"
		args = ForwardArgs(sock, r.ForwardTarget)
		exports = append(append([]string{}, exports...), r.ForwardEnv+"="+sock)
	}
	for _, kv := range exports {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || !isEnvName(name) {
			continue
		}
		b.WriteString("export " + name + "=" + ShellQuote(value) + "; ")
	}
	if r.PathPrepend != "" {
		b.WriteString("export PATH=" + ShellQuote(r.PathPrepend) + `:"$PATH"; `)
	}
	return args, b.String()
}

// ForwardArgs are the ssh options that reverse-forward the remote Unix
// socket sock to the local TCP address target, failing the connection when
// the server refuses the forward (ExitOnForwardFailure) instead of running
// the command without it.
func ForwardArgs(sock, target string) []string {
	return []string{"-o", "ExitOnForwardFailure=yes", "-R", sock + ":" + target}
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// maxRemoteScriptOutput bounds what RemoteScript keeps of stdout and stderr.
const maxRemoteScriptOutput = 1 << 20

// RemoteScript runs script under `bash -c` on host through the same
// transport as an agent turn (remoteBashInvocation: the script on ssh's
// stdin, never in argv; -T; the host-key policy; BatchMode) and returns its
// stdout. extraArgs are further ssh options (ForwardArgs). A non-zero exit
// is an error carrying the tail of stderr.
func RemoteScript(ctx context.Context, host, script string, extraArgs ...string) ([]byte, error) {
	if host == "" {
		return nil, errors.New("agent: remote script: no host")
	}
	remoteArg, payload := remoteBashInvocation("-c", script)
	args := append([]string{"-T"}, sshStrictHostOption(host)...)
	args = append(args, "-o", "BatchMode=yes", "-o", "ConnectTimeout=15")
	args = append(args, extraArgs...)
	args = append(args, host, remoteArg)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	feed, err := attachRemoteStdin(cmd, payload)
	if err != nil {
		return nil, err
	}
	return runSSH(cmd, func() { feed.start() }, feed.join)
}

// RemoteUpload streams src to `command` on host: the ONE ssh remote-command
// argument, run by the remote user's login shell, so it must hold none of
// the characters whose meaning differs between login shells (' \ ! and
// newline, see remoteBashInvocation) — callers build it from fixed words
// and hex. The command reads src on its stdin.
func RemoteUpload(ctx context.Context, host, command string, src io.Reader) error {
	if host == "" {
		return errors.New("agent: remote upload: no host")
	}
	if !uploadCommandOK(command) {
		return fmt.Errorf("agent: remote upload: unsafe remote command %q", command)
	}
	args := append([]string{"-T"}, sshStrictHostOption(host)...)
	args = append(args, "-o", "BatchMode=yes", "-o", "ConnectTimeout=15", host, command)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = src
	_, err := runSSH(cmd, nil, nil)
	return err
}

// uploadCommandOK admits the shell operators an upload command needs (&&
// and a > redirect) and nothing that is parsed differently by csh, fish
// or a POSIX shell.
func uploadCommandOK(command string) bool {
	if strings.ContainsAny(command, "'\\!\n\r\"`$;|<(){}*?[]#~") {
		return false
	}
	return !strings.Contains(strings.ReplaceAll(command, "&&", ""), "&")
}

// runSSH runs an ssh command in its own process group (killed whole on
// ctx expiry, with a WaitDelay) and returns its stdout.
func runSSH(cmd *exec.Cmd, started, waited func()) ([]byte, error) {
	procgroup.Configure(cmd, 5*time.Second)
	cmd.Env = itervoxAgentEnv()
	var stdout bytes.Buffer
	stderr := newTailBuffer(4 << 10)
	cmd.Stdout = &limitedWriter{w: &stdout, n: maxRemoteScriptOutput}
	cmd.Stderr = stderr
	if err := procgroup.Start(cmd); err != nil {
		return nil, fmt.Errorf("agent: ssh: start: %w", err)
	}
	if started != nil {
		started()
	}
	err := cmd.Wait()
	if waited != nil {
		waited()
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return stdout.Bytes(), fmt.Errorf("agent: ssh: %w", err)
		}
		return stdout.Bytes(), fmt.Errorf("agent: ssh: %w: %s", err, msg)
	}
	return stdout.Bytes(), nil
}

// limitedWriter keeps at most n bytes and discards the rest (never fails,
// so ssh is never stopped by a full buffer).
type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n > 0 {
		k := min(len(p), l.n)
		_, _ = l.w.Write(p[:k])
		l.n -= k
	}
	return len(p), nil
}
