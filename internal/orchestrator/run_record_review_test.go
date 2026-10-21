package orchestrator

// #127 — the review chain identifies a reviewer by its own run, not by the
// reviewerInjectedProfiles side map: the exit handler reads the finishing
// run's record, and every run that continues a reviewer (a retry, a held or
// rerouted reviewer, a paused or input-required one) carries the reviewer's
// profile itself.

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
)

func reviewChainOrchestrator(t *testing.T, issue domain.Issue) (*Orchestrator, *recordingRunner) {
	t.Helper()
	o, rr := breakerOrchestrator(t, config.BackendFallbackConfig{}, issue)
	o.cfg.Agent.ReviewerProfiles = []string{"reviewer", "reviewer-codex"}
	o.cfg.Agent.ReviewerProfile = "reviewer"
	o.cfg.Tracker.CompletionState = "Done"
	return o, rr
}

func runnerCalls(rr *recordingRunner) int {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	return len(rr.commands)
}

// TestReplacedReviewerAdvancesTheChainOnce: reconcile deletes the first
// reviewer's Running entry before its success exit arrives. The exit still
// advances the chain (from the run's record), and a second, late copy of that
// exit — arriving once the next reviewer has replaced the entry — does not
// advance it again.
func TestReplacedReviewerAdvancesTheChainOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "T", State: "Done"}
	o, rr := reviewChainOrchestrator(t, issue)
	state := NewState(o.cfg)
	now := time.Now()

	ResetReviewChain(&state, issue.Identifier)
	o.dispatchReviewerForIssue(ctx, &state, issue, "reviewer", now)
	first := state.Running[issue.ID]
	require.NotNil(t, first)
	require.Equal(t, "reviewer", first.Kind)
	firstRun := first.RunID

	// Reconcile stops the reviewer: the issue left the review state.
	o.issueProfilesMu.RLock()
	assert.Empty(t, o.issueProfiles[issue.Identifier], "a reviewer never writes the issue's profile override")
	o.issueProfilesMu.RUnlock()
	markRunStopped(state, issue.ID, firstRun)
	delete(state.Running, issue.ID)
	delete(state.Claimed, issue.ID)
	state = o.handleEvent(ctx, state, OrchestratorEvent{
		Type: EventWorkerExited, IssueID: issue.ID, RunID: firstRun,
		RunEntry: &RunEntry{Issue: issue, RunID: firstRun, Kind: "reviewer", ProfileName: "reviewer",
			TerminalReason: TerminalCanceledByReconciliation},
	})
	require.Equal(t, 0, state.ReviewChainIndex[issue.Identifier], "reconcile's stop event does not advance the chain")

	exit := func() OrchestratorEvent {
		return OrchestratorEvent{Type: EventWorkerExited, IssueID: issue.ID, RunID: firstRun,
			RunEntry: &RunEntry{Issue: issue, TerminalReason: TerminalSucceeded}}
	}
	state = o.handleEvent(ctx, state, exit())
	second := state.Running[issue.ID]
	require.NotNil(t, second, "the first reviewer's exit dispatches the next reviewer")
	assert.Equal(t, "reviewer", second.Kind)
	assert.Equal(t, "reviewer-codex", second.ProfileName)
	assert.Equal(t, 1, state.ReviewChainIndex[issue.Identifier])
	require.Eventually(t, func() bool { return runnerCalls(rr) == 2 }, 5*time.Second, 5*time.Millisecond)

	// The same exit again, now that the next reviewer replaced the entry.
	state = o.handleEvent(ctx, state, exit())
	assert.Same(t, second, state.Running[issue.ID], "the replacement keeps running")
	assert.Equal(t, 1, state.ReviewChainIndex[issue.Identifier], "the chain advanced exactly once")
	assert.Len(t, state.ReviewVerdicts[issue.Identifier], 1, "one verdict for the first reviewer")
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 2, runnerCalls(rr), "no further reviewer was started")

	// The second reviewer's own exit closes the quorum.
	state = o.handleEvent(ctx, state, OrchestratorEvent{Type: EventWorkerExited, IssueID: issue.ID, RunID: second.RunID,
		RunEntry: &RunEntry{Issue: issue, TerminalReason: TerminalSucceeded}})
	assert.NotContains(t, state.ReviewChainIndex, issue.Identifier, "the quorum closed")
	assert.NotContains(t, state.Running, issue.ID)
	assert.Len(t, state.ReviewVerdicts[issue.Identifier], 2)
}

// TestStoppedWorkerSuccessStartsNoReview: a worker's success starts a review,
// but not when reconcile stopped the run before its exit arrived — that is
// what bounds review dispatch on an issue that sits terminal.
func TestStoppedWorkerSuccessStartsNoReview(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "T", State: "In Progress"}
		o, _ := reviewChainOrchestrator(t, issue)
		o.cfg.Agent.AutoReview = true
		state := NewState(o.cfg)
		state = o.dispatch(ctx, state, issue, 0)
		worker := state.Running[issue.ID]
		require.NotNil(t, worker)
		require.Empty(t, worker.Kind)
		if stopped {
			markRunStopped(state, issue.ID, worker.RunID)
			delete(state.Running, issue.ID)
		}
		state = o.handleEvent(ctx, state, OrchestratorEvent{Type: EventWorkerExited, IssueID: issue.ID, RunID: worker.RunID,
			RunEntry: &RunEntry{Issue: issue, TerminalReason: TerminalSucceeded}})
		if stopped {
			assert.NotContains(t, state.Running, issue.ID, "a stopped run is not reviewed")
		} else {
			require.Contains(t, state.Running, issue.ID, "a finished worker is reviewed")
			assert.Equal(t, "reviewer", state.Running[issue.ID].Kind)
		}
		cancel()
	}
}

// TestOperatorProfileSurvivesAReview (#79, #127): the operator's per-issue
// profile is theirs. A review neither replaces it while it runs nor deletes
// it when it finishes, so the next implementer run still uses it.
func TestOperatorProfileSurvivesAReview(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "T", State: "In Progress"}
	o, _ := breakerOrchestrator(t, config.BackendFallbackConfig{}, issue)
	o.SetIssueProfile(issue.Identifier, "coder")
	state := NewState(o.cfg)

	o.dispatchReviewerForIssue(ctx, &state, issue, "reviewer", time.Now())
	reviewer := state.Running[issue.ID]
	require.NotNil(t, reviewer)
	assert.Equal(t, "reviewer", reviewer.ProfileName)
	assert.Equal(t, "coder", o.issueProfileForDispatch(state, issue.Identifier), "the reviewer leaves the operator's profile in place")

	state = o.handleEvent(ctx, state, OrchestratorEvent{Type: EventWorkerExited, IssueID: issue.ID, RunID: reviewer.RunID,
		RunEntry: &RunEntry{Issue: issue, TerminalReason: TerminalSucceeded}})
	assert.Equal(t, "coder", o.issueProfileForDispatch(state, issue.Identifier), "the review's end leaves it in place too")

	state = o.dispatch(ctx, state, issue, 0)
	require.NotNil(t, state.Running[issue.ID])
	assert.Equal(t, "coder", state.Running[issue.ID].ProfileName)
	assert.Empty(t, state.Running[issue.ID].Kind)
}

// TestReviewerContinuationsStayReviewers: a reviewer's retry, the resume of a
// paused reviewer, and a reviewer awaiting input all run the reviewer again,
// under its own profile, not the operator's.
func TestReviewerContinuationsStayReviewers(t *testing.T) {
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "T", State: "In Progress"}

	t.Run("retry", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		o, _ := breakerOrchestrator(t, config.BackendFallbackConfig{}, issue)
		o.SetIssueProfile(issue.Identifier, "coder")
		state := NewState(o.cfg)
		o.dispatchReviewerForIssue(ctx, &state, issue, "reviewer", time.Now())
		run := state.Running[issue.ID]
		attempt := 0
		state = o.handleEvent(ctx, state, OrchestratorEvent{Type: EventWorkerExited, IssueID: issue.ID, RunID: run.RunID,
			RunEntry: &RunEntry{Issue: issue, TerminalReason: TerminalFailed, RetryAttempt: &attempt}})
		require.Contains(t, state.RetryAttempts, issue.ID)
		assert.Equal(t, "reviewer", state.RetryAttempts[issue.ID].ReviewerProfile)

		state = o.fireRetries(ctx, state, time.Now().Add(time.Hour))
		retried := state.Running[issue.ID]
		require.NotNil(t, retried)
		assert.Equal(t, "reviewer", retried.Kind)
		assert.Equal(t, "reviewer", retried.ProfileName)
		assert.True(t, state.LatestRun[issue.ID].IsReviewer(), "the retry's record says reviewer")
	})

	t.Run("pause", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		o, _ := breakerOrchestrator(t, config.BackendFallbackConfig{}, issue)
		o.SetIssueProfile(issue.Identifier, "coder")
		state := NewState(o.cfg)
		o.dispatchReviewerForIssue(ctx, &state, issue, "reviewer", time.Now())
		run := state.Running[issue.ID]
		o.userCancelledMu.Lock()
		o.userCancelledIDs[issue.Identifier] = struct{}{}
		o.userCancelledMu.Unlock()
		state = o.handleEvent(ctx, state, OrchestratorEvent{Type: EventWorkerExited, IssueID: issue.ID, RunID: run.RunID,
			Error: context.Canceled, RunEntry: &RunEntry{Issue: issue, TerminalReason: TerminalFailed}})
		require.Contains(t, state.PausedIdentifiers, issue.Identifier)
		require.NotNil(t, state.PausedSessions[issue.Identifier], "a paused reviewer is recorded without a session")
		assert.True(t, state.PausedSessions[issue.Identifier].Reviewer)

		delete(state.PausedIdentifiers, issue.Identifier) // the operator resumes it
		state = o.dispatch(ctx, state, issue, 0)
		resumed := state.Running[issue.ID]
		require.NotNil(t, resumed)
		assert.Equal(t, "reviewer", resumed.Kind)
		assert.Equal(t, "reviewer", resumed.ProfileName)
	})

	t.Run("input required", func(t *testing.T) {
		o, _ := breakerOrchestrator(t, config.BackendFallbackConfig{}, issue)
		o.SetInputRequiredFile(filepath.Join(t.TempDir(), "input_required.json"))
		waiting := &InputRequiredEntry{IssueID: issue.ID, Identifier: issue.Identifier, SessionID: "s1",
			ProfileName: "reviewer", Reviewer: true, QueuedAt: time.Now()}
		pending := buildPendingInputResumeEntry(waiting, "yes")
		require.True(t, pending.Reviewer)
		assert.True(t, inputRequiredEntryFromPending(pending).Reviewer)

		o.saveInputRequiredToDisk(map[string]*InputRequiredEntry{issue.Identifier: waiting},
			map[string]*PendingInputResumeEntry{issue.Identifier: pending})
		loaded := o.loadInputRequiredFromDisk(NewState(o.cfg))
		require.Contains(t, loaded.InputRequiredIssues, issue.Identifier)
		assert.True(t, loaded.InputRequiredIssues[issue.Identifier].Reviewer, "a restart keeps the reviewer")
		assert.True(t, loaded.PendingInputResumes[issue.Identifier].Reviewer)
	})
}
