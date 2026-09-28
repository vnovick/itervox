package orchestrator

// CORE-051 — a structured quota limit (agent.LimitSignal, CORE-050) is
// classified on the FIRST failure: the worker exits TerminalRateLimited and
// the event loop evaluates the rate_limited fallback immediately, without
// consuming a retry — including with max_retries: 0 ("unlimited"), where the
// old retry-exhaustion gate never fired and the issue re-ran on the
// exhausted backend forever. With no eligible fallback the exit falls
// through to the normal retry path, rescheduled with the vendor delay.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

const structuredLimitText = "You've hit your session limit · resets 3pm (UTC)"

func quotaLimit(resetsAt time.Time) *agent.LimitSignal {
	return &agent.LimitSignal{
		Kind: agent.LimitKindQuota, Source: agent.LimitSourceRateLimitEvent,
		Status: "rejected", LimitType: "five_hour", ResetsAt: resetsAt,
	}
}

// firstFailureOrchestrator: profiles default (claude) and fallback (codex),
// retries as given, a parked runner for any dispatched run.
func firstFailureOrchestrator(t *testing.T, maxRetries int, rules ...RateLimitedAutomation) (*Orchestrator, domain.Issue) {
	t.Helper()
	cfg := automationBaseCfg()
	cfg.Agent.MaxRetries = maxRetries
	cfg.Agent.MaxRetryBackoffMs = 300_000
	cfg.Agent.MaxSwitchesPerIssuePerWindow = 2
	cfg.Agent.SwitchWindowHours = 6
	cfg.Tracker.FailedState = "Failed"
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"default":   {Command: "claude"},
		"fallback":  {Command: "codex"},
		"responder": {Command: "codex"},
	}
	issue := automationIssue("In Progress")
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	o := New(cfg, mt, &agenttest.FakeRunner{Stall: true}, nil)
	o.SetRateLimitedAutomations(rules)
	return o, issue
}

func switchRule() RateLimitedAutomation {
	return RateLimitedAutomation{ID: "rl", SwitchToProfile: "fallback", SwitchToBackend: "codex", AutoResume: true}
}

// limitExit feeds one worker exit for the run in state.Running (or a fresh
// run on profile/backend at attempt when none is running) into the real
// event-loop handler.
func limitExit(ctx context.Context, o *Orchestrator, state State, issue domain.Issue, profile, backend string, attempt int, reason TerminalReason, limit *agent.LimitSignal) State {
	if _, ok := state.Running[issue.ID]; !ok {
		state.Running[issue.ID] = &RunEntry{Issue: issue, Backend: backend, ProfileName: profile, RetryAttempt: &attempt}
		state.Claimed[issue.ID] = struct{}{}
	}
	return o.handleEvent(ctx, state, OrchestratorEvent{
		Type:     EventWorkerExited,
		IssueID:  issue.ID,
		RunEntry: &RunEntry{Issue: issue, TerminalReason: reason, RetryAttempt: &attempt},
		Error:    errors.New("turn 1: " + structuredLimitText),
		Limit:    limit,
	})
}

func TestWorkerExit_StructuredRateLimit_SwitchesWithoutRetries(t *testing.T) {
	t.Run("worker send site", func(t *testing.T) {
		ev := runWorkerOnce(t, fixedResultRunner{result: agent.TurnResult{
			Failed: true, FailureText: structuredLimitText, InputTokens: 5,
			LastLimit: quotaLimit(time.Now().Add(time.Hour)),
		}})
		require.NotNil(t, ev.RunEntry)
		assert.Equal(t, TerminalRateLimited, ev.RunEntry.TerminalReason)
		require.NotNil(t, ev.Limit, "the exit carries the LimitSignal")
		assert.True(t, ev.Limit.Terminal())
		assert.Contains(t, ev.Error.Error(), structuredLimitText)

		// CORE-166 ordering: a quota-limited turn is never parked as input-required.
		ev = runWorkerOnce(t, fixedResultRunner{result: agent.TurnResult{
			Failed: true, InputRequired: true, FailureText: structuredLimitText,
			LastLimit: quotaLimit(time.Time{}),
		}})
		assert.Equal(t, TerminalRateLimited, ev.RunEntry.TerminalReason, "a limit wins over input-required")
		assert.Nil(t, ev.InputRequiredEntry)

		// An advisory throttle alone does not end the turn as rate-limited.
		ev = runWorkerOnce(t, fixedResultRunner{result: agent.TurnResult{
			Failed: true, FailureText: "boom", InputTokens: 5,
			LastLimit: &agent.LimitSignal{Kind: agent.LimitKindThrottle, RetryAfter: time.Minute},
		}})
		assert.Equal(t, TerminalFailed, ev.RunEntry.TerminalReason)
		require.NotNil(t, ev.Limit, "the throttle's vendor delay rides along for the retry")
	})

	t.Run("event loop switches on the first failure", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		o, issue := firstFailureOrchestrator(t, 5, switchRule())

		out := limitExit(ctx, o, NewState(o.cfg), issue, "default", "claude", 0, TerminalRateLimited, quotaLimit(time.Now().Add(time.Hour)))

		require.Contains(t, out.Running, issue.ID, "the recovery must be dispatched on the first failure")
		assert.Equal(t, "fallback", out.Running[issue.ID].ProfileName)
		assert.Equal(t, "codex", out.Running[issue.ID].Backend)
		assert.NotContains(t, out.RetryAttempts, issue.ID, "no retry may be scheduled (or consumed) on the limited backend")
		assert.Equal(t, "fallback", out.IssueProfiles[issue.Identifier])
		assert.Len(t, out.SwitchHistory[issue.ID], 1)
		cancel()
		o.workersWg.Wait(5 * time.Second)
	})
}

func TestMaxRetriesZero_StillSwitchesOnRateLimit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o, issue := firstFailureOrchestrator(t, 0, switchRule())

	out := limitExit(ctx, o, NewState(o.cfg), issue, "default", "claude", 3, TerminalRateLimited, quotaLimit(time.Now().Add(time.Hour)))

	require.Contains(t, out.Running, issue.ID, "max_retries: 0 must still switch on a structured limit")
	assert.Equal(t, "fallback", out.Running[issue.ID].ProfileName)
	assert.NotContains(t, out.RetryAttempts, issue.ID)
	cancel()
	o.workersWg.Wait(5 * time.Second)
}

func TestWorkerExit_StructuredRateLimit_RespectsSwitchCap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// A helper rule (no auto-resume) so each limited helper run can itself
	// hit the limit and re-fire the rule without the self-switch guard.
	o, issue := firstFailureOrchestrator(t, 5, RateLimitedAutomation{ID: "rl-helper", ProfileName: "responder"})
	limit := quotaLimit(time.Now().Add(time.Hour))

	state := limitExit(ctx, o, NewState(o.cfg), issue, "default", "claude", 0, TerminalRateLimited, limit)
	require.Contains(t, state.Running, issue.ID, "1st structured limit switches")
	state = limitExit(ctx, o, state, issue, "", "", 0, TerminalRateLimited, limit)
	// CORE-053 knock-on: the 1st helper ran on codex and hit the limit, so
	// the codex breaker is open and the 2nd helper (codex again) is held in
	// the automation queue with backend_limited instead of starting at once.
	// The switch is still accepted and counted, which is what this test is
	// about.
	require.Len(t, state.SwitchHistory[issue.ID], 2, "2nd structured limit switches")
	require.Len(t, state.AutomationQueue, 1)
	for _, e := range state.AutomationQueue {
		assert.Equal(t, AutomationQueueReasonBackendLimited, e.Reason, "held, not dropped")
	}

	state = limitExit(ctx, o, state, issue, "", "", 0, TerminalRateLimited, limit)
	assert.NotContains(t, state.Running, issue.ID, "3rd structured limit within the window is not switched")
	assert.Len(t, state.SwitchHistory[issue.ID], 2, "the cap is not exceeded")
	require.Contains(t, state.RetryAttempts, issue.ID, "it falls to the retry path")
	assert.Equal(t, 1, state.RetryAttempts[issue.ID].Attempt, "and consumes a retry as today")
	cancel()
	o.workersWg.Wait(5 * time.Second)
}

func TestMaxRetriesZero_NoEligibleFallback_ReschedulesWithVendorDelay(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// A rule exists but its filter never matches this issue.
	o, issue := firstFailureOrchestrator(t, 0, RateLimitedAutomation{
		ID: "rl", SwitchToProfile: "fallback", AutoResume: true, States: []string{"Nope"},
	})

	before := time.Now()
	resets := before.Add(45 * time.Minute)
	out := limitExit(ctx, o, NewState(o.cfg), issue, "default", "claude", 0, TerminalRateLimited, quotaLimit(resets))

	assert.NotContains(t, out.Running, issue.ID)
	require.Contains(t, out.RetryAttempts, issue.ID, "no fallback: rescheduled, not dropped")
	due := out.RetryAttempts[issue.ID].DueAt
	assert.False(t, due.Before(resets.Add(-time.Second)), "rescheduled at the vendor reset (%v), got %v", resets, due)
	assert.Empty(t, out.SwitchHistory[issue.ID], "the switch cap counter is not consumed")
	assert.NotContains(t, out.PausedIdentifiers, issue.Identifier, "max_retries: 0 never exhausts")

	// No reset time: the normal backoff still applies (never a tight loop).
	out = limitExit(ctx, o, NewState(o.cfg), issue, "default", "claude", 0, TerminalRateLimited, quotaLimit(time.Time{}))
	require.Contains(t, out.RetryAttempts, issue.ID)
	assert.False(t, out.RetryAttempts[issue.ID].DueAt.Before(before.Add(10*time.Second)), "at least the 10s first backoff")

	// A far reset is capped so a misparsed time cannot park the issue for days.
	out = limitExit(ctx, o, NewState(o.cfg), issue, "default", "claude", 0, TerminalRateLimited, quotaLimit(before.Add(96*time.Hour)))
	assert.True(t, out.RetryAttempts[issue.ID].DueAt.Before(before.Add(rateLimitVendorDelayCap+time.Minute)))

	// A throttle on an ordinary failure reschedules with the vendor delay and
	// keeps its terminal reason.
	out = limitExit(ctx, o, NewState(o.cfg), issue, "default", "claude", 0, TerminalFailed,
		&agent.LimitSignal{Kind: agent.LimitKindThrottle, RetryAfter: 90 * time.Second})
	require.Contains(t, out.RetryAttempts, issue.ID)
	assert.False(t, out.RetryAttempts[issue.ID].DueAt.Before(before.Add(90*time.Second)))
	cancel()
	o.workersWg.Wait(5 * time.Second)
}

// TestWorkerExit_StructuredRateLimit_ReviewerKeepsNormalPath: the CORE-031
// guard holds on the first-failure path — a limited reviewer is not switched
// onto an implementer profile.
func TestWorkerExit_StructuredRateLimit_ReviewerKeepsNormalPath(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o, issue := firstFailureOrchestrator(t, 5, switchRule())
	state := NewState(o.cfg)
	attempt := 0
	state.Running[issue.ID] = &RunEntry{Issue: issue, Kind: "reviewer", ProfileName: "default", Backend: "claude", RetryAttempt: &attempt}
	state.Claimed[issue.ID] = struct{}{}

	out := limitExit(ctx, o, state, issue, "", "", 0, TerminalRateLimited, quotaLimit(time.Now().Add(time.Hour)))
	assert.NotContains(t, out.Running, issue.ID, "no recovery for a reviewer")
	assert.Empty(t, out.SwitchHistory[issue.ID])
	assert.NotEqual(t, "fallback", out.IssueProfiles[issue.Identifier])
	assert.Contains(t, out.RetryAttempts, issue.ID, "the reviewer takes the normal retry path")
	cancel()
	o.workersWg.Wait(5 * time.Second)
}

// TestWorkerExit_StructuredRateLimit_SelfSwitchGuardHolds: the CORE-032
// guard holds — a limited run on the rule's own target is not re-switched.
func TestWorkerExit_StructuredRateLimit_SelfSwitchGuardHolds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o, issue := firstFailureOrchestrator(t, 5, switchRule())
	out := limitExit(ctx, o, NewState(o.cfg), issue, "fallback", "codex", 0, TerminalRateLimited, quotaLimit(time.Now().Add(time.Hour)))
	assert.NotContains(t, out.Running, issue.ID)
	assert.Empty(t, out.SwitchHistory[issue.ID])
	assert.Contains(t, out.RetryAttempts, issue.ID)
	cancel()
	o.workersWg.Wait(5 * time.Second)
}
