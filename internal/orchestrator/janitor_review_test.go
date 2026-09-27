package orchestrator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// CORE-034: the review maps (ReviewVerdicts, ReviewOutcomes, ReviewChainIndex)
// had no retention policy — a chain abandoned by a reviewer failure left its
// index behind, and verdicts/outcomes were only reset when the same issue was
// reviewed again. The janitor now prunes them for issues that are no longer
// tracked (terminal, or absent from two consecutive polls), never mid-chain.
func TestJanitorPrunesReviewLedgersForUntrackedIssues(t *testing.T) {
	cfg := automationBaseCfg()
	active := domain.Issue{ID: "id-active", Identifier: "ENG-ACTIVE", Title: "T", State: "In Progress"}
	mt := tracker.NewMemoryTracker([]domain.Issue{active}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	o := New(cfg, mt, nil, nil)
	o.DryRun = true // no agent runs; the janitor pass is what is under test

	state := NewState(cfg)
	seed := func(ident string) {
		state.ReviewVerdicts[ident] = []ReviewVerdict{{Profile: "r1", Verdict: ReviewVerdictApprove}}
		state.ReviewOutcomes[ident] = ReviewOutcome{Approvals: 1}
		state.ReviewChainIndex[ident] = 1
	}
	// Terminal, idle: prune.
	seed("ENG-DONE")
	state.PrevIssueStates["ENG-DONE"] = "Done"
	// Terminal but a reviewer is still in flight for it (a fan-out reviewer
	// whose Running entry reconcile already deleted — #58): keep.
	seed("ENG-DONE-MIDCHAIN")
	state.PrevIssueStates["ENG-DONE-MIDCHAIN"] = "Done"
	o.issueProfiles["ENG-DONE-MIDCHAIN"] = "r2"
	o.reviewerInjectedProfiles["ENG-DONE-MIDCHAIN"] = struct{}{}
	// Absent from the tracker for two consecutive polls, idle: prune.
	seed("ENG-GONE")
	state.PrevActiveIdentifiers = map[string]struct{}{"ENG-ACTIVE": {}}
	// Absent, but the next reviewer of its chain has been dispatched and not
	// yet exited: keep.
	seed("ENG-GONE-MIDCHAIN")
	o.issueProfiles["ENG-GONE-MIDCHAIN"] = "r2"
	o.reviewerInjectedProfiles["ENG-GONE-MIDCHAIN"] = struct{}{}
	// Still tracked (active): keep.
	seed("ENG-ACTIVE")

	state = o.onTick(context.Background(), state)

	for _, ident := range []string{"ENG-DONE", "ENG-GONE"} {
		assert.NotContains(t, state.ReviewVerdicts, ident, "%s verdicts must be pruned", ident)
		assert.NotContains(t, state.ReviewOutcomes, ident, "%s outcome must be pruned", ident)
		assert.NotContains(t, state.ReviewChainIndex, ident, "%s chain index must be pruned", ident)
	}
	for _, ident := range []string{"ENG-DONE-MIDCHAIN", "ENG-GONE-MIDCHAIN", "ENG-ACTIVE"} {
		require.Contains(t, state.ReviewVerdicts, ident, "%s verdicts must be kept", ident)
		assert.Contains(t, state.ReviewOutcomes, ident, "%s outcome must be kept", ident)
		assert.Contains(t, state.ReviewChainIndex, ident, "%s chain index must be kept (never prune mid-chain)", ident)
	}
}

// The mid-chain predicate also covers a live run or a pending retry for the
// identifier (both keyed by issue ID in State).
func TestReviewMidChainPredicateCoversRunningAndRetry(t *testing.T) {
	o := New(automationBaseCfg(), nil, nil, nil)
	state := NewState(automationBaseCfg())
	state.Running["id-1"] = &RunEntry{Issue: domain.Issue{ID: "id-1", Identifier: "ENG-1"}}
	state.RetryAttempts["id-2"] = &RetryEntry{IssueID: "id-2", Identifier: "ENG-2"}
	mid := o.reviewMidChainPredicate(&state)
	assert.True(t, mid("ENG-1"))
	assert.True(t, mid("ENG-2"))
	assert.False(t, mid("ENG-3"))

	for _, ident := range []string{"ENG-1", "ENG-2", "ENG-3"} {
		state.ReviewChainIndex[ident] = 0
	}
	removed := pruneReviewLedgers(&state, func(string) bool { return true }, mid)
	assert.Equal(t, 1, removed)
	assert.Contains(t, state.ReviewChainIndex, "ENG-1")
	assert.Contains(t, state.ReviewChainIndex, "ENG-2")
	assert.NotContains(t, state.ReviewChainIndex, "ENG-3")
}
