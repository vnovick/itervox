package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPIDFilePathResolvesSymlinks pins that two spellings of ONE checkout
// agree on the pid file, which is what makes the "another daemon is already
// running" guard work.
//
// filepath.Abs alone does not resolve symlinks, so a symlinked checkout
// produced a different absolute string for the same directory: two daemons on
// the same WORKFLOW.md wrote two different pid files, neither saw the other,
// and both clobbered the same .itervox state — the exact failure the guard
// exists to prevent.
func TestPIDFilePathResolvesSymlinks(t *testing.T) {
	real := filepath.Join(t.TempDir(), "checkout")
	require.NoError(t, os.MkdirAll(real, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(real, "WORKFLOW.md"), []byte("---\n---\n"), 0o644))

	link := filepath.Join(t.TempDir(), "linked")
	require.NoError(t, os.Symlink(real, link))

	viaReal, err := pidFilePath(filepath.Join(real, "WORKFLOW.md"))
	require.NoError(t, err)
	viaLink, err := pidFilePath(filepath.Join(link, "WORKFLOW.md"))
	require.NoError(t, err)

	assert.Equal(t, viaReal, viaLink,
		"a symlinked checkout must resolve to the same pid file, or the "+
			"already-running guard never fires and two daemons share one .itervox")
}

// TestPIDFilePathToleratesMissingDirectory — EvalSymlinks fails on a path that
// does not exist yet, which is the normal first-run case. It must degrade, not
// error.
func TestPIDFilePathToleratesMissingDirectory(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-created-yet", "WORKFLOW.md")
	got, err := pidFilePath(missing)
	require.NoError(t, err, "a not-yet-created project must still resolve a pid path")
	assert.Contains(t, got, filepath.Join(".itervox", "daemon.pid"))
}

// TestWarnIfDaemonRunning pins the advisory used by the one-shot subcommands
// that write `.itervox/` state outside the daemon — `init --update` and
// `deps analyze`. They bypass requireNoLiveDaemon entirely, so their writes
// race the running daemon's: a migration rewrites WORKFLOW.md under a daemon
// that already parsed it, and an analyzer pass rewrites the sidecar the daemon
// is reading.
func TestWarnIfDaemonRunning(t *testing.T) {
	dir := t.TempDir()
	wf := filepath.Join(dir, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(wf, []byte("---\n---\n"), 0o644))

	capture := func(fn func()) string {
		t.Helper()
		orig := os.Stderr
		r, w, err := os.Pipe()
		require.NoError(t, err)
		os.Stderr = w
		fn()
		require.NoError(t, w.Close())
		os.Stderr = orig
		out, err := io.ReadAll(r)
		require.NoError(t, err)
		return string(out)
	}

	// No pid file: silent. A warning on every invocation would be noise.
	assert.Empty(t, capture(func() { warnIfDaemonRunning(wf, "test") }))

	// A live daemon (this process stands in for one) must be reported.
	_, err := writePIDFile(wf)
	require.NoError(t, err)
	out := capture(func() { warnIfDaemonRunning(wf, "`itervox deps analyze`") })
	assert.Contains(t, out, "a daemon is running")
	assert.Contains(t, out, "itervox deps analyze",
		"the warning must name the action so the operator knows what raced")
}
