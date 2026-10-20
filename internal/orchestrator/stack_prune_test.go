package orchestrator

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
)

// TestStackMissExpiresWhenBlockerLeavesReview (#103): a recorded miss holds
// the dependent only while its blocker stays in the same review state; when
// the blocker leaves review (and later returns) the dependent is tried again.
func TestStackMissExpiresWhenBlockerLeavesReview(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tracker.ActiveStates = []string{"Todo", "In Progress"}
	cfg.Tracker.TerminalStates = []string{"Done", "Cancelled"}
	cfg.Agent.MaxConcurrentAgents = 3
	state := NewState(cfg)
	state.StackOnReviewState = "in review"
	ident, id, st := "ENG-1", "id1", "In Review"
	dependent := domain.Issue{ID: "id2", Identifier: "ENG-2", Title: "T", State: "Todo",
		BlockedBy: []domain.BlockerRef{{ID: &id, Identifier: &ident, State: &st}}}
	state.StackUnavailable["ENG-2"] = "ENG-1@in review"
	state.StackUnavailable["ENG-9"] = "ENG-8@in review" // no longer a candidate

	pruneStackUnavailable(&state, []domain.Issue{dependent})
	assert.Equal(t, "ENG-1@in review", state.StackUnavailable["ENG-2"], "blocker unchanged: the miss holds")
	assert.NotContains(t, state.StackUnavailable, "ENG-9", "a dependent that is no longer a candidate is pruned")
	assert.Contains(t, IneligibleReason(dependent, state, cfg), "blocked_by")

	rework := "Rework"
	moved := dependent
	moved.BlockedBy = []domain.BlockerRef{dependent.BlockedBy[0]}
	moved.BlockedBy[0].State = &rework
	pruneStackUnavailable(&state, []domain.Issue{moved})
	assert.NotContains(t, state.StackUnavailable, "ENG-2", "the blocker left review: the miss expires")
	assert.Equal(t, "", IneligibleReason(dependent, state, cfg), "back in review: tried again")
}

// TestShouldBackOutUnstacked (#103): only a fresh, unstacked worktree of an
// issue admitted for an in-review blocker backs out; an input-required
// resume, a reused worktree or a stacked one never does.
func TestShouldBackOutUnstacked(t *testing.T) {
	assert.True(t, shouldBackOutUnstacked(false, "", "ENG-1@in review"), "fresh or reused, an unstacked worktree backs out")
	assert.False(t, shouldBackOutUnstacked(true, "", "ENG-1@in review"), "input-required resume")
	assert.False(t, shouldBackOutUnstacked(false, "itervox/eng-1", "ENG-1@in review"), "stacked")
	assert.False(t, shouldBackOutUnstacked(false, "", ""), "not admitted for a review blocker")
}

// TestStackOnReviewStateFromStart (#103): the review state is known before
// the first tick publishes a snapshot, so a worker admitted on that tick
// still sees why it was admitted (it read an empty value and ran unstacked).
func TestStackOnReviewStateFromStart(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tracker.CompletionState = " In Review "
	assert.Equal(t, "", NewState(cfg).StackOnReviewState, "stacked_prs off")
	cfg.Dependencies.StackedPRs = true
	assert.Equal(t, "in review", NewState(cfg).StackOnReviewState)

	o := &Orchestrator{cfg: cfg}
	o.lastSnap = State{} // a snapshot that predates any tick
	ident, st := "ENG-1", "In Review"
	issue := domain.Issue{Identifier: "ENG-2", BlockedBy: []domain.BlockerRef{{Identifier: &ident, State: &st}}}
	assert.Equal(t, "ENG-1@in review", o.reviewStackKeyNow(issue))
}
