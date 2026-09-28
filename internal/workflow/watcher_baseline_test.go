package workflow_test

import (
	"context"
	"crypto/sha256"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/workflow"
)

// startCountingWatchFrom is startCountingWatch for WatchFrom: the watcher's
// baseline is the hash the caller loaded, not the watcher's first reading.
func startCountingWatchFrom(t *testing.T, path string, baseline [32]byte) *atomic.Int32 {
	t.Helper()
	prevPoll := workflow.SetPollInterval(swPoll)
	prevDebounce := workflow.SetDebounceInterval(swDebounce)
	ctx, cancel := context.WithCancel(context.Background())
	var reloads atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = workflow.WatchFrom(ctx, path, baseline, func() { reloads.Add(1) })
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		workflow.SetPollInterval(prevPoll)
		workflow.SetDebounceInterval(prevDebounce)
	})
	return &reloads
}

// Load's ContentHash is the hash of exactly the bytes it parsed.
func TestLoadContentHashIsParsedBytes(t *testing.T) {
	path := newSelfWriteFixture(t)
	wf, err := workflow.Load(path)
	require.NoError(t, err)
	require.Equal(t, sha256.Sum256([]byte(selfWriteFixture)), wf.ContentHash)
}

// M1-close C2 (BH3 window): the daemon loads WORKFLOW.md, then starts the
// watcher. An operator edit landing between the two used to become the
// watcher's baseline, so it never reloaded and the running config silently
// differed from the file. With the loaded hash as the baseline, the edit is
// a change from the start and reloads.
func TestWatchFromReloadsOnEditBetweenLoadAndWatch(t *testing.T) {
	path := newSelfWriteFixture(t)
	wf, err := workflow.Load(path)
	require.NoError(t, err)

	// The test hook: an operator edit between Load and the watcher start.
	require.NoError(t, os.WriteFile(path, []byte(selfWriteFixture+"operator edit in the load window\n"), 0o644))

	reloads := startCountingWatchFrom(t, path, wf.ContentHash)
	require.Eventually(t, func() bool { return reloads.Load() == 1 },
		3*time.Second, 10*time.Millisecond,
		"an edit between config load and watcher start was never reloaded")
	time.Sleep(swSettle)
	require.Equal(t, int32(1), reloads.Load())
}

// The other half: a daemon save that lands on top of the loaded bytes before
// the watcher's first reading is still recognised as the daemon's own (its
// link's pre is the loaded baseline) and does not reload.
func TestWatchFromIgnoresSelfWriteBeforeFirstReading(t *testing.T) {
	path := newSelfWriteFixture(t)
	wf, err := workflow.Load(path)
	require.NoError(t, err)
	workflow.ForgetSelfWrites(path) // what the daemon's reload step does after Load

	require.NoError(t, workflow.PatchIntField(path, "max_concurrent_agents", 5))

	reloads := startCountingWatchFrom(t, path, wf.ContentHash)
	time.Sleep(swSettle)
	require.Equal(t, int32(0), reloads.Load(), "a daemon save on top of the loaded bytes reloaded")
}

// BH3 (round B) in its new home: a save that landed before Load (the reload
// window) leaves a link Load's bytes already reflect; the reload step drops
// it, so the next save is suppressed rather than breaking the chain.
func TestWatchFromIgnoresSelfWriteAfterSaveBeforeLoad(t *testing.T) {
	path := newSelfWriteFixture(t)
	require.NoError(t, workflow.PatchIntField(path, "max_concurrent_agents", 5)) // reload-window save
	wf, err := workflow.Load(path)
	require.NoError(t, err)
	workflow.ForgetSelfWrites(path)
	reloads := startCountingWatchFrom(t, path, wf.ContentHash)
	time.Sleep(5 * swPoll)

	require.NoError(t, workflow.PatchIntField(path, "max_concurrent_agents", 7))
	time.Sleep(swSettle)
	require.Equal(t, int32(0), reloads.Load(),
		"a stale pre-load self-write link turned the next daemon save into a spurious reload")
}

// M1-close D4 (BH3 ABA): a daemon save on top of the loaded bytes, then an
// operator revert to EXACTLY the loaded bytes, both before WatchFrom's first
// reading. The first reading equals the loaded hash, so a hash-only check saw
// "no change" — yet the running config holds the save's value and the file
// does not. A self-write chain from the loaded hash that does not end on the
// current bytes means a foreign write intervened: reload.
func TestWatchFromReloadsOnRevertToLoadedBytesAfterSelfWrite(t *testing.T) {
	path := newSelfWriteFixture(t)
	wf, err := workflow.Load(path)
	require.NoError(t, err)
	workflow.ForgetSelfWrites(path)

	require.NoError(t, workflow.PatchIntField(path, "max_concurrent_agents", 5)) // daemon save
	require.NoError(t, os.WriteFile(path, []byte(selfWriteFixture), 0o644))      // operator revert

	reloads := startCountingWatchFrom(t, path, wf.ContentHash)
	require.Eventually(t, func() bool { return reloads.Load() == 1 },
		3*time.Second, 10*time.Millisecond,
		"an operator revert to the loaded bytes after a daemon save was swallowed")
	time.Sleep(swSettle)
	require.Equal(t, int32(1), reloads.Load())
}

// The legitimate cycle stays quiet: a daemon save and its own rollback to the
// loaded bytes before the first reading end the chain on the current bytes.
func TestWatchFromIgnoresSelfWriteRolledBackBeforeFirstReading(t *testing.T) {
	path := newSelfWriteFixture(t)
	wf, err := workflow.Load(path)
	require.NoError(t, err)
	workflow.ForgetSelfWrites(path)

	require.NoError(t, workflow.PatchIntField(path, "max_concurrent_agents", 5))
	require.NoError(t, workflow.PatchIntField(path, "max_concurrent_agents", 3)) // rollback
	reloads := startCountingWatchFrom(t, path, wf.ContentHash)
	time.Sleep(swSettle)
	require.Equal(t, int32(0), reloads.Load(), "a daemon save and its rollback reloaded")
}

// M1-close E2: before the first reading, an operator edit X, a daemon save
// over X, then an operator revert to the loaded bytes. The daemon's link
// starts at X, not at the loaded hash, so no chain from the loaded bytes
// exists and the D4 check stayed quiet — while memory holds the save's value
// and the file the loaded bytes. Any registered link that is not on the
// chain from the loaded hash is evidence of a foreign write: reload.
func TestWatchFromReloadsWhenSelfWriteChainIsDetachedFromLoadedBytes(t *testing.T) {
	path := newSelfWriteFixture(t)
	wf, err := workflow.Load(path)
	require.NoError(t, err)
	workflow.ForgetSelfWrites(path)

	require.NoError(t, os.WriteFile(path, []byte(selfWriteFixture+"operator edit\n"), 0o644)) // X
	require.NoError(t, workflow.PatchIntField(path, "max_concurrent_agents", 5))              // daemon save over X
	require.NoError(t, os.WriteFile(path, []byte(selfWriteFixture), 0o644))                   // revert to loaded

	reloads := startCountingWatchFrom(t, path, wf.ContentHash)
	require.Eventually(t, func() bool { return reloads.Load() == 1 },
		3*time.Second, 10*time.Millisecond,
		"a daemon save detached from the loaded bytes was swallowed")
	time.Sleep(swSettle)
	require.Equal(t, int32(1), reloads.Load())
}

// Quiet cases under the E2 rule: a burst of daemon saves on top of the
// loaded bytes before the first reading is one chain and stays quiet.
func TestWatchFromIgnoresSelfWriteBurstBeforeFirstReading(t *testing.T) {
	path := newSelfWriteFixture(t)
	wf, err := workflow.Load(path)
	require.NoError(t, err)
	workflow.ForgetSelfWrites(path)
	for i := range 30 {
		require.NoError(t, workflow.PatchIntField(path, "max_concurrent_agents", 4+i%3))
	}
	reloads := startCountingWatchFrom(t, path, wf.ContentHash)
	time.Sleep(swSettle)
	require.Equal(t, int32(0), reloads.Load(), "a burst of daemon saves before the first reading reloaded")
}
