package main

import (
	"testing"
	"time"
)

func TestSameBindTarget(t *testing.T) {
	for _, tc := range []struct {
		name        string
		currentAddr string
		host        string
		port        int
		want        bool
	}{
		// The regression: a cosmetic host rename is the same socket, and
		// treating it as a rebind made the daemon collide with itself.
		{"localhost alias of loopback", "127.0.0.1:8090", "localhost", 8090, true},
		{"identical literal", "127.0.0.1:8090", "127.0.0.1", 8090, true},
		{"different port", "127.0.0.1:8090", "127.0.0.1", 8091, false},
		{"loopback to wildcard", "127.0.0.1:8090", "0.0.0.0", 8090, false},
		// An explicit 0 means "give me a fresh OS-assigned port", so it must
		// always rebind even though the current port is known.
		{"explicit zero always rebinds", "127.0.0.1:8090", "127.0.0.1", 0, false},
		{"no current listener", "", "127.0.0.1", 8090, false},
		{"unresolvable current", "not-an-addr", "127.0.0.1", 8090, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameBindTarget(tc.currentAddr, tc.host, tc.port); got != tc.want {
				t.Errorf("sameBindTarget(%q, %q, %d) = %v, want %v",
					tc.currentAddr, tc.host, tc.port, got, tc.want)
			}
		})
	}
}

// TestAwaitStop pins both outcomes of the shutdown join that run() performs
// on each of its two exit paths. The server-exits-first path used to skip
// this wait entirely, which let main()'s reload loop start a second
// orchestrator — and so a second outbox handle on one file — while the first
// was still running.
func TestAwaitStop(t *testing.T) {
	t.Run("returns true when the component stops in time", func(t *testing.T) {
		done := make(chan error, 1)
		done <- nil
		if !awaitStop(done, time.Second) {
			t.Fatal("awaitStop reported a timeout for an already-finished component")
		}
	})

	t.Run("returns false when the component overruns the grace", func(t *testing.T) {
		done := make(chan error) // never fires
		start := time.Now()
		if awaitStop(done, 20*time.Millisecond) {
			t.Fatal("awaitStop reported success for a component that never stopped")
		}
		if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
			t.Fatalf("awaitStop returned after %v, before the grace elapsed", elapsed)
		}
	})
}
