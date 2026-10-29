//go:build windows

package hub

import (
	"errors"
	"fmt"
	"os"

	"github.com/vnovick/itervox/internal/winflock"
)

// lockSupported reports whether the hub election works on this platform.
const lockSupported = true

// Lock is a held hub lock: an exclusive LockFileEx lock on
// <registry>/hub.lock for the hub's lifetime, released by the OS the
// instant the holder dies. The lock covers a byte beyond the file's end, so
// the LockInfo JSON the holder writes stays readable (lock_flock.go has the
// Unix counterpart and the full contract).
type Lock struct {
	f    *os.File
	path string
}

// tryLock takes the hub lock without blocking. acquired=false with a nil
// error means another daemon (or another open of the file) holds it.
func tryLock(path string) (lock *Lock, acquired bool, err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("hub: open lock %s: %w", path, err)
	}
	if err := winflock.TryLock(f, true); err != nil {
		_ = f.Close()
		if errors.Is(err, winflock.ErrLocked) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("hub: lock %s: %w", path, err)
	}
	return &Lock{f: f, path: path}, true, nil
}

// lockHeld reports whether some process holds the hub lock at path. It
// probes with a shared non-blocking lock on a fresh handle, released at
// once; a missing file is "not held".
func lockHeld(path string) (bool, error) {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("hub: open lock %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	if err := winflock.TryLock(f, false); err != nil {
		if errors.Is(err, winflock.ErrLocked) {
			return true, nil
		}
		return false, fmt.Errorf("hub: probe lock %s: %w", path, err)
	}
	_ = winflock.Unlock(f)
	return false, nil
}

// publish replaces the lock file's content with data.
func (l *Lock) publish(data []byte) error {
	if err := l.f.Truncate(0); err != nil {
		return fmt.Errorf("hub: truncate lock: %w", err)
	}
	if _, err := l.f.WriteAt(data, 0); err != nil {
		return fmt.Errorf("hub: write lock: %w", err)
	}
	return nil
}

// Release empties the lock file (a watcher sees the write and takes over at
// once) and drops the lock. Idempotent.
func (l *Lock) Release() {
	if l == nil || l.f == nil {
		return
	}
	_ = l.f.Truncate(0)
	_ = winflock.Unlock(l.f)
	_ = l.f.Close()
	l.f = nil
}
