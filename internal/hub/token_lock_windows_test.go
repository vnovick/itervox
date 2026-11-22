//go:build windows

package hub

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/winflock"
)

// lockTokenStore takes the same exclusive lock apitokens takes for a write
// (LockFileEx on <tokens.json>.lock), so a test rewrite of the file is
// serialized with the store's own writes.
func lockTokenStore(t *testing.T, path string) func() {
	t.Helper()
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	require.NoError(t, err)
	deadline := time.Now().Add(10 * time.Second)
	for {
		err = winflock.TryLock(f, true)
		if err == nil || !errors.Is(err, winflock.ErrLocked) || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	require.NoError(t, err)
	return func() {
		_ = winflock.Unlock(f)
		_ = f.Close()
	}
}
