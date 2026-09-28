package orchestrator

// CORE-054 — declarative agent.backend_fallback: profile_map rerouting,
// reset-time switch-back with a minimum dwell, and precedence against the
// operator pin and the rate_limited automations.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/config"
)

func TestBackendFallback_ProfileMap_UsesCodexCommand(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := breakerIssue("1")
	o, rr := breakerOrchestrator(t, fallbackOn(), a)
	state := NewState(o.cfg)
	state.IssueProfiles[a.Identifier] = "coder"

	state = limitExit(ctx, o, state, a, "coder", "claude", 0, TerminalRateLimited, quotaLimit(time.Now().Add(time.Hour)))
	require.Contains(t, state.Running, a.ID)
	assert.Equal(t, "coder-codex", state.Running[a.ID].ProfileName)
	rr.waitCount(t, "codex", 1)
	cmds := rr.snapshot()
	require.Len(t, cmds, 1)
	assert.True(t, strings.HasSuffix(cmds[0], "codex --model gpt-5"),
		"the mapped profile's own codex command runs, not a hint over claude: %q", cmds[0])
	assert.NotContains(t, cmds[0], "claude")
	cancel()
}

func TestBackendFallback_MinDwell_NoFlapOnSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := breakerIssue("1")
	o, _ := breakerOrchestrator(t, fallbackOn(), a)
	state := NewState(o.cfg)
	state.IssueProfiles[a.Identifier] = "coder"
	state = limitExit(ctx, o, state, a, "coder", "claude", 0, TerminalRateLimited, quotaLimit(time.Now().Add(10*time.Minute)))
	require.Equal(t, "codex", state.Running[a.ID].Backend)
	switchedAt := state.AutoSwitchInfo[a.Identifier].SwitchedAt
	require.False(t, switchedAt.IsZero())

	// The fallback run succeeds while Claude is still limited: the override
	// must survive (the old clear-on-success flapped the next dispatch back).
	attempt := 0
	state = o.handleEvent(ctx, state, OrchestratorEvent{
		Type: EventWorkerExited, IssueID: a.ID,
		RunEntry: &RunEntry{Issue: a, TerminalReason: TerminalSucceeded, RetryAttempt: &attempt},
	})
	assert.Equal(t, "coder-codex", state.IssueProfiles[a.Identifier], "no flap on success")
	assert.Equal(t, "codex", state.IssueBackends[a.Identifier])

	// Claude's reset passes, but the minimum dwell has not: still no switch back.
	e := state.BackendHealth["claude"]
	e.LimitedUntil = time.Now().Add(-time.Second)
	state.BackendHealth["claude"] = e
	assert.Zero(t, o.revertBackendFallbackSwitches(&state, switchedAt.Add(29*time.Minute)))
	state = o.dispatch(ctx, state, a, 0)
	require.Contains(t, state.Running, a.ID)
	assert.Equal(t, "codex", state.Running[a.ID].Backend, "inside the dwell the issue stays on codex")
	delete(state.Running, a.ID)
	delete(state.Claimed, a.ID)

	// Dwell elapsed and the reset passed: switch back.
	assert.Equal(t, 1, o.revertBackendFallbackSwitches(&state, switchedAt.Add(31*time.Minute)))
	assert.NotContains(t, state.IssueBackends, a.Identifier)
	assert.Equal(t, "", state.IssueProfiles[a.Identifier], "back to the natural profile")
	assert.NotContains(t, state.AutoSwitchInfo, a.Identifier)
	cancel()
}

func TestBackendFallback_SwitchBackWaitsForReset(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := breakerIssue("1")
	o, _ := breakerOrchestrator(t, fallbackOn(), a)
	state := NewState(o.cfg)
	state.IssueProfiles[a.Identifier] = "coder"
	state = limitExit(ctx, o, state, a, "coder", "claude", 0, TerminalRateLimited, quotaLimit(time.Now().Add(5*time.Hour)))
	switchedAt := state.AutoSwitchInfo[a.Identifier].SwitchedAt
	assert.Zero(t, o.revertBackendFallbackSwitches(&state, switchedAt.Add(time.Hour)),
		"dwell elapsed but Claude has not reset: stay on codex")
	assert.Equal(t, "codex", state.IssueBackends[a.Identifier])
	cancel()
}

func TestBackendFallback_PrecedenceOverRateLimitedRules(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, b := breakerIssue("1"), breakerIssue("2")
	o, _ := breakerOrchestrator(t, fallbackOn(), a, b)
	o.SetRateLimitedAutomations([]RateLimitedAutomation{{ID: "rl", SwitchToProfile: "responder", SwitchToBackend: "codex", AutoResume: true}})
	state := NewState(o.cfg)
	state.IssueProfiles[a.Identifier] = "coder" // mapped by backend_fallback
	state.IssueProfiles[b.Identifier] = "solo"  // not mapped

	state = limitExit(ctx, o, state, a, "coder", "claude", 0, TerminalRateLimited, quotaLimit(time.Now().Add(time.Hour)))
	require.Contains(t, state.Running, a.ID)
	assert.Equal(t, "coder-codex", state.Running[a.ID].ProfileName, "a mapped profile takes backend_fallback")
	assert.Empty(t, state.Running[a.ID].AutomationID, "the rate_limited rule does not also fire")
	assert.Equal(t, AutoSwitchSourceBackendFallback, state.AutoSwitchInfo[a.Identifier].Source)

	state = limitExit(ctx, o, state, b, "solo", "claude", 0, TerminalRateLimited, quotaLimit(time.Now().Add(time.Hour)))
	require.Contains(t, state.Running, b.ID)
	assert.Equal(t, "rl", state.Running[b.ID].AutomationID, "an unmapped profile falls back to the rate_limited rules")
	assert.Equal(t, "responder", state.Running[b.ID].ProfileName)
	assert.Equal(t, AutoSwitchSourceAutomation, state.AutoSwitchInfo[b.Identifier].Source)
	cancel()
}

func TestBackendFallback_OperatorPinBlocksFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := breakerIssue("1")
	o, _ := breakerOrchestrator(t, fallbackOn(), a)
	state := NewState(o.cfg)
	state.IssueProfiles[a.Identifier] = "coder"
	now := time.Now()
	o.recordBackendLimit(&state, "claude", "", "ENG-9", quotaLimit(now.Add(time.Hour)), now)
	o.SetIssueBackend(a.Identifier, "claude")

	state = o.dispatch(ctx, state, a, 0)
	assert.NotContains(t, state.Running, a.ID, "an operator pin beats backend_fallback")
	assert.Equal(t, IneligibleBackendLimited, IneligibleReason(a, state, o.cfg))
	assert.NotContains(t, state.AutoSwitchInfo, a.Identifier)
	cancel()
}

func TestBackendFallback_ChainHopIgnoresDwell(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := breakerIssue("1")
	o, _ := breakerOrchestrator(t, fallbackOn(), a)
	state := NewState(o.cfg)
	state.IssueProfiles[a.Identifier] = "coder"
	state = limitExit(ctx, o, state, a, "coder", "claude", 0, TerminalRateLimited, quotaLimit(time.Now().Add(time.Minute)))
	require.Equal(t, "codex", state.Running[a.ID].Backend)
	// Claude resets (a probe would be admitted); then the codex fallback is
	// limited inside the dwell. The dwell must not keep the issue on codex.
	delete(state.BackendHealth, "claude")
	state = limitExit(ctx, o, state, a, "coder-codex", "codex", 0, TerminalRateLimited, quotaLimit(time.Now().Add(time.Hour)))
	require.Contains(t, state.Running, a.ID)
	assert.Equal(t, "claude", state.Running[a.ID].Backend, "hop along the chain despite min_dwell")
	assert.Equal(t, "coder", state.Running[a.ID].ProfileName)
	cancel()
}

func TestBackendFallback_ReviewerUsesOwnMapping(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := breakerIssue("1")
	o, _ := breakerOrchestrator(t, fallbackOn(), a)
	state := NewState(o.cfg)
	now := time.Now()
	o.recordBackendLimit(&state, "claude", "", "ENG-9", quotaLimit(now.Add(time.Hour)), now)

	o.dispatchReviewerForIssue(ctx, &state, a, "reviewer", now)
	require.Contains(t, state.Running, a.ID)
	assert.Equal(t, "reviewer", state.Running[a.ID].Kind)
	assert.Equal(t, "reviewer-codex", state.Running[a.ID].ProfileName)
	assert.Equal(t, "codex", state.Running[a.ID].Backend)
	assert.NotContains(t, state.IssueProfiles, a.Identifier, "a reviewer reroute leaves the implementer's profile alone")
	cancel()
}

func TestBackendFallback_ReviewerHeldIsRetriedNotLost(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := breakerIssue("1")
	o, _ := breakerOrchestrator(t, config.BackendFallbackConfig{}, a)
	state := NewState(o.cfg)
	now := time.Now()
	o.recordBackendLimit(&state, "claude", "", "ENG-9", quotaLimit(now.Add(time.Hour)), now)

	o.dispatchReviewerForIssue(ctx, &state, a, "reviewer", now)
	assert.NotContains(t, state.Running, a.ID)
	require.Contains(t, state.RetryAttempts, a.ID, "the review is retried when the breaker admits it")
	assert.True(t, o.exitedRunIsReviewer(nil, a.Identifier), "the retry runs the reviewer profile")
	cancel()
}

// CORE-056: the pin check uses the same resolver as dispatch (CORE-115).
func TestCheckIssueBackendPin_RefusesMismatchedCommand(t *testing.T) {
	o, _ := breakerOrchestrator(t, config.BackendFallbackConfig{})
	o.cfg.Agent.Profiles["wrapped"] = config.AgentProfile{Command: "./agent.sh"}
	o.SetIssueProfile("ENG-2", "wrapped")

	err := o.CheckIssueBackendPin("ENG-1", "codex") // default command: claude --model opus
	require.Error(t, err)
	assert.Contains(t, err.Error(), `runs "claude"`)
	assert.NoError(t, o.CheckIssueBackendPin("ENG-1", "claude"))
	assert.NoError(t, o.CheckIssueBackendPin("ENG-2", "codex"), "a wrapper takes the hint")
	o.SetIssueProfile("ENG-3", "coder-codex")
	assert.Error(t, o.CheckIssueBackendPin("ENG-3", "claude"), "the issue's profile command decides")
}
