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
