package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/vnovick/itervox/internal/config"
)

// dispatchMatchingInputRequiredAutomations must skip stale entries when an
// input_required automation configures MaxAge > 0. This locks gap A: a third
// bot retry cannot fire on an issue that's been blocked longer than the rule
// allows.
//
// Test approach: register a single rule with MaxAge=1h, drive
// dispatchMatchingInputRequiredAutomations against:
//   - a fresh entry (queued 5m ago)  — must dispatch
//   - a stale entry (queued 2h ago)  — must skip
//
// "Dispatch" is observed via state.Claimed[issue.ID]: startAutomationRun
// claims the issue before launching the worker goroutine, so a race-free,
// no-runtime-deps assertion on Claimed proves the dispatch decision.
func TestInputRequiredAutomation_SkipsStaleEntries(t *testing.T) {
	cfg := automationBaseCfg()
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"input-responder": {Command: "claude", Backend: "claude"},
	}

	o := newOrchestratorForTest(cfg)
	o.SetInputRequiredAutomations([]InputRequiredAutomation{
		{
			ID:          "auto-fresh-only",
			ProfileName: "input-responder",
			MaxAge:      time.Hour, // anything older than 1h is stale
		},
	})

	now := time.Now()
	state := NewState(cfg)
	issue := automationIssue("Todo")

	// Fresh entry: queued 5 minutes ago.
	freshEntry := &InputRequiredEntry{
		IssueID:    issue.ID,
		Identifier: issue.Identifier,
		Context:    "needs clarification",
		QueuedAt:   now.Add(-5 * time.Minute),
	}
	o.dispatchMatchingInputRequiredAutomations(t.Context(), &state, issue, freshEntry, now, nil)
	_, freshClaimed := state.Claimed[issue.ID]
	assert.True(t, freshClaimed, "fresh entry must be dispatched (claimed)")

	// Reset state for the stale case.
	state = NewState(cfg)
	staleEntry := &InputRequiredEntry{
		IssueID:    issue.ID,
		Identifier: issue.Identifier,
		Context:    "still asking after 2h",
		QueuedAt:   now.Add(-2 * time.Hour),
	}
	o.dispatchMatchingInputRequiredAutomations(t.Context(), &state, issue, staleEntry, now, nil)
	_, staleClaimed := state.Claimed[issue.ID]
	assert.False(t, staleClaimed, "stale entry must NOT be dispatched")
}

// MaxAge=0 preserves pre-feature behaviour: every entry that matches the
// other filters is dispatched regardless of age.
func TestInputRequiredAutomation_NoMaxAgeMatchesEverything(t *testing.T) {
	cfg := automationBaseCfg()
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"input-responder": {Command: "claude", Backend: "claude"},
	}

	o := newOrchestratorForTest(cfg)
	o.SetInputRequiredAutomations([]InputRequiredAutomation{
		{
			ID:          "auto-no-age-cap",
			ProfileName: "input-responder",
			MaxAge:      0,
		},
	})

	now := time.Now()
	state := NewState(cfg)
	issue := automationIssue("Todo")
	veryOld := &InputRequiredEntry{
		IssueID:    issue.ID,
		Identifier: issue.Identifier,
		Context:    "ancient",
		QueuedAt:   now.Add(-90 * 24 * time.Hour),
	}
	o.dispatchMatchingInputRequiredAutomations(t.Context(), &state, issue, veryOld, now, nil)
	_, claimed := state.Claimed[issue.ID]
	assert.True(t, claimed, "MaxAge=0 must dispatch every age — back-compat")
}

// newOrchestratorForTest builds a minimal Orchestrator that is enough to
// reach state.Claimed within startAutomationRun. The runWorker goroutine
// it spawns has its own deferred recover, so we don't need to wire
// workspace / runner / tracker for these unit tests — we only assert on
// the synchronous state mutation that happens BEFORE the goroutine.
func newOrchestratorForTest(cfg *config.Config) *Orchestrator {
	return &Orchestrator{
		cfg:           cfg,
		workerCancels: make(map[string]context.CancelFunc),
		events:        make(chan OrchestratorEvent, 16),
	}
}
