package orchestrator

// CORE-157 — reviewerInjectedProfiles (and the reviewer profile it pins in
// o.issueProfiles) was cleared only when a reviewer SUCCEEDED. After a
// reviewer run was abandoned — retries exhausted, cancelled with no retry
// pending, stalled with no retry pending, or hard-terminated by the user —
// every later run of the issue kept resolving to the reviewer profile.
//
// The marker must still survive (a) a reviewer failure that schedules a
// retry (the retry IS the reviewer; exitedRunIsReviewer / CORE-031 depend on
// it), (b) TerminalCanceledByReconciliation (reconcile emits it before a
// fan-out reviewer's own success exit — #58 defect 1), and (c) the hand-off
// between reviewers of a multi-reviewer chain.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

func reviewerInjected(o *Orchestrator, identifier string) (string, bool) {
	o.issueProfilesMu.RLock()
	defer o.issueProfilesMu.RUnlock()
	_, injected := o.reviewerInjectedProfiles[identifier]
	return o.issueProfiles[identifier], injected
}

// End to end on the real dispatch path: reviewer dispatch → failure → retry
// (still the reviewer) → retries exhausted → the NEXT dispatch of the issue
// must resolve to the implementer profile, not the reviewer.
func TestReviewerProfileClearedAfterReviewerRetriesExhausted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg := automationBaseCfg()
	cfg.Agent.Command = "claude"
	cfg.Agent.MaxTurns = 1
	cfg.Agent.MaxRetries = 1
	cfg.Agent.MaxRetryBackoffMs = 10
	cfg.Agent.Profiles = map[string]config.AgentProfile{"reviewer": {Command: "claude"}}
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "T", State: "In Progress"}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	o := New(cfg, mt, agenttest.FailRunner("Error: boom"), nil)
	state := NewState(o.cfg)

	o.dispatchReviewerForIssue(ctx, &state, issue, "reviewer", time.Now())
	require.Equal(t, "reviewer", state.Running[issue.ID].Kind)

	// First failure schedules a retry: the retry is still the reviewer.
	state, ev := nextWorkerExit(t, ctx, o, state)
	state = o.handleEvent(ctx, state, ev)
	require.Contains(t, state.RetryAttempts, issue.ID)
	profile, injected := reviewerInjected(o, issue.Identifier)
	assert.True(t, injected, "a reviewer failure that schedules a retry keeps the reviewer marker")
	assert.Equal(t, "reviewer", profile)
	state = o.fireRetries(ctx, state, time.Now().Add(time.Hour))
	require.NotNil(t, state.Running[issue.ID])
	assert.Equal(t, "reviewer", state.Running[issue.ID].ProfileName, "the retry re-runs the reviewer")

	// Second failure exhausts max_retries=1: the reviewer run is abandoned.
	state, ev = nextWorkerExit(t, ctx, o, state)
	state = o.handleEvent(ctx, state, ev)
	require.NotContains(t, state.Running, issue.ID)

	profile, injected = reviewerInjected(o, issue.Identifier)
	assert.False(t, injected, "an abandoned reviewer run must clear reviewerInjectedProfiles")
	assert.Empty(t, profile, "an abandoned reviewer run must clear its injected profile override")

	delete(state.PausedIdentifiers, issue.Identifier) // operator resumes the issue
	state = o.dispatch(ctx, state, issue, 0)
	require.NotNil(t, state.Running[issue.ID])
	assert.Empty(t, state.Running[issue.ID].ProfileName, "the next dispatch must use the implementer (default) profile")
	assert.NotEqual(t, "reviewer", state.Running[issue.ID].Kind)

	cancel()
	o.workersWg.Wait(5 * time.Second)
}

// Every exit shape, driven through handleEvent with a real State: which ones
// abandon the reviewer run and which must keep the marker.
func TestReviewerInjectedProfileExitMatrix(t *testing.T) {
	const ident = "ENG-1"
	issue := domain.Issue{ID: "id1", Identifier: ident, Title: "T", State: "In Progress"}
	cases := []struct {
		name        string
		ev          func() OrchestratorEvent
		prep        func(o *Orchestrator, s *State)
		wantCleared bool
	}{
		{
			name: "context canceled, no retry pending",
			ev: func() OrchestratorEvent {
				return OrchestratorEvent{Type: EventWorkerExited, IssueID: issue.ID, Error: context.Canceled,
					RunEntry: &RunEntry{Issue: issue, TerminalReason: TerminalFailed}}
			},
			wantCleared: true,
		},
		{
			name: "stalled, no retry pending",
			ev: func() OrchestratorEvent {
				return OrchestratorEvent{Type: EventWorkerExited, IssueID: issue.ID,
					RunEntry: &RunEntry{Issue: issue, TerminalReason: TerminalStalled}}
			},
			wantCleared: true,
		},
		{
			name: "terminated by user",
			prep: func(o *Orchestrator, _ *State) {
				o.userTerminatedMu.Lock()
				o.userTerminatedIDs[ident] = struct{}{}
				o.userTerminatedMu.Unlock()
			},
			ev: func() OrchestratorEvent {
				return OrchestratorEvent{Type: EventWorkerExited, IssueID: issue.ID, Error: context.Canceled,
					RunEntry: &RunEntry{Issue: issue, TerminalReason: TerminalFailed}}
			},
			wantCleared: true,
		},
		{
			name: "stalled with a retry pending keeps the reviewer (the retry is the reviewer)",
			prep: func(_ *Orchestrator, s *State) {
				s.RetryAttempts[issue.ID] = &RetryEntry{IssueID: issue.ID, Identifier: ident, Attempt: 1, DueAt: time.Now().Add(time.Hour)}
			},
			ev: func() OrchestratorEvent {
				return OrchestratorEvent{Type: EventWorkerExited, IssueID: issue.ID,
					RunEntry: &RunEntry{Issue: issue, TerminalReason: TerminalStalled}}
			},
			wantCleared: false,
		},
		{
			name: "context canceled with a retry pending keeps the reviewer",
			prep: func(_ *Orchestrator, s *State) {
				s.RetryAttempts[issue.ID] = &RetryEntry{IssueID: issue.ID, Identifier: ident, Attempt: 1, DueAt: time.Now().Add(time.Hour)}
			},
			ev: func() OrchestratorEvent {
				return OrchestratorEvent{Type: EventWorkerExited, IssueID: issue.ID, Error: context.Canceled,
					RunEntry: &RunEntry{Issue: issue, TerminalReason: TerminalFailed}}
			},
			wantCleared: false,
		},
		{
			name: "canceled by reconciliation keeps the reviewer (#58: the reviewer's own exit follows)",
			ev: func() OrchestratorEvent {
				return OrchestratorEvent{Type: EventWorkerExited, IssueID: issue.ID,
					RunEntry: &RunEntry{Issue: issue, Kind: "reviewer", ProfileName: "reviewer", TerminalReason: TerminalCanceledByReconciliation}}
			},
			wantCleared: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := automationBaseCfg()
			cfg.Agent.Profiles = map[string]config.AgentProfile{"reviewer": {Command: "claude"}}
			mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
			o := New(cfg, mt, nil, nil)
			state := NewState(cfg)
			// The state a reviewer dispatch leaves behind (dispatchReviewerForIssue).
			state.Running[issue.ID] = &RunEntry{Issue: issue, Kind: "reviewer", ProfileName: "reviewer"}
			state.Claimed[issue.ID] = struct{}{}
			state.ReviewChainIndex[ident] = 1
			o.issueProfiles[ident] = "reviewer"
			o.reviewerInjectedProfiles[ident] = struct{}{}
			if tc.prep != nil {
				tc.prep(o, &state)
			}

			state = o.handleEvent(context.Background(), state, tc.ev())

			profile, injected := reviewerInjected(o, ident)
			_, chainOpen := state.ReviewChainIndex[ident]
			if tc.wantCleared {
				assert.False(t, injected, "marker must be cleared")
				assert.Empty(t, profile, "reviewer profile override must be cleared")
				assert.Empty(t, o.issueProfileForDispatch(state, ident), "next dispatch resolves the implementer profile")
				assert.False(t, chainOpen, "an abandoned chain must not stay open")
			} else {
				assert.True(t, injected, "marker must be kept")
				assert.Equal(t, "reviewer", profile)
				assert.True(t, chainOpen, "a live chain must stay open")
			}
		})
	}
}
