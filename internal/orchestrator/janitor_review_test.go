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
	// Terminal, idle: prune. Its last reviewer has exited.
	seed("ENG-DONE")
	state.PrevIssueStates["ENG-DONE"] = "Done"
	state.LatestRun["id-done"] = RunRecord{ID: "run-d", Identifier: "ENG-DONE", Kind: "reviewer", ProfileName: "r2", Exited: true}
	// Terminal but a reviewer is still in flight for it (a fan-out reviewer
	// whose Running entry reconcile already deleted — #58): keep.
	seed("ENG-DONE-MIDCHAIN")
	state.PrevIssueStates["ENG-DONE-MIDCHAIN"] = "Done"
	state.LatestRun["id-done-mid"] = RunRecord{ID: "run-dm", Identifier: "ENG-DONE-MIDCHAIN", Kind: "reviewer", ProfileName: "r2", Stopped: true}
	// Absent from the tracker for two consecutive polls, idle: prune.
	seed("ENG-GONE")
	state.PrevActiveIdentifiers = map[string]struct{}{"ENG-ACTIVE": {}}
	// Absent, but the next reviewer of its chain has been dispatched and not
	// yet exited: keep.
	seed("ENG-GONE-MIDCHAIN")
	state.LatestRun["id-gone-mid"] = RunRecord{ID: "run-gm", Identifier: "ENG-GONE-MIDCHAIN", Kind: "reviewer", ProfileName: "r2"}
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

// TestSuspendedReviewerKeepsItsChain (#127): a reviewer waiting for input,
// with a reply queued, paused, or pending while the daemon drains has an
// exited run record, but it will continue. Its chain survives the janitor
// while the issue sits outside the active states (an operator moved it to
// a non-active "Input Required" state), so the resumed reviewer can still
// advance the chain; an idle issue's chain is pruned as before.
func TestSuspendedReviewerKeepsItsChain(t *testing.T) {
	cfg := automationBaseCfg()
	active := domain.Issue{ID: "id-active", Identifier: "ENG-ACTIVE", Title: "T", State: "In Progress"}
	// The waiting reviewer's issue still exists, in a non-active state.
	waiting := domain.Issue{ID: "id-wait", Identifier: "ENG-WAIT", Title: "T", State: "Input Required"}
	mt := tracker.NewMemoryTracker([]domain.Issue{active, waiting}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	o := New(cfg, mt, nil, nil)
	o.DryRun = true

	state := NewState(cfg)
	exited := func(id, ident string) {
		state.ReviewChainIndex[ident] = 1
		state.LatestRun[id] = RunRecord{ID: "run-" + id, Identifier: ident, Kind: "reviewer", ProfileName: "r1", Exited: true}
	}
	exited("id-wait", "ENG-WAIT")
	state.InputRequiredIssues["ENG-WAIT"] = &InputRequiredEntry{IssueID: "id-wait", Identifier: "ENG-WAIT", ProfileName: "r1", Reviewer: true}
	exited("id-reply", "ENG-REPLY")
	state.PendingInputResumes["ENG-REPLY"] = &PendingInputResumeEntry{IssueID: "id-reply", Identifier: "ENG-REPLY", ProfileName: "r1", Reviewer: true}
	exited("id-paused", "ENG-PAUSED")
	state.PausedSessions["ENG-PAUSED"] = &PausedSessionInfo{ProfileName: "r1", Reviewer: true}
	exited("id-idle", "ENG-IDLE")
	// A worker (not a reviewer) waiting for input does not hold a chain.
	exited("id-worker", "ENG-WORKER")
	state.InputRequiredIssues["ENG-WORKER"] = &InputRequiredEntry{IssueID: "id-worker", Identifier: "ENG-WORKER"}
	state.PrevActiveIdentifiers = map[string]struct{}{"ENG-ACTIVE": {}}

	for range 2 { // absent from two consecutive polls
		state = o.onTick(context.Background(), state)
	}

	require.Contains(t, state.InputRequiredIssues, "ENG-WAIT", "precondition: the input wait survives the general janitor")
	for _, ident := range []string{"ENG-WAIT", "ENG-REPLY", "ENG-PAUSED"} {
		assert.Contains(t, state.ReviewChainIndex, ident, "%s: a suspended reviewer keeps its chain", ident)
	}
	assert.NotContains(t, state.ReviewChainIndex, "ENG-IDLE", "an idle issue's chain is pruned")
	assert.NotContains(t, state.ReviewChainIndex, "ENG-WORKER", "a waiting worker holds no chain")
}

// The mid-chain predicate covers every suspended reviewer continuation,
// including a review pending while the daemon drains (#127).
func TestReviewMidChainPredicateCoversSuspendedReviewers(t *testing.T) {
	o := New(automationBaseCfg(), nil, nil, nil)
	state := NewState(automationBaseCfg())
	state.InputRequiredIssues["ENG-1"] = &InputRequiredEntry{Identifier: "ENG-1", Reviewer: true}
	state.PendingInputResumes["ENG-2"] = &PendingInputResumeEntry{Identifier: "ENG-2", Reviewer: true}
	state.PausedSessions["ENG-3"] = &PausedSessionInfo{Reviewer: true}
	state.PendingReviews["ENG-4"] = PendingReview{Identifier: "ENG-4", Profile: "r1"}
	state.InputRequiredIssues["ENG-5"] = &InputRequiredEntry{Identifier: "ENG-5"} // a worker
	state.PausedSessions["ENG-6"] = &PausedSessionInfo{}                          // a worker
	mid := o.reviewMidChainPredicate(&state)
	for _, ident := range []string{"ENG-1", "ENG-2", "ENG-3", "ENG-4"} {
		assert.True(t, mid(ident), ident)
	}
	assert.False(t, mid("ENG-5"))
	assert.False(t, mid("ENG-6"))
}
