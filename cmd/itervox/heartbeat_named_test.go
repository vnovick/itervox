package main

import (
	"errors"
	"testing"
	"time"

	"github.com/vnovick/itervox/internal/server"
)

// TestHeartbeatWriterSkipsWhenClean — todolist4 P2-3 named test.
// Confirms MaybeWrite's dirty-gating: after a successful write clears dirty,
// subsequent invocations during the same interval window must not write.
func TestHeartbeatWriterSkipsWhenClean(t *testing.T) {
	calls := 0
	w := newHeartbeatWriter("/tmp/test_clean.md", heartbeatOptions{},
		func() server.StateSnapshot { return server.StateSnapshot{} },
		1*time.Millisecond)
	w.writeFile = func(string, string) error { calls++; return nil }

	now := time.Now().UTC()
	if err := w.WriteNow(now); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 write after WriteNow; got %d", calls)
	}

	// Without Request() being called, MaybeWrite must be a no-op even after
	// the throttle interval elapses.
	if err := w.MaybeWrite(now.Add(100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("clean state must not write; got %d", calls)
	}
}

// TestHeartbeatRetriesAfterWriteFailure — todolist4 P3-9 named test.
// Verifies that a failed writeAt leaves dirty=true so the next MaybeWrite
// tries again immediately (does not extend the next attempt by minInterval).
func TestHeartbeatRetriesAfterWriteFailure(t *testing.T) {
	failNext := true
	calls := 0
	w := newHeartbeatWriter("/tmp/test_retry.md", heartbeatOptions{},
		func() server.StateSnapshot { return server.StateSnapshot{} },
		1*time.Millisecond)
	w.writeFile = func(string, string) error {
		calls++
		if failNext {
			return errors.New("disk full")
		}
		return nil
	}

	w.Request()
	now := time.Now().UTC()
	if err := w.MaybeWrite(now); err == nil {
		t.Fatal("expected error from failing writeFile")
	}
	if calls != 1 {
		t.Fatalf("expected 1 attempted write; got %d", calls)
	}

	// Failure must NOT clear dirty — the next MaybeWrite must retry.
	failNext = false
	if err := w.MaybeWrite(now.Add(100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("expected retry to write; got call count %d", calls)
	}
}
