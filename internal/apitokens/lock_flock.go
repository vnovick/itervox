//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package apitokens

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// lockFile takes an exclusive flock(2) on lockPath, polling until deadline.
// It serializes a token mutation by the daemon (dashboard) against one by
// `itervox token` in another process, so neither read-modify-write loses
// the other's change (a lost revoke would resurrect a token). The lock file
// is created 0600 with O_NOFOLLOW, never deleted, and released by the
// kernel if the holder dies.
func lockFile(lockPath string, deadline time.Time) (func(), error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("apitokens: open lock %s: %w", lockPath, err)
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
		if !errors.Is(err, syscall.EWOULDBLOCK) || !time.Now().Before(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("apitokens: token store is locked by another process (%s): %w", lockPath, err)
		}
		time.Sleep(min(wait, time.Until(deadline)))
		wait = min(2*wait, 50*time.Millisecond)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
