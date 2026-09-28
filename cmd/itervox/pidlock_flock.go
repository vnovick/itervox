//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"syscall"
)

// pidLockSupported reports whether tryLockPIDFile is a real lock.
const pidLockSupported = true

// tryLockPIDFile takes a NON-blocking exclusive flock(2) on lockPath for the
// daemon's lifetime (CORE-039). acquired=false with a nil error means another
// open file description — another daemon, or another in-process claimant —
// holds it.
//
// flock locks belong to the open file description, so the kernel releases the
// lock the instant the holder dies, SIGKILL included. That makes "is the lock
// free?" an exact answer to "is the daemon that wrote this pid file gone?",
// which a pid probe cannot give: a pid can be recycled by an unrelated process
// or, for a container entrypoint, by the restarted daemon itself.
//
// The lock file is never deleted (same rule as internal/workflow's sidecar):
// unlinking a lock file another claimant may already have open would let a
// third claimant lock a fresh inode alongside it.
//
// Permissions: the lock is created 0600 and opened with O_NOFOLLOW. flock(2)
// works on a read-only descriptor, so any local user who can open the file
// can hold LOCK_EX on it and stop every daemon for the project from starting;
// a planted symlink would make the daemon create and lock a file of the
// attacker's choosing. A lock file left 0644 by an older daemon is tightened
// with fchmod when we own it; when we do not, the open still proceeds (the
// lock semantics are unchanged) and a warning is logged.
func tryLockPIDFile(lockPath string) (release func(), acquired bool, err error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("open pid lock %s: %w", lockPath, err)
	}
	if info, statErr := f.Stat(); statErr == nil && info.Mode().Perm() != 0o600 {
		if chErr := f.Chmod(0o600); chErr != nil {
			slog.Warn("itervox: pid lock is accessible to other users and cannot be tightened",
				"path", lockPath, "mode", info.Mode().Perm().String(), "error", chErr)
		}
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("flock pid lock %s: %w", lockPath, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, true, nil
}
