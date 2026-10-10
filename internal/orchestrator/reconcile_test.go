package orchestrator_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
)

func cfgWithStall(stallMs int) *config.Config {
	cfg := baseConfig()
	cfg.Agent.StallTimeoutMs = stallMs
	cfg.Agent.MaxRetryBackoffMs = 300000
	return cfg
}

func runningEntry(issueID, state string, lastEventAt *time.Time) *orchestrator.RunEntry {
	issue := makeIssue(issueID, "ENG-1", state, nil, nil)
	entry := &orchestrator.RunEntry{
		Issue:        issue,
		StartedAt:    time.Now().Add(-10 * time.Minute),
		LastEventAt:  lastEventAt,
		WorkerCancel: func() {},
	}
	return entry
}

func TestReconcileStallsKillsStalled(t *testing.T) {
	cfg := cfgWithStall(1000) // 1 second stall timeout
	state := orchestrator.NewState(cfg)
	old := time.Now().Add(-2 * time.Minute)
	state.Running["id1"] = runningEntry("id1", "In Progress", &old)
	events := make(chan orchestrator.OrchestratorEvent, 10)

	state = orchestrator.ReconcileStalls(state, cfg, time.Now(), events, nil)
	_, still := state.Running["id1"]
	assert.False(t, still, "stalled session should be removed from running")
}

func TestReconcileStallsDisabledWhenZero(t *testing.T) {
	cfg := cfgWithStall(0)
	state := orchestrator.NewState(cfg)
	old := time.Now().Add(-2 * time.Minute)
	state.Running["id1"] = runningEntry("id1", "In Progress", &old)
	events := make(chan orchestrator.OrchestratorEvent, 10)

	state = orchestrator.ReconcileStalls(state, cfg, time.Now(), events, nil)
	_, still := state.Running["id1"]
	assert.True(t, still, "stall detection disabled when stall_timeout_ms=0")
}

func TestReconcileTrackerStatesTerminalCleansup(t *testing.T) {
	cfg := cfgWithStall(300000)
	state := orchestrator.NewState(cfg)
	state.Running["id1"] = runningEntry("id1", "In Progress", nil)

	mt := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id1", "ENG-1", "Done", nil, nil)},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)
	events := make(chan orchestrator.OrchestratorEvent, 10)

	state = orchestrator.ReconcileTrackerStates(context.Background(), state, mt, events, nil)
	_, still := state.Running["id1"]
	assert.False(t, still, "terminal state should remove from running")
}

func TestReconcileTrackerStatesActiveUpdatesSnapshot(t *testing.T) {
	cfg := cfgWithStall(300000)
	state := orchestrator.NewState(cfg)
	state.Running["id1"] = runningEntry("id1", "Todo", nil)

	mt := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)
	events := make(chan orchestrator.OrchestratorEvent, 10)

	state = orchestrator.ReconcileTrackerStates(context.Background(), state, mt, events, nil)
	entry, ok := state.Running["id1"]
	assert.True(t, ok)
	assert.Equal(t, "In Progress", entry.Issue.State)
}

func TestReconcileTrackerStatesNonActiveStopsWithoutCleanup(t *testing.T) {
	cfg := cfgWithStall(300000)
	state := orchestrator.NewState(cfg)
	state.Running["id1"] = runningEntry("id1", "In Progress", nil)

	// "Blocked" is neither active nor terminal
	mt := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id1", "ENG-1", "Blocked", nil, nil)},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)
	events := make(chan orchestrator.OrchestratorEvent, 10)

	state = orchestrator.ReconcileTrackerStates(context.Background(), state, mt, events, nil)
	_, still := state.Running["id1"]
	assert.False(t, still, "non-active state should stop worker")
}

func TestReconcileTrackerStatesRefreshFailureKeepsWorkers(t *testing.T) {
	cfg := cfgWithStall(300000)
	state := orchestrator.NewState(cfg)
	state.Running["id1"] = runningEntry("id1", "In Progress", nil)

	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	mt.InjectError(errors.New("network error"))
	events := make(chan orchestrator.OrchestratorEvent, 10)

	state = orchestrator.ReconcileTrackerStates(context.Background(), state, mt, events, nil)
	_, still := state.Running["id1"]
	assert.True(t, still, "refresh failure must keep workers running")
}

// TestReconcileKeepsReviewerOnTerminalCompletionState (#79): a reviewer runs
// on the issue the implementer moved to tracker.completion_state. When that
// state is also terminal ("Done"), reconciliation keeps the reviewer running;
// any other terminal state still stops it, and a plain worker is still
// stopped on the completion state.
func TestReconcileKeepsReviewerOnTerminalCompletionState(t *testing.T) {
	for _, tc := range []struct {
		name, kind, trackerState string
		keep                     bool
	}{
		{"reviewer on the completion state", "reviewer", "Done", true},
		{"reviewer on another terminal state", "reviewer", "Cancelled", false},
		{"worker on the completion state", "", "Done", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := cfgWithStall(300000)
			cfg.Tracker.CompletionState = "Done"
			cfg.Tracker.TerminalStates = []string{"Done", "Cancelled"}
			state := orchestrator.NewState(cfg)
			now := time.Now()
			entry := runningEntry("id1", "In Progress", &now)
			entry.Kind = tc.kind
			state.Running["id1"] = entry
			mt := tracker.NewMemoryTracker([]domain.Issue{makeIssue("id1", "ENG-1", tc.trackerState, nil, nil)},
				cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
			events := make(chan orchestrator.OrchestratorEvent, 10)

			state = orchestrator.ReconcileTrackerStates(context.Background(), state, mt, events, nil)
			_, still := state.Running["id1"]
			assert.Equal(t, tc.keep, still)
		})
	}
}
