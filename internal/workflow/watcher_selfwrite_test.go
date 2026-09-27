package workflow_test

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/workflow"
)

// CORE-116: self-write suppression. Each test drives the real Watch loop and
// the real locked patchers; "daemon write" means a write through a
// workflow.Patch*/ApplyAndWriteFrontMatter call in this process, "operator
// edit" means a plain os.WriteFile that bypasses the package.

const selfWriteFixture = `---
tracker:
  kind: linear
agent:
  max_concurrent_agents: 3
---
prompt body
`

const (
	swPoll     = 10 * time.Millisecond
	swDebounce = 150 * time.Millisecond
	// swSettle comfortably exceeds poll + debounce so any reload that is
	// going to fire has fired.
	swSettle = 900 * time.Millisecond
)

// startCountingWatch runs workflow.Watch on path with shrunken intervals and
// returns the reload counter. Cleanup stops the watcher and waits for its
// goroutine to exit BEFORE restoring the intervals, so the restore never
// races the watcher's reads.
func startCountingWatch(t *testing.T, path string) *atomic.Int32 {
	t.Helper()
	prevPoll := workflow.SetPollInterval(swPoll)
	prevDebounce := workflow.SetDebounceInterval(swDebounce)
	ctx, cancel := context.WithCancel(context.Background())
	var reloads atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = workflow.Watch(ctx, path, func() { reloads.Add(1) })
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		workflow.SetPollInterval(prevPoll)
		workflow.SetDebounceInterval(prevDebounce)
	})
	// Let Watch capture its baseline stamp before the test writes.
	time.Sleep(5 * swPoll)
	return &reloads
}

func newSelfWriteFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "WORKFLOW.md")
	require.NoError(t, os.WriteFile(path, []byte(selfWriteFixture), 0o644))
	return path
}

// (a) A daemon write alone must not reload.
func TestWatchIgnoresRegisteredSelfWrite(t *testing.T) {
	path := newSelfWriteFixture(t)
	reloads := startCountingWatch(t, path)

	require.NoError(t, workflow.PatchIntField(path, "max_concurrent_agents", 5))

	time.Sleep(swSettle)
	require.Equal(t, int32(0), reloads.Load(),
		"a daemon settings save reloaded WORKFLOW.md — every in-flight agent turn would be killed")
}

// (c) An operator edit alone must still reload.
func TestWatchStillFiresOnForeignEdit(t *testing.T) {
	path := newSelfWriteFixture(t)
	reloads := startCountingWatch(t, path)

	require.NoError(t, os.WriteFile(path,
		[]byte(selfWriteFixture+"operator appended a line\n"), 0o644))

	require.Eventually(t, func() bool { return reloads.Load() == 1 },
		3*time.Second, 10*time.Millisecond, "an operator edit must reload")
	time.Sleep(swSettle)
	require.Equal(t, int32(1), reloads.Load())
}

// (b) A daemon write immediately followed by an operator edit (both inside
// one debounce window) must still reload: the settled bytes are not the
// daemon's.
func TestWatchStillFiresWhenForeignEditRacesSelfWrite(t *testing.T) {
	path := newSelfWriteFixture(t)
	reloads := startCountingWatch(t, path)

	require.NoError(t, workflow.PatchIntField(path, "max_concurrent_agents", 5))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append(data, []byte("operator edit\n")...), 0o644))

	require.Eventually(t, func() bool { return reloads.Load() == 1 },
		3*time.Second, 10*time.Millisecond,
		"an operator edit racing a daemon write was swallowed by self-write suppression")
	time.Sleep(swSettle)
	require.Equal(t, int32(1), reloads.Load())
}

// The mirror of (b): an operator edit immediately followed by a daemon
// read-modify-write. The daemon's write carries the operator's edit forward,
// so the settled bytes DO equal the daemon's post-image — but the operator's
// edit is not live in memory, so this must reload.
func TestWatchStillFiresWhenSelfWriteFollowsForeignEdit(t *testing.T) {
	path := newSelfWriteFixture(t)
	reloads := startCountingWatch(t, path)

	require.NoError(t, os.WriteFile(path,
		[]byte(selfWriteFixture+"operator edit\n"), 0o644))
	require.NoError(t, workflow.PatchIntField(path, "max_concurrent_agents", 5))

	require.Eventually(t, func() bool { return reloads.Load() == 1 },
		3*time.Second, 10*time.Millisecond,
		"an operator edit carried forward by a later daemon write was swallowed")
	time.Sleep(swSettle)
	require.Equal(t, int32(1), reloads.Load())
}

// One-shot: once a self-write has settled and been consumed, a later foreign
// rewrite of the identical bytes (new mtime) must reload.
func TestWatchStillFiresOnForeignEditMatchingConsumedHash(t *testing.T) {
	path := newSelfWriteFixture(t)
	reloads := startCountingWatch(t, path)

	require.NoError(t, workflow.PatchIntField(path, "max_concurrent_agents", 5))
	time.Sleep(swSettle)
	require.Equal(t, int32(0), reloads.Load(), "the self-write itself must be suppressed")

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	future := time.Now().Add(5 * time.Second)
	require.NoError(t, os.WriteFile(path, data, 0o644))
	require.NoError(t, os.Chtimes(path, future, future))

	require.Eventually(t, func() bool { return reloads.Load() == 1 },
		3*time.Second, 10*time.Millisecond,
		"a foreign rewrite of bytes matching an already-consumed self-write was swallowed")
}

// (d) Two rapid daemon writes inside one debounce window must not reload.
func TestWatchIgnoresRapidSelfWrites(t *testing.T) {
	path := newSelfWriteFixture(t)
	reloads := startCountingWatch(t, path)

	require.NoError(t, workflow.PatchIntField(path, "max_concurrent_agents", 5))
	require.NoError(t, workflow.PatchAgentBoolField(path, "inline_input", true))

	time.Sleep(swSettle)
	require.Equal(t, int32(0), reloads.Load(), "two back-to-back daemon saves reloaded")
}

// (e) A daemon write whose runtime apply fails and whose rollback branch
// rewrites the previous value (adapter_settings.go SetAutoClearWorkspace /
// SetDepsAnalysisMode shape) must not reload spuriously — including the
// cycle case where the rollback restores the original bytes exactly.
func TestWatchIgnoresSelfWriteRolledBack(t *testing.T) {
	path := newSelfWriteFixture(t)
	reloads := startCountingWatch(t, path)

	require.NoError(t, workflow.PatchIntField(path, "max_concurrent_agents", 5))
	require.NoError(t, workflow.PatchIntField(path, "max_concurrent_agents", 3)) // rollback
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, selfWriteFixture, string(got), "rollback should restore the original bytes")

	time.Sleep(swSettle)
	require.Equal(t, int32(0), reloads.Load(), "a rolled-back daemon save reloaded")

	// And the rollback cycle must be fully consumed: a later foreign edit
	// still reloads exactly once.
	require.NoError(t, os.WriteFile(path, []byte(selfWriteFixture+"operator\n"), 0o644))
	require.Eventually(t, func() bool { return reloads.Load() == 1 },
		3*time.Second, 10*time.Millisecond)
}

// Doc.Save (the SSH-host writer) is covered like every Patch* call.
func TestWatchIgnoresDocSaveSelfWrite(t *testing.T) {
	path := newSelfWriteFixture(t)
	reloads := startCountingWatch(t, path)

	require.NoError(t, workflow.NewDoc(path).SetSSHHosts([]string{"build-1"}, nil).Save())

	time.Sleep(swSettle)
	require.Equal(t, int32(0), reloads.Load(), "an SSH-host Doc.Save reloaded")
}

// WriteAndReload is the explicit opt-out for a value whose consumer only
// refreshes on reload (tracker states: the tracker client copies them once
// per run generation). It must reload like an operator edit.
func TestWatchStillFiresOnWriteAndReload(t *testing.T) {
	path := newSelfWriteFixture(t)
	reloads := startCountingWatch(t, path)

	require.NoError(t, workflow.WriteAndReload(path,
		workflow.MutateTrackerStates([]string{"Todo"}, []string{"Done"}, "Done")))

	require.Eventually(t, func() bool { return reloads.Load() == 1 },
		3*time.Second, 10*time.Millisecond, "WriteAndReload must still reload")
}

// BH3 guard for the alternative fix ("skip every link whose pre does not
// match"): an operator edit X, a daemon save over X, an operator revert to
// the baseline, and a daemon save over the baseline leave the file at the
// last daemon post via a link whose pre IS the baseline — yet the first
// daemon save's value is live in memory and gone from the file. Skipping the
// non-connecting links would suppress this; it must reload.
func TestWatchStillFiresWhenOperatorRevertsBetweenSelfWrites(t *testing.T) {
	path := newSelfWriteFixture(t)
	reloads := startCountingWatch(t, path)

	require.NoError(t, os.WriteFile(path, []byte(selfWriteFixture+"operator edit\n"), 0o644))
	require.NoError(t, workflow.PatchIntField(path, "max_concurrent_agents", 5))
	require.NoError(t, os.WriteFile(path, []byte(selfWriteFixture), 0o644)) // operator reverts everything
	require.NoError(t, workflow.PatchAgentBoolField(path, "inline_input", true))

	require.Eventually(t, func() bool { return reloads.Load() == 1 },
		3*time.Second, 10*time.Millisecond,
		"an operator revert between two daemon saves was swallowed")
	time.Sleep(swSettle)
	require.Equal(t, int32(1), reloads.Load())
}

// M1-close D5: a burst of more than 16 daemon saves inside one debounce
// window (holding the TUI's +/- key auto-repeats at ~30 saves/s) overflowed
// the 16-entry registry, evicted the chain's first links, and caused one
// spurious reload. 40 back-to-back saves must settle without a reload.
func TestWatchIgnoresBurstOfSelfWritesBeyondOldRingSize(t *testing.T) {
	path := newSelfWriteFixture(t)
	reloads := startCountingWatch(t, path)

	for i := range 40 {
		require.NoError(t, workflow.PatchIntField(path, "max_concurrent_agents", 4+i%2))
	}

	time.Sleep(swSettle)
	require.Equal(t, int32(0), reloads.Load(), "a burst of daemon saves reloaded WORKFLOW.md")
}
