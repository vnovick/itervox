package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
)

// TestDepsAnalyzerRefusedWhileBackendLimited (CORE-173 a): the dependency
// analyzer runs an agent outside the dispatch gate, so it used to start on
// a backend whose breaker (CORE-053) was open. Both enqueue paths (manual
// POST and the auto-analyze scheduler) now refuse with server.ErrBackendLimited
// while the analyzer profile's backend is limited; a profile on a healthy
// backend is unaffected.
func TestDepsAnalyzerRefusedWhileBackendLimited(t *testing.T) {
	enabled := true
	cfg := &config.Config{
		Agent: config.AgentConfig{
			Command:               "claude",
			DepsAnalyzerChunkSize: 8,
			MaxConcurrentAgents:   1,
			Profiles: map[string]config.AgentProfile{
				"deps-claude": {Command: "claude --model haiku", Enabled: &enabled},
				"deps-codex":  {Command: "codex --model gpt-5-mini", Enabled: &enabled},
			},
		},
		Tracker: config.TrackerConfig{ActiveStates: []string{"Todo"}, TerminalStates: []string{"Done"}},
		Polling: config.PollingConfig{IntervalMs: 50},
	}
	dir := t.TempDir()
	healthPath := filepath.Join(dir, "backend_health.json")
	body, err := json.Marshal(map[string]any{"version": 1, "backends": map[string]any{
		"claude": map[string]any{"backend": "claude", "status": "limited", "kind": "quota",
			"limited_until": time.Now().Add(time.Hour).UTC(), "reset_known": true, "since": time.Now().UTC()},
	}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(healthPath, body, 0o600))

	tr := tracker.NewMemoryTracker(nil, []string{"Todo"}, []string{"Done"})
	runner := &stubRunner{response: `{"edges":[]}`}
	orch := orchestrator.New(cfg, tr, runner, nil)
	orch.SetBackendHealthFile(healthPath)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = orch.Run(ctx); close(done) }() // test driver
	t.Cleanup(func() { cancel(); <-done })
	require.Eventually(t, func() bool {
		_, _, limited := orch.BackendLimitedForProfile("deps-claude", time.Now())
		return limited
	}, 5*time.Second, 10*time.Millisecond, "the persisted breaker is loaded")

	svc := newDepsAnalyzerService(ctx, orch, cfg, tr, runner, filepath.Join(dir, "sidecar.json"), "", func() {})
	for _, trigger := range []string{depsAnalyzeTriggerManual, "auto"} {
		_, _, err := svc.EnqueueAnalysisWithTrigger("deps-claude", "auto", trigger)
		assert.True(t, errors.Is(err, server.ErrBackendLimited), "trigger %s: want server.ErrBackendLimited, got %v", trigger, err)
	}
	time.Sleep(50 * time.Millisecond)
	assert.Zero(t, runner.calls.Load(), "no analyzer pass on a limited backend")

	// Control: the codex profile's breaker is closed.
	id, _, err := svc.EnqueueAnalysis("deps-codex", "auto")
	require.NoError(t, err)
	require.Eventually(t, func() bool { r, ok := svc.Status(id); return ok && r.Status != "queued" && r.Status != "running" },
		5*time.Second, 5*time.Millisecond)
}
