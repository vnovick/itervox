package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestListenStrictKernelPickOnPortZero — `server.port: 0` is the "let the
// OS pick a free port" escape hatch that makes two-itervox-in-parallel work
// out of the box. The bound address must reflect the actual port, not the
// requested 0.
func TestListenStrictKernelPickOnPortZero(t *testing.T) {
	ln, actualAddr, err := listenStrict("127.0.0.1", 0)
	if err != nil {
		t.Fatalf("listenStrict(0): %v", err)
	}
	defer func() { _ = ln.Close() }()
	if strings.HasSuffix(actualAddr, ":0") {
		t.Errorf("listenStrict must return the kernel-assigned port; got %q", actualAddr)
	}
	if actualAddr != ln.Addr().String() {
		t.Errorf("returned addr %q != listener addr %q", actualAddr, ln.Addr().String())
	}
}

// TestFatalStartupErrorIsRecognised — wrapping an error in fatalStartupError
// is the signal the outer restart loop uses to bail instead of retrying. The
// helper must detect it both directly and through one Unwrap layer (the
// loop sees it after fmt.Errorf("...%w...", err) wrapping).
func TestFatalStartupErrorIsRecognised(t *testing.T) {
	inner := os.ErrPermission
	fatal := fatalStartupError{inner: inner}
	if !isFatalStartupError(fatal) {
		t.Error("direct fatalStartupError must be recognised")
	}
	wrapped := fmt.Errorf("wrapper: %w", fatal)
	if !isFatalStartupError(wrapped) {
		t.Error("wrapped fatalStartupError must be recognised")
	}
	if isFatalStartupError(inner) {
		t.Error("plain error must NOT be recognised as fatal")
	}
}

// TestRequireNoLiveDaemonAllowsStalePidfile — a previous daemon that wrote a
// pidfile and then died (SIGKILL, crash) must NOT block a fresh start. The
// helper detects the stale PID and logs a notice; caller proceeds to write a
// fresh pidfile.
func TestRequireNoLiveDaemonAllowsStalePidfile(t *testing.T) {
	tmp := t.TempDir()
	wf := filepath.Join(tmp, "WORKFLOW.md")
	if err := os.WriteFile(wf, []byte("---\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pidDir := filepath.Join(tmp, ".itervox")
	if err := os.MkdirAll(pidDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// PID 1 is `init` on Unix; we want a guaranteed-dead PID. PID 0 is
	// reserved; 2^31-1 is guaranteed to be free on any system that hasn't
	// wrapped its PID space.
	stalePID := 2147483646
	if err := os.WriteFile(filepath.Join(pidDir, "daemon.pid"),
		[]byte("2147483646\t"+wf+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	livePid, _, _, err := requireNoLiveDaemon(wf)
	if err != nil {
		t.Errorf("stale pidfile must not block startup; got %v", err)
	}
	if livePid != 0 {
		t.Errorf("livePid should be 0 for a stale pidfile; got %d", livePid)
	}
	_ = stalePID
}

// TestRequireNoLiveDaemonRefusesWhenPidAlive — when the previous daemon's PID
// is still alive (our own PID is the easiest live-PID we can guarantee), the
// helper must refuse to start.
func TestRequireNoLiveDaemonRefusesWhenPidAlive(t *testing.T) {
	tmp := t.TempDir()
	wf := filepath.Join(tmp, "WORKFLOW.md")
	if err := os.WriteFile(wf, []byte("---\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pidDir := filepath.Join(tmp, ".itervox")
	if err := os.MkdirAll(pidDir, 0o755); err != nil {
		t.Fatal(err)
	}
	livePID := os.Getpid()
	contents := []byte(strings.TrimSpace("") +
		stringFromInt(livePID) + "\t" + wf + "\n")
	if err := os.WriteFile(filepath.Join(pidDir, "daemon.pid"), contents, 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, _, err := requireNoLiveDaemon(wf)
	if err == nil {
		t.Fatal("expected refusal when previous daemon PID is alive")
	}
	if !strings.Contains(err.Error(), "another daemon already running") {
		t.Errorf("error message should explain conflict; got %q", err.Error())
	}
}

func stringFromInt(n int) string {
	// avoid bringing in strconv just for this; same trick as the test helper
	// pattern used elsewhere in this package.
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// TestWriteAndReadDashboardURLFile_RoundTrip — the helper writes the URL
// atomically and the read path returns exactly what was written.
func TestWriteAndReadDashboardURLFile_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	wf := filepath.Join(tmp, "WORKFLOW.md")
	url := "http://127.0.0.1:8091/"
	if err := writeDashboardURLFile(wf, url); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, err := os.ReadFile(dashboardURLFilePath(wf))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != url {
		t.Errorf("dashboard_url contents = %q; want %q", string(data), url)
	}
}

// TestRemoveDashboardURLFile_NoErrorWhenAbsent — shutdown handler must
// tolerate a missing dashboard_url file (operator may have deleted it
// manually before the daemon exited).
func TestRemoveDashboardURLFile_NoErrorWhenAbsent(t *testing.T) {
	tmp := t.TempDir()
	wf := filepath.Join(tmp, "WORKFLOW.md")
	removeDashboardURLFile(wf) // must not panic; logs at most
}

// TestRemoveHeartbeatFile_NoErrorWhenAbsent — same for HEARTBEAT.md.
func TestRemoveHeartbeatFile_NoErrorWhenAbsent(t *testing.T) {
	tmp := t.TempDir()
	wf := filepath.Join(tmp, "WORKFLOW.md")
	removeHeartbeatFile(wf)
}

// TestListenStrictRejectsAddrInUse — strict listen on an in-use port returns
// the operator-friendly diagnostic rather than auto-shifting.
func TestListenStrictRejectsAddrInUse(t *testing.T) {
	// Bind a random port first, then try to bind it again strictly.
	a, _, err := listenStrict("127.0.0.1", 0)
	if err != nil {
		t.Fatalf("first bind: %v", err)
	}
	defer func() { _ = a.Close() }()
	port := a.Addr().(interface{ String() string }).String()
	_ = port
	// Now try to bind the same port strictly — must fail with a port-in-use
	// diagnostic (not auto-shift).
	addr := a.Addr().String()
	idx := strings.LastIndex(addr, ":")
	if idx < 0 {
		t.Fatalf("unexpected addr shape: %q", addr)
	}
	portN := 0
	for _, c := range addr[idx+1:] {
		portN = portN*10 + int(c-'0')
	}
	_, _, err = listenStrict("127.0.0.1", portN)
	if err == nil {
		t.Fatal("expected listenStrict to fail on in-use port")
	}
	if !strings.Contains(err.Error(), "already in use") {
		t.Errorf("error should explain the collision; got %q", err.Error())
	}
}
