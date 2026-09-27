package orchestrator

// M3-close fix round: breaker caps (V1/BH-M3-3), the operator clear action,
// remote zone-less resets (BH-M3-4), the forward-after-send rule (BH-M3-5),
// orphaned breakers (BH-M3-6) and quarantined ledgers (BH-M3-7/V3).

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
)

func TestBackendHealth_LimitedUntilIsCapped(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	far := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		sig  *agent.LimitSignal
		max  time.Duration
	}{
		{"structured five_hour", &agent.LimitSignal{Kind: agent.LimitKindQuota, Source: agent.LimitSourceRateLimitEvent, LimitType: "five_hour", ResetsAt: far}, 5*time.Hour + 15*time.Minute},
		{"structured seven_day", &agent.LimitSignal{Kind: agent.LimitKindQuota, Source: agent.LimitSourceRateLimitEvent, LimitType: "seven_day_opus", ResetsAt: far}, 7*24*time.Hour + time.Hour},
		{"structured unknown window", &agent.LimitSignal{Kind: agent.LimitKindQuota, Source: agent.LimitSourceRateLimitEvent, ResetsAt: far}, 24 * time.Hour},
		{"text reset", &agent.LimitSignal{Kind: agent.LimitKindQuota, Source: agent.LimitSourceText, ResetsAt: now.Add(48 * time.Hour)}, 6 * time.Hour},
		{"result text reset", &agent.LimitSignal{Kind: agent.LimitKindQuota, Source: agent.LimitSourceResult, ResetsAt: far}, 6 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, _ := breakerOrchestrator(t, config.BackendFallbackConfig{})
			state := NewState(o.cfg)
			require.True(t, o.recordBackendLimit(&state, "claude", "", "ENG-1", tc.sig, now))
			e := state.BackendHealth["claude"]
			assert.False(t, e.LimitedUntil.After(now.Add(tc.max)), "until %v exceeds the %v cap", e.LimitedUntil, tc.max)
			assert.True(t, e.LimitedUntil.After(now), "still limited")
		})
	}

	t.Run("a legitimate seven_day reset in 5 days is kept", func(t *testing.T) {
		o, _ := breakerOrchestrator(t, config.BackendFallbackConfig{})
		state := NewState(o.cfg)
		reset := now.Add(5 * 24 * time.Hour)
		o.recordBackendLimit(&state, "claude", "", "ENG-1", &agent.LimitSignal{Kind: agent.LimitKindQuota, Source: agent.LimitSourceRateLimitEvent, LimitType: "seven_day", ResetsAt: reset}, now)
		assert.True(t, state.BackendHealth["claude"].LimitedUntil.Equal(reset))
		assert.True(t, state.BackendHealth["claude"].ResetKnown)
	})

	t.Run("api_retry delay of 1e12 ms", func(t *testing.T) {
		o, _ := breakerOrchestrator(t, config.BackendFallbackConfig{})
		state := NewState(o.cfg)
		sig := &agent.LimitSignal{Kind: agent.LimitKindThrottle, Source: agent.LimitSourceAPIRetry, ErrorCategory: "rate_limit", RetryAfter: time.Duration(1e12) * time.Millisecond}
		for range 3 {
			o.recordBackendThrottle(&state, "claude", "", "ENG-1", sig, now)
		}
		e := state.BackendHealth["claude"]
		require.Equal(t, BackendStatusLimited, e.Status)
		assert.False(t, e.LimitedUntil.After(now.Add(time.Hour)), "throttle cooldown capped at 1h, got %v", e.LimitedUntil.Sub(now))
	})
}

func TestBackendHealth_LoadClampsPersistedFarReset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backend_health.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"version":1,"backends":{"claude":{"backend":"claude","status":"limited","kind":"quota","source":"rate_limit_event","limited_until":"2100-01-01T00:00:00Z","reset_known":true,"since":"2026-09-27T00:00:00Z"}}}`), 0o644))
	o, _ := breakerOrchestrator(t, config.BackendFallbackConfig{})
	o.SetBackendHealthFile(path)
	state := o.loadBackendHealthFromDisk(NewState(o.cfg))
	e, ok := state.BackendHealth["claude"]
	require.True(t, ok)
	assert.False(t, e.LimitedUntil.After(time.Now().Add(24*time.Hour+time.Minute)), "an already persisted far reset heals on load: %v", e.LimitedUntil)
}

func TestBackendHealth_HoldRetryDelayCapped(t *testing.T) {
	now := time.Now()
	hold := BackendHold{Key: "claude", Until: now.Add(7 * 24 * time.Hour)}
	d := time.Duration(holdRetryDelay(State{PollIntervalMs: 1000}, hold, now)) * time.Millisecond
	assert.LessOrEqual(t, d, rateLimitVendorDelayCap, "a held retry never sleeps past the 6h retry cap")
}

func TestClearBackendBreaker_EventMediated(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	path := filepath.Join(t.TempDir(), "backend_health.json")
	o, _ := breakerOrchestrator(t, config.BackendFallbackConfig{})
	o.SetBackendHealthFile(path)
	o.events = make(chan OrchestratorEvent, 4)
	state := NewState(o.cfg)
	now := time.Now()
	o.recordBackendLimit(&state, "claude", "h1", "ENG-9", quotaLimit(now.Add(time.Hour)), now)
	o.recordBackendLimit(&state, "codex", "", "ENG-9", quotaLimit(now.Add(time.Hour)), now)
	setBackendHold(&state, "ENG-1", BackendHold{Key: "claude@h1", Until: now.Add(time.Hour)})

	require.True(t, o.ClearBackendBreaker("claude", "h1"), "queued on the event loop")
	_, stillThere := state.BackendHealth["claude@h1"]
	require.True(t, stillThere, "the caller never mutates State")
	ev := <-o.events
	require.Equal(t, EventClearBackendBreaker, ev.Type)
	state = o.handleEvent(ctx, state, ev)
	assert.NotContains(t, state.BackendHealth, "claude@h1")
	assert.Contains(t, state.BackendHealth, "codex", "only the named breaker is cleared")
	assert.NotContains(t, state.BackendLimitedHolds, "ENG-1")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "claude@h1", "the clear is persisted")

	o.events = make(chan OrchestratorEvent) // unbuffered, nobody reading: full
	assert.False(t, o.ClearBackendBreaker("codex", ""), "a full queue reports false (503), never blocks")
}

func TestLimitForHost_RemoteAssumedZoneBecomesUnknown(t *testing.T) {
	reset := time.Now().Add(2 * time.Hour)
	assumed := &agent.LimitSignal{Kind: agent.LimitKindQuota, Source: agent.LimitSourceText, ResetsAt: reset, ResetZoneAssumed: true}
	got := limitForHost(assumed, "build-1")
	require.NotNil(t, got)
	assert.True(t, got.ResetsAt.IsZero(), "a zone-less text reset from a remote host is unknown")
	assert.False(t, assumed.ResetsAt.IsZero(), "the input is not mutated")
	assert.Equal(t, reset, limitForHost(assumed, "").ResetsAt, "local runs keep it (the daemon's zone is the host's)")
	explicit := &agent.LimitSignal{Kind: agent.LimitKindQuota, Source: agent.LimitSourceText, ResetsAt: reset}
	assert.Equal(t, reset, limitForHost(explicit, "build-1").ResetsAt, "an explicit zone is kept")
	assert.Nil(t, limitForHost(nil, "build-1"))
}

func TestLimitForwarder_ResendsAfterDroppedSend(t *testing.T) {
	var f limitForwarder
	sig := &agent.LimitSignal{Kind: agent.LimitKindThrottle, Source: agent.LimitSourceAPIRetry}
	require.NotNil(t, f.candidate(sig))
	// The send was dropped (channel full): not marked sent, offered again.
	require.NotNil(t, f.candidate(sig), "a dropped forward is retried on the next progress")
	f.sent(sig)
	assert.Nil(t, f.candidate(sig), "once delivered it is not forwarded twice")
	assert.Nil(t, f.candidate(&agent.LimitSignal{Kind: agent.LimitKindQuota}), "only api_retry throttles are forwarded")
}

func TestBackendHealth_PrunesOrphanBreakers(t *testing.T) {
	o, _ := breakerOrchestrator(t, config.BackendFallbackConfig{})
	o.cfg.Agent.SSHHosts = []string{"h1"}
	// Only claude runs now: drop every codex profile.
	for name, p := range o.cfg.Agent.Profiles {
		if config.ProfileRunsBackend(p, o.cfg.Agent.Command) == "codex" {
			delete(o.cfg.Agent.Profiles, name)
		}
	}
	state := NewState(o.cfg)
	now := time.Now()
	state.BackendHealth["claude@gone"] = BackendHealthEntry{Backend: "claude", Host: "gone", Status: BackendStatusLimited, LimitedUntil: now.Add(time.Hour)}
	state.BackendHealth["codex"] = BackendHealthEntry{Backend: "codex", Status: BackendStatusProbing}
	state.BackendHealth["claude@h1"] = BackendHealthEntry{Backend: "claude", Host: "h1", Status: BackendStatusProbing}
	state.BackendHealth["claude"] = BackendHealthEntry{Backend: "claude", Status: BackendStatusLimited, LimitedUntil: now.Add(time.Hour)}

	assert.True(t, o.pruneOrphanBreakers(&state))
	assert.NotContains(t, state.BackendHealth, "claude@gone", "a removed SSH host's breaker is dropped")
	assert.NotContains(t, state.BackendHealth, "codex", "a backend nothing dispatches to cannot be probed")
	assert.Contains(t, state.BackendHealth, "claude@h1")
	assert.Contains(t, state.BackendHealth, "claude")
}

func TestBackendHealth_CorruptOrFutureFileQuarantined(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"corrupt", `{"version":1,"backends":`},
		{"future version", `{"version":99,"backends":{}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "backend_health.json")
			require.NoError(t, os.WriteFile(path, []byte(tc.body), 0o644))
			o, _ := breakerOrchestrator(t, config.BackendFallbackConfig{})
			o.SetBackendHealthFile(path)
			state := o.loadBackendHealthFromDisk(NewState(o.cfg))
			assert.Empty(t, state.BackendHealth)
			// CORE-173 c: timestamped copies, see quarantine_test.go.
			assert.Equal(t, tc.body, latestQuarantine(t, path), "the unreadable file is preserved before it can be overwritten")
		})
	}
}

func TestAutoSwitched_FutureVersionQuarantined(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auto_switched.json")
	body := `{"version":99,"overrides":{"ENG-1":{"profile":"x"}}}`
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	o, _ := breakerOrchestrator(t, config.BackendFallbackConfig{})
	o.SetAutoSwitchedFile(path)
	state := o.loadAutoSwitchedFromDisk(NewState(o.cfg))
	assert.NotContains(t, state.IssueProfiles, "ENG-1", "an unknown version is rejected, not read as v2")
	assert.Equal(t, body, latestQuarantine(t, path))

	require.NoError(t, os.WriteFile(path, []byte(`{"version":`), 0o644))
	_ = o.loadAutoSwitchedFromDisk(NewState(o.cfg))
	assert.Equal(t, `{"version":`, latestQuarantine(t, path), "a corrupt file is quarantined too")
}
