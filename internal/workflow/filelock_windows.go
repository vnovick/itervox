//go:build windows

package workflow

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/vnovick/itervox/internal/winflock"
)

// lockFile is filelock_flock.go's lockFile on Windows: an exclusive
// LockFileEx lock on the sidecar lock file for path, polled until deadline.
// The OS releases it when the handle closes, a crashed writer included, and
// the sidecar is never deleted, for the same reasons as on Unix.
func lockFile(path string, deadline time.Time) (func(), error) {
	lockPath := lockFilePath(path)
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("workflow lock: open %s: %w", lockPath, err)
	}
	wait := time.Millisecond
	for {
		err = winflock.TryLock(f, true)
		if err == nil {
			break
		}
		if !errors.Is(err, winflock.ErrLocked) {
			_ = f.Close()
			return nil, fmt.Errorf("workflow lock: lock %s: %w", lockPath, err)
		}
		if !time.Now().Before(deadline) {
			_ = f.Close()
			return nil, editLockBusyError(lockPath, "another process")
		}
		time.Sleep(min(wait, time.Until(deadline)))
		wait = min(2*wait, 50*time.Millisecond) // filelock_flock.go's lockPollMax
	}
	return func() {
		_ = winflock.Unlock(f)
		_ = f.Close()
	}, nil
}
