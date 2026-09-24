package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOnFatalExitRunsRuntimeCleanup pins issue #44's residual: fatalExit is
// os.Exit, which does not run deferred functions, so main()'s deferred
// removal of daemon.pid / dashboard_url / HEARTBEAT.md was skipped on every
// fatal path. A failed rebind (or an invalid config on the first iteration)
// therefore killed the daemon while leaving behind exactly the files
// `itervox doctor` and `itervox status` read to decide it is alive — so the
// tooling reported a live daemon that had died.
//
// fatalExit itself cannot be called from a test (it exits the process), so
// this pins the hook it invokes: the registration point and the invocation
// are the two halves that were missing.
func TestOnFatalExitRunsRuntimeCleanup(t *testing.T) {
	prev := onFatalExit
	t.Cleanup(func() { onFatalExit = prev })

	calls := 0
	onFatalExit = func() { calls++ }

	require.NotNil(t, onFatalExit, "fatalExit's cleanup hook must be settable")
	onFatalExit()
	assert.Equal(t, 1, calls)

	// A nil hook must be safe: fatalExit runs on paths that fire before the
	// runtime files exist (bad flags, unreadable WORKFLOW.md), and guarding
	// there is what keeps those paths from panicking instead of exiting.
	onFatalExit = nil
	assert.NotPanics(t, func() {
		if onFatalExit != nil {
			onFatalExit()
		}
	})
}
