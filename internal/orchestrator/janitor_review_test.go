package orchestrator

import (
	"context"
	"testing"
	"time"

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

// #135 review P2: issue detail shows the issue's latest review from
// ReviewVerdicts/ReviewOutcomes, so a finished review must survive the
// janitor pass of a normal completion poll. Its verdicts and outcome now share
// IssueStatusHistory's retention; the chain index (the in-flight marker) is
// still pruned, so abandoned-chain cleanup is unchanged; and once the status
// history ages out the review goes with it.
func TestJanitorKeepsFinishedReviewWhileStatusHistoryRetained(t *testing.T) {
	cfg := automationBaseCfg()
	done := domain.Issue{ID: "id-done", Identifier: "ENG-DONE", Title: "T", State: "Done"}
	old := domain.Issue{ID: "id-old", Identifier: "ENG-OLD", Title: "T", State: "Done"}
	mt := tracker.NewMemoryTracker([]domain.Issue{done, old}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	o := New(cfg, mt, nil, nil)
	o.DryRun = true

	state := NewState(cfg)
	now := time.Now()
	seed := func(ident, id string, changedAt time.Time) {
		state.ReviewVerdicts[ident] = []ReviewVerdict{{Profile: "r1", Verdict: ReviewVerdictBlock, Reasons: []string{"no tests"}}}
		state.ReviewOutcomes[ident] = ReviewOutcome{Blocked: true, Blocks: 1, Quorum: "any_block"}
		// A stale marker as an abandoned chain would leave behind.
		state.ReviewChainIndex[ident] = 1
		state.PrevIssueStates[ident] = "Done"
		state.IssueStatusHistory[ident] = []IssueStatusChange{{
			Identifier: ident, FromState: "In Progress", ToState: "Done",
			Source: StatusSourceTrackerObserved, At: changedAt,
		}}
		state.LatestRun[id] = RunRecord{ID: "run-" + id, Identifier: ident, Kind: "reviewer", ProfileName: "r1", Exited: true}
	}
	seed("ENG-DONE", "id-done", now.Add(-time.Minute))
	seed("ENG-OLD", "id-old", now.Add(-issueStatusHistoryRetention-time.Hour))
	// A previous poll saw other work, so an issue absent from both polls
	// counts as untracked (pruneIssueStatusHistory drops ENG-OLD's
	// PrevIssueStates with its history, so the terminal set no longer has it).
	state.PrevActiveIdentifiers = map[string]struct{}{"ENG-ACTIVE": {}}

	state = o.onTick(context.Background(), state)

	require.Contains(t, state.IssueStatusHistory, "ENG-DONE", "fixture: recent status history is retained")
	require.Len(t, state.ReviewVerdicts["ENG-DONE"], 1, "the finished review's verdicts must survive the poll")
	assert.Equal(t, []string{"no tests"}, state.ReviewVerdicts["ENG-DONE"][0].Reasons)
	assert.Equal(t, ReviewOutcome{Blocked: true, Blocks: 1, Quorum: "any_block"}, state.ReviewOutcomes["ENG-DONE"])
	assert.NotContains(t, state.ReviewChainIndex, "ENG-DONE", "an abandoned chain's index is still pruned")

	assert.NotContains(t, state.IssueStatusHistory, "ENG-OLD", "fixture: aged status history is pruned")
	assert.NotContains(t, state.ReviewVerdicts, "ENG-OLD", "review leaves with the status history")
	assert.NotContains(t, state.ReviewOutcomes, "ENG-OLD")
	assert.NotContains(t, state.ReviewChainIndex, "ENG-OLD")
}
