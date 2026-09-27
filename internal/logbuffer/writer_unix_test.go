//go:build unix

package logbuffer

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// R1: Add performs no disk I/O, including for an identifier it has never
// seen. Pre-fix-F, the first Add for an identifier counted the lines of its
// existing file (getOrCreate's seeding) on the caller's goroutine — the
// event loop's, for a stall warning or an auto-resume notice. Here that file
// is a FIFO with no writer, so any open of it blocks: a stalled disk.
func TestAdd_FirstLineForAnIdentifierDoesNotReadItsFile(t *testing.T) {
	dir := t.TempDir()
	b := New()
	b.SetLogDir(dir)
	// Registered before the FIFO cleanup below, so it runs after it (LIFO):
	// the writer must be unblocked before Close waits for it to exit.
	t.Cleanup(func() { _ = b.Close(context.Background()) })
	fifo := issuePath(dir, "ENG-1")
	require.NoError(t, syscall.Mkfifo(fifo, 0o644))
	t.Cleanup(func() {
		// Unblock the writer: it may be blocked opening the FIFO (count or
		// append), or about to. An O_RDWR open never blocks (Linux and
		// Darwin) and completes a pending open from the other side; holding
		// it briefly and closing lets a reader see EOF. Repeat until a Flush
		// barrier gets through, so an open that raced one round is caught by
		// the next, then remove the FIFO.
		defer func() { _ = os.Remove(fifo) }()
		for range 400 {
			if f, err := os.OpenFile(fifo, os.O_RDWR, 0); err == nil {
				time.Sleep(time.Millisecond)
				_ = f.Close()
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			err := b.Flush(ctx)
			cancel()
			if !errors.Is(err, context.DeadlineExceeded) {
				return
			}
		}
	})

	require.True(t, returnsPromptly(func() { b.Add("ENG-1", "stall warning") }),
		"the first Add for an identifier read its on-disk file (R1)")
	require.Equal(t, []string{"stall warning"}, b.Get("ENG-1"))
}
