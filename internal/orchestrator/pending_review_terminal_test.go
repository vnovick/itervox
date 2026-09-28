package orchestrator

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

func pendingReviewHarness(t *testing.T, completion string, issues ...domain.Issue) (*Orchestrator, State, *gatedRunner) {
	t.Helper()
	cfg := automationBaseCfg()
	cfg.Agent.MaxConcurrentAgents = 2
	cfg.Agent.MaxTurns = 1
	cfg.Agent.ReviewerProfile = "reviewer"
	cfg.Agent.Profiles = map[string]config.AgentProfile{"default": {}, "reviewer": {}}
	cfg.Tracker.CompletionState = completion
	mt := tracker.NewMemoryTracker(issues, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	rr := &gatedRunner{release: make(chan struct{})}
	o := New(cfg, mt, rr, nil)
	o.SetPendingReviewsFile(filepath.Join(t.TempDir(), "pending_reviews.json"))
	t.Cleanup(func() { close(rr.release); o.workersWg.Wait(5 * time.Second) })
	return o, NewState(cfg), rr
}

func reviewerRunning(state State, identifier string) bool {
	for _, r := range state.Running {
		if r.Issue.Identifier == identifier && r.Kind == "reviewer" {
			return true
		}
	}
	return false
}

// TestPendingReviewDroppedWhenIssueWentTerminal (CORE-174): a review refused
// mid-drain is not replayed after the restart when the issue has since moved
// to a terminal state (Done / Cancelled) or was deleted.
func TestPendingReviewDroppedWhenIssueWentTerminal(t *testing.T) {
	done := domain.Issue{ID: "id-1", Identifier: "ENG-1", Title: "T", State: "Cancelled"}
	o, state, _ := pendingReviewHarness(t, "In Review", done)
	now := time.Now()
	state.PendingReviews = map[string]PendingReview{
		"ENG-1": {IssueID: "id-1", Identifier: "ENG-1", Profile: "reviewer", QueuedAt: now, IssueState: "In Review"},
		"ENG-9": {IssueID: "id-gone", Identifier: "ENG-9", Profile: "reviewer", QueuedAt: now, IssueState: "In Review"},
	}
	o.resumePendingReviews(context.Background(), &state, now)

	assert.False(t, reviewerRunning(state, "ENG-1"), "no review of a cancelled issue")
	assert.NotContains(t, state.PendingReviews, "ENG-1", "the terminal issue's marker is dropped")
	assert.NotContains(t, state.PendingReviews, "ENG-9", "a deleted issue's marker is dropped")
}

// TestPendingReviewKeptWhenCompletionStateIsTerminal (CORE-174 control):
// with a terminal tracker.completion_state the issue is terminal BY DESIGN
// when its review is queued, so that review still runs.
func TestPendingReviewKeptWhenCompletionStateIsTerminal(t *testing.T) {
	issue := domain.Issue{ID: "id-2", Identifier: "ENG-2", Title: "T", State: "Done"}
	o, state, rr := pendingReviewHarness(t, "Done", issue)
	now := time.Now()
	state.PendingReviews = map[string]PendingReview{
		"ENG-2": {IssueID: "id-2", Identifier: "ENG-2", Profile: "reviewer", QueuedAt: now, IssueState: "Done"},
	}
	o.resumePendingReviews(context.Background(), &state, now)
	require.True(t, reviewerRunning(state, "ENG-2"), "the review of a completion-state issue still runs")
	require.Eventually(t, func() bool { return rr.count() == 1 }, 5*time.Second, 5*time.Millisecond)
}
