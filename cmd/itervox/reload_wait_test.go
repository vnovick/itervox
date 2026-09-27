package main

import (
	"context"
	"os"
	"syscall"
	"testing"
	"time"
)

// M4-close BH LOW — the pause between generations (reloadDelay) must not
// ignore a SIGTERM/SIGINT or the top-level ctx: a signal during the wait
// stops the daemon at once instead of starting another generation first.
func TestReloadWaitStopsOnSignalOrCtx(t *testing.T) {
	t.Run("signal", func(t *testing.T) {
		sigCh := make(chan os.Signal, 1)
		sigCh <- syscall.SIGTERM
		start := time.Now()
		if !waitBeforeReload(context.Background(), 10*time.Second, sigCh) {
			t.Fatal("a signal during the reload wait must stop the daemon")
		}
		if time.Since(start) > 2*time.Second {
			t.Fatalf("the wait ignored the signal for %s", time.Since(start))
		}
	})
	t.Run("ctx", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		start := time.Now()
		if !waitBeforeReload(ctx, 10*time.Second, make(chan os.Signal)) {
			t.Fatal("a cancelled ctx must stop the wait")
		}
		if time.Since(start) > 2*time.Second {
			t.Fatalf("the wait ignored ctx for %s", time.Since(start))
		}
	})
	t.Run("elapses", func(t *testing.T) {
		if waitBeforeReload(context.Background(), 20*time.Millisecond, make(chan os.Signal)) {
			t.Fatal("with no signal the wait elapses and the loop reloads")
		}
	})
}
