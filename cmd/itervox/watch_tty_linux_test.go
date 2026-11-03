//go:build linux

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// openTestPTY opens a pseudo-terminal pair: master (the "terminal
// emulator" side) and slave (what a program sees as its terminal).
func openTestPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pseudo-terminal available: %v", err)
	}
	require.NoError(t, unix.IoctlSetPointerInt(int(m.Fd()), unix.TIOCSPTLCK, 0))
	n, err := unix.IoctlGetInt(int(m.Fd()), unix.TIOCGPTN)
	require.NoError(t, err)
	s, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close(); _ = m.Close() })
	return m, s
}

// termMode is the part of a terminal's termios that a raw mode, an echo
// change or a line-discipline change would alter.
func termMode(t *testing.T, f *os.File) unix.Termios {
	t.Helper()
	tio, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)
	require.NoError(t, err)
	return *tio
}

// TestWatchNeverChangesTerminalMode: on a real terminal (the panel path:
// redrawn in place, in colour) watch never changes the terminal's mode —
// no raw mode, no echo change, no alternate screen or other DEC private
// mode — and never reads stdin: what was typed is still there afterwards.
func TestWatchNeverChangesTerminalMode(t *testing.T) {
	master, slave := openTestPTY(t)
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	// stdin is the same terminal, with a line already typed into it.
	oldStdin := os.Stdin
	os.Stdin = slave
	t.Cleanup(func() { os.Stdin = oldStdin })
	_, err := master.Write([]byte("keep-me\n"))
	require.NoError(t, err)

	// Drain what watch draws, as a terminal emulator would.
	var mu sync.Mutex
	var drawn bytes.Buffer
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			mu.Lock()
			drawn.Write(buf[:n])
			mu.Unlock()
			if err != nil {
				return
			}
		}
	}()

	d := newFakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/state":
			writeJSONBody(w, map[string]any{"maxConcurrentAgents": 1, "running": []any{map[string]any{
				"identifier": "ENG-1", "title": "Fix it", "state": "In Progress", "backend": "claude", "elapsedMs": 1000}}})
		case "/api/v1/feed":
			writeJSONBody(w, map[string]any{"events": []any{}, "headSeq": 3, "nextSeq": 3})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	before := termMode(t, slave)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan int)
	go func() {
		done <- clientCommand(ctx, "watch", []string{"--url", d.srv.URL, "--interval", "10ms"}, slave, io.Discard)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for countPath(d, "/api/v1/state") < 8 && time.Now().Before(deadline) {
		assert.Equal(t, before, termMode(t, slave), "the terminal mode while watch runs")
		time.Sleep(2 * time.Millisecond)
	}
	require.GreaterOrEqual(t, countPath(d, "/api/v1/state"), 8)
	cancel()
	require.Equal(t, 0, <-done)
	assert.Equal(t, before, termMode(t, slave), "the terminal mode after watch")
	assert.NotZero(t, before.Lflag&unix.ICANON, "the terminal is still in canonical (cooked) mode")
	assert.NotZero(t, before.Lflag&unix.ECHO, "with echo on")

	time.Sleep(50 * time.Millisecond) // the last redraw reaches the drain
	mu.Lock()
	out := drawn.String()
	mu.Unlock()
	assert.Contains(t, out, "\x1b[2J", "the terminal path ran: the panel is redrawn in place")
	assert.Contains(t, out, "\x1b[32m●", "in colour")
	assert.Contains(t, out, "ENG-1")
	assert.NotContains(t, out, "\x1b[?", "no DEC private mode: no alternate screen, hidden cursor, mouse or bracketed paste")

	// stdin was never read: the typed line is still waiting.
	got := make(chan string, 1)
	go func() {
		buf := make([]byte, 64)
		n, _ := slave.Read(buf)
		got <- string(buf[:n])
	}()
	select {
	case line := <-got:
		assert.Equal(t, "keep-me\n", line, "watch never reads stdin")
	case <-time.After(5 * time.Second):
		t.Fatal("the typed line is gone: something read stdin")
	}
}
