package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
)

// TestAdapterEmitPRMergedFailureRecorded (M6-close BH-M6-2): a failed
// pr_merged dispatch (here: the issue cannot be fetched) lands in the
// RecentFailures ring the dashboard's Failures panel reads.
func TestAdapterEmitPRMergedFailureRecorded(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tracker.ActiveStates = []string{"Todo"}
	cfg.Polling.IntervalMs = 50
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, nil)
	orch := orchestrator.New(cfg, mt, &agenttest.FakeRunner{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = orch.Run(ctx); close(done) }() // test driver
	t.Cleanup(func() { cancel(); <-done })

	adapter := &orchestratorAdapter{orch: orch, cfg: cfg, tr: mt, notify: func() {}}
	err := adapter.EmitPRMerged(context.Background(), "ENG-404", "https://github.com/a/b/pull/9", 9, "sha", "main", "feat")
	require.Error(t, err)
	require.Eventually(t, func() bool {
		for _, f := range orch.Snapshot().RecentFailures {
			if f.Kind == orchestrator.FailureKindAutomation && f.Identifier == "ENG-404" &&
				f.Source == "pr_merged" && strings.Contains(f.Message, "#9") {
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond, "the pr_merged emit failure is recorded in RecentFailures")
}
