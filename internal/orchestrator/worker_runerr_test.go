package orchestrator

// CORE-029 (worker half) — when the runner returns an error, the worker's
// exit cause must keep the vendor FailureText (so the exhausted-retry
// rate-limit classifier sees it) while still wrapping the runner error with
// %w (so errors.Is(err, context.Canceled) keeps routing orchestrator-driven
// cancellations to the claim-release branch). The zero-token "clean session
// end" shortcut applies only when the runner returned no error.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// fixedResultRunner returns one canned (TurnResult, error) for every turn.
type fixedResultRunner struct {
	result agent.TurnResult
	err    error
}

func (r fixedResultRunner) RunTurn(context.Context, agent.Logger, func(agent.TurnResult), *string, string, string, string, string, string, int, int, agent.PermissionMode) (agent.TurnResult, error) {
	return r.result, r.err
}

// runWorkerOnce drives the real runWorker synchronously against runner and
// returns the EventWorkerExited it emitted.
func runWorkerOnce(t *testing.T, runner agent.Runner) OrchestratorEvent {
	t.Helper()
	cfg := automationBaseCfg()
	cfg.Agent.MaxTurns = 3
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "T", State: "In Progress"}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	o := New(cfg, mt, runner, nil)
	o.events = make(chan OrchestratorEvent, 64)

	o.workersWg.Add(issue.Identifier)
	o.runWorker(t.Context(), issue, 0, "", "claude", "claude", "", true, nil, nil, nil)

	for {
		select {
		case ev := <-o.events:
			if ev.Type == EventWorkerExited {
				return ev
			}
		default:
			t.Fatal("runWorker returned without emitting EventWorkerExited")
			return OrchestratorEvent{}
		}
	}
}

func TestWorker_ReadTimeoutKeepsFailureText(t *testing.T) {
	const failureText = "You've hit your usage limit · resets 3pm (Europe/London)"
	readErr := errors.New("agent: read timeout after 30000ms idle")
	ev := runWorkerOnce(t, fixedResultRunner{
		result: agent.TurnResult{Failed: true, FailureText: failureText, InputTokens: 12, OutputTokens: 3},
		err:    readErr,
	})

	require.NotNil(t, ev.RunEntry)
	assert.Equal(t, TerminalFailed, ev.RunEntry.TerminalReason)
	require.Error(t, ev.Error)
	assert.Contains(t, ev.Error.Error(), failureText,
		"the exit cause must carry the vendor FailureText so the rate-limit classifier can see it")
	assert.ErrorIs(t, ev.Error, readErr, "the runner error must stay reachable through %%w")
	assert.False(t, errors.Is(ev.Error, context.Canceled))
	assert.True(t, IsRateLimitFailure(ev.Error.Error()),
		"the exhausted-retry classifier input (ev.Error.Error()) must classify as a rate limit")
}

func TestWorker_ReadTimeoutWithZeroTokensIsNotCleanEnd(t *testing.T) {
	readErr := errors.New("agent: read timeout after 30000ms idle")
	cases := []struct {
		name   string
		result agent.TurnResult
		err    error
	}{
		{"read timeout, failed flag set", agent.TurnResult{Failed: true}, readErr},
		{"read timeout, failed flag unset", agent.TurnResult{}, readErr},
		{"start failure", agent.TurnResult{Failed: true}, fmt.Errorf("agent: start: %w", errors.New("exec: \"claude\": executable file not found in $PATH"))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := runWorkerOnce(t, fixedResultRunner{result: tc.result, err: tc.err})
			require.NotNil(t, ev.RunEntry)
			assert.Equal(t, TerminalFailed, ev.RunEntry.TerminalReason,
				"a runner error on a zero-token turn is a failure, not a clean session end")
			require.Error(t, ev.Error)
			assert.ErrorIs(t, ev.Error, tc.err)
		})
	}
}

func TestWorker_CanceledContextStaysCanceled(t *testing.T) {
	const failureText = "partial: the agent was mid-reply"
	canceledErr := fmt.Errorf("agent: turn aborted: %w", context.Canceled)
	ev := runWorkerOnce(t, fixedResultRunner{
		result: agent.TurnResult{Failed: true, FailureText: failureText, InputTokens: 4, OutputTokens: 1},
		err:    canceledErr,
	})

	require.NotNil(t, ev.RunEntry)
	require.Error(t, ev.Error)
	assert.True(t, errors.Is(ev.Error, context.Canceled),
		"FailureText folding must keep %%w so the cancellation branch still recognises the exit")
	assert.True(t, strings.Contains(ev.Error.Error(), failureText))

	// Feed the real event into the real event loop handler: the
	// context.Canceled branch releases the claim and schedules no retry.
	cfg := automationBaseCfg()
	cfg.Agent.MaxRetries = 3
	o := New(cfg, tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates), fixedResultRunner{}, nil)
	state := NewState(cfg)
	state.Claimed[ev.IssueID] = struct{}{}
	state.Running[ev.IssueID] = &RunEntry{Issue: ev.RunEntry.Issue, Backend: "claude"}

	out := o.handleEvent(t.Context(), state, ev)
	assert.NotContains(t, out.Claimed, ev.IssueID, "cancelled exit must release the claim")
	assert.NotContains(t, out.RetryAttempts, ev.IssueID, "cancelled exit must not schedule a retry")
}
