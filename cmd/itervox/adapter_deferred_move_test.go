package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
)

// TestAdapterDefersAgentMoveButNotDashboardMove (#177) drives the production
// move path — the REAL orchestratorAdapter over a REAL running orchestrator
// with a worker mid-turn. The move_state agent action's move is deferred (the
// tracker is untouched, the worker keeps running); the dashboard's move of
// the same issue goes straight to the tracker and reconciliation stops the
// worker, as it always did.
func TestAdapterDefersAgentMoveButNotDashboardMove(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tracker.ActiveStates = []string{"Todo", "In Progress"}
	cfg.Tracker.TerminalStates = []string{"Done", "Cancelled"}
	cfg.Agent.MaxConcurrentAgents = 1
	cfg.Agent.MaxTurns = 1
	cfg.Polling.IntervalMs = 20
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "T", State: "In Progress"}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	runner := &turnBlockingRunner{started: make(chan struct{})}
	orch := orchestrator.New(cfg, mt, runner, tempWorkspaceProvider{root: t.TempDir()})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = orch.Run(ctx); close(done) }() // test driver
	t.Cleanup(func() { cancel(); <-done })

	select {
	case <-runner.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the worker never started")
	}
	adapter := &orchestratorAdapter{orch: orch, cfg: cfg, tr: mt, notify: func() {}}
	stateOf := func() string {
		got, err := mt.FetchIssueStatesByIDs(context.Background(), []string{"id1"})
		require.NoError(t, err)
		require.Len(t, got, 1)
		return got[0].State
	}

	agentCtx := server.WithIssueStatusSource(context.Background(), server.IssueStatusSourceAgent)
	require.NoError(t, adapter.UpdateIssueState(agentCtx, "ENG-1", "Done"))
	assert.Equal(t, "In Progress", stateOf(), "the agent's move waits for the worker")
	assert.Contains(t, orch.Snapshot().DeferredStateMoves, "ENG-1")
	time.Sleep(200 * time.Millisecond) // several reconcile ticks
	assert.False(t, runner.wasCancelled(), "a deferred move never stops the worker")

	dashCtx := server.WithIssueStatusSource(context.Background(), server.IssueStatusSourceDashboard)
	require.NoError(t, adapter.UpdateIssueState(dashCtx, "ENG-1", "Cancelled"))
	assert.Equal(t, "Cancelled", stateOf(), "a person's move goes straight to the tracker")
	require.Eventually(t, runner.wasCancelled, 3*time.Second, 20*time.Millisecond,
		"a person's terminal move still stops the running worker")
	require.Eventually(t, func() bool { return len(orch.Snapshot().DeferredStateMoves) == 0 },
		2*time.Second, 20*time.Millisecond, "the person's move drops the agent's deferred move")
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, "Cancelled", stateOf(), "the person's move wins")
}
