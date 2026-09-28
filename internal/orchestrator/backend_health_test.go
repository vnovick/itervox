package orchestrator

// CORE-053 — backend-wide circuit breaker (State.BackendHealth) with the
// backend_limited ineligible reason. CORE-054 fallback tests live in
// backend_fallback_test.go and share these helpers.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// recordingRunner parks every run until its context ends and records the
// runner command each run was started with, so a test can count runs per
// backend.
type recordingRunner struct {
	mu       sync.Mutex
	commands []string
}

func (r *recordingRunner) RunTurn(ctx context.Context, _ agent.Logger, _ func(agent.TurnResult), _ *string, _, _, command, _, _ string, _, _ int, _ agent.PermissionMode) (agent.TurnResult, error) {
	r.mu.Lock()
	r.commands = append(r.commands, command)
	r.mu.Unlock()
	<-ctx.Done()
	return agent.TurnResult{Failed: true}, ctx.Err()
}

func (r *recordingRunner) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.commands)
}

func (r *recordingRunner) count(backend string) int {
	n := 0
	for _, c := range r.snapshot() {
		if agent.BackendFromCommand(c) == backend {
			n++
		}
	}
	return n
}

// waitCount waits until at least n runs on backend were started.
func (r *recordingRunner) waitCount(t *testing.T, backend string, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for r.count(backend) < n {
		if time.Now().After(deadline) {
			t.Fatalf("want %d %s runs, got %v", n, backend, r.snapshot())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func breakerIssue(n string) domain.Issue {
	return domain.Issue{ID: "id-" + n, Identifier: "ENG-" + n, Title: "T" + n, State: "In Progress"}
}

// fallbackOn is a typical agent.backend_fallback block: coder ⇄ coder-codex,
// the default command → coder-codex, reviewer → reviewer-codex.
func fallbackOn() config.BackendFallbackConfig {
	return config.BackendFallbackConfig{
		Enabled: true,
		Chain:   []string{"claude", "codex"},
		ProfileMap: map[string]map[string]string{
			"coder":       {"codex": "coder-codex"},
			"coder-codex": {"claude": "coder"},
			"default":     {"codex": "coder-codex"},
			"reviewer":    {"codex": "reviewer-codex"},
		},
		OnUnmapped:             config.BackendFallbackUnmappedHold,
		DefaultCooldownMinutes: 15,
		MinDwellMinutes:        30,
		SwitchBack:             config.BackendFallbackSwitchBackAtReset,
	}
}

func breakerOrchestrator(t *testing.T, fb config.BackendFallbackConfig, issues ...domain.Issue) (*Orchestrator, *recordingRunner) {
	t.Helper()
	cfg := automationBaseCfg()
	cfg.Agent.Command = "claude --model opus"
	cfg.Agent.MaxConcurrentAgents = 5
	cfg.Agent.MaxRetries = 5
	cfg.Agent.MaxRetryBackoffMs = 300_000
	cfg.Agent.MaxSwitchesPerIssuePerWindow = 4
	cfg.Agent.MaxTurns = 1
	cfg.Agent.SwitchWindowHours = 6
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"coder":          {Command: "claude --model opus"},
		"coder-codex":    {Command: "codex --model gpt-5"},
		"solo":           {Command: "claude --model haiku"},
		"responder":      {Command: "codex --model gpt-5-mini"},
		"reviewer":       {Command: "claude --model sonnet"},
		"reviewer-codex": {Command: "codex --model gpt-5-mini"},
	}
	cfg.Agent.BackendFallback = fb
	mt := tracker.NewMemoryTracker(issues, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	rr := &recordingRunner{}
	o := New(cfg, mt, rr, nil)
	t.Cleanup(func() { o.workersWg.Wait(5 * time.Second) })
	return o, rr
}

func runningIdentifiers(state State) []string {
	var out []string
	for _, e := range state.Running {
		out = append(out, e.Issue.Identifier)
	}
	slices.Sort(out)
	return out
}

func TestBackendHealth_GlobalBreaker_ReroutesOtherIssues(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, b := breakerIssue("1"), breakerIssue("2")
	o, rr := breakerOrchestrator(t, fallbackOn(), a, b)
	state := NewState(o.cfg)
	state.IssueProfiles[a.Identifier] = "coder"
	state.IssueProfiles[b.Identifier] = "coder"

	// Issue A hits the Claude quota; the breaker opens for the whole backend.
	state = limitExit(ctx, o, state, a, "coder", "claude", 0, TerminalRateLimited, quotaLimit(time.Now().Add(time.Hour)))
	entry, ok := state.BackendHealth[BackendHealthKey("claude", "")]
	require.True(t, ok, "the limit must open the claude breaker")
	assert.Equal(t, BackendStatusLimited, entry.Status)

	claudeBefore := rr.count("claude")
	// Issue B never ran; it must go straight to Codex without rediscovering the limit.
	state = o.dispatch(ctx, state, b, 0)
	require.Contains(t, state.Running, b.ID)
	assert.Equal(t, "codex", state.Running[b.ID].Backend)
	assert.Equal(t, "coder-codex", state.Running[b.ID].ProfileName)
	rr.waitCount(t, "codex", 2) // A's own fallback plus B
	assert.Equal(t, claudeBefore, rr.count("claude"), "the Claude runner must not be called again")

	require.Contains(t, state.Running, a.ID, "A is rerouted on its first failure")
	assert.Equal(t, "codex", state.Running[a.ID].Backend)
	assert.NotContains(t, state.RetryAttempts, a.ID, "no retry consumed")
	info := state.AutoSwitchInfo[b.Identifier]
	assert.Equal(t, AutoSwitchSourceBackendFallback, info.Source)
	assert.Equal(t, "claude", info.FromBackend)
	assert.Equal(t, "codex", info.ToBackend)
	assert.Empty(t, state.PausedIdentifiers)
	cancel()
}

func TestBackendHealth_AllLimited_IneligibleUntilEarliestReset(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, b := breakerIssue("1"), breakerIssue("2")
	o, _ := breakerOrchestrator(t, fallbackOn(), a, b)
	state := NewState(o.cfg)
	state.IssueProfiles[a.Identifier] = "coder"
	state.IssueProfiles[b.Identifier] = "coder"
	now := time.Now()
	claudeReset := now.Add(2 * time.Hour).Truncate(time.Second)
	codexReset := now.Add(time.Hour).Truncate(time.Second)

	state = limitExit(ctx, o, state, a, "coder", "claude", 0, TerminalRateLimited, quotaLimit(claudeReset))
	require.Equal(t, "codex", state.Running[a.ID].Backend)
	// The fallback itself hits its limit: both backends are now limited.
	state = limitExit(ctx, o, state, a, "coder-codex", "codex", 0, TerminalRateLimited, quotaLimit(codexReset))
	assert.NotContains(t, state.Running, a.ID, "no healthy target: A is held, not run")
	assert.NotContains(t, state.Claimed, a.ID, "the hold is a reason, not a claim")

	state = o.onTick(ctx, state)
	assert.NotContains(t, state.Running, b.ID)
	assert.Equal(t, IneligibleBackendLimited, IneligibleReason(b, state, o.cfg), "why-idle shows backend_limited")
	assert.Equal(t, IneligibleBackendLimited, IneligibleReason(a, state, o.cfg))
	hold, ok := state.BackendLimitedHolds[b.Identifier]
	require.True(t, ok)
	assert.True(t, hold.Until.Equal(codexReset), "held until the EARLIEST reset (%v), got %v", codexReset, hold.Until)
	assert.Empty(t, state.PausedIdentifiers, "a hold is never a pause")
	assert.Empty(t, state.RetryAttempts, "and consumes no retry")

	// The codex reset passes: the next tick admits exactly one probe on codex.
	e := state.BackendHealth[BackendHealthKey("codex", "")]
	e.LimitedUntil = time.Now().Add(-time.Second)
	state.BackendHealth[BackendHealthKey("codex", "")] = e
	state = o.onTick(ctx, state)
	require.Len(t, state.Running, 1, "one probe on the reset backend")
	for _, r := range state.Running {
		assert.Equal(t, "codex", r.Backend)
	}
	cancel()
}

func TestBackendHealth_HalfOpenProbe_SingleDispatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, b, c := breakerIssue("1"), breakerIssue("2"), breakerIssue("3")
	o, rr := breakerOrchestrator(t, config.BackendFallbackConfig{}, a, b, c)
	state := NewState(o.cfg)
	now := time.Now()
	require.True(t, o.recordBackendLimit(&state, "claude", "", "ENG-9", quotaLimit(now.Add(time.Hour)), now))
	// The published reset has passed (LimitedUntil expired): half-open.
	e := state.BackendHealth["claude"]
	e.LimitedUntil = now.Add(-time.Second)
	state.BackendHealth["claude"] = e

	state = o.onTick(ctx, state) // three eligible issues, ONE tick
	require.Len(t, state.Running, 1, "exactly one probe starts, got %v", runningIdentifiers(state))
	probe := runningIdentifiers(state)[0]
	e = state.BackendHealth["claude"]
	assert.Equal(t, BackendStatusProbing, e.Status)
	assert.Equal(t, probe, e.ProbeIssue)
	for _, is := range []domain.Issue{a, b, c} {
		if is.Identifier != probe {
			assert.Equal(t, IneligibleBackendLimited, IneligibleReason(is, state, o.cfg), is.Identifier)
		}
	}
	rr.waitCount(t, "claude", 1)

	// The probe succeeds: the breaker closes and the others dispatch.
	var probeIssue domain.Issue
	for _, r := range state.Running {
		probeIssue = r.Issue
	}
	attempt := 0
	state = o.handleEvent(ctx, state, OrchestratorEvent{
		Type: EventWorkerExited, IssueID: probeIssue.ID,
		RunEntry: &RunEntry{Issue: probeIssue, TerminalReason: TerminalSucceeded, RetryAttempt: &attempt},
	})
	_, stillTracked := state.BackendHealth["claude"]
	assert.False(t, stillTracked, "a successful probe closes the breaker")
	state = o.onTick(ctx, state)
	for _, is := range []domain.Issue{a, b, c} {
		if is.Identifier != probe {
			assert.Contains(t, state.Running, is.ID, "the held issues dispatch once the breaker is closed")
		}
	}
	cancel()
}

func TestBackendHealth_PersistRoundtrip(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := breakerIssue("1")
	path := filepath.Join(t.TempDir(), "backend_health.json")
	o, _ := breakerOrchestrator(t, config.BackendFallbackConfig{}, a)
	o.SetBackendHealthFile(path)
	reset := time.Now().Add(time.Hour).Truncate(time.Second)
	state := limitExit(ctx, o, NewState(o.cfg), a, "", "claude", 0, TerminalRateLimited, quotaLimit(reset))
	require.Contains(t, state.BackendHealth, "claude")

	data, err := os.ReadFile(path)
	require.NoError(t, err, "the breaker is persisted when it opens")
	var probe map[string]any
	require.NoError(t, json.Unmarshal(data, &probe))
	assert.EqualValues(t, 1, probe["version"])

	// Restart: a fresh orchestrator reloads the breaker and still refuses.
	o2, _ := breakerOrchestrator(t, config.BackendFallbackConfig{}, a)
	o2.SetBackendHealthFile(path)
	loaded := o2.loadBackendHealthFromDisk(NewState(o2.cfg))
	got, ok := loaded.BackendHealth["claude"]
	require.True(t, ok, "the breaker survives the restart")
	assert.Equal(t, BackendStatusLimited, got.Status)
	assert.True(t, got.LimitedUntil.Equal(reset), "want %v got %v", reset, got.LimitedUntil)
	assert.True(t, got.ResetKnown)
	assert.Equal(t, string(agent.LimitKindQuota), got.Kind)
	assert.Equal(t, "five_hour", got.LimitType)
	assert.Empty(t, got.ProbeIssue, "no run survives a restart, so no probe reservation does")

	loaded = o2.dispatch(ctx, loaded, a, 0)
	assert.NotContains(t, loaded.Running, a.ID, "the reloaded breaker still gates dispatch")
	assert.Equal(t, IneligibleBackendLimited, IneligibleReason(a, loaded, o2.cfg))
	cancel()
}

func TestBackendHealth_UnknownResetUsesDefaultCooldown(t *testing.T) {
	for _, tc := range []struct {
		name string
		fb   config.BackendFallbackConfig
		want time.Duration
	}{
		{"no backend_fallback block", config.BackendFallbackConfig{}, 15 * time.Minute},
		{"configured cooldown", config.BackendFallbackConfig{DefaultCooldownMinutes: 20}, 20 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, _ := breakerOrchestrator(t, tc.fb)
			state := NewState(o.cfg)
			now := time.Now()
			require.True(t, o.recordBackendLimit(&state, "codex", "", "ENG-1", quotaLimit(time.Time{}), now))
			e := state.BackendHealth["codex"]
			assert.Equal(t, BackendStatusLimited, e.Status, "unknown reset is limited, never healthy")
			assert.False(t, e.ResetKnown)
			assert.WithinDuration(t, now.Add(tc.want), e.LimitedUntil, time.Second)
		})
	}
}

// TestBackendHealth_KeyedByWorkerHost: a limit seen on SSH host A does not
// limit local runs (different credentials).
func TestBackendHealth_KeyedByWorkerHost(t *testing.T) {
	o, _ := breakerOrchestrator(t, config.BackendFallbackConfig{})
	state := NewState(o.cfg)
	now := time.Now()
	require.True(t, o.recordBackendLimit(&state, "claude", "host-a", "ENG-1", quotaLimit(now.Add(time.Hour)), now))
	assert.Contains(t, state.BackendHealth, "claude@host-a")
	assert.NotContains(t, state.BackendHealth, "claude")
}

func TestBackendHealth_RepeatedAPIRetryOpensBreaker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, b, c := breakerIssue("1"), breakerIssue("2"), breakerIssue("3")
	o, _ := breakerOrchestrator(t, config.BackendFallbackConfig{}, a, b, c)
	state := NewState(o.cfg)
	attempt := 0
	state.Running[a.ID] = &RunEntry{Issue: a, Backend: "claude", RetryAttempt: &attempt}
	state.Claimed[a.ID] = struct{}{}
	retry := func(st State) State {
		return o.handleEvent(ctx, st, OrchestratorEvent{
			Type: EventWorkerUpdate, IssueID: a.ID, RunEntry: &RunEntry{},
			Limit: &agent.LimitSignal{Kind: agent.LimitKindThrottle, Source: agent.LimitSourceAPIRetry,
				ErrorCategory: "rate_limit", HTTPStatus: 429, RetryAfter: 30 * time.Second},
		})
	}
	state = retry(state)
	state = retry(state)
	e := state.BackendHealth["claude"]
	assert.Equal(t, BackendStatusWarning, e.Status, "a couple of api_retry events only warn")
	state = o.dispatch(ctx, state, b, 0)
	require.Contains(t, state.Running, b.ID, "a warning does not gate dispatch")

	state = retry(state)
	e = state.BackendHealth["claude"]
	assert.Equal(t, BackendStatusLimited, e.Status, "repeated api_retry rate limits open the breaker")
	assert.Equal(t, string(agent.LimitKindThrottle), e.Kind)
	assert.True(t, e.LimitedUntil.After(time.Now().Add(4*time.Minute)))
	state = o.dispatch(ctx, state, c, 0)
	assert.NotContains(t, state.Running, c.ID)
	assert.Equal(t, IneligibleBackendLimited, IneligibleReason(c, state, o.cfg))
	cancel()
}

// progressRunner reports the same api_retry throttle twice mid-turn (the
// second report carries no new signal), then fails the turn.
type progressRunner struct{}

func (progressRunner) RunTurn(_ context.Context, _ agent.Logger, onProgress func(agent.TurnResult), _ *string, _, _, _, _, _ string, _, _ int, _ agent.PermissionMode) (agent.TurnResult, error) {
	sig := &agent.LimitSignal{Kind: agent.LimitKindThrottle, Source: agent.LimitSourceAPIRetry, ErrorCategory: "overloaded", HTTPStatus: 529}
	onProgress(agent.TurnResult{LastLimit: sig})
	onProgress(agent.TurnResult{LastLimit: sig, LastText: "still going"})
	return agent.TurnResult{Failed: true, FailureText: "boom", InputTokens: 3}, nil
}

func TestWorker_ForwardsAPIRetryThrottleMidTurn(t *testing.T) {
	cfg := automationBaseCfg()
	cfg.Agent.MaxTurns = 1
	issue := breakerIssue("1")
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	o := New(cfg, mt, progressRunner{}, nil)
	o.events = make(chan OrchestratorEvent, 64)
	o.workersWg.Add(issue.Identifier)
	o.runWorker(t.Context(), issue, 0, "", "claude", "claude", "", true, nil, nil, nil)
	forwarded := 0
	for len(o.events) > 0 {
		ev := <-o.events
		if ev.Type == EventWorkerUpdate && ev.Limit != nil {
			forwarded++
			assert.Equal(t, agent.LimitSourceAPIRetry, ev.Limit.Source)
		}
	}
	assert.Equal(t, 1, forwarded, "each new api_retry signal is forwarded once, mid-turn")
}

func TestBackendHealth_QueuedAutomationHeldNotDropped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := breakerIssue("1")
	o, _ := breakerOrchestrator(t, config.BackendFallbackConfig{}, a)
	state := NewState(o.cfg)
	now := time.Now()
	o.recordBackendLimit(&state, "claude", "", "ENG-9", quotaLimit(now.Add(time.Hour)), now)
	assert.True(t, IsQueueableAutomationReason(IneligibleBackendLimited))

	accepted := o.dispatchOrQueueAutomation(ctx, &state, a, AutomationDispatch{
		AutomationID: "nightly", ProfileName: "solo",
		Trigger: AutomationTriggerContext{Type: config.AutomationTriggerCron, FiredAt: now},
	}, now)
	assert.True(t, accepted, "held, not dropped")
	assert.NotContains(t, state.Running, a.ID)
	require.Len(t, state.AutomationQueue, 1)
	for _, e := range state.AutomationQueue {
		assert.Equal(t, AutomationQueueReasonBackendLimited, e.Reason)
	}

	// The queue drains once the breaker closes.
	delete(state.BackendHealth, "claude")
	o.drainAutomationQueue(ctx, &state, time.Now())
	assert.Contains(t, state.Running, a.ID)
	assert.Empty(t, state.AutomationQueue)
	cancel()
}

func TestBackendHealth_PendingInputResumeWaits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := breakerIssue("1")
	o, _ := breakerOrchestrator(t, fallbackOn(), a)
	state := NewState(o.cfg)
	now := time.Now()
	o.recordBackendLimit(&state, "claude", "", "ENG-9", quotaLimit(now.Add(time.Hour)), now)
	state.PendingInputResumes[a.Identifier] = &PendingInputResumeEntry{
		IssueID: a.ID, Identifier: a.Identifier, SessionID: "sess-1", Backend: "claude",
		Command: "claude --model opus", UserMessage: "yes",
	}
	state = o.processPendingInputResumes(ctx, state, now)
	assert.NotContains(t, state.Running, a.ID, "a claude session cannot resume on a limited claude")
	assert.Contains(t, state.PendingInputResumes, a.Identifier, "the reply is kept for later")
	assert.Contains(t, state.BackendLimitedHolds, a.Identifier)

	delete(state.BackendHealth, "claude")
	state = o.processPendingInputResumes(ctx, state, time.Now())
	require.Contains(t, state.Running, a.ID)
	assert.Equal(t, "claude", state.Running[a.ID].Backend, "the session resumes on its own backend")
	cancel()
}

func TestBackendHealth_HeldRetryKeepsAttempt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := breakerIssue("1")
	o, _ := breakerOrchestrator(t, config.BackendFallbackConfig{}, a)
	state := NewState(o.cfg)
	now := time.Now()
	reset := now.Add(time.Hour)
	o.recordBackendLimit(&state, "claude", "", "ENG-9", quotaLimit(reset), now)
	state = ScheduleRetry(state, a.ID, 3, a.Identifier, "boom", now.Add(-time.Minute), 0)

	state = o.fireRetries(ctx, state, now)
	assert.NotContains(t, state.Running, a.ID)
	require.Contains(t, state.RetryAttempts, a.ID, "the retry is kept, not lost")
	assert.Equal(t, 3, state.RetryAttempts[a.ID].Attempt, "a hold consumes no retry")
	assert.False(t, state.RetryAttempts[a.ID].DueAt.Before(reset.Add(-time.Second)), "due when the breaker admits it")
	assert.Contains(t, state.Claimed, a.ID)
	cancel()
}

func TestBackendHealth_ProbeReservationReleasedWhenProbeGone(t *testing.T) {
	o, _ := breakerOrchestrator(t, config.BackendFallbackConfig{})
	state := NewState(o.cfg)
	now := time.Now()
	state.BackendHealth["claude"] = BackendHealthEntry{Backend: "claude", Status: BackendStatusProbing, ProbeIssue: "ENG-7"}
	advanceBackendHealth(&state, now)
	assert.Empty(t, state.BackendHealth["claude"].ProbeIssue, "a probe that is neither running nor retrying frees the slot")
}
