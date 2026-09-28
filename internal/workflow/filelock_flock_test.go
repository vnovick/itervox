//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package workflow

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const lockTestFixture = "---\nagent:\n  max_concurrent_agents: 3\n---\nbody\n"

func lockTestPath(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "WORKFLOW.md")
	require.NoError(t, os.WriteFile(path, []byte(lockTestFixture), 0o644))
	return path
}

// BH4: a foreign holder of LOCK_EX on the sidecar — a suspended
// `itervox init --update`, or another local user holding it on a READ-ONLY
// descriptor (flock needs no write access) — used to hang every settings save
// forever, with the in-process mutex held. The lock must give up after a
// bounded deadline with a clear, typed error.
func TestEditLockTimesOutWhileForeignHolderKeepsIt(t *testing.T) {
	path := lockTestPath(t)
	prev := editLockTimeout
	editLockTimeout = 200 * time.Millisecond
	t.Cleanup(func() { editLockTimeout = prev })

	// A separate open file description, read-only, as another user could.
	require.NoError(t, os.WriteFile(lockFilePath(path), nil, 0o644))
	holder, err := os.Open(lockFilePath(path))
	require.NoError(t, err)
	require.NoError(t, syscall.Flock(int(holder.Fd()), syscall.LOCK_EX))
	released := false
	release := func() {
		if !released {
			released = true
			_ = syscall.Flock(int(holder.Fd()), syscall.LOCK_UN)
			_ = holder.Close()
		}
	}
	t.Cleanup(release)

	done := make(chan error, 2)
	go func() { done <- PatchIntField(path, "max_concurrent_agents", 5) }()
	go func() { done <- PatchIntField(path, "max_concurrent_agents", 6) }() // queued in-process behind the first
	for range 2 {
		select {
		case err := <-done:
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrEditLockBusy), "want ErrEditLockBusy, got %v", err)
			assert.Contains(t, err.Error(), "itervox init --update")
		case <-time.After(3 * time.Second):
			release() // unblock the hung writer so the test binary can exit
			t.Fatal("a settings save blocked indefinitely on a foreign WORKFLOW.md lock holder")
		}
	}
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, lockTestFixture, string(got), "a timed-out save must not write")

	release()
	require.NoError(t, PatchIntField(path, "max_concurrent_agents", 7), "the lock works again once released")
}

// BH4: the sidecar is created 0600, an existing 0644 one is tightened, and a
// planted symlink is refused rather than followed (O_NOFOLLOW) — the same
// hardening as cmd/itervox/pidlock_flock.go.
func TestEditLockFileHardened(t *testing.T) {
	t.Run("created 0600", func(t *testing.T) {
		path := lockTestPath(t)
		require.NoError(t, PatchIntField(path, "max_concurrent_agents", 5))
		info, err := os.Stat(lockFilePath(path))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	})
	t.Run("existing 0644 tightened", func(t *testing.T) {
		path := lockTestPath(t)
		require.NoError(t, os.WriteFile(lockFilePath(path), nil, 0o644))
		require.NoError(t, os.Chmod(lockFilePath(path), 0o644))
		require.NoError(t, PatchIntField(path, "max_concurrent_agents", 5))
		info, err := os.Stat(lockFilePath(path))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	})
	t.Run("symlink refused", func(t *testing.T) {
		path := lockTestPath(t)
		target := filepath.Join(t.TempDir(), "victim")
		require.NoError(t, os.Symlink(target, lockFilePath(path)))
		err := PatchIntField(path, "max_concurrent_agents", 5)
		require.Error(t, err, "a symlinked lock file must be refused, not followed")
		_, statErr := os.Lstat(target)
		assert.True(t, os.IsNotExist(statErr), "the symlink target must not be created")
		got, rerr := os.ReadFile(path)
		require.NoError(t, rerr)
		assert.Equal(t, lockTestFixture, string(got))
	})
}
