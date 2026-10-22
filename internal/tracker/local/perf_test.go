package local

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLocalTrackerFiltersBeforeConvert (#210): a candidate poll of a
// 500-issue board with 3 active issues builds and sorts only those 3 — the
// state filter runs on the parsed files before any issue is converted. The
// allocation count is the observable: converting an issue allocates its
// description, labels and comments, so converting all 501 costs thousands.
func TestLocalTrackerFiltersBeforeConvert(t *testing.T) {
	tr, dir := newTestTracker(t)
	for i := 1; i <= 501; i++ {
		state := "Done"
		if i%167 == 0 { // ITX-167, ITX-334, ITX-501: 3 active issues
			state = "Todo"
		}
		writeRaw(t, filepath.Join(dir, fmt.Sprintf("ITX-%d.md", i)), fmt.Sprintf(
			"---\ntitle: Issue %d\nstate: %s\nlabels: [perf, backend]\n---\nBody of issue %d.\n", i, state, i))
	}
	got, err := tr.FetchCandidateIssues(t.Context())
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, []string{"ITX-167", "ITX-334", "ITX-501"}, []string{got[0].Identifier, got[1].Identifier, got[2].Identifier})

	// As under a healthy watch: reads are served from the entries, so the
	// count is the filter + convert path alone, not a directory rescan.
	tr.mu.Lock()
	tr.watched = true
	tr.mu.Unlock()
	allocs := testing.AllocsPerRun(20, func() {
		issues, err := tr.FetchCandidateIssues(t.Context())
		if err != nil || len(issues) != 3 {
			t.Fatalf("got %d issues, err %v", len(issues), err)
		}
	})
	assert.Less(t, allocs, 100.0, "converting the 498 inactive issues too would cost thousands of allocations")

	byState, err := tr.FetchIssuesByStates(t.Context(), []string{"done"})
	require.NoError(t, err)
	assert.Len(t, byState, 498, "the state match stays case-insensitive")
	byID, err := tr.FetchIssueStatesByIDs(t.Context(), []string{"ITX-2", "ITX-334", "ITX-999"})
	require.NoError(t, err)
	require.Len(t, byID, 2)
	assert.Equal(t, "ITX-2", byID[0].ID)
	assert.Equal(t, "ITX-334", byID[1].ID)
}

// TestWatchSafetyRescanSlowsWhileWatched (#210): while the directory watch
// holds, the safety-net rescan runs at the healthy interval (60 s in
// production), not every rescan interval; without a watch it keeps the
// fast rate.
func TestWatchSafetyRescanSlowsWhileWatched(t *testing.T) {
	assert.Equal(t, HealthyRescanInterval, healthyRescan(0))
	assert.Equal(t, 60*time.Second, HealthyRescanInterval)
	assert.Equal(t, 5*time.Second, healthyRescan(5*time.Second), "an explicit interval is used for both")

	t.Run("watched", func(t *testing.T) {
		tr, dir := newTestTracker(t)
		writeRaw(t, filepath.Join(dir, "ITX-1.md"), "---\ntitle: One\nstate: Backlog\n---\n")
		events := make(chan fsnotify.Event) // no file event ever arrives
		changes := startWatch(t, func(ctx context.Context, changed func()) {
			tr.watch(ctx, events, nil, 20*time.Millisecond, time.Hour, changed, func() bool { return true })
		})
		time.Sleep(30 * time.Millisecond)
		writeRaw(t, filepath.Join(dir, "ITX-1.md"), "---\ntitle: One\nstate: Todo\n---\n")
		noChange(t, changes, 300*time.Millisecond, "a healthy watch rescans hourly here, not every 20 ms")
	})
	t.Run("unwatched", func(t *testing.T) {
		tr, dir := newTestTracker(t)
		writeRaw(t, filepath.Join(dir, "ITX-1.md"), "---\ntitle: One\nstate: Backlog\n---\n")
		changes := startWatch(t, func(ctx context.Context, changed func()) {
			tr.watch(ctx, nil, nil, 20*time.Millisecond, time.Hour, changed, func() bool { return false })
		})
		time.Sleep(30 * time.Millisecond)
		writeRaw(t, filepath.Join(dir, "ITX-1.md"), "---\ntitle: One\nstate: Todo\n---\n")
		waitChange(t, changes, 2*time.Second, "without a watch the fast rescan finds the edit")
	})
}
