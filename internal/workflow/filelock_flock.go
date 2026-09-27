//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package workflow

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"syscall"
	"time"
)

// lockPollMax caps the back-off between LOCK_NB attempts while the sidecar is
// held by someone else.
const lockPollMax = 50 * time.Millisecond

// lockFile takes an exclusive flock(2) on the sidecar lock file for path
// (see lockFilePath), waiting until deadline at most. It is the inter-process
// half of lockForPath (CORE-149): `itervox init --update` and `itervox models
// refresh` run as separate processes from the daemon, so the in-process
// editMu alone cannot serialize their read-modify-write against the daemon's
// settings patchers.
//
// flock locks belong to the open file description, so the lock is released
// by the returned unlock or, if the process dies, by the kernel when the
// descriptor closes — a crashed writer never leaves the file locked. The
// sidecar is never deleted: unlinking a lock file another process may have
// open would let a third process lock a fresh inode alongside it.
//
// Hardening (BH4), the same as cmd/itervox/pidlock_flock.go: the sidecar is
// created 0600 and opened with O_NOFOLLOW, and one left 0644 by an older
// build is tightened with fchmod when we own it. flock(2) works on a
// read-only descriptor, so any local user who can open the file can hold
// LOCK_EX on it; a planted symlink would make every writer create and lock a
// file of the attacker's choosing. And the lock is taken with LOCK_NB polled
// up to deadline rather than a blocking LOCK_EX: a holder that never lets go
// (a suspended `itervox init --update`, or that other user) now costs one
// failed save with a clear error instead of hanging every save forever.
func lockFile(path string, deadline time.Time) (func(), error) {
	lockPath := lockFilePath(path)
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("workflow lock: open %s: %w", lockPath, err)
	}
	if info, statErr := f.Stat(); statErr == nil && info.Mode().Perm() != 0o600 {
		if chErr := f.Chmod(0o600); chErr != nil {
			slog.Warn("workflow lock: lock file is accessible to other users and cannot be tightened",
				"path", lockPath, "mode", info.Mode().Perm().String(), "error", chErr)
		}
	}
	wait := time.Millisecond
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = f.Close()
			return nil, fmt.Errorf("workflow lock: flock %s: %w", lockPath, err)
		}
		if !time.Now().Before(deadline) {
			_ = f.Close()
			return nil, editLockBusyError(lockPath, "another process")
		}
		time.Sleep(min(wait, time.Until(deadline)))
		wait = min(2*wait, lockPollMax)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
