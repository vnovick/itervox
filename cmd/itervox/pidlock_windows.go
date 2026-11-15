//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/vnovick/itervox/internal/winflock"
)

// pidLockSupported reports whether tryLockPIDFile is a real lock.
const pidLockSupported = true

// tryLockPIDFile is pidlock_flock.go's tryLockPIDFile on Windows: a
// NON-blocking exclusive LockFileEx lock on lockPath for the daemon's
// lifetime (CORE-039). The OS releases it when the handle closes, so a
// killed daemon frees it at once; acquired=false with a nil error means
// another handle holds it. The lock file is never deleted.
func tryLockPIDFile(lockPath string) (release func(), acquired bool, err error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("open pid lock %s: %w", lockPath, err)
	}
	if err := winflock.TryLock(f, true); err != nil {
		_ = f.Close()
		if errors.Is(err, winflock.ErrLocked) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("lock pid lock %s: %w", lockPath, err)
	}
	return func() {
		_ = winflock.Unlock(f)
		_ = f.Close()
	}, true, nil
}
