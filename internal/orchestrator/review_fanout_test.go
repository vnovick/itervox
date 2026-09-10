package orchestrator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

func fixedNow() time.Time {
	return time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
}

func fanoutCfg(profiles ...string) *config.Config {
	cfg := &config.Config{}
	cfg.Agent.ReviewerProfiles = profiles
	return cfg
}

// TestReviewerProfileChainReturnsEveryProfile is issue #58's headline: the
// chain was truncated to one entry, so "reviewer is a single profile with no
// verification fan-out" described shipping behaviour even though the machinery
// existed. Asserting the LENGTH is what the truncation broke.
func TestReviewerProfileChainReturnsEveryProfile(t *testing.T) {
	got := ReviewerProfileChain(fanoutCfg("security", "correctness", "perf"))

	require.Len(t, got, 3, "the chain must not be truncated — that was the #58 gate")
	assert.Equal(t, []string{"security", "correctness", "perf"}, got)
}

// TestReviewerProfileChainTrimsAndDropsBlanks pins that formatting noise in
// config does not become a phantom reviewer that never runs and never reports.
func TestReviewerProfileChainTrimsAndDropsBlanks(t *testing.T) {
	got := ReviewerProfileChain(fanoutCfg("  security  ", "", "   ", "perf"))
	assert.Equal(t, []string{"security", "perf"}, got)
}

// TestReviewerProfileChainSingleEntryIsUnchanged pins that every existing
// single-reviewer deployment behaves exactly as before ungating.
func TestReviewerProfileChainSingleEntryIsUnchanged(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.ReviewerProfile = "reviewer"
	assert.Equal(t, []string{"reviewer"}, ReviewerProfileChain(cfg))
}

// TestReviewChainInFlightTracksPendingReviewers pins #58 defect 2's signal:
// the auto-clear decision must be able to tell "another reviewer is pending"
// from "the quorum closed".
func TestReviewChainInFlightTracksPendingReviewers(t *testing.T) {
	st := State{ReviewChainIndex: map[string]int{"ENG-1": 0}}
	assert.True(t, reviewChainInFlight(st, "ENG-1"))
	assert.False(t, reviewChainInFlight(st, "ENG-2"), "unrelated issues must not be held")

	delete(st.ReviewChainIndex, "ENG-1")
	assert.False(t, reviewChainInFlight(st, "ENG-1"),
		"once the quorum closes the workspace must be clearable again")
	assert.False(t, reviewChainInFlight(State{}, "ENG-1"))
}

// TestAdvanceReviewChainWalksEveryReviewerThenCloses is the end-to-end chain
// behaviour that the truncation made unreachable: with three reviewers the
// chain must advance twice and close on the third, recording all three
// verdicts.
func TestAdvanceReviewChainWalksEveryReviewerThenCloses(t *testing.T) {
	chain := []string{"security", "correctness", "perf"}
	st := State{
		ReviewChainIndex: map[string]int{"ENG-1": 0},
		ReviewVerdicts:   map[string][]ReviewVerdict{},
		ReviewOutcomes:   map[string]ReviewOutcome{},
	}
	now := fixedNow()

	next, done := AdvanceReviewChain(&st, "ENG-1", "security",
		ReviewVerdict{Verdict: ReviewVerdictApprove}, nil, chain, "all", now)
	require.False(t, done, "two reviewers remain")
	assert.Equal(t, "correctness", next)

	next, done = AdvanceReviewChain(&st, "ENG-1", "correctness",
		ReviewVerdict{Verdict: ReviewVerdictApprove}, nil, chain, "all", now)
	require.False(t, done, "one reviewer remains")
	assert.Equal(t, "perf", next)

	_, done = AdvanceReviewChain(&st, "ENG-1", "perf",
		ReviewVerdict{Verdict: ReviewVerdictApprove}, nil, chain, "all", now)
	require.True(t, done, "the chain must close after the last reviewer")

	assert.Len(t, st.ReviewVerdicts["ENG-1"], 3, "every reviewer's verdict must be recorded")
	assert.False(t, st.ReviewOutcomes["ENG-1"].Blocked)
	_, stillIndexed := st.ReviewChainIndex["ENG-1"]
	assert.False(t, stillIndexed, "a closed chain must release the workspace hold")
}

// TestAdvanceReviewChainBlocksWhenAnyReviewerBlocks pins that fan-out is worth
// having: a single dissenting reviewer blocks under the "all" quorum, which is
// the entire point of asking more than one.
func TestAdvanceReviewChainBlocksWhenAnyReviewerBlocks(t *testing.T) {
	chain := []string{"security", "correctness"}
	st := State{
		ReviewChainIndex: map[string]int{"ENG-1": 0},
		ReviewVerdicts:   map[string][]ReviewVerdict{},
		ReviewOutcomes:   map[string]ReviewOutcome{},
	}
	now := fixedNow()

	_, done := AdvanceReviewChain(&st, "ENG-1", "security",
		ReviewVerdict{Verdict: ReviewVerdictApprove}, nil, chain, "all", now)
	require.False(t, done)

	_, done = AdvanceReviewChain(&st, "ENG-1", "correctness",
		ReviewVerdict{Verdict: ReviewVerdictBlock}, nil, chain, "all", now)
	require.True(t, done)

	assert.True(t, st.ReviewOutcomes["ENG-1"].Blocked,
		"one blocking reviewer must block the quorum")
}

// TestAdvanceReviewChainFailsClosedOnUnreadableVerdict pins that a reviewer
// which finished without recording a judgement blocks rather than silently
// counting as approval.
func TestAdvanceReviewChainFailsClosedOnUnreadableVerdict(t *testing.T) {
	chain := []string{"security"}
	st := State{
		ReviewChainIndex: map[string]int{"ENG-1": 0},
		ReviewVerdicts:   map[string][]ReviewVerdict{},
		ReviewOutcomes:   map[string]ReviewOutcome{},
	}

	_, done := AdvanceReviewChain(&st, "ENG-1", "security",
		ReviewVerdict{}, errNoWorkspaceForReview, chain, "all", fixedNow())

	require.True(t, done)
	assert.True(t, st.ReviewOutcomes["ENG-1"].Blocked,
		"a missing verdict must never be read as approval")
}
