//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package hub

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// lockSupported reports whether the hub election works on this platform.
const lockSupported = true

// Lock is a held hub lock: an exclusive flock(2) on <registry>/hub.lock for
// the hub's lifetime. flock locks belong to the open file description, so
// the kernel releases the lock the instant the holder dies, SIGKILL
// included: "is the lock free?" is an exact answer to "is the hub gone?".
//
// The file is created 0600 with O_NOFOLLOW and never deleted (unlinking a
// lock file another daemon may already have open would let a third one lock
// a fresh inode alongside it). It also carries the hub's LockInfo as JSON,
// which the holder writes once it serves and truncates on release.
type Lock struct {
	f    *os.File
	path string
}

// tryLock takes the hub lock without blocking. acquired=false with a nil
// error means another daemon (or another open of the file) holds it.
func tryLock(path string) (lock *Lock, acquired bool, err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("hub: open lock %s: %w", path, err)
	}
	if err := flock(f, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("hub: flock %s: %w", path, err)
	}
	if info, statErr := f.Stat(); statErr == nil && info.Mode().Perm() != 0o600 {
		_ = f.Chmod(0o600)
	}
	return &Lock{f: f, path: path}, true, nil
}

// lockHeld reports whether some process holds the hub lock at path. It
// probes with a shared non-blocking lock on a fresh descriptor, released at
// once; a missing file is "not held".
func lockHeld(path string) (bool, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("hub: open lock %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	if err := flock(f, syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return true, nil
		}
		return false, fmt.Errorf("hub: probe lock %s: %w", path, err)
	}
	_ = flock(f, syscall.LOCK_UN)
	return false, nil
}

func flock(f *os.File, how int) error {
	for {
		err := syscall.Flock(int(f.Fd()), how)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
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
	_ = flock(l.f, syscall.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}
