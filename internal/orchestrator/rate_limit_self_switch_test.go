package orchestrator

// CORE-032 — when both backends are rate-limited, an exhausted run on the
// rule's own switch target must not re-fire the rule onto itself: that
// burns a switch-cap slot and a full retry round for nothing, and with
// cooldown 0 and cap 0 it loops without bound.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

func selfSwitchOrchestrator(t *testing.T) (*Orchestrator, domain.Issue) {
	t.Helper()
	cfg := automationBaseCfg()
	cfg.Agent.MaxRetries = 1
	cfg.Agent.MaxSwitchesPerIssuePerWindow = 5
	cfg.Agent.SwitchWindowHours = 6
	cfg.Tracker.FailedState = "Failed"
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"default":  {Command: "claude"},
		"fallback": {Command: "codex"},
	}
	issue := automationIssue("In Progress")
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	o := New(cfg, mt, &agenttest.FakeRunner{Stall: true}, nil)
	o.SetRateLimitedAutomations([]RateLimitedAutomation{{
		ID:              "rate-limit-switch",
		SwitchToProfile: "fallback",
		SwitchToBackend: "codex",
		AutoResume:      true,
		Cooldown:        time.Hour,
	}})
	return o, issue
}

// exhaustRateLimited feeds the real exhausted-retry exit for the run
// currently in state.Running (or a fresh one on profile/backend when none
// is running) into the real event-loop handler.
func exhaustRateLimited(ctx context.Context, o *Orchestrator, state State, issue domain.Issue, profile, backend string) State {
	attempt := 1
	if _, ok := state.Running[issue.ID]; !ok {
		state.Running[issue.ID] = &RunEntry{Issue: issue, Backend: backend, ProfileName: profile, RetryAttempt: &attempt}
		state.Claimed[issue.ID] = struct{}{}
	}
	return o.handleEvent(ctx, state, OrchestratorEvent{
		Type:     EventWorkerExited,
		IssueID:  issue.ID,
		RunEntry: &RunEntry{Issue: issue, TerminalReason: TerminalFailed, RetryAttempt: &attempt},
		Error:    errors.New("turn 1: You've hit your usage limit · resets 3pm"),
	})
}

func switchCount(state State, issueID string) int {
	return len(state.SwitchHistory[issueID])
}

func TestRateLimitedSwitch_StillSwitchesToDifferentTarget(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o, issue := selfSwitchOrchestrator(t)

	out := exhaustRateLimited(ctx, o, NewState(o.cfg), issue, "default", "claude")

	require.Contains(t, out.Running, issue.ID, "default → fallback must dispatch the recovery")
	assert.Equal(t, "fallback", out.Running[issue.ID].ProfileName)
	assert.Equal(t, "fallback", out.IssueProfiles[issue.Identifier])
	assert.Equal(t, 1, switchCount(out, issue.ID))
	_, muted := o.rateLimitCooldownUntil(out, issue.ID+"|default")
	assert.True(t, muted, "the source profile's cooldown is set")

	cancel()
	o.workersWg.Wait(5 * time.Second)
}

func TestRateLimitedSwitch_SkipsSelfSwitch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o, issue := selfSwitchOrchestrator(t)

	// A recorded source → target switch: default exhausted, fallback running.
	state := exhaustRateLimited(ctx, o, NewState(o.cfg), issue, "default", "claude")
	require.Contains(t, state.Running, issue.ID)
	require.Equal(t, "fallback", state.Running[issue.ID].ProfileName)
	require.Equal(t, 1, switchCount(state, issue.ID))
	queueBefore := len(state.AutomationQueue)

	// The recovery (target) run is rate-limited too and exhausts its retries.
	out := exhaustRateLimited(ctx, o, state, issue, "fallback", "codex")

	assert.NotContains(t, out.Running, issue.ID, "no automation may be dispatched onto the profile that just failed")
	assert.Len(t, out.AutomationQueue, queueBefore, "no automation may be queued")
	assert.Equal(t, 1, switchCount(out, issue.ID), "no new switch may be recorded in SwitchHistory")
	_, muted := o.rateLimitCooldownUntil(out, issue.ID+"|fallback")
	assert.False(t, muted, "no cooldown may be set for the self-switch")
	for len(o.events) > 0 {
		assert.NotEqual(t, EventDispatchAutomation, (<-o.events).Type)
	}

	cancel()
	o.workersWg.Wait(5 * time.Second)
}
