//go:build windows

package winflock

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestTryLockExcludesOtherHandlesAndKeepsContentReadable pins the two
// properties the daemon's lock files rely on: a second handle cannot take
// the lock while the first holds it (as with flock), and the locked byte
// lies beyond the content, so another process can still read what the
// holder published (the hub lock carries its LockInfo JSON).
func TestTryLockExcludesOtherHandlesAndKeepsContentReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	a, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	if err := TryLock(a, true); err != nil {
		t.Fatalf("first TryLock: %v", err)
	}
	if _, err := a.WriteAt([]byte("hello"), 0); err != nil {
		t.Fatal(err)
	}

	b, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	if err := TryLock(b, true); !errors.Is(err, ErrLocked) {
		t.Fatalf("second exclusive TryLock = %v, want ErrLocked", err)
	}
	if err := TryLock(b, false); !errors.Is(err, ErrLocked) {
		t.Fatalf("shared TryLock while exclusively held = %v, want ErrLocked", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "hello" {
		t.Fatalf("content while locked = %q, %v; want hello", got, err)
	}

	if err := Unlock(a); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if err := TryLock(b, true); err != nil {
		t.Fatalf("TryLock after Unlock: %v", err)
	}
	_ = b.Close()
	// Closing the holder's handle releases the lock too.
	c, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if err := TryLock(c, true); err != nil {
		t.Fatalf("TryLock after the holder closed: %v", err)
	}
}
