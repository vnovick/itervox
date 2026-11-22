// Package sshtest is a fake `ssh` client for tests of guardrails on SSH
// worker hosts: the test binary itself, started under the name "ssh"
// (Install puts a symlink first on PATH), runs the remote command locally
// under `/bin/sh -c "<joined arguments>"` — OpenSSH joins every argument
// after the host into one string and sshd hands it to the remote login
// shell — in a separate "remote" HOME, and emulates `-R socket:host:port`
// reverse forwards with a Unix socket listener that relays to the TCP
// address, the way sshd does (and, like sshd, it does not unlink the
// socket file when the connection ends).
//
// No real ssh or sshd runs. Each test binary that uses it calls Main first
// thing in TestMain.
package sshtest

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Environment of the fake: the remote HOME, an argv log (one argument per
// line, then "--END--"), and a switch that makes the "server" refuse every
// reverse forward.
const (
	EnvRemoteHome     = "ITERVOX_FAKE_SSH_HOME"
	EnvArgvLog        = "ITERVOX_FAKE_SSH_ARGV_LOG"
	EnvRefuseForward  = "ITERVOX_FAKE_SSH_REFUSE_FORWARD"
	EnvFailIfContains = "ITERVOX_FAKE_SSH_FAIL_IF_CONTAINS"
)

// Main runs the fake ssh client and exits when the process was started as
// "ssh"; otherwise it returns.
func Main() {
	if filepath.Base(os.Args[0]) != "ssh" {
		return
	}
	os.Exit(run(os.Args[1:]))
}

// Install makes `ssh` on PATH the fake for the rest of the test and
// returns the fake remote HOME and the argv log path.
func Install(t testing.TB) (home, argvLog string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	home = filepath.Join(root, "remote-home")
	for _, d := range []string{bin, home} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(exe, filepath.Join(bin, "ssh")); err != nil {
		t.Fatal(err)
	}
	argvLog = filepath.Join(root, "ssh-argv.log")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(EnvRemoteHome, home)
	t.Setenv(EnvArgvLog, argvLog)
	return home, argvLog
}

func run(args []string) int {
	if log := os.Getenv(EnvArgvLog); log != "" {
		if f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			_, _ = fmt.Fprintf(f, "%s\n--END--\n", strings.Join(args, "\n"))
			_ = f.Close()
		}
	}
	var forwards []string
	exitOnForwardFailure := false
	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-o" && i+1 < len(args):
			i++
			if strings.EqualFold(args[i], "ExitOnForwardFailure=yes") {
				exitOnForwardFailure = true
			}
		case a == "-R" && i+1 < len(args):
			i++
			forwards = append(forwards, args[i])
		case a == "-p" || a == "-i" || a == "-l" || a == "-F":
			i++
		case strings.HasPrefix(a, "-"):
		default:
			goto host
		}
	}
host:
	if i >= len(args) {
		_, _ = fmt.Fprintln(os.Stderr, "fake ssh: no host")
		return 255
	}
	command := strings.Join(args[i+1:], " ")
	if s := os.Getenv(EnvFailIfContains); s != "" && strings.Contains(command, s) {
		_, _ = fmt.Fprintln(os.Stderr, "fake ssh: connection refused")
		return 255
	}
	home := os.Getenv(EnvRemoteHome)
	if home == "" {
		_, _ = fmt.Fprintln(os.Stderr, "fake ssh: no remote home")
		return 255
	}
	var listeners []net.Listener
	var wg sync.WaitGroup
	defer func() {
		for _, l := range listeners {
			_ = l.Close()
		}
	}()
	for _, spec := range forwards {
		sock, target, ok := splitForward(spec)
		var l net.Listener
		var err error
		if ok && os.Getenv(EnvRefuseForward) == "" {
			l, err = net.Listen("unix", sock)
		} else {
			err = fmt.Errorf("administratively prohibited")
		}
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Warning: remote port forwarding failed for listen path %s: %v\n", sock, err)
			if exitOnForwardFailure {
				_, _ = fmt.Fprintln(os.Stderr, "Error: remote port forwarding failed")
				return 255
			}
			continue
		}
		l.(*net.UnixListener).SetUnlinkOnClose(false) // sshd leaves the socket file behind
		listeners = append(listeners, l)
		wg.Add(1)
		go func() {
			defer wg.Done()
			relay(l, target)
		}()
	}
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir = home
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = remoteEnv(home)
	err := cmd.Run()
	for _, l := range listeners {
		_ = l.Close()
	}
	listeners = nil
	wg.Wait()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			if ee.ExitCode() < 0 {
				return 255
			}
			return ee.ExitCode()
		}
		_, _ = fmt.Fprintln(os.Stderr, "fake ssh:", err)
		return 255
	}
	return 0
}

// splitForward splits "socket:host:port".
func splitForward(spec string) (sock, target string, ok bool) {
	sock, target, ok = strings.Cut(spec, ":")
	return sock, target, ok && strings.HasPrefix(sock, "/") && strings.Contains(target, ":")
}

func relay(l net.Listener, target string) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		go func() {
			defer func() { _ = c.Close() }()
			d, err := net.Dial("tcp", target)
			if err != nil {
				return
			}
			defer func() { _ = d.Close() }()
			done := make(chan struct{}, 2)
			go func() { _, _ = io.Copy(d, c); done <- struct{}{} }()
			go func() { _, _ = io.Copy(c, d); done <- struct{}{} }()
			<-done
		}()
	}
}

// remoteEnv is the environment of the "remote" session: this process's
// environment (PATH: the same tools as the test host) with the remote HOME,
// and nothing the daemon side sets for its local agents.
func remoteEnv(home string) []string {
	var out []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		switch {
		case name == "HOME", name == "GIT_CONFIG_PARAMETERS", strings.HasPrefix(name, "ITERVOX_GUARDRAIL_"),
			strings.HasPrefix(name, "ITERVOX_FAKE_SSH_"):
			continue
		}
		out = append(out, kv)
	}
	return append(out, "HOME="+home)
}
