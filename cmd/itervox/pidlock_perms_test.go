//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func permTestWorkflow(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	return filepath.Join(dir, "WORKFLOW.md")
}

// The pid lock must not be openable by other local users: any process that
// can open it — read-only is enough for flock(2) — can hold LOCK_EX and keep
// every daemon for this project from starting. So after a claim the lock and
// the pid record are 0600 and .itervox/runtime/ is 0700.
func TestClaimPIDFileCreatesPrivateLockAndRecord(t *testing.T) {
	wf := permTestWorkflow(t)
	_, _, pidPath, err := claimPIDFile(wf)
	require.NoError(t, err)
	t.Cleanup(func() { removePIDFile(wf) })

	lockPath := pidLockPath(pidPath)
	for path, want := range map[string]os.FileMode{
		lockPath:               0o600,
		pidPath:                0o600,
		filepath.Dir(lockPath): 0o700,
	} {
		info, statErr := os.Lstat(path)
		require.NoError(t, statErr, path)
		assert.Equal(t, want, info.Mode().Perm(), "%s mode", path)
	}
}

// A runtime dir and lock file left world-readable by an older daemon are
// tightened on the next claim (we own them), not just new ones.
func TestClaimPIDFileTightensExistingRuntimeDirAndLock(t *testing.T) {
	wf := permTestWorkflow(t)
	pidPath, err := pidFilePath(wf)
	require.NoError(t, err)
	lockPath := pidLockPath(pidPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(lockPath), 0o755))
	require.NoError(t, os.Chmod(filepath.Dir(lockPath), 0o755)) // defeat umask
	require.NoError(t, os.WriteFile(lockPath, nil, 0o644))
	require.NoError(t, os.Chmod(lockPath, 0o644))

	_, _, _, err = claimPIDFile(wf)
	require.NoError(t, err)
	t.Cleanup(func() { removePIDFile(wf) })

	dirInfo, err := os.Stat(filepath.Dir(lockPath))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())
	lockInfo, err := os.Stat(lockPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), lockInfo.Mode().Perm())
}

// A symlink planted at the lock path is refused (O_NOFOLLOW): the daemon
// must neither create nor lock the file it points at.
func TestTryLockPIDFileRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere")
	lockPath := filepath.Join(dir, "daemon.pid.lock")
	require.NoError(t, os.Symlink(target, lockPath))

	release, acquired, err := tryLockPIDFile(lockPath)
	if release != nil {
		release()
	}
	require.Error(t, err, "a symlinked lock path must be refused")
	assert.False(t, acquired)
	_, statErr := os.Lstat(target)
	assert.True(t, os.IsNotExist(statErr), "the symlink target must not be created, lstat err=%v", statErr)
}
