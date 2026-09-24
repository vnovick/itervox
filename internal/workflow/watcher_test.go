package workflow_test

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/workflow"
)

func TestWatcherTriggersOnChange(t *testing.T) {
	// Shrink the quiet period; the production default is seconds-long by
	// design and this test only cares that a settled change fires at all.
	defer workflow.SetDebounceInterval(workflow.SetDebounceInterval(10 * time.Millisecond))

	dir := t.TempDir()
	wfPath := filepath.Join(dir, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(wfPath, []byte("---\n---\nBody.\n"), 0o644))

	var called atomic.Int32
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		_ = workflow.Watch(ctx, wfPath, func() {
			called.Add(1)
		})
	}()

	// Give the watcher time to start
	time.Sleep(50 * time.Millisecond)

	// Trigger a change
	require.NoError(t, os.WriteFile(wfPath, []byte("---\n---\nUpdated.\n"), 0o644))

	assert.Eventually(t, func() bool {
		return called.Load() > 0
	}, 3*time.Second, 50*time.Millisecond, "onChange should be called after file write")
}

// The reported symptom: editing several things in WORKFLOW.md and saving as you
// go used to reload the daemon once per save. Each reload tears down the run
// loop and rebinds the HTTP listener, so a multi-part edit was unusable. A
// burst of writes must coalesce into exactly ONE reload.
func TestWatcherCoalescesBurstIntoSingleReload(t *testing.T) {
	defer workflow.SetDebounceInterval(workflow.SetDebounceInterval(2 * time.Second))

	dir := t.TempDir()
	wfPath := filepath.Join(dir, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(wfPath, []byte("---\n---\nBody.\n"), 0o644))

	var called atomic.Int32
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	go func() {
		_ = workflow.Watch(ctx, wfPath, func() { called.Add(1) })
	}()
	time.Sleep(50 * time.Millisecond)

	// Five saves spread across more than one poll tick each, all inside the
	// 2s quiet period — i.e. exactly what editing several settings looks like.
	for i := range 5 {
		body := "---\n---\nEdit " + string(rune('A'+i)) + ".\n"
		require.NoError(t, os.WriteFile(wfPath, []byte(body), 0o644))
		time.Sleep(400 * time.Millisecond)
	}

	// Nothing should have fired yet: the file never stayed still for 2s.
	assert.Equal(t, int32(0), called.Load(),
		"a reload fired mid-burst — the daemon would restart on a half-finished edit")

	assert.Eventually(t, func() bool {
		return called.Load() == 1
	}, 6*time.Second, 50*time.Millisecond, "the settled burst should produce exactly one reload")

	// And it must not fire again for the same settled edit.
	time.Sleep(2 * time.Second)
	assert.Equal(t, int32(1), called.Load(), "onChange fired more than once for one burst")
}

// A change that never settles must not fire, and must not be lost either — it
// fires once the writing stops.
func TestWatcherDoesNotFireWhileFileKeepsChanging(t *testing.T) {
	defer workflow.SetDebounceInterval(workflow.SetDebounceInterval(3 * time.Second))

	dir := t.TempDir()
	wfPath := filepath.Join(dir, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(wfPath, []byte("---\n---\nBody.\n"), 0o644))

	var called atomic.Int32
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	go func() {
		_ = workflow.Watch(ctx, wfPath, func() { called.Add(1) })
	}()
	time.Sleep(50 * time.Millisecond)

	// Keep writing for longer than the quiet period.
	deadline := time.Now().Add(5 * time.Second)
	for i := 0; time.Now().Before(deadline); i++ {
		require.NoError(t, os.WriteFile(wfPath,
			[]byte("---\n---\nStill typing "+string(rune('A'+i%26))+".\n"), 0o644))
		time.Sleep(500 * time.Millisecond)
	}
	assert.Equal(t, int32(0), called.Load(),
		"fired while the file was still being written")

	// Now stop; the pending change must still be delivered.
	assert.Eventually(t, func() bool {
		return called.Load() == 1
	}, 8*time.Second, 50*time.Millisecond, "the pending change was dropped instead of deferred")
}
