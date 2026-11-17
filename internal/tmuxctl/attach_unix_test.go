//go:build unix

package tmuxctl

import (
	"bytes"
	"context"
	"errors"
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
	a, err := c.Attach(ctx, target, false, 100, 30)
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
	a, err := c.Attach(ctx, panes[0].ID, true, 0, 0)
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
	a, err := c.Attach(context.Background(), "=itervox-none00", true, 80, 24)
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
