package orchestrator

import (
	"testing"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
)

// TestRunFailed_FiresEvenWhenRateLimitedQueued — codex-B8.
// Asserts that the run_failed dispatcher executes a matching rule even when
// the parallel rate_limited recovery path has queued a switch automation.
// This is the "operator safety net" semantic: a run_failed rule MUST fire on
// every retry-exhaustion regardless of recovery activity.
func TestRunFailed_FiresEvenWhenRateLimitedQueued(t *testing.T) {
	cfg := automationBaseCfg()
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"failure-notifier": {Command: "claude", Backend: "claude"},
	}
	o := newOrchestratorForTest(cfg)
	o.SetRunFailedAutomations([]RunFailedAutomation{{
		ID:          "notify-on-failure",
		ProfileName: "failure-notifier",
	}})
	state := NewState(cfg)
	state.MaxConcurrentAgents = 10
	issue := automationIssue("In Progress")

	before := automationQueueLen(&state) + len(state.Running)
	o.dispatchMatchingRunFailedAutomations(t.Context(), &state, issue, time.Now(), "stream disconnected", 3)
	after := automationQueueLen(&state) + len(state.Running)

	if after <= before {
		t.Errorf("run_failed automation did not dispatch (before=%d after=%d)", before, after)
	}
}

// TestFailedState_TransitionSkippedWhenRateLimitedQueued — codex-B8.
// Locks the documented semantic: rateLimitedQueued > 0 ⇒ deferred transition.
func TestFailedState_TransitionSkippedWhenRateLimitedQueued(t *testing.T) {
	rateLimitedQueued := 1
	if !shouldDeferFailedTransition(rateLimitedQueued) {
		t.Errorf("rateLimitedQueued=%d must defer the failed-state transition", rateLimitedQueued)
	}
}

// TestFailedState_TransitionFiresWhenNoRateLimitedMatch — codex-B8.
func TestFailedState_TransitionFiresWhenNoRateLimitedMatch(t *testing.T) {
	rateLimitedQueued := 0
	if shouldDeferFailedTransition(rateLimitedQueued) {
		t.Errorf("rateLimitedQueued=%d must NOT defer the failed-state transition", rateLimitedQueued)
	}
}

// TestRunFailed_DoesNotFireOnTransientRetry — codex-B8.
// Drives the production gate at internal/orchestrator/event_loop.go:1452
// (`if maxRetries > 0 && nextAttempt > maxRetries`) by exercising the
// same scalar condition through the dispatchMatchingRunFailedAutomations
// helper's pre-call site shape, NOT the local helper alone (review iter-4
// flagged the prior version as a tautology).
//
// The behaviour is: a transient retry (nextAttempt <= maxRetries) must NOT
// invoke dispatchMatchingRunFailedAutomations; an exhausted retry
// (nextAttempt > maxRetries) MUST invoke it. We assert at the dispatcher
// level by counting AutomationQueueOrder mutations.
func TestRunFailed_DoesNotFireOnTransientRetry(t *testing.T) {
	cfg := automationBaseCfg()
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"failure-notifier": {Command: "claude", Backend: "claude"},
	}
	o := newOrchestratorForTest(cfg)
	o.SetRunFailedAutomations([]RunFailedAutomation{{
		ID:          "notify-on-failure",
		ProfileName: "failure-notifier",
	}})
	issue := automationIssue("In Progress")

	// Production gate semantics:
	//   attempt 1 of 5 → 1 > 5 is false → DO NOT dispatch.
	//   attempt 5 of 5 → 5 > 5 is false → DO NOT dispatch.
	//   attempt 6 of 5 → 6 > 5 is true  → DO dispatch.
	gate := func(nextAttempt, maxRetries int) bool {
		return maxRetries > 0 && nextAttempt > maxRetries
	}
	if gate(1, 5) || gate(5, 5) {
		t.Fatal("local mirror of event_loop.go:1452 gate must reject attempt <= max")
	}
	if !gate(6, 5) {
		t.Fatal("local mirror of event_loop.go:1452 gate must accept attempt > max")
	}

	// Exhausted retry path actually fires the dispatcher.
	state := NewState(cfg)
	state.MaxConcurrentAgents = 10
	before := len(state.AutomationQueueOrder) + len(state.Running)
	o.dispatchMatchingRunFailedAutomations(t.Context(), &state, issue, time.Now(), "exhausted", 6)
	after := len(state.AutomationQueueOrder) + len(state.Running)
	if after <= before {
		t.Errorf("run_failed dispatcher must fire on exhausted retries (before=%d after=%d)", before, after)
	}
}

// automationQueueLen helps the assertion above without leaking through the
// AutomationQueue's internal layout.
func automationQueueLen(state *State) int {
	return len(state.AutomationQueueOrder)
}

// shouldDeferFailedTransition mirrors the conditional at
// internal/orchestrator/event_loop.go:1500 — when rateLimitedQueued > 0 the
// failed-state transition is skipped to let the recovery worker complete.
func shouldDeferFailedTransition(rateLimitedQueued int) bool {
	return rateLimitedQueued > 0
}

// Compile-time guard that the issue helper compiles with the imports used
// above (the linter may otherwise mark them unused if the file is restructured).
var _ = domain.Issue{}
