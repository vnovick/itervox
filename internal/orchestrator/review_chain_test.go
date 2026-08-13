package orchestrator

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/config"
)

func reviewChainState() State {
	cfg := &config.Config{}
	cfg.Agent.MaxConcurrentAgents = 2
	return NewState(cfg)
}

// TestAdvanceReviewChainSequencesThenCloses walks a three-reviewer chain end
// to end: each call reports the next profile until the last one, which closes
// the quorum instead.
func TestAdvanceReviewChainSequencesThenCloses(t *testing.T) {
	state := reviewChainState()
	chain := []string{"security", "correctness", "perf"}
	ResetReviewChain(&state, "ENG-1")
	now := time.Now()

	next, done := AdvanceReviewChain(&state, "ENG-1", "security",
		ReviewVerdict{Verdict: ReviewVerdictApprove}, nil, chain, config.ReviewQuorumAnyBlock, now)
	require.False(t, done)
	require.Equal(t, "correctness", next, "chain must advance in configured order")

	next, done = AdvanceReviewChain(&state, "ENG-1", "correctness",
		ReviewVerdict{Verdict: ReviewVerdictApprove}, nil, chain, config.ReviewQuorumAnyBlock, now)
	require.False(t, done)
	require.Equal(t, "perf", next)

	next, done = AdvanceReviewChain(&state, "ENG-1", "perf",
		ReviewVerdict{Verdict: ReviewVerdictApprove}, nil, chain, config.ReviewQuorumAnyBlock, now)
	require.True(t, done, "the last reviewer must close the chain")
	require.Empty(t, next)

	outcome := state.ReviewOutcomes["ENG-1"]
	require.False(t, outcome.Blocked)
	require.Equal(t, 3, outcome.Approvals)
	require.NotContains(t, state.ReviewChainIndex, "ENG-1", "a closed chain must not stay in flight")
}

// TestAdvanceReviewChainBlockingVerdictGates: one dissenting reviewer blocks
// under the default quorum, and the chain still runs to completion so the
// operator sees every reviewer's reasoning rather than stopping at the first
// objection.
func TestAdvanceReviewChainBlockingVerdictGates(t *testing.T) {
	state := reviewChainState()
	chain := []string{"security", "correctness"}
	ResetReviewChain(&state, "ENG-1")
	now := time.Now()

	_, done := AdvanceReviewChain(&state, "ENG-1", "security",
		ReviewVerdict{Verdict: ReviewVerdictBlock, Reasons: []string{"unvalidated input"}}, nil,
		chain, config.ReviewQuorumAnyBlock, now)
	require.False(t, done, "a block must not short-circuit the remaining reviewers")

	_, done = AdvanceReviewChain(&state, "ENG-1", "correctness",
		ReviewVerdict{Verdict: ReviewVerdictApprove}, nil, chain, config.ReviewQuorumAnyBlock, now)
	require.True(t, done)

	outcome := state.ReviewOutcomes["ENG-1"]
	require.True(t, outcome.Blocked)
	require.Equal(t, 1, outcome.Blocks)
	require.Equal(t, []string{"unvalidated input"}, state.ReviewVerdicts["ENG-1"][0].Reasons,
		"the blocking reviewer's reasoning must be preserved for the operator")
}

// TestAdvanceReviewChainVerdictErrorFailsClosed is the important one: a
// reviewer that ran but left no readable verdict must count as a BLOCK, not
// be dropped. Dropping it would shrink the quorum denominator, so a crashing
// reviewer could make the gate pass by attrition.
func TestAdvanceReviewChainVerdictErrorFailsClosed(t *testing.T) {
	state := reviewChainState()
	chain := []string{"security"}
	ResetReviewChain(&state, "ENG-1")

	_, done := AdvanceReviewChain(&state, "ENG-1", "security",
		ReviewVerdict{}, errors.New("verdict file missing"),
		chain, config.ReviewQuorumAnyBlock, time.Now())
	require.True(t, done)

	outcome := state.ReviewOutcomes["ENG-1"]
	require.True(t, outcome.Blocked, "an unreadable verdict must block")
	require.Equal(t, 1, outcome.Blocks)
	require.Equal(t, 0, outcome.Approvals)
	require.Contains(t, state.ReviewVerdicts["ENG-1"][0].Reasons[0], "no parseable verdict recorded",
		"the operator must be told WHY it blocked")
}

// TestResetReviewChainClearsPriorVerdicts: a re-review must be judged on its
// own evidence. Inheriting a stale block would make an issue permanently
// unreviewable.
func TestResetReviewChainClearsPriorVerdicts(t *testing.T) {
	state := reviewChainState()
	chain := []string{"security"}
	ResetReviewChain(&state, "ENG-1")
	AdvanceReviewChain(&state, "ENG-1", "security",
		ReviewVerdict{Verdict: ReviewVerdictBlock}, nil, chain, config.ReviewQuorumAnyBlock, time.Now())
	require.True(t, state.ReviewOutcomes["ENG-1"].Blocked)

	ResetReviewChain(&state, "ENG-1")
	require.Empty(t, state.ReviewVerdicts["ENG-1"])
	require.NotContains(t, state.ReviewOutcomes, "ENG-1")
	require.Equal(t, 0, state.ReviewChainIndex["ENG-1"], "a reset chain restarts at the first reviewer")
}

// TestReviewChainSingleProfileIsUnchanged pins the back-compat guarantee:
// with one reviewer the chain closes on the first verdict, so existing
// single-reviewer configs see exactly one reviewer run as before.
func TestReviewChainSingleProfileIsUnchanged(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.ReviewerProfile = "reviewer"
	chain := ReviewerProfileChain(cfg)
	require.Len(t, chain, 1)

	state := reviewChainState()
	ResetReviewChain(&state, "ENG-1")
	next, done := AdvanceReviewChain(&state, "ENG-1", "reviewer",
		ReviewVerdict{Verdict: ReviewVerdictApprove}, nil, chain, config.DefaultReviewQuorum, time.Now())
	require.True(t, done)
	require.Empty(t, next, "a single-profile chain must never dispatch a second reviewer")
}

// TestReviewVerdictPathOnlyForFanoutReviewers pins the injection boundary:
// the verdict instruction reaches fan-out reviewers and nobody else, so
// normal workers and single-reviewer setups see an unchanged prompt.
func TestReviewVerdictPathOnlyForFanoutReviewers(t *testing.T) {
	single := &config.Config{}
	single.Agent.ReviewerProfile = "reviewer"
	require.Empty(t, reviewVerdictRelPathFor(single, "ENG-1", "reviewer"),
		"a single-reviewer setup must not get verdict plumbing")

	fanout := &config.Config{}
	fanout.Agent.ReviewerProfiles = []string{"security", "correctness"}
	require.Equal(t, ".itervox/review/ENG-1/security/verdict.json",
		reviewVerdictRelPathFor(fanout, "ENG-1", "security"))
	require.Empty(t, reviewVerdictRelPathFor(fanout, "ENG-1", "implementer"),
		"a non-reviewer profile must never be asked for a verdict")
	require.Empty(t, reviewVerdictRelPathFor(fanout, "ENG-1", ""))
}

// TestReviewVerdictBlockStatesTheFailClosedRule: the agent must be told that
// omitting the file counts as a block, otherwise the fail-closed default is
// a trap rather than a contract.
func TestReviewVerdictBlockStatesTheFailClosedRule(t *testing.T) {
	block := buildReviewVerdictBlock(".itervox/review/ENG-1/security/verdict.json")
	require.Contains(t, block, ".itervox/review/ENG-1/security/verdict.json")
	require.Contains(t, block, "is recorded as `block`")
	require.Contains(t, block, "independent reviewers")
}
