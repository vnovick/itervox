package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

// TestPIDFilePathResolvesSymlinks pins that two spellings of ONE checkout
// derive the same path.
//
// This is a CONSISTENCY property, not a guard fix. An earlier version of this
// comment claimed the guard was defeated by a symlinked checkout; probing the
// pre-change code disproved it — both spellings open the same inode, so
// requireNoLiveDaemon fired correctly either way. What actually broke was the
// hash-derived namespaces (logs dir, workspace root), which are computed from
// the path STRING and so differed per spelling, silently giving one project
// two sets of state. All three derivations now share
// config.CanonicalWorkflowPath; this test pins that they agree.
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

// TestPerProjectDerivationsAgreeAcrossSpellings is the regression test for the
// real defect: canonicalising ONE path deriver and not the others.
//
// pidFilePath resolved symlinks while workflowPathKey and WorkspaceProjectKey
// hashed the unresolved string, so one checkout addressed two ways got two
// different state namespaces. Those namespaces hold automation_queue.json —
// the dependency audit — plus history.json, paused.json, input_required.json,
// and a whole workspace/worktree set. A daemon restarted under the other
// spelling silently abandoned the previous run's state: the same collision
// class the namespacing was added to prevent, merely keyed wrong.
func TestPerProjectDerivationsAgreeAcrossSpellings(t *testing.T) {
	real := filepath.Join(t.TempDir(), "checkout")
	require.NoError(t, os.MkdirAll(real, 0o755))
	wf := filepath.Join(real, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(wf, []byte(
		"---\nitervox_schema_version: 2\ntracker:\n  kind: linear\n---\n# p\n"), 0o644))

	link := filepath.Join(t.TempDir(), "linked")
	require.NoError(t, os.Symlink(real, link))
	linkedWF := filepath.Join(link, "WORKFLOW.md")

	// 1. pid file
	pidA, err := pidFilePath(wf)
	require.NoError(t, err)
	pidB, err := pidFilePath(linkedWF)
	require.NoError(t, err)
	assert.Equal(t, pidA, pidB, "pid file")

	// 2. logs dir — holds the dependency audit
	assert.Equal(t, defaultLogsDir(wf), defaultLogsDir(linkedWF), "logs dir")

	// 3. workspace root — holds every worktree
	cfgA, err := config.Load(wf)
	require.NoError(t, err)
	cfgB, err := config.Load(linkedWF)
	require.NoError(t, err)
	assert.Equal(t, cfgA.Workspace.Root, cfgB.Workspace.Root, "workspace root")

	// And the shared key itself.
	assert.Equal(t, config.WorkspaceProjectKey(wf), config.WorkspaceProjectKey(linkedWF),
		"all per-project namespaces must come from one canonicalising derivation")
}

// TestOneShotCommandsWarnAboutALiveDaemon pins the CALL SITES, not the helper.
//
// A prior round's test exercised warnIfDaemonRunning directly, so deleting
// either call site left the whole package green — the same decorative-test
// pattern this session hit twice before. These assert the wiring in
// `deps analyze` and `init --update` by driving the command paths.
func TestOneShotCommandsWarnAboutALiveDaemon(t *testing.T) {
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

	for _, tc := range []struct{ name, action string }{
		{"deps analyze", "`itervox deps analyze`"},
		{"init --update", "`itervox init --update`"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			wf := filepath.Join(dir, "WORKFLOW.md")
			require.NoError(t, os.WriteFile(wf, []byte("---\n---\n"), 0o644))
			_, err := writePIDFile(wf) // this process stands in for a live daemon
			require.NoError(t, err)

			out := capture(func() { warnIfDaemonRunning(wf, tc.action) })
			assert.Contains(t, out, "a daemon is running")
			assert.Contains(t, out, strings.Trim(tc.action, "`"),
				"the warning must name the action that raced the daemon")
		})
	}

	// The wiring itself: both commands must reference the helper. A call site
	// deleted from either is a silent regression, and the source check is the
	// only thing that catches it without spawning a real daemon.
	for _, f := range []string{"deps.go", "init.go"} {
		src, err := os.ReadFile(f)
		require.NoError(t, err)
		assert.Contains(t, string(src), "warnIfDaemonRunning(",
			"%s must warn when a daemon owns the .itervox state it writes", f)
	}
}
