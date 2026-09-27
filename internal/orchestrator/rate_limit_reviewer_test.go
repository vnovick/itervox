package orchestrator

// CORE-031 — a rate-limited REVIEWER run must not be "recovered" as an
// implementer worker on the rate_limited switch profile. The reviewer's
// Kind is lost on the retry (fireRetries → dispatch() builds a Kind-less
// RunEntry), so the exhaustion branch must recover reviewer identity from
// reviewerInjectedProfiles (read under issueProfilesMu), the same durable
// source the TerminalSucceeded path uses (#58).

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

func reviewerRateLimitOrchestrator(t *testing.T) (*Orchestrator, domain.Issue) {
	t.Helper()
	cfg := automationBaseCfg()
	cfg.Agent.Command = "claude"
	cfg.Agent.MaxTurns = 1
	cfg.Agent.MaxRetries = 1
	cfg.Agent.MaxRetryBackoffMs = 10
	cfg.Agent.MaxSwitchesPerIssuePerWindow = 5
	cfg.Agent.SwitchWindowHours = 6
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"reviewer": {Command: "claude"},
		"fallback": {Command: "codex"},
	}
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "T", State: "In Progress"}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	o := New(cfg, mt, agenttest.RateLimitedFailRunner(), nil)
	o.SetRateLimitedAutomations([]RateLimitedAutomation{{
		ID:              "rate-limit-switch",
		SwitchToProfile: "fallback",
		SwitchToBackend: "codex",
		AutoResume:      true,
	}})
	return o, issue
}

// nextWorkerExit handles events from o.events in order until the first
// EventWorkerExited, which it returns unhandled. Only worker goroutines
// write to o.events here; the test goroutine plays the event loop.
func nextWorkerExit(t *testing.T, ctx context.Context, o *Orchestrator, state State) (State, OrchestratorEvent) {
	t.Helper()
	for {
		select {
		case ev := <-o.events:
			if ev.Type == EventWorkerExited {
				return state, ev
			}
			state = o.handleEvent(ctx, state, ev)
		case <-ctx.Done():
			t.Fatal("timed out waiting for EventWorkerExited")
		}
	}
}

// failOnceThenExhaust drives the real path the spec names: first exit →
// ScheduleRetry, fireRetries → dispatch() → second exit exhausts
// max_retries=1. start launches the first run.
func failOnceThenExhaust(t *testing.T, ctx context.Context, o *Orchestrator, state State, start func(*State)) State {
	t.Helper()
	start(&state)
	state, ev := nextWorkerExit(t, ctx, o, state)
	state = o.handleEvent(ctx, state, ev)
	require.Contains(t, state.RetryAttempts, "id1", "first rate-limited failure must schedule a retry")

	state = o.fireRetries(ctx, state, time.Now().Add(time.Hour))
	retry := state.Running["id1"]
	require.NotNil(t, retry, "fireRetries must dispatch the retry")
	require.Empty(t, retry.Kind, "the retry RunEntry built by dispatch() carries no Kind — the defect's precondition")

	state, ev = nextWorkerExit(t, ctx, o, state)
	return o.handleEvent(ctx, state, ev)
}

func TestRateLimitedSwitch_SkipsReviewerRuns(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	o, issue := reviewerRateLimitOrchestrator(t)
	state := NewState(o.cfg)
	profilesBefore := map[string]string{}
	for k, v := range state.IssueProfiles {
		profilesBefore[k] = v
	}

	out := failOnceThenExhaust(t, ctx, o, state, func(s *State) {
		o.dispatchReviewerForIssue(ctx, s, issue, "reviewer", time.Now())
		require.Equal(t, "reviewer", s.Running[issue.ID].Kind)
	})

	assert.NotContains(t, out.Running, issue.ID, "no recovery run may be dispatched for a rate-limited reviewer")
	assert.NotContains(t, out.Claimed, issue.ID, "the reviewer's claim is released")
	assert.Equal(t, profilesBefore, out.IssueProfiles, "state.IssueProfiles must be unchanged")
	assert.NotContains(t, out.AutoSwitchedIdentifiers, issue.Identifier)
	assert.Empty(t, out.SwitchHistory[issue.ID], "no switch may be recorded")
	for len(o.events) > 0 {
		ev := <-o.events
		assert.NotEqual(t, EventDispatchAutomation, ev.Type, "no recovery automation may be queued")
	}

	cancel()
	o.workersWg.Wait(5 * time.Second)
}

// Control: the same harness DOES observe a recovery for an implementer run,
// so the reviewer test's "zero recoveries" is not vacuous.
func TestRateLimitedSwitch_StillRecoversImplementerRuns(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	o, issue := reviewerRateLimitOrchestrator(t)
	state := NewState(o.cfg)

	out := failOnceThenExhaust(t, ctx, o, state, func(s *State) {
		*s = o.dispatch(ctx, *s, issue, 0)
	})

	require.Contains(t, out.Running, issue.ID, "an implementer run is recovered on the switch profile")
	assert.Equal(t, "fallback", out.Running[issue.ID].ProfileName)
	assert.Equal(t, "worker", out.Running[issue.ID].Kind)
	assert.Equal(t, "fallback", out.IssueProfiles[issue.Identifier])

	cancel()
	o.workersWg.Wait(5 * time.Second)
}
