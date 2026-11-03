package orchestrator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
)

// TestEvalPromptDigestMatchesDispatchedRun (#201): the eval harness names a
// scenario's prompt version with the digest a dispatched run of the same
// profile under the same config gets (#186), so an eval result and the
// Prompt versions section speak about the same version.
func TestEvalPromptDigestMatchesDispatchedRun(t *testing.T) {
	f := newProvenanceFixture(t)
	dispatched, _ := f.digestFor(t, "")
	composed, err := ComposeEvalPrompt(EvalPromptInput{Config: f.cfg, ProfileName: "implementer", Backend: "claude", Issue: f.issue})
	require.NoError(t, err)
	assert.Regexp(t, hex64, composed.Digest)
	assert.Equal(t, dispatched, composed.Digest)

	// A reviewer profile is digested with agent.reviewer_prompt, as its
	// dispatched runs are.
	f.cfg.Agent.ReviewerProfile = "implementer"
	f.cfg.Agent.ReviewerPrompt = "REVIEW {{ issue.identifier }}"
	reviewer, _ := f.digestFor(t, "reviewer")
	composed, err = ComposeEvalPrompt(EvalPromptInput{Config: f.cfg, ProfileName: "implementer", Backend: "claude", Issue: f.issue})
	require.NoError(t, err)
	assert.Equal(t, reviewer, composed.Digest)
	assert.NotEqual(t, dispatched, composed.Digest)
}

// TestCompletedRunRecordsBranchPRAndReviewVerdict (#201): the hotspots job
// attributes a run through its history entry, so the exit's branch and PR
// and a reviewer's verdict are written onto the CompletedRun.
func TestCompletedRunRecordsBranchPRAndReviewVerdict(t *testing.T) {
	f := newProvenanceFixture(t)
	entry := f.start(t)
	attempt := 0
	f.state = f.o.handleEvent(t.Context(), f.state, OrchestratorEvent{
		Type: EventWorkerExited, IssueID: f.issue.ID, RunID: entry.RunID,
		RunEntry: &RunEntry{Issue: f.issue, TerminalReason: TerminalSucceeded, RetryAttempt: &attempt,
			BranchName: "feat/eng-1-work", PRURL: "https://github.com/acme/app/pull/7"},
	})
	hist := f.o.RunHistory()
	require.Len(t, hist, 1)
	assert.Equal(t, "feat/eng-1-work", hist[0].BranchName)
	assert.Equal(t, "https://github.com/acme/app/pull/7", hist[0].PRURL)
	assert.Empty(t, hist[0].ReviewVerdict, "a worker run has no verdict")

	// A reviewer whose verdict could not be read is recorded as the block
	// the quorum counted (fail-closed), and its branch falls back to the
	// issue's worktree branch when the exit names none.
	reviewer := &RunEntry{Issue: f.issue, ProfileName: "implementer", Backend: "claude", Kind: "reviewer"}
	_ = f.o.startRun(t.Context(), &f.state, reviewer)
	f.state.Running[f.issue.ID] = reviewer
	f.state = f.o.handleEvent(t.Context(), f.state, OrchestratorEvent{
		Type: EventWorkerExited, IssueID: f.issue.ID, RunID: reviewer.RunID,
		RunEntry: &RunEntry{Issue: f.issue, TerminalReason: TerminalSucceeded, RetryAttempt: &attempt},
	})
	hist = f.o.RunHistory()
	require.Len(t, hist, 2)
	assert.Equal(t, "reviewer", hist[1].Kind)
	assert.Equal(t, ReviewVerdictBlock, hist[1].ReviewVerdict)
	assert.Equal(t, "itervox/eng-1", hist[1].BranchName)
}

func TestHistoryBranchFallsBackToWorktreeBranch(t *testing.T) {
	named := "feat/named"
	assert.Equal(t, "feat/named", historyBranch(nil, domain.Issue{Identifier: "ENG-2", BranchName: &named}))
	assert.Equal(t, "itervox/eng-2", historyBranch(nil, domain.Issue{Identifier: "ENG-2"}))
	assert.Equal(t, "exit/branch", historyBranch(&RunEntry{BranchName: "exit/branch"}, domain.Issue{Identifier: "ENG-2"}))
	assert.Empty(t, historyBranch(nil, domain.Issue{}))
}
