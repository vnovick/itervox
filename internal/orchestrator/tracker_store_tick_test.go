package orchestrator

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/store"
)

// listCountingTracker counts the tracker's list reads (#113).
type listCountingTracker struct {
	*tracker.MemoryTracker
	lists atomic.Int64
}

func (c *listCountingTracker) FetchCandidateIssues(ctx context.Context) ([]domain.Issue, error) {
	c.lists.Add(1)
	return c.MemoryTracker.FetchCandidateIssues(ctx)
}

func (c *listCountingTracker) FetchIssuesByStates(ctx context.Context, states []string) ([]domain.Issue, error) {
	c.lists.Add(1)
	return c.MemoryTracker.FetchIssuesByStates(ctx, states)
}

func (c *listCountingTracker) RateLimitSnapshot() *tracker.RateLimitSnapshot { return nil }

// TestTickReadsComeFromTrackerStore (#113): with the store in front of the
// tracker, ticks after the cold pull make no list call, and still see the
// tracker's issues.
func TestTickReadsComeFromTrackerStore(t *testing.T) {
	cfg := dependencyAuditConfig()
	up := &listCountingTracker{MemoryTracker: tracker.NewMemoryTracker([]domain.Issue{
		{ID: "issue-1", Identifier: "ENG-1", State: "Todo"},
		{ID: "issue-2", Identifier: "ENG-2", State: "Done"},
	}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)}
	tr, _, err := store.Wrap(up, store.Config{
		Path:         filepath.Join(t.TempDir(), "tracker_store.json"),
		Scope:        "test",
		ActiveStates: cfg.Tracker.ActiveStates,
		Views:        [][]string{cfg.Tracker.TerminalStates},
		SyncInterval: time.Hour,
	})
	require.NoError(t, err)

	o := New(cfg, tr, nil, nil)
	state := NewState(cfg)
	state = o.onTick(t.Context(), state)
	afterFirst := up.lists.Load()
	require.Positive(t, afterFirst, "the cold store pulls once")

	for range 5 {
		state = o.onTick(t.Context(), state)
	}
	assert.Equal(t, afterFirst, up.lists.Load(), "ticks after the cold pull call no tracker list endpoint")
	assert.Equal(t, []CandidateSeenRow{{Identifier: "ENG-1"}}, state.CandidateSeen, "the tick saw the tracker's candidates")
}
