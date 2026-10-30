//go:build windows

package apitokens

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/vnovick/itervox/internal/winflock"
)

// lockFile is lock_flock.go's lockFile on Windows: an exclusive LockFileEx
// lock on lockPath, polled until deadline, released by the OS if the holder
// dies.
func lockFile(lockPath string, deadline time.Time) (func(), error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("apitokens: open lock %s: %w", lockPath, err)
	}
	wait := time.Millisecond
	for {
		err = winflock.TryLock(f, true)
		if err == nil {
			break
		}
		if !errors.Is(err, winflock.ErrLocked) || !time.Now().Before(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("apitokens: token store is locked by another process (%s): %w", lockPath, err)
		}
		time.Sleep(min(wait, time.Until(deadline)))
		wait = min(2*wait, 50*time.Millisecond)
	}
	return func() {
		_ = winflock.Unlock(f)
		_ = f.Close()
	}, nil
}
