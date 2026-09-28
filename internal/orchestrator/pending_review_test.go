package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// M4-close BH-M4-2 — a worker that succeeds mid-drain has its auto-review
// refused (no admission while draining). The refusal is persisted as a
// pending review, so the reviewer runs after the restart instead of being
// lost with the worktree left behind.
func TestReviewerRefusedMidDrainRunsAfterRestart(t *testing.T) {
	dir := t.TempDir()
	pendingPath := filepath.Join(dir, "pending_reviews.json")
	newCfg := func() *config.Config {
		cfg := automationBaseCfg()
		cfg.Agent.MaxConcurrentAgents = 1
		cfg.Agent.MaxTurns = 1
		cfg.Polling.IntervalMs = 20
		cfg.Agent.AutoReview = true
		cfg.Agent.ReviewerProfile = "reviewer"
		cfg.Agent.Profiles = map[string]config.AgentProfile{"default": {}, "reviewer": {}}
		cfg.Tracker.CompletionState = "In Review"
		return cfg
	}
	issue := domain.Issue{ID: "id-1", Identifier: "ENG-1", Title: "T", State: "In Progress"}
	cfg1 := newCfg()
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg1.Tracker.ActiveStates, cfg1.Tracker.TerminalStates)

	// Generation 1: the worker is mid-turn when the drain begins, then
	// succeeds. The reviewer is refused.
	rr1 := &gatedRunner{release: make(chan struct{})}
	o1 := New(cfg1, mt, rr1, nil)
	o1.SetPendingReviewsFile(pendingPath)
	ctx1, cancel1 := context.WithCancel(context.Background())
	done1 := make(chan struct{})
	go func() { defer close(done1); _ = o1.Run(ctx1) }()
	require.Eventually(t, func() bool { return rr1.count() == 1 }, 5*time.Second, 5*time.Millisecond)
	require.NoError(t, o1.RequestDrain())
	require.Eventually(t, func() bool { return o1.Snapshot().Draining }, 5*time.Second, 5*time.Millisecond)
	close(rr1.release)
	select {
	case <-o1.Drained():
	case <-time.After(5 * time.Second):
		t.Fatal("drain never completed")
	}
	cancel1()
	<-done1
	assert.Equal(t, 1, rr1.count(), "the reviewer did not start during the drain")
	data, err := os.ReadFile(pendingPath)
	require.NoError(t, err, "the refused review is persisted")
	assert.Contains(t, string(data), `"ENG-1"`)
	assert.Contains(t, string(data), `"reviewer"`)

	// Generation 2 (the restart): the pending review is dispatched.
	rr2 := &gatedRunner{release: make(chan struct{})}
	o2 := New(newCfg(), mt, rr2, nil)
	o2.SetPendingReviewsFile(pendingPath)
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan struct{})
	go func() { defer close(done2); _ = o2.Run(ctx2) }()
	defer func() { cancel2(); <-done2 }()
	require.Eventually(t, func() bool {
		for _, r := range o2.Snapshot().Running {
			if r.Issue.Identifier == "ENG-1" && r.Kind == "reviewer" {
				return true
			}
		}
		return false
	}, 5*time.Second, 5*time.Millisecond, "the reviewer runs after the restart")
	close(rr2.release)
	require.Eventually(t, func() bool {
		b, err := os.ReadFile(pendingPath)
		return err == nil && !strings.Contains(string(b), "ENG-1")
	}, 5*time.Second, 5*time.Millisecond, "the marker is cleared once the reviewer started")
}
