//go:build unix

package tmuxctl

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// ptyReader collects what an attachment draws on its own goroutine.
type ptyReader struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	done chan struct{}
}

func readAll(a *Attachment) *ptyReader {
	r := &ptyReader{done: make(chan struct{})}
	go func() {
		defer close(r.done)
		b := make([]byte, 4096)
		for {
			n, err := a.Read(b)
			r.mu.Lock()
			r.buf.Write(b[:n])
			r.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	return r
}

func (r *ptyReader) waitFor(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		ok := strings.Contains(r.buf.String(), want)
		r.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	t.Fatalf("the terminal never drew %q; got %q", want, r.buf.String())
}

// #298 with real tmux: an attach in a PTY draws the session, a line typed
// into it reaches the program and is drawn back, a resize reaches the PTY,
// and detaching (Close) leaves the session running.
func TestAttachTypesReadsResizesAndDetachesLeavingTheSessionRunning(t *testing.T) {
	dir, script := fakeAgent(t)
	c := privateServer(t, "itervox-att1", dir, script)
	ctx := context.Background()
	panes, err := c.ListPanes(ctx)
	if err != nil || len(panes) != 1 {
		t.Fatalf("ListPanes = %v, %v", panes, err)
	}
	waitScreen(t, c, panes[0].ID, "ready")

	target, err := AttachTarget("itervox-att1", "")
	if err != nil {
		t.Fatal(err)
	}
	a, err := c.Attach(ctx, target, AttachMode{}, 100, 30)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	r := readAll(a)
	r.waitFor(t, "ready")
	if cols, rows, err := a.Size(); err != nil || cols != 100 || rows != 30 {
		t.Fatalf("Size = %d x %d, %v; want 100 x 30", cols, rows, err)
	}

	if _, err := a.Write([]byte("typed live\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	r.waitFor(t, "got: typed live")
	waitScreen(t, c, panes[0].ID, "got: typed live")

	if err := a.Resize(90, 25); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if cols, rows, err := a.Size(); err != nil || cols != 90 || rows != 25 {
		t.Fatalf("Size after Resize = %d x %d, %v; want 90 x 25", cols, rows, err)
	}

	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		t.Fatal("Read did not end after Close")
	}
	if err := a.Resize(80, 24); !errors.Is(err, ErrAttachClosed) {
		t.Fatalf("Resize after Close = %v, want ErrAttachClosed", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("second Close = %v, want nil", err)
	}
	if ok, err := c.HasSession(ctx, "itervox-att1"); err != nil || !ok {
		t.Fatalf("the session did not survive the detach: %v, %v", ok, err)
	}
}

// A read-only attach draws the pane but never types into it.
func TestReadOnlyAttachRefusesWrites(t *testing.T) {
	dir, script := fakeAgent(t)
	c := privateServer(t, "itervox-att2", dir, script)
	ctx := context.Background()
	panes, err := c.ListPanes(ctx)
	if err != nil || len(panes) != 1 {
		t.Fatalf("ListPanes = %v, %v", panes, err)
	}
	waitScreen(t, c, panes[0].ID, "ready")
	a, err := c.Attach(ctx, panes[0].ID, readOnlyMode(t, c), 0, 0)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer func() { _ = a.Close() }()
	r := readAll(a)
	r.waitFor(t, "ready")
	if !a.ReadOnly() {
		t.Fatal("ReadOnly() = false")
	}
	if _, err := a.Write([]byte("nope\r")); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("Write on a read-only attach = %v, want ErrReadOnly", err)
	}
	if cols, rows, err := a.Size(); err != nil || cols != DefaultTermCols || rows != DefaultTermRows {
		t.Fatalf("default Size = %d x %d, %v", cols, rows, err)
	}
}

// An attach to a session that does not exist ends at once (the client
// exits): Read fails, Close reaps it.
func TestAttachToAMissingSessionEnds(t *testing.T) {
	dir, script := fakeAgent(t)
	c := privateServer(t, "itervox-att3", dir, script)
	a, err := c.Attach(context.Background(), "=itervox-none00", AttachMode{ReadOnly: true}, 80, 24)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	r := readAll(a)
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		t.Fatal("an attach to a missing session kept running")
	}
	_ = a.Close()
}

// readOnlyMode is the read-only attach the daemon makes with this tmux:
// ignore-size where tmux takes client flags.
func readOnlyMode(t *testing.T, c Client) AttachMode {
	t.Helper()
	v, err := c.Version(context.Background())
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	return AttachMode{ReadOnly: true, IgnoreSize: ClientFlagsSupported(v)}
}

// windowState is the session's window size and attached client count, as
// tmux reports them.
func windowState(t *testing.T, c Client, pane string) string {
	t.Helper()
	out, err := exec.Command("tmux", "-S", c.Socket, "display-message", "-p", "-t", pane,
		"#{window_width}x#{window_height} clients=#{session_attached}").Output()
	if err != nil {
		t.Fatalf("display-message: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func waitWindow(t *testing.T, c Client, pane, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	got := ""
	for time.Now().Before(deadline) {
		if got = windowState(t, c, pane); got == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("window = %q, want %q", got, want)
}

// A small read-only client (a narrow browser panel) never shrinks the
// window the session's own client sees; a small read-write client does,
// which shows the check would catch a shrink.
func TestReadOnlyAttachDoesNotShrinkTheWindow(t *testing.T) {
	dir, script := fakeAgent(t)
	c := privateServer(t, "itervox-att4", dir, script)
	ctx := context.Background()
	panes, err := c.ListPanes(ctx)
	if err != nil || len(panes) != 1 {
		t.Fatalf("ListPanes = %v, %v", panes, err)
	}
	pane := panes[0].ID
	waitScreen(t, c, pane, "ready")
	mode := readOnlyMode(t, c)
	if !mode.IgnoreSize {
		t.Skip("this tmux has no client flags (-f, tmux 3.2+): a read-only client may size the window")
	}

	// The operator's own (read-write) client, 120 x 40: the window is
	// 120 x 39 (one row is tmux's status line).
	big, err := c.Attach(ctx, "=itervox-att4", AttachMode{}, 120, 40)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer func() { _ = big.Close() }()
	readAll(big).waitFor(t, "ready")
	waitWindow(t, c, pane, "120x39 clients=1")

	// A 40 x 10 read-only client attaches and draws, and the window keeps
	// its size, also when the client resizes.
	small, err := c.Attach(ctx, "=itervox-att4", mode, 40, 10)
	if err != nil {
		t.Fatalf("Attach read-only: %v", err)
	}
	readAll(small).waitFor(t, "ready")
	waitWindow(t, c, pane, "120x39 clients=2")
	if err := small.Resize(30, 8); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	for range 10 {
		time.Sleep(50 * time.Millisecond)
		if got := windowState(t, c, pane); got != "120x39 clients=2" {
			t.Fatalf("a read-only client resized the window: %q", got)
		}
	}
	if err := small.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	waitWindow(t, c, pane, "120x39 clients=1")

	// Control: a 40 x 10 read-write client does size the window (IgnoreSize
	// is not honoured without ReadOnly).
	rw, err := c.Attach(ctx, "=itervox-att4", AttachMode{IgnoreSize: true}, 40, 10)
	if err != nil {
		t.Fatalf("Attach read-write: %v", err)
	}
	defer func() { _ = rw.Close() }()
	readAll(rw).waitFor(t, "ready")
	waitWindow(t, c, pane, "40x9 clients=2")
}
