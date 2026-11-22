//go:build unix

package hub

import (
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// lockTokenStore takes the same exclusive lock apitokens takes for a write
// (flock on <tokens.json>.lock), so a test rewrite of the file is
// serialized with the store's own writes.
func lockTokenStore(t *testing.T, path string) func() {
	t.Helper()
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	require.NoError(t, err)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if err != syscall.EINTR {
			break
		}
	}
	require.NoError(t, err)
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}
}
