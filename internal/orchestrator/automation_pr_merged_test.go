package orchestrator

import (
	"testing"
	"time"

	"github.com/vnovick/itervox/internal/config"
)

func TestDispatchMatchingPRMergedAutomations_FiresOnce(t *testing.T) {
	cfg := automationBaseCfg()
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"notify-merge": {Command: "claude", Backend: "claude"},
	}
	o := newOrchestratorForTest(cfg)
	o.SetPRMergedAutomations([]PRMergedAutomation{{
		ID:          "notify-merge",
		ProfileName: "notify-merge",
	}})
	state := NewState(cfg)
	state.MaxConcurrentAgents = 10
	issue := automationIssue("In Review")
	event := PRMergedEvent{
		PRURL:     "https://github.com/x/y/pull/7",
		PRNumber:  7,
		MergedSHA: "abc123",
		MergedAt:  time.Now(),
	}
	o.dispatchMatchingPRMergedAutomations(t.Context(), &state, issue, event, time.Now())
	if state.AutomationDispatchesPRMergedTotal != 1 {
		t.Errorf("dispatches counter = %d; want 1", state.AutomationDispatchesPRMergedTotal)
	}
}

func TestDispatchMatchingPRMergedAutomations_DedupSecondFire(t *testing.T) {
	cfg := automationBaseCfg()
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"notify-merge": {Command: "claude", Backend: "claude"},
	}
	o := newOrchestratorForTest(cfg)
	o.SetPRMergedAutomations([]PRMergedAutomation{{
		ID:          "notify-merge",
		ProfileName: "notify-merge",
	}})
	state := NewState(cfg)
	state.MaxConcurrentAgents = 10
	issue := automationIssue("In Review")
	event := PRMergedEvent{PRURL: "https://github.com/x/y/pull/7", PRNumber: 7}

	o.dispatchMatchingPRMergedAutomations(t.Context(), &state, issue, event, time.Now())
	o.dispatchMatchingPRMergedAutomations(t.Context(), &state, issue, event, time.Now())

	if state.AutomationDispatchesPRMergedTotal != 1 {
		t.Errorf("dispatches = %d; want 1 (dedup must prevent second fire)", state.AutomationDispatchesPRMergedTotal)
	}
	if state.AutomationDroppedPRMergedDedupTotal != 1 {
		t.Errorf("dedup-drops = %d; want 1", state.AutomationDroppedPRMergedDedupTotal)
	}
}

func TestPRMergedDedupKey_IsStable(t *testing.T) {
	a := prMergedDedupKey("ENG-1", "https://github.com/x/y/pull/7", "automation-a")
	b := prMergedDedupKey("ENG-1", "https://github.com/x/y/pull/7", "automation-a")
	if a != b {
		t.Errorf("dedup key not stable across calls: %q vs %q", a, b)
	}
}
