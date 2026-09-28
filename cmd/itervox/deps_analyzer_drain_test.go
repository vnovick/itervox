package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
)

// M4-close BH-M4-3 — the deps analyzer runs an agent, so it is admission and
// must be refused while the daemon drains: the manual POST path (mapped to
// 409 draining) and the 60 s auto-analyze scheduler both go through
// EnqueueAnalysisWithTrigger.
func TestDepsAnalyzerRefusedWhileDraining(t *testing.T) {
	enabled := true
	cfg := &config.Config{
		Agent: config.AgentConfig{
			DepsAnalyzerChunkSize: 8,
			Profiles: map[string]config.AgentProfile{
				"deps-analyzer": {Command: "stub-analyzer", Enabled: &enabled},
			},
		},
		Tracker: config.TrackerConfig{ActiveStates: []string{"Todo"}},
	}
	tr := tracker.NewMemoryTracker(nil, []string{"Todo"}, nil)
	runner := &stubRunner{response: `{"edges":[]}`}
	orch := orchestrator.New(cfg, tr, runner, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc := newDepsAnalyzerService(ctx, orch, cfg, tr, runner, t.TempDir()+"/sidecar.json", "", func() {})

	// Control: admission open, the manual enqueue is accepted.
	id, _, err := svc.EnqueueAnalysis("deps-analyzer", "auto")
	require.NoError(t, err)
	require.Eventually(t, func() bool { r, ok := svc.Status(id); return ok && r.Status != "queued" && r.Status != "running" },
		5*time.Second, 5*time.Millisecond)

	require.NoError(t, orch.RequestDrain())

	before := runner.calls.Load()
	// "manual" is the POST path (EnqueueAnalysis); "auto" is exactly the
	// call runDepsAutoAnalyzeTick makes every 60 s.
	for _, trigger := range []string{depsAnalyzeTriggerManual, "auto"} {
		_, _, err := svc.EnqueueAnalysisWithTrigger("deps-analyzer", "auto", trigger)
		assert.True(t, errors.Is(err, server.ErrDraining), "trigger %s: want server.ErrDraining, got %v", trigger, err)
	}
	_, _, err = svc.EnqueueAnalysis("deps-analyzer", "full")
	assert.True(t, errors.Is(err, server.ErrDraining), "manual POST path: got %v", err)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, before, runner.calls.Load(), "no analyzer pass started while draining")
}
