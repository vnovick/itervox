package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
)

// #135 — the dashboard shows an issue's latest review from the snapshot.

func writeVerdictFile(t *testing.T, ws, identifier, profile string, v ReviewVerdict) {
	t.Helper()
	dir := ReviewDir(ws, identifier, profile)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	body, err := json.Marshal(v)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ReviewVerdictFileName), body, 0o600))
}

// A single reviewer's verdict is recorded as the issue's review, like a
// chain's; each round replaces the last.
func TestSingleReviewerVerdictIsRecorded(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "T", State: "In Progress"}
	o, _ := breakerOrchestrator(t, config.BackendFallbackConfig{}, issue)
	ws := t.TempDir()
	o.workspace = &stubWorkspaceProvider{path: ws}
	o.cfg.Agent.ReviewerProfile = "reviewer"
	o.cfg.Agent.ReviewQuorum = config.ReviewQuorumAnyBlock
	state := NewState(o.cfg)

	review := func() {
		t.Helper()
		o.dispatchReviewerForIssue(ctx, &state, issue, "reviewer", time.Now())
		run := state.Running[issue.ID]
		require.NotNil(t, run)
		state = o.handleEvent(ctx, state, OrchestratorEvent{Type: EventWorkerExited, IssueID: issue.ID, RunID: run.RunID,
			RunEntry: &RunEntry{Issue: issue, TerminalReason: TerminalSucceeded}})
	}

	writeVerdictFile(t, ws, "ENG-1", "reviewer", ReviewVerdict{Verdict: "approve", Reasons: []string{"looks good"}})
	state.ReviewChainIndex["ENG-1"] = 1 // left by a multi-reviewer config before a reload
	review()
	require.Len(t, state.ReviewVerdicts["ENG-1"], 1)
	assert.Equal(t, ReviewVerdictApprove, state.ReviewVerdicts["ENG-1"][0].Verdict)
	assert.Equal(t, []string{"looks good"}, state.ReviewVerdicts["ENG-1"][0].Reasons)
	assert.Equal(t, ReviewOutcome{Approvals: 1, Quorum: config.ReviewQuorumAnyBlock}, state.ReviewOutcomes["ENG-1"])
	assert.NotContains(t, state.ReviewChainIndex, "ENG-1", "a single review is never in flight between reviewers")

	// The next round wrote nothing: it is a block, and it replaces the last.
	require.NoError(t, os.RemoveAll(ReviewDir(ws, "ENG-1", "reviewer")))
	review()
	require.Len(t, state.ReviewVerdicts["ENG-1"], 1)
	assert.Equal(t, ReviewVerdictBlock, state.ReviewVerdicts["ENG-1"][0].Verdict)
	assert.Contains(t, state.ReviewVerdicts["ENG-1"][0].Reasons[0], "no parseable verdict")
	assert.True(t, state.ReviewOutcomes["ENG-1"].Blocked)
}

// A manual review ("Re-run review") is a fresh review by the whole chain:
// the previous round's verdicts go, and the first reviewer starts.
func TestManualReviewStartsTheWholeChain(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "T", State: "In Progress"}
	o, _ := breakerOrchestrator(t, config.BackendFallbackConfig{}, issue)
	o.cfg.Agent.ReviewerProfile = "reviewer"
	o.cfg.Agent.ReviewerProfiles = []string{"reviewer", "reviewer-codex"}
	state := NewState(o.cfg)
	state.ReviewVerdicts["ENG-1"] = []ReviewVerdict{{Profile: "reviewer", Verdict: ReviewVerdictBlock}, {Profile: "reviewer-codex", Verdict: ReviewVerdictApprove}}
	state.ReviewOutcomes["ENG-1"] = ReviewOutcome{Blocked: true, Approvals: 1, Blocks: 1}

	state = o.handleEvent(ctx, state, OrchestratorEvent{Type: EventDispatchReviewer, Identifier: "ENG-1", ReviewerProfile: "reviewer"})
	run := state.Running[issue.ID]
	require.NotNil(t, run)
	assert.Equal(t, "reviewer", run.ProfileName)
	assert.Equal(t, "reviewer", run.Kind)
	idx, inFlight := state.ReviewChainIndex["ENG-1"]
	assert.True(t, inFlight, "the chain is in flight, so the second reviewer follows")
	assert.Equal(t, 0, idx)
	assert.Empty(t, state.ReviewVerdicts["ENG-1"], "the previous round's verdicts are gone")
	assert.NotContains(t, state.ReviewOutcomes, "ENG-1")
}

// A review whose first reviewer cannot start (its profile is missing) leaves
// no chain "in flight" with nobody running, which would show "Reviewing…"
// forever and keep the workspace.
func TestReviewWhoseFirstReviewerCannotStartClosesTheChain(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "T", State: "In Progress"}
	o, _ := breakerOrchestrator(t, config.BackendFallbackConfig{}, issue)
	o.cfg.Agent.ReviewerProfile = "missing-a"
	o.cfg.Agent.ReviewerProfiles = []string{"missing-a", "reviewer"}
	state := NewState(o.cfg)

	state = o.handleEvent(ctx, state, OrchestratorEvent{Type: EventDispatchReviewer, Identifier: "ENG-1", ReviewerProfile: "missing-a"})
	assert.NotContains(t, state.Running, issue.ID, "the missing profile does not start")
	assert.NotContains(t, state.ReviewChainIndex, "ENG-1", "so the chain is not left in flight")
}

// A manual review is refused while a retry for the issue is waiting.
func TestManualReviewWaitsForAPendingRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "T", State: "In Progress"}
	o, _ := breakerOrchestrator(t, config.BackendFallbackConfig{}, issue)
	o.cfg.Agent.ReviewerProfile = "reviewer"
	o.cfg.Agent.ReviewerProfiles = []string{"reviewer", "reviewer-codex"}
	state := NewState(o.cfg)
	state.ReviewChainIndex["ENG-1"] = 1
	state = ScheduleRetry(state, issue.ID, 1, "ENG-1", "backend limited", time.Now(), 60_000)
	retryAs(state, issue.ID, "reviewer-codex")

	state = o.handleEvent(ctx, state, OrchestratorEvent{Type: EventDispatchReviewer, Identifier: "ENG-1", ReviewerProfile: "reviewer"})
	assert.NotContains(t, state.Running, issue.ID)
	assert.Equal(t, 1, state.ReviewChainIndex["ENG-1"], "the chain in flight is left alone")
	assert.Equal(t, "reviewer-codex", state.RetryAttempts[issue.ID].ReviewerProfile)
}
