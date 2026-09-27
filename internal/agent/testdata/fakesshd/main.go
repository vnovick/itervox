// Command fakesshd is the sshd half of the faithful fake ssh used by the
// internal/agent tests (ssh_fake_test.go documents the model). It is built
// once per test run by TestMain, WITHOUT -race: a race-instrumented binary
// takes about a second to start on macOS, and every fake ssh connection
// starts one. It is test-only and never linked into itervox.
package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

// fakeSSHDEnv is set by the fake ssh client script; it is stripped from the
// remote command's environment.
const fakeSSHDEnv = "ITERVOX_FAKE_SSHD"

func main() {
	os.Exit(runFakeSSHD(os.Args[1:]))
}

// runFakeSSHD is the sshd half of the faithful fake (see the file comment
// for the model). args[0] is the joined remote command.
func runFakeSSHD(args []string) int {
	joined := ""
	if len(args) > 0 {
		joined = args[0]
	}
	// The stdin payload is NUL-terminated (remoteBashInvocation); read up to
	// it for the record/fail hooks, then replay it ahead of the rest of stdin.
	in := bufio.NewReader(os.Stdin)
	var head []byte
	if os.Getenv("FAKE_SSH_NO_EXEC") == "1" || os.Getenv("FAKE_SSH_FAIL_IF_CONTAINS") != "" {
		head, _ = in.ReadBytes(0)
	}
	if os.Getenv("FAKE_SSH_NO_EXEC") == "1" {
		if p := os.Getenv("FAKE_SSH_REMOTE_LOG"); p != "" {
			if f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
				_, _ = f.Write(head)
				_ = f.Close()
			}
		}
		return 0
	}
	if needle := os.Getenv("FAKE_SSH_FAIL_IF_CONTAINS"); needle != "" &&
		(strings.Contains(joined, needle) || bytes.Contains(head, []byte(needle))) {
		fmt.Fprintln(os.Stderr, "ssh: fake transport failure")
		return 255
	}

	shell := os.Getenv("FAKE_SSH_LOGIN_SHELL")
	if shell == "" {
		shell = "/bin/bash"
	}
	env := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, fakeSSHDEnv+"=") && !strings.HasPrefix(kv, "FAKE_SSHD_ROLE=") {
			env = append(env, kv)
		}
	}
	dropbear := os.Getenv("FAKE_SSHD_MODEL") == "dropbear"
	server := os.Getenv("FAKE_SSHD_ROLE") == "server"
	var remote *exec.Cmd
	switch {
	case dropbear && !server:
		// Dropbear model, client half: the "connection server" is a separate
		// process that calls setsid once (svr-main.c) — outside the local
		// client's group, so the CORE-001 kill of the client does not reach it.
		self, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, "fake sshd:", err)
			return 255
		}
		remote = exec.Command(self, joined)
		remote.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		remote.Env = append(append([]string{}, env...), fakeSSHDEnv+"=1", "FAKE_SSHD_ROLE=server")
	case dropbear && server:
		// Dropbear model, server half: session channels run WITHOUT a new
		// session or group (execchild makes none), so the remote command, a
		// sibling session on the same connection and this server process all
		// share one process group. The server records itself and the sibling.
		remote = exec.Command(shell, "-c", joined)
		remote.Env = env
		// This channel's client going away closes the channel, not the
		// connection (ssh ControlMaster keeps it): relaying into the dead
		// channel must fail with EPIPE, not kill the server with SIGPIPE.
		signal.Ignore(syscall.SIGPIPE)
		sibling := exec.Command("/bin/sleep", "300")
		if err := sibling.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "fake sshd:", err)
			return 255
		}
		if p := os.Getenv("FAKE_SSHD_DROPBEAR_LOG"); p != "" {
			_ = os.WriteFile(p, []byte(fmt.Sprintf("%d %d\n", os.Getpid(), sibling.Process.Pid)), 0o644)
		}
		// A connection server outlives one closed channel: linger after the
		// remote command is gone (the test kills this group when it ends).
		defer time.Sleep(15 * time.Second)
	default:
		// OpenSSH model: every session in a new session and process group.
		remote = exec.Command(shell, "-c", joined)
		remote.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		remote.Env = env
	}
	stdinW, err := remote.StdinPipe()
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake sshd:", err)
		return 255
	}
	stdoutR, err := remote.StdoutPipe()
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake sshd:", err)
		return 255
	}
	stderrR, err := remote.StderrPipe()
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake sshd:", err)
		return 255
	}
	if err := remote.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "fake sshd:", err)
		return 255
	}
	if p := os.Getenv("FAKE_SSHD_SESSION_LOG"); p != "" {
		if f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			_, _ = fmt.Fprintln(f, remote.Process.Pid)
			_ = f.Close()
		}
	}
	// Client stdin -> remote stdin. Client EOF is forwarded as remote EOF
	// (ssh sends channel EOF); client death closes stdinW with the process.
	go func() {
		_, _ = io.Copy(stdinW, io.MultiReader(bytes.NewReader(head), in))
		_ = stdinW.Close()
	}()
	var drained sync.WaitGroup
	for _, c := range []struct {
		dst io.Writer
		src io.Reader
	}{{os.Stdout, stdoutR}, {os.Stderr, stderrR}} {
		drained.Add(1)
		go func() {
			defer drained.Done()
			_, _ = io.Copy(c.dst, c.src)
		}()
	}
	drained.Wait()
	err = remote.Wait()
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 255
		}
		return ee.ExitCode()
	}
	fmt.Fprintln(os.Stderr, "fake sshd:", err)
	return 255
}
