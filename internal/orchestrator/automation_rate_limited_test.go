package orchestrator

import (
	"context"
	"errors"
	"os"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// IsRateLimitFailure must classify common vendor 429 / quota error messages
// and ignore unrelated errors so a generic crash never accidentally swaps
// the agent.
func TestIsRateLimitFailure_Classifier(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want bool
	}{
		{"empty", "", false},
		{"generic crash", "panic: runtime error", false},
		{"compile error", "syntax error near unexpected token", false},
		{"anthropic 429 body", "Error: HTTP 429: rate_limit_exceeded", true},
		{"openai quota", "RateLimitError: You exceeded your current quota", true},
		{"plain rate limit phrase", "rate limit reached, please retry later", true},
		{"too many requests", "Too Many Requests", true},
		{"case-insensitive", "RATE_LIMIT_EXCEEDED on us-central-1", true},
		// v0.2.0 todolist5 B8.a — Claude Max / Pro / Codex phrasings that
		// don't share a substring with the older default patterns.
		{"claude max quota", "You're out of extra usage · resets 10pm (Asia/Jerusalem)", true},
		{"claude pro tier limit", "You've reached the limit for your current Claude usage tier", true},
		{"codex out of credits", "Error: account is out of credits", true},
		{"reset hint alone", "rate window resets at 22:00", true},
		// Generic 5xx and crash messages still classify as non-rate-limit
		// so we don't false-positive into auto-switching.
		{"generic 503", "503 service unavailable", false},
		{"connection reset", "connection reset by peer", false},
		{"undefined symbol", "compile failed: undefined symbol foo", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsRateLimitFailure(tc.msg))
		})
	}
}

// Gap §5.1 — operator-configurable error-pattern list. Empty fallback uses
// the default list; non-empty replaces it; case-insensitive substring match.
func TestIsRateLimitFailureWithPatterns_OperatorOverride(t *testing.T) {
	// Empty patterns → defaults still hit on "rate_limit_exceeded".
	assert.True(t, IsRateLimitFailureWithPatterns("Error: rate_limit_exceeded", nil))
	// Custom list rejects the default phrasing if not included.
	assert.False(t, IsRateLimitFailureWithPatterns(
		"Error: rate_limit_exceeded",
		[]string{"my-vendor-throttle"},
	))
	// Custom list catches a vendor-specific shape.
	assert.True(t, IsRateLimitFailureWithPatterns(
		"upstream returned ANTHROPIC-OVERLOAD-503",
		[]string{"anthropic-overload"},
	))
	// Empty strings inside the list are skipped (defensive).
	assert.True(t, IsRateLimitFailureWithPatterns(
		"hit my-throttle",
		[]string{"", "my-throttle", ""},
	))
}

// SetRateLimitedAutomations / snapRateLimitedAutomations under -race: same
// invariant as the input-required and pr_opened registries. Concurrent writers
// and readers must not race on the slice header.
func TestRateLimitedAutomations_RaceSafe(t *testing.T) {
	o := &Orchestrator{cfg: &config.Config{}}
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			for range 100 {
				o.SetRateLimitedAutomations([]RateLimitedAutomation{
					{ID: "rl", ProfileName: "fallback", SwitchToProfile: "claude-fallback"},
				})
				_ = o.snapRateLimitedAutomations()
			}
		})
	}
	wg.Wait()
	assert.Len(t, o.snapRateLimitedAutomations(), 1)
}

// allowRateLimitSwitch must enforce the rolling-window cap. Once cap is hit,
// further calls must return false until older stamps fall out of the window.
func TestAllowRateLimitSwitch_RollingWindowCap(t *testing.T) {
	o := &Orchestrator{cfg: &config.Config{}}
	o.cfg.Agent.MaxSwitchesPerIssuePerWindow = 2
	o.cfg.Agent.SwitchWindowHours = 6

	now := time.Now()
	require.True(t, o.allowRateLimitSwitch("issue-1", now), "first switch within cap")
	o.recordRateLimitSwitch("issue-1", now)
	require.True(t, o.allowRateLimitSwitch("issue-1", now), "second switch within cap")
	o.recordRateLimitSwitch("issue-1", now)
	assert.False(t, o.allowRateLimitSwitch("issue-1", now),
		"third switch must be rejected once cap is reached within the window")

	// Advance past the window — old stamps fall off, switch becomes available.
	future := now.Add(7 * time.Hour)
	assert.True(t, o.allowRateLimitSwitch("issue-1", future),
		"after the window, the cap should reset")
}

// MaxSwitchesPerIssuePerWindow == 0 means "unlimited" — operator opt-out.
func TestAllowRateLimitSwitch_ZeroMeansUnlimited(t *testing.T) {
	o := &Orchestrator{cfg: &config.Config{}}
	o.cfg.Agent.MaxSwitchesPerIssuePerWindow = 0
	now := time.Now()
	for range 100 {
		require.True(t, o.allowRateLimitSwitch("issue-1", now))
		o.recordRateLimitSwitch("issue-1", now)
	}
}

// Cooldown must mute a per-(issue, profile) tuple until the deadline passes.
// This prevents thrash when an operator's claude profile and codex profile
// are both throttled at the same time.
func TestRateLimitCooldown_MutesPerProfileTuple(t *testing.T) {
	o := &Orchestrator{cfg: &config.Config{}}
	now := time.Now()
	o.setRateLimitCooldown("issue-1|claude-coder", now.Add(30*time.Minute))

	until, muted := o.rateLimitCooldownUntil("issue-1|claude-coder")
	require.True(t, muted)
	assert.True(t, now.Before(until), "muted until 30min ahead")

	// Different (issue, profile) tuple — not muted.
	_, otherMuted := o.rateLimitCooldownUntil("issue-1|codex-coder")
	assert.False(t, otherMuted, "cooldown is per-(issue, profile), not per-issue")
}

// dispatchMatchingRateLimitedAutomations runs inside the event loop, so it must
// not send to o.events. When capacity is unavailable, it should enqueue directly
// while preserving the rate-limit-specific trigger context fields.
func TestDispatchMatchingRateLimitedAutomations_PopulatesTriggerContext(t *testing.T) {
	o := &Orchestrator{
		cfg: &config.Config{},
	}
	o.cfg.Agent.MaxSwitchesPerIssuePerWindow = 5
	o.cfg.Agent.SwitchWindowHours = 6
	o.cfg.Agent.Profiles = map[string]config.AgentProfile{"codex-coder": {}}
	o.SetRateLimitedAutomations([]RateLimitedAutomation{
		{
			ID:              "switch-when-claude-throttled",
			ProfileName:     "fallback",
			SwitchToProfile: "codex-coder",
			SwitchToBackend: "codex",
			AutoResume:      true,
			Cooldown:        30 * time.Minute,
		},
	})

	state := NewState(o.cfg)
	state.Running["busy"] = &RunEntry{Issue: domain.Issue{ID: "busy", Identifier: "BUSY-1"}}
	state.IssueProfiles["ENG-1"] = "claude-coder"
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "Rate limited", State: "In Progress"}
	queued := o.dispatchMatchingRateLimitedAutomations(
		context.Background(), &state, issue, time.Now(),
		"claude-coder", "claude", "rate_limit_exceeded", 5,
		180_000, 22_000,
	)

	require.Equal(t, 1, queued, "rule must enqueue exactly one recovery")
	require.Len(t, state.AutomationQueue, 1)
	entry := sortedAutomationQueue(state)[0]
	assert.Equal(t, "codex-coder", entry.ProfileName)
	assert.True(t, entry.UseIssueLifecycle)
	assert.Equal(t, config.AutomationTriggerRateLimited, entry.Trigger.Type)
	assert.Equal(t, "claude-coder", entry.Trigger.FailedProfile)
	assert.Equal(t, "claude", entry.Trigger.FailedBackend)
	assert.Equal(t, 180_000, entry.Trigger.PromptTokensTotal)
	assert.Equal(t, 22_000, entry.Trigger.CompletionTokensTotal)
	assert.Equal(t, "codex-coder", entry.Trigger.SwitchedToProfile)
	assert.Equal(t, "codex", entry.Trigger.SwitchedToBackend)
	assert.True(t, entry.AutoResume)
}

func TestDispatchMatchingRateLimitedAutomations_InvalidProfileDoesNotConsumeCapCooldownOrOverride(t *testing.T) {
	mt := tracker.NewMemoryTracker([]domain.Issue{{ID: "id1", Identifier: "ENG-1"}}, []string{"In Progress"}, []string{"Done"})
	o := &Orchestrator{
		cfg:     &config.Config{},
		tracker: mt,
	}
	o.cfg.Agent.MaxSwitchesPerIssuePerWindow = 1
	o.cfg.Agent.SwitchWindowHours = 6
	o.SetRateLimitedAutomations([]RateLimitedAutomation{
		{
			ID:              "auto",
			ProfileName:     "fallback",
			SwitchToProfile: "codex-coder",
			SwitchToBackend: "codex",
			AutoResume:      true,
			Cooldown:        time.Hour,
		},
	})

	now := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	state := State{
		IssueProfiles:           map[string]string{},
		IssueBackends:           map[string]string{},
		AutoSwitchedIdentifiers: map[string]struct{}{},
		AutoSwitchedAt:          map[string]time.Time{},
	}
	queued := o.dispatchMatchingRateLimitedAutomations(
		context.Background(), &state,
		domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "Rate limited", State: "In Progress"}, now,
		"claude-coder", "claude", "rate_limit_exceeded", 5, 0, 0,
	)

	assert.Zero(t, queued, "invalid profile must not report an accepted recovery")
	assert.Empty(t, state.IssueProfiles, "failed acceptance must not switch profile")
	assert.Empty(t, state.IssueBackends, "failed acceptance must not switch backend")
	assert.Empty(t, state.AutoSwitchedIdentifiers, "failed acceptance must not mark auto-switch")
	assert.Empty(t, state.AutoSwitchedAt, "failed acceptance must not record switch timestamp")
	assert.True(t, o.allowRateLimitSwitch("id1", now), "failed acceptance must not burn switch cap")
	_, muted := o.rateLimitCooldownUntil("id1|claude-coder")
	assert.False(t, muted, "failed acceptance must not set cooldown")
	assert.Never(t, func() bool {
		return countMemoryTrackerComments(t, mt, "id1") > 0
	}, 100*time.Millisecond, 10*time.Millisecond, "failed acceptance must not post tracker comments")
}

// AutoResume + SwitchToProfile must override state.IssueProfiles so the
// next dispatch picks up the new profile. SwitchToBackend likewise
// overrides state.IssueBackends. AutoResume=false must NOT override.
func TestDispatchMatchingRateLimitedAutomations_AutoSwitchOverrides(t *testing.T) {
	o := &Orchestrator{
		cfg:    &config.Config{},
		events: make(chan OrchestratorEvent, 8),
	}
	o.cfg.Agent.MaxSwitchesPerIssuePerWindow = 5
	o.cfg.Agent.SwitchWindowHours = 6
	o.cfg.Agent.Profiles = map[string]config.AgentProfile{"codex-coder": {}}
	o.SetRateLimitedAutomations([]RateLimitedAutomation{
		{
			ID:              "auto",
			ProfileName:     "fallback",
			SwitchToProfile: "codex-coder",
			SwitchToBackend: "codex",
			AutoResume:      true,
		},
	})

	state := State{IssueProfiles: map[string]string{}, IssueBackends: map[string]string{}}
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "Rate limited", State: "In Progress"}
	o.dispatchMatchingRateLimitedAutomations(
		context.Background(), &state, issue, time.Now(),
		"claude-coder", "claude", "rate_limit_exceeded", 5, 1, 1,
	)

	assert.Equal(t, "codex-coder", state.IssueProfiles["ENG-1"], "auto-switch must override profile")
	assert.Equal(t, "codex", state.IssueBackends["ENG-1"], "auto-switch must override backend")
}

// Gap §1.3 — the auto-switch must mark the issue in
// AutoSwitchedIdentifiers so the succeeded-exit branch can later revert
// the override without disturbing operator-set overrides.
func TestDispatchMatchingRateLimitedAutomations_MarksAutoSwitched(t *testing.T) {
	o := &Orchestrator{
		cfg:    &config.Config{},
		events: make(chan OrchestratorEvent, 8),
	}
	o.cfg.Agent.MaxSwitchesPerIssuePerWindow = 5
	o.cfg.Agent.SwitchWindowHours = 6
	o.cfg.Agent.Profiles = map[string]config.AgentProfile{"codex-coder": {}}
	o.SetRateLimitedAutomations([]RateLimitedAutomation{
		{
			ID:              "auto",
			ProfileName:     "fallback",
			SwitchToProfile: "codex-coder",
			AutoResume:      true,
		},
	})

	state := State{IssueProfiles: map[string]string{}, IssueBackends: map[string]string{}, AutoSwitchedIdentifiers: map[string]struct{}{}}
	o.dispatchMatchingRateLimitedAutomations(
		context.Background(), &state,
		domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "Rate limited", State: "In Progress"}, time.Now(),
		"claude-coder", "claude", "rate_limit_exceeded", 5, 0, 0,
	)
	_, marked := state.AutoSwitchedIdentifiers["ENG-1"]
	assert.True(t, marked, "auto-switch must mark the identifier so the override can be reverted later")
}

func TestIssueProfileForDispatch_UsesPersistedAutoSwitchState(t *testing.T) {
	cfg := automationBaseCfg()
	state := NewState(cfg)
	state.IssueProfiles["ENG-1"] = "codex-coder"
	state.IssueBackends["ENG-1"] = "codex"

	o := &Orchestrator{
		cfg:           cfg,
		issueProfiles: map[string]string{},
		issueBackends: map[string]string{},
	}

	assert.Equal(t, "codex-coder", o.issueProfileForDispatch(state, "ENG-1"))
	assert.Equal(t, "codex", o.issueBackendForDispatch(state, "ENG-1"))

	o.issueProfiles["ENG-1"] = "operator-pinned"
	o.issueBackends["ENG-1"] = "claude"
	assert.Equal(t, "operator-pinned", o.issueProfileForDispatch(state, "ENG-1"),
		"operator overrides must take precedence over persisted auto-switch state")
	assert.Equal(t, "claude", o.issueBackendForDispatch(state, "ENG-1"))
}

func TestEventDispatchAutomation_RateLimitedAutoResumeClearsAutoSwitchPause(t *testing.T) {
	cfg := automationBaseCfg()
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"fallback": {Command: "codex"},
	}
	state := NewState(cfg)
	state.PausedIdentifiers["ENG-1"] = "id1"
	state.AutoSwitchedIdentifiers["ENG-1"] = struct{}{}

	o := &Orchestrator{cfg: cfg, DryRun: true}
	issue := automationIssue("In Progress")
	ev := OrchestratorEvent{
		Type:  EventDispatchAutomation,
		Issue: &issue,
		Automation: &AutomationDispatch{
			AutomationID: "rate-limit-switch",
			ProfileName:  "fallback",
			AutoResume:   true,
			Trigger: AutomationTriggerContext{
				Type:              config.AutomationTriggerRateLimited,
				SwitchedToProfile: "fallback",
			},
		},
	}

	out := o.handleEvent(t.Context(), state, ev)
	assert.NotContains(t, out.PausedIdentifiers, "ENG-1",
		"rate-limited auto_resume dispatch must clear the pause created by retry exhaustion")
	assert.Contains(t, out.Claimed, "id1", "dry-run dispatch should claim the issue after the pause is cleared")
}

func TestEventDispatchAutomation_NonAutoSwitchPauseStillBlocks(t *testing.T) {
	cfg := automationBaseCfg()
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"fallback": {Command: "codex"},
	}
	state := NewState(cfg)
	state.PausedIdentifiers["ENG-1"] = "id1"

	o := &Orchestrator{cfg: cfg, DryRun: true}
	issue := automationIssue("In Progress")
	ev := OrchestratorEvent{
		Type:  EventDispatchAutomation,
		Issue: &issue,
		Automation: &AutomationDispatch{
			AutomationID: "manual-review",
			ProfileName:  "fallback",
			Trigger: AutomationTriggerContext{
				Type: config.AutomationTriggerRunFailed,
			},
		},
	}

	out := o.handleEvent(t.Context(), state, ev)
	assert.Contains(t, out.PausedIdentifiers, "ENG-1",
		"manual pauses must remain an automation dispatch guard")
	assert.NotContains(t, out.Claimed, "id1", "paused issue should not dispatch")
}

func TestWorkerExitedRateLimitedAutoSwitchSkipsFailedStateAndQueuesSwitchProfile(t *testing.T) {
	cfg := automationBaseCfg()
	cfg.Agent.MaxRetries = 1
	cfg.Agent.MaxSwitchesPerIssuePerWindow = 5
	cfg.Agent.SwitchWindowHours = 6
	cfg.Tracker.FailedState = "Failed"
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"default":  {Command: "claude"},
		"fallback": {Command: "codex"},
	}
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	o := &Orchestrator{
		cfg:           cfg,
		tracker:       mt,
		runner:        &agenttest.FakeRunner{Stall: true},
		events:        make(chan OrchestratorEvent, 2),
		workerCancels: map[string]context.CancelFunc{},
	}
	o.SetRateLimitedAutomations([]RateLimitedAutomation{{
		ID:              "rate-limit-switch",
		ProfileName:     "default",
		SwitchToProfile: "fallback",
		SwitchToBackend: "codex",
		AutoResume:      true,
	}})

	issue := automationIssue("In Progress")
	attempt := 1
	run := &RunEntry{
		Issue:        issue,
		Backend:      "claude",
		ProfileName:  "default",
		RetryAttempt: &attempt,
		InputTokens:  100,
		OutputTokens: 25,
	}
	state := NewState(cfg)
	state.Running[issue.ID] = run
	state.Claimed[issue.ID] = struct{}{}

	out := o.handleEvent(t.Context(), state, OrchestratorEvent{
		Type:     EventWorkerExited,
		IssueID:  issue.ID,
		RunEntry: run,
		Error:    errors.New("rate_limit_exceeded"),
	})

	assert.NotContains(t, out.PausedIdentifiers, issue.Identifier)
	assert.NotContains(t, out.DiscardingIdentifiers, issue.Identifier,
		"rate_limited recovery must run before failed_state discard marks the issue ineligible")
	assert.Contains(t, out.Claimed, issue.ID)
	assert.Equal(t, "fallback", out.IssueProfiles[issue.Identifier])
	assert.Equal(t, "codex", out.IssueBackends[issue.Identifier])
	require.Contains(t, out.Running, issue.ID)
	recovery := out.Running[issue.ID]
	assert.Equal(t, "rate-limit-switch", recovery.AutomationID)
	assert.Equal(t, config.AutomationTriggerRateLimited, recovery.TriggerType)
	assert.Equal(t, "fallback", recovery.ProfileName)
	assert.Equal(t, "codex", recovery.Backend)
	assert.Equal(t, "worker", recovery.Kind)
}

func TestRateLimitedAutoSwitchRunUsesNormalIssueLifecycle(t *testing.T) {
	cfg := automationBaseCfg()
	cfg.Tracker.CompletionState = "Done"
	cfg.Agent.Command = "claude"
	cfg.Agent.MaxTurns = 1
	cfg.Agent.ReadTimeoutMs = 1_000
	cfg.Agent.TurnTimeoutMs = 1_000
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"fallback": {Command: "codex"},
	}
	issue := automationIssue("In Progress")
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	o := &Orchestrator{
		cfg:           cfg,
		tracker:       mt,
		runner:        agenttest.SuccessRunner("session-rate-limit"),
		events:        make(chan OrchestratorEvent, 8),
		workerCancels: map[string]context.CancelFunc{},
	}
	state := NewState(cfg)

	o.startAutomationRun(t.Context(), &state, issue, time.Now(), AutomationDispatch{
		AutomationID:      "rate-limit-switch",
		ProfileName:       "fallback",
		Instructions:      "Continue the implementation under the fallback profile.",
		AutoResume:        true,
		UseIssueLifecycle: true,
		Trigger: AutomationTriggerContext{
			Type:              config.AutomationTriggerRateLimited,
			SwitchedToProfile: "fallback",
			SwitchedToBackend: "codex",
		},
	})

	entry := state.Running[issue.ID]
	require.NotNil(t, entry)
	assert.Equal(t, "worker", entry.Kind)
	assert.Equal(t, "rate-limit-switch", entry.AutomationID)
	assert.Equal(t, config.AutomationTriggerRateLimited, entry.TriggerType)

	require.Eventually(t, func() bool {
		got, err := mt.FetchIssueDetail(t.Context(), issue.ID)
		return err == nil && got.State == "Done"
	}, 2*time.Second, 20*time.Millisecond,
		"rate_limited auto-switch recovery must transition the issue like a normal worker")
}

func TestDispatchMatchingRateLimitedAutomations_NoOverrideWhenAutoResumeFalse(t *testing.T) {
	o := &Orchestrator{
		cfg:    &config.Config{},
		events: make(chan OrchestratorEvent, 8),
	}
	o.cfg.Agent.MaxSwitchesPerIssuePerWindow = 5
	o.cfg.Agent.SwitchWindowHours = 6
	o.SetRateLimitedAutomations([]RateLimitedAutomation{
		{
			ID:              "manual",
			ProfileName:     "fallback",
			SwitchToProfile: "codex-coder",
			AutoResume:      false,
		},
	})
	state := State{IssueProfiles: map[string]string{}}
	o.dispatchMatchingRateLimitedAutomations(
		context.Background(), &state,
		domain.Issue{ID: "id1", Identifier: "ENG-1"}, time.Now(),
		"claude-coder", "claude", "rate_limit", 5, 0, 0,
	)
	assert.Empty(t, state.IssueProfiles, "auto_resume=false must not silently override")
}

// Gap §5.3 — auto-switched overrides must round-trip through disk so a
// daemon restart re-applies them. Save the overrides, construct a fresh
// orchestrator pointing at the same file, load → state must match.
func TestAutoSwitchedOverrides_PersistRoundtrip(t *testing.T) {
	tmp := t.TempDir()
	path := tmp + "/auto_switched.json"

	// Step 1: a "writer" orchestrator persists three overrides.
	writer := &Orchestrator{cfg: &config.Config{}}
	writer.SetAutoSwitchedFile(path)
	autoSwitched := map[string]struct{}{
		"ENG-1": {},
		"ENG-2": {},
		"ENG-3": {},
	}
	profiles := map[string]string{
		"ENG-1": "codex-coder",
		"ENG-2": "claude-haiku",
		"ENG-3": "qa-fallback",
	}
	backends := map[string]string{
		"ENG-1": "codex",
		// ENG-2: no backend override
		"ENG-3": "claude",
	}
	switchedAt := map[string]time.Time{
		"ENG-1": time.Date(2026, 5, 7, 10, 0, 0, 0, time.UTC),
		"ENG-3": time.Date(2026, 5, 7, 11, 0, 0, 0, time.UTC),
	}
	writer.saveAutoSwitchedToDisk(autoSwitched, profiles, backends, switchedAt)

	// Step 2: a fresh "reader" orchestrator loads from the same file.
	reader := &Orchestrator{cfg: &config.Config{}}
	reader.SetAutoSwitchedFile(path)
	state := State{
		IssueProfiles:           map[string]string{},
		IssueBackends:           map[string]string{},
		AutoSwitchedIdentifiers: map[string]struct{}{},
		AutoSwitchedAt:          map[string]time.Time{},
	}
	state = reader.loadAutoSwitchedFromDisk(state)

	assert.Equal(t, "codex-coder", state.IssueProfiles["ENG-1"])
	assert.Equal(t, "claude-haiku", state.IssueProfiles["ENG-2"])
	assert.Equal(t, "qa-fallback", state.IssueProfiles["ENG-3"])
	assert.Equal(t, "codex", state.IssueBackends["ENG-1"])
	assert.Empty(t, state.IssueBackends["ENG-2"], "no backend override → not persisted")
	assert.Equal(t, "claude", state.IssueBackends["ENG-3"])
	for id := range autoSwitched {
		_, marked := state.AutoSwitchedIdentifiers[id]
		assert.True(t, marked, "AutoSwitchedIdentifiers must round-trip for %s", id)
	}
	assert.Equal(t, switchedAt["ENG-1"], state.AutoSwitchedAt["ENG-1"])
	_, eng2Stamped := state.AutoSwitchedAt["ENG-2"]
	assert.False(t, eng2Stamped, "missing switched_at should remain absent")
	assert.Equal(t, switchedAt["ENG-3"], state.AutoSwitchedAt["ENG-3"])
}

func TestAutoSwitchedOverrides_BackCompatMissingTimestampLoadsWithoutExpiryTimestamp(t *testing.T) {
	tmp := t.TempDir()
	path := tmp + "/auto_switched.json"
	require.NoError(t, os.WriteFile(path, []byte(`{"ENG-1":{"profile":"codex-coder","backend":"codex"}}`), 0o644))

	reader := &Orchestrator{cfg: &config.Config{}}
	reader.SetAutoSwitchedFile(path)
	state := State{
		IssueProfiles:           map[string]string{},
		IssueBackends:           map[string]string{},
		AutoSwitchedIdentifiers: map[string]struct{}{},
		AutoSwitchedAt:          map[string]time.Time{},
	}
	state = reader.loadAutoSwitchedFromDisk(state)

	assert.Equal(t, "codex-coder", state.IssueProfiles["ENG-1"])
	assert.Equal(t, "codex", state.IssueBackends["ENG-1"])
	_, marked := state.AutoSwitchedIdentifiers["ENG-1"]
	assert.True(t, marked)
	_, stamped := state.AutoSwitchedAt["ENG-1"]
	assert.False(t, stamped, "legacy records without switched_at must not get an artificial timestamp")
}

// Saving an empty map followed by loading must clear the on-disk state
// (the reader sees no overrides). Critical for the cleared-on-success path.
func TestAutoSwitchedOverrides_EmptyMapClearsFile(t *testing.T) {
	tmp := t.TempDir()
	path := tmp + "/auto_switched.json"
	o := &Orchestrator{cfg: &config.Config{}}
	o.SetAutoSwitchedFile(path)
	o.saveAutoSwitchedToDisk(
		map[string]struct{}{"ENG-1": {}},
		map[string]string{"ENG-1": "x"},
		nil,
		map[string]time.Time{"ENG-1": time.Now()},
	)
	o.saveAutoSwitchedToDisk(map[string]struct{}{}, map[string]string{}, nil, nil)

	state := State{
		IssueProfiles:           map[string]string{},
		IssueBackends:           map[string]string{},
		AutoSwitchedIdentifiers: map[string]struct{}{},
	}
	state = o.loadAutoSwitchedFromDisk(state)
	assert.Empty(t, state.AutoSwitchedIdentifiers)
	assert.Empty(t, state.IssueProfiles)
}

// Gap §6.2 — TTL-based revert: overrides whose AutoSwitchedAt is older
// than the configured TTL get cleared from IssueProfiles + IssueBackends
// + AutoSwitchedIdentifiers + AutoSwitchedAt. Newer overrides survive.
// Operator-set overrides (no AutoSwitchedAt entry) are untouched.
func TestRevertExpiredAutoSwitches_DropsAgedOverrides(t *testing.T) {
	now := time.Now()
	state := &State{
		IssueProfiles: map[string]string{
			"OLD-1":      "codex-coder",
			"NEW-1":      "codex-coder",
			"OPERATOR-1": "operator-pinned",
		},
		IssueBackends: map[string]string{
			"OLD-1": "codex",
			"NEW-1": "codex",
		},
		AutoSwitchedIdentifiers: map[string]struct{}{
			"OLD-1": {},
			"NEW-1": {},
		},
		AutoSwitchedAt: map[string]time.Time{
			"OLD-1": now.Add(-25 * time.Hour),
			"NEW-1": now.Add(-30 * time.Minute),
		},
	}

	reverted := RevertExpiredAutoSwitches(state, 24*time.Hour, now)
	assert.Equal(t, 1, reverted, "only the OLD-1 override should be reverted")

	_, oldKept := state.IssueProfiles["OLD-1"]
	_, newKept := state.IssueProfiles["NEW-1"]
	_, operKept := state.IssueProfiles["OPERATOR-1"]
	assert.False(t, oldKept, "expired auto-switch must be reverted")
	assert.True(t, newKept, "fresh auto-switch must survive")
	assert.True(t, operKept, "operator-set override (no AutoSwitchedAt entry) must survive")
}

func TestRevertExpiredAutoSwitches_NoOpWhenTTLZero(t *testing.T) {
	state := &State{
		AutoSwitchedAt: map[string]time.Time{"X": time.Now().Add(-100 * time.Hour)},
	}
	assert.Zero(t, RevertExpiredAutoSwitches(state, 0, time.Now()))
}

func TestRevertExpiredAutoSwitchesForTick_PersistsBeforeDispatch(t *testing.T) {
	tmp := t.TempDir()
	path := tmp + "/auto_switched.json"
	now := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	cfg := &config.Config{}
	cfg.Agent.SwitchRevertHours = 1
	o := &Orchestrator{cfg: cfg}
	o.SetAutoSwitchedFile(path)

	state := State{
		IssueProfiles: map[string]string{
			"ENG-1": "codex-coder",
		},
		IssueBackends: map[string]string{
			"ENG-1": "codex",
		},
		AutoSwitchedIdentifiers: map[string]struct{}{
			"ENG-1": {},
		},
		AutoSwitchedAt: map[string]time.Time{
			"ENG-1": now.Add(-2 * time.Hour),
		},
	}

	reverted := o.revertExpiredAutoSwitchesForTick(&state, now)
	require.Equal(t, 1, reverted)
	assert.Empty(t, state.IssueProfiles)
	assert.Empty(t, state.IssueBackends)
	assert.Empty(t, state.AutoSwitchedIdentifiers)
	assert.Empty(t, state.AutoSwitchedAt)

	reader := &Orchestrator{cfg: &config.Config{}}
	reader.SetAutoSwitchedFile(path)
	loaded := State{
		IssueProfiles:           map[string]string{},
		IssueBackends:           map[string]string{},
		AutoSwitchedIdentifiers: map[string]struct{}{},
		AutoSwitchedAt:          map[string]time.Time{},
	}
	loaded = reader.loadAutoSwitchedFromDisk(loaded)
	assert.Empty(t, loaded.IssueProfiles, "expired override must be removed from disk before later dispatch can reload it")
	assert.Empty(t, loaded.AutoSwitchedIdentifiers)
}

// PruneRateLimitedMaps must drop switchHistory entries older than 2*window
// and cooldown entries whose deadline has passed (gap §1.1, §1.2).
func TestPruneRateLimitedMaps_EvictsStale(t *testing.T) {
	o := &Orchestrator{cfg: &config.Config{}}
	o.cfg.Agent.SwitchWindowHours = 6
	now := time.Now()

	// Old switch (12h ago = 2*window) → should be pruned.
	o.recordRateLimitSwitch("issue-old", now.Add(-13*time.Hour))
	// Recent switch → should be kept.
	o.recordRateLimitSwitch("issue-recent", now.Add(-1*time.Hour))
	// Expired cooldown → should be pruned.
	o.setRateLimitCooldown("a|p", now.Add(-1*time.Hour))
	// Active cooldown → should be kept.
	o.setRateLimitCooldown("b|p", now.Add(1*time.Hour))

	o.PruneRateLimitedMaps(now)

	o.switchHistoryMu.Lock()
	_, oldKept := o.switchHistory["issue-old"]
	_, recentKept := o.switchHistory["issue-recent"]
	o.switchHistoryMu.Unlock()
	assert.False(t, oldKept, "stale switchHistory entry should be evicted")
	assert.True(t, recentKept, "recent switchHistory entry should survive")

	o.rateLimitCooldownMu.Lock()
	_, expiredKept := o.rateLimitCooldown["a|p"]
	_, activeKept := o.rateLimitCooldown["b|p"]
	o.rateLimitCooldownMu.Unlock()
	assert.False(t, expiredKept, "expired cooldown entry should be evicted")
	assert.True(t, activeKept, "active cooldown entry should survive")
}

func TestRateLimitCapExhaustedCommentDedupeUsesResetWindow(t *testing.T) {
	o := &Orchestrator{cfg: &config.Config{}}
	o.cfg.Agent.MaxSwitchesPerIssuePerWindow = 1
	o.cfg.Agent.SwitchWindowHours = 6
	now := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	o.recordRateLimitSwitch("id1", now.Add(-2*time.Hour))
	require.False(t, o.allowRateLimitSwitch("id1", now), "test setup must put issue at cap")

	assert.True(t, o.claimRateLimitCapComment("id1", now), "first cap comment should be claimed")
	o.rateLimitCapCommentMu.Lock()
	until := o.rateLimitCapCommentUntil["id1"]
	o.rateLimitCapCommentMu.Unlock()
	assert.Equal(t, now.Add(4*time.Hour), until, "dedupe should open when the oldest switch leaves the window")

	assert.False(t, o.claimRateLimitCapComment("id1", now.Add(time.Minute)),
		"repeat cap comments inside the same closed window should be suppressed")
	assert.True(t, o.claimRateLimitCapComment("id1", now.Add(4*time.Hour+time.Second)),
		"cap comments should be allowed again after the window opens")
}

func TestDispatchMatchingRateLimitedAutomations_DedupesCapExhaustedComment(t *testing.T) {
	mt := tracker.NewMemoryTracker([]domain.Issue{{ID: "id1", Identifier: "ENG-1"}}, []string{"In Progress"}, []string{"Done"})
	o := &Orchestrator{
		cfg:     &config.Config{},
		tracker: mt,
		events:  make(chan OrchestratorEvent, 8),
	}
	o.cfg.Agent.MaxSwitchesPerIssuePerWindow = 1
	o.cfg.Agent.SwitchWindowHours = 6
	o.SetRateLimitedAutomations([]RateLimitedAutomation{
		{
			ID:              "auto",
			ProfileName:     "fallback",
			SwitchToProfile: "codex-coder",
			AutoResume:      true,
		},
	})
	now := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	o.recordRateLimitSwitch("id1", now.Add(-time.Hour))
	state := State{IssueProfiles: map[string]string{}}
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", State: "In Progress"}

	queued := o.dispatchMatchingRateLimitedAutomations(
		context.Background(), &state, issue, now,
		"claude-coder", "claude", "rate_limit", 5, 0, 0,
	)
	assert.Zero(t, queued)
	require.Eventually(t, func() bool {
		return countMemoryTrackerComments(t, mt, "id1") == 1
	}, time.Second, 10*time.Millisecond)

	queued = o.dispatchMatchingRateLimitedAutomations(
		context.Background(), &state, issue, now.Add(time.Minute),
		"claude-coder", "claude", "rate_limit", 6, 0, 0,
	)
	assert.Zero(t, queued)
	assert.Never(t, func() bool {
		return countMemoryTrackerComments(t, mt, "id1") > 1
	}, 100*time.Millisecond, 10*time.Millisecond, "repeat cap hits inside the same window should not post duplicate comments")
}

func countMemoryTrackerComments(t *testing.T, mt *tracker.MemoryTracker, issueID string) int {
	t.Helper()
	issue, err := mt.FetchIssueDetail(t.Context(), issueID)
	require.NoError(t, err)
	return len(issue.Comments)
}

func TestSnapshotClonesAutoSwitchedMaps(t *testing.T) {
	o := &Orchestrator{}
	switchedAt := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	state := State{
		AutoSwitchedIdentifiers: map[string]struct{}{
			"ENG-1": {},
		},
		AutoSwitchedAt: map[string]time.Time{
			"ENG-1": switchedAt,
		},
	}

	o.storeSnap(state)
	delete(state.AutoSwitchedIdentifiers, "ENG-1")
	delete(state.AutoSwitchedAt, "ENG-1")
	snap := o.Snapshot()

	_, marked := snap.AutoSwitchedIdentifiers["ENG-1"]
	assert.True(t, marked, "snapshot must not alias State.AutoSwitchedIdentifiers")
	assert.Equal(t, switchedAt, snap.AutoSwitchedAt["ENG-1"], "snapshot must not alias State.AutoSwitchedAt")

	snap.AutoSwitchedIdentifiers["ENG-2"] = struct{}{}
	snap.AutoSwitchedAt["ENG-2"] = switchedAt.Add(time.Hour)
	snap2 := o.Snapshot()
	_, leakedIdentifier := snap2.AutoSwitchedIdentifiers["ENG-2"]
	_, leakedTime := snap2.AutoSwitchedAt["ENG-2"]
	assert.False(t, leakedIdentifier, "mutating returned snapshot must not alter stored AutoSwitchedIdentifiers")
	assert.False(t, leakedTime, "mutating returned snapshot must not alter stored AutoSwitchedAt")
}

// Rules whose IdentifierRegex doesn't match the issue must not fire.
func TestDispatchMatchingRateLimitedAutomations_IdentifierFilter(t *testing.T) {
	o := &Orchestrator{
		cfg:    &config.Config{},
		events: make(chan OrchestratorEvent, 8),
	}
	o.cfg.Agent.MaxSwitchesPerIssuePerWindow = 5
	o.cfg.Agent.SwitchWindowHours = 6
	o.SetRateLimitedAutomations([]RateLimitedAutomation{
		{
			ID:              "eng-only",
			ProfileName:     "fallback",
			SwitchToProfile: "codex-coder",
			IdentifierRegex: regexp.MustCompile(`^ENG-`),
		},
	})

	state := State{IssueProfiles: map[string]string{}}
	o.dispatchMatchingRateLimitedAutomations(
		context.Background(), &state,
		domain.Issue{Identifier: "BUG-1"}, time.Now(),
		"claude-coder", "claude", "rate_limit", 5, 0, 0,
	)
	assert.Empty(t, o.events, "BUG-1 must not match an ENG-only rule")
}
