package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
)

// CORE-055 — backend health and auto-switch state in the snapshot and
// HEARTBEAT.md.

func TestSnapshot_BackendHealthRows(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	reset := now.Add(3 * time.Hour)
	s := orchestrator.State{
		BackendHealth: map[string]orchestrator.BackendHealthEntry{
			"claude": {Backend: "claude", Status: orchestrator.BackendStatusLimited, Kind: "quota",
				LimitType: "five_hour", LimitedUntil: reset, ResetKnown: true, Since: now},
			"codex@build-1": {Backend: "codex", Host: "build-1", Status: orchestrator.BackendStatusLimited,
				Kind: "quota", LimitedUntil: now.Add(15 * time.Minute), ResetKnown: false, Since: now},
		},
		BackendLimitedHolds: map[string]orchestrator.BackendHold{
			"ENG-1": {Key: "claude", Until: reset},
			"ENG-2": {Key: "claude", Until: reset},
		},
		AutoSwitchInfo: map[string]orchestrator.AutoSwitchRecord{
			"ENG-3": {Source: orchestrator.AutoSwitchSourceBackendFallback, FromBackend: "claude", FromKey: "claude",
				ToBackend: "codex", ToProfile: "coder-codex", FromProfile: "coder", Reason: "backend_fallback: claude limited", SwitchedAt: now},
		},
		AutoSwitchedIdentifiers: map[string]struct{}{"ENG-3": {}, "ENG-4": {}},
		IssueBackends:           map[string]string{"ENG-3": "codex", "ENG-4": "codex"},
		IssueProfiles:           map[string]string{"ENG-3": "coder-codex", "ENG-4": "responder"},
	}
	rows := backendHealthRows(s, []string{"claude", "codex"})
	require.Len(t, rows, 3, "one row per known backend plus the host-scoped breaker")
	byKey := map[string]server.BackendHealthRow{}
	for _, r := range rows {
		byKey[orchestrator.BackendHealthKey(r.Backend, r.Host)] = r
	}
	claude := byKey["claude"]
	assert.Equal(t, server.BackendHealthLimited, claude.Status)
	require.NotNil(t, claude.LimitedUntil)
	assert.True(t, claude.LimitedUntil.Equal(reset))
	assert.Equal(t, 2, claude.HeldIssues)
	assert.Equal(t, 1, claude.ReroutedIssues)
	codex := byKey["codex"]
	assert.Equal(t, server.BackendHealthHealthy, codex.Status, "a known backend with no breaker is healthy")
	assert.Nil(t, codex.LimitedUntil)
	host := byKey["codex@build-1"]
	assert.Nil(t, host.LimitedUntil, "an unknown reset is null, never a fabricated time")
	require.NotNil(t, host.RetryAt, "the cooldown end is carried separately")

	sw := autoSwitchRows(s)
	require.Len(t, sw, 2)
	assert.Equal(t, "ENG-3", sw[0].Identifier)
	assert.Equal(t, "backend_fallback", sw[0].Source)
	assert.Equal(t, "claude", sw[0].FromBackend)
	assert.Equal(t, "ENG-4", sw[1].Identifier)
	assert.Equal(t, "unknown", sw[1].Source, "an override persisted before provenance renders as unknown")
	assert.Equal(t, "codex", sw[1].ToBackend)
}

func TestSnapshot_KnownBackends(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.Command = "claude --model opus"
	profiles := map[string]config.AgentProfile{"c": {Command: "codex"}, "w": {Command: "./wrap.sh"}}
	assert.Equal(t, []string{"claude", "codex"}, knownBackends(cfg, profiles))
}

func TestHeartbeat_BackendsSection(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	reset := now.Add(3 * time.Hour)
	retry := now.Add(15 * time.Minute)
	snap := server.StateSnapshot{
		BackendHealth: []server.BackendHealthRow{
			{Backend: "claude", Status: server.BackendHealthLimited, Kind: "quota", LimitType: "five_hour",
				LimitedUntil: &reset, RetryAt: &reset, HeldIssues: 2, ReroutedIssues: 1},
			{Backend: "codex", Status: server.BackendHealthHealthy},
			{Backend: "codex", Host: "build-1", Status: server.BackendHealthLimited, Kind: "throttle", RetryAt: &retry},
			{Backend: "claude", Host: "build-2", Status: server.BackendHealthProbing, ProbeIssue: "ENG-9"},
		},
		AutoSwitches: []server.AutoSwitchRow{{Identifier: "ENG-3", Source: "backend_fallback"}},
	}
	out := renderHeartbeat(snap, heartbeatOptions{}, now)
	require.Equal(t, 1, strings.Count(out, "\n## Backends\n"))
	assert.Contains(t, out, "- claude: limited until 2026-09-27T15:00:00Z (quota, five_hour); held 2, rerouted 1\n")
	assert.Contains(t, out, "- codex: healthy\n")
	assert.Contains(t, out, "- codex@build-1: limited, reset unknown, retry at 2026-09-27T12:15:00Z (throttle)\n")
	assert.Contains(t, out, "- claude@build-2: probing (probe ENG-9)\n")
	assert.Contains(t, out, "- Auto-switched issues: 1\n")

	empty := renderHeartbeat(server.StateSnapshot{}, heartbeatOptions{}, now)
	assert.Contains(t, empty, "\n## Backends\n- none reported\n", "a pre-CORE-055 snapshot still renders the section")
}

// TestSnapshot_BackendHealthJSONKeys is the wire shape the dashboard reads:
// .backendHealth[0] carries backend, status and limitedUntil (null when the
// reset is unknown or the backend is healthy).
func TestSnapshot_BackendHealthJSONKeys(t *testing.T) {
	rows := backendHealthRows(orchestrator.State{}, []string{"claude"})
	data, err := json.Marshal(server.StateSnapshot{BackendHealth: rows})
	require.NoError(t, err)
	var decoded struct {
		BackendHealth []map[string]any `json:"backendHealth"`
	}
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Len(t, decoded.BackendHealth, 1)
	for _, k := range []string{"backend", "status", "limitedUntil"} {
		assert.Contains(t, decoded.BackendHealth[0], k)
	}
	assert.Nil(t, decoded.BackendHealth[0]["limitedUntil"])
}

// The orchestrator adapter must satisfy the optional pin checker, or the
// handler would silently skip the CORE-056 refusal.
var _ server.IssueBackendPinChecker = (*orchestratorAdapter)(nil)

// Likewise for the clear-breaker action (M3-close V1).
var _ server.BackendBreakerClearer = (*orchestratorAdapter)(nil)

// BH-M3-8: a stale hold (its breaker closed or probing for that very issue)
// is not counted as held.
func TestSnapshot_BackendHealthRows_HeldCountsActiveHoldsOnly(t *testing.T) {
	s := orchestrator.State{
		BackendHealth: map[string]orchestrator.BackendHealthEntry{
			"claude": {Backend: "claude", Status: orchestrator.BackendStatusProbing, ProbeIssue: "ENG-2"},
		},
		BackendLimitedHolds: map[string]orchestrator.BackendHold{
			"ENG-1": {Key: "claude"}, // held behind ENG-2's probe: active
			"ENG-2": {Key: "claude"}, // the probe itself: not held
			"ENG-3": {Key: "codex"},  // codex breaker closed: stale
		},
	}
	byKey := map[string]server.BackendHealthRow{}
	for _, r := range backendHealthRows(s, []string{"claude", "codex"}) {
		byKey[r.Backend] = r
	}
	assert.Equal(t, 1, byKey["claude"].HeldIssues)
	assert.Equal(t, 0, byKey["codex"].HeldIssues)
}
