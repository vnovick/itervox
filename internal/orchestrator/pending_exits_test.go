package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// TestPendingExitsOnlyForTheCurrentRun: deliverExit marks the issue, the
// mark counts only for a run that started before it (a leftover mark from an
// earlier run whose exit was dropped is ignored), and handling the exit
// clears it.
func TestPendingExitsOnlyForTheCurrentRun(t *testing.T) {
	o := New(dependencyAuditConfig(), tracker.NewMemoryTracker(nil, nil, nil), nil, nil)
	state := NewState(dependencyAuditConfig())
	state.Running["id1"] = &RunEntry{Issue: domain.Issue{ID: "id1", Identifier: "ENG-1"}, StartedAt: time.Now().Add(-time.Minute)}
	assert.Empty(t, o.pendingExits(state))

	o.exitsSent.Store("id1", time.Now())
	assert.Equal(t, map[string]bool{"id1": true}, o.pendingExits(state))

	state.Running["id1"].StartedAt = time.Now().Add(time.Second) // a later run
	assert.Empty(t, o.pendingExits(state), "a mark from before this run started is stale")

	state = o.handleEvent(t.Context(), state, OrchestratorEvent{Type: EventWorkerExited, IssueID: "id1"})
	_, marked := o.exitsSent.Load("id1")
	assert.False(t, marked, "handling the exit clears the mark")
}

// markCheckingTracker records, when the issue is moved to the completion
// state, whether the orchestrator had already marked the run as finishing.
type markCheckingTracker struct {
	*tracker.MemoryTracker
	o          *Orchestrator
	completion string
	markedAt   chan bool
}

func (m *markCheckingTracker) UpdateIssueState(ctx context.Context, id, state string) error {
	if state == m.completion {
		_, marked := m.o.exitsSent.Load(id)
		select {
		case m.markedAt <- marked:
		default:
		}
	}
	return m.MemoryTracker.UpdateIssueState(ctx, id, state)
}

// TestWorkerMarksRunBeforeMovingToCompletion: the run is marked as finishing
// before the issue turns terminal in the tracker, so no reconcile tick can
// see a terminal issue with a run it would still stop.
func TestWorkerMarksRunBeforeMovingToCompletion(t *testing.T) {
	cfg := dependencyAuditConfig()
	cfg.Tracker.CompletionState = "Done"
	cfg.Tracker.TerminalStates = []string{"Done"}
	cfg.Agent.MaxTurns = 1
	cfg.Polling.IntervalMs = 20
	mt := &markCheckingTracker{
		MemoryTracker: tracker.NewMemoryTracker([]domain.Issue{{ID: "id1", Identifier: "ENG-1", Title: "T", State: "Todo"}},
			cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates),
		completion: "Done",
		markedAt:   make(chan bool, 1),
	}
	o := New(cfg, mt, agenttest.NewFakeRunner([]agent.StreamEvent{
		{Type: "system", SessionID: "s1"},
		{Type: "result", SessionID: "s1"},
	}), nil)
	mt.o = o
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { _ = o.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	select {
	case marked := <-mt.markedAt:
		assert.True(t, marked, "marked before the issue turned terminal")
	case <-time.After(5 * time.Second):
		t.Fatal("the worker never moved the issue to the completion state")
	}
}
