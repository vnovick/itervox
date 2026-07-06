package orchestrator

import (
	"testing"
	"time"

	"github.com/vnovick/itervox/internal/config"
)

func TestSelfReentryCounter_IncrementsOnDrop(t *testing.T) {
	cfg := automationBaseCfg()
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"input-responder": {Command: "claude", Backend: "claude"},
	}
	o := newOrchestratorForTest(cfg)
	o.SetInputRequiredAutomations([]InputRequiredAutomation{{
		ID:          "input-responder",
		ProfileName: "input-responder",
	}})
	state := NewState(cfg)
	issue := automationIssue("Todo")
	prevRun := &RunEntry{
		AutomationID: "input-responder",
		TriggerType:  config.AutomationTriggerInputRequired,
		Issue:        issue,
	}
	entry := &InputRequiredEntry{
		IssueID: issue.ID,
		Context: "Should I proceed?",
	}
	o.dispatchMatchingInputRequiredAutomations(t.Context(), &state, issue, entry, time.Now(), prevRun)
	if state.AutomationDropsSelfReentryTotal != 1 {
		t.Errorf("AutomationDropsSelfReentryTotal = %d; want 1", state.AutomationDropsSelfReentryTotal)
	}
	// A second invocation should bump the counter again.
	o.dispatchMatchingInputRequiredAutomations(t.Context(), &state, issue, entry, time.Now(), prevRun)
	if state.AutomationDropsSelfReentryTotal != 2 {
		t.Errorf("AutomationDropsSelfReentryTotal = %d; want 2", state.AutomationDropsSelfReentryTotal)
	}
}

func TestSelfReentryCounter_DoesNotIncrementOnHumanLaunchedExit(t *testing.T) {
	cfg := automationBaseCfg()
	cfg.Agent.MaxConcurrentAgents = 10
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"input-responder": {Command: "claude", Backend: "claude"},
	}
	o := newOrchestratorForTest(cfg)
	o.SetInputRequiredAutomations([]InputRequiredAutomation{{
		ID:          "input-responder",
		ProfileName: "input-responder",
	}})
	state := NewState(cfg)
	issue := automationIssue("Todo")
	// AutomationID empty = user-launched run; the guard must NOT fire.
	prevRun := &RunEntry{AutomationID: "", Issue: issue}
	entry := &InputRequiredEntry{IssueID: issue.ID, Context: "?"}
	o.dispatchMatchingInputRequiredAutomations(t.Context(), &state, issue, entry, time.Now(), prevRun)
	if state.AutomationDropsSelfReentryTotal != 0 {
		t.Errorf("counter must stay 0 for human-launched exit; got %d", state.AutomationDropsSelfReentryTotal)
	}
}
