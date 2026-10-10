package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// TestStartRunGivesEachDispatchItsOwnIdentity: every dispatch gets a fresh
// run ID, which is also its log session ID, starts in the dispatched phase,
// and the worker context carries the same run.
func TestStartRunGivesEachDispatchItsOwnIdentity(t *testing.T) {
	o := New(dependencyAuditConfig(), tracker.NewMemoryTracker(nil, nil, nil), nil, nil)
	first, second := &RunEntry{}, &RunEntry{}
	ctx1 := o.startRun(t.Context(), first)
	ctx2 := o.startRun(t.Context(), second)

	require.NotEmpty(t, first.RunID)
	assert.NotEqual(t, first.RunID, second.RunID)
	assert.Equal(t, first.RunID, first.SessionID)
	assert.Equal(t, RunDispatched, first.Phase)
	assert.Equal(t, first.RunID, runIDFrom(ctx1))
	assert.Equal(t, second.RunID, runIDFrom(ctx2))
	assert.Empty(t, runIDFrom(t.Context()))
}

// TestStaleExitLeavesTheCurrentRunAlone: the exit of a run that was stopped
// and replaced by a new run on the same issue (old run ID) leaves the new run
// running, claimed and uncancelled, and schedules nothing (#125). Before run
// IDs the exit deleted the new run's Running entry and cancelled its worker.
func TestStaleExitLeavesTheCurrentRunAlone(t *testing.T) {
	o := New(dependencyAuditConfig(), tracker.NewMemoryTracker(nil, nil, nil), nil, nil)
	state := NewState(dependencyAuditConfig())
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "T", State: "In Progress"}
	workerCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	current := &RunEntry{Issue: issue, StartedAt: time.Now(), WorkerCancel: cancel}
	o.startRun(workerCtx, current)
	state.Running[issue.ID] = current
	state.Claimed[issue.ID] = struct{}{}
	o.workerCancelsMu.Lock()
	o.workerCancels[issue.Identifier] = cancel
	o.workerCancelsMu.Unlock()

	for _, reason := range []TerminalReason{TerminalFailed, TerminalSucceeded, TerminalStalled} {
		attempt := 0
		state = o.handleEvent(t.Context(), state, OrchestratorEvent{
			Type:     EventWorkerExited,
			IssueID:  issue.ID,
			RunID:    "run-old",
			RunEntry: &RunEntry{Issue: issue, RunID: "run-old", TerminalReason: reason, RetryAttempt: &attempt},
		})
		require.Same(t, current, state.Running[issue.ID], "%s: the current run is still running", reason)
		assert.Contains(t, state.Claimed, issue.ID, "%s: still claimed", reason)
		assert.Empty(t, state.RetryAttempts, "%s: no retry scheduled", reason)
		assert.NoError(t, workerCtx.Err(), "%s: the current worker was not cancelled", reason)
	}

	// The current run's own exit is applied.
	attempt := 0
	state = o.handleEvent(t.Context(), state, OrchestratorEvent{
		Type:     EventWorkerExited,
		IssueID:  issue.ID,
		RunID:    current.RunID,
		RunEntry: &RunEntry{Issue: issue, TerminalReason: TerminalFailed, RetryAttempt: &attempt},
	})
	assert.NotContains(t, state.Running, issue.ID)
	assert.Equal(t, RunExited, current.Phase)
}

// TestStaleUpdateLeavesTheCurrentRunAlone: progress from a superseded run's
// worker does not overwrite the current run's counters or session, and the
// current run's own first update moves it to running.
func TestStaleUpdateLeavesTheCurrentRunAlone(t *testing.T) {
	o := New(dependencyAuditConfig(), tracker.NewMemoryTracker(nil, nil, nil), nil, nil)
	state := NewState(dependencyAuditConfig())
	current := &RunEntry{Issue: domain.Issue{ID: "id1", Identifier: "ENG-1"}, StartedAt: time.Now()}
	o.startRun(t.Context(), current)
	state.Running["id1"] = current

	state = o.handleEvent(t.Context(), state, OrchestratorEvent{
		Type: EventWorkerUpdate, IssueID: "id1", RunID: "run-old",
		RunEntry: &RunEntry{TurnCount: 7, TotalTokens: 900, SessionID: "run-old", AgentSessionID: "old-session"},
	})
	assert.Zero(t, current.TurnCount)
	assert.Zero(t, current.TotalTokens)
	assert.Equal(t, current.RunID, current.SessionID)
	assert.Empty(t, current.AgentSessionID)
	assert.Equal(t, RunDispatched, current.Phase)

	state = o.handleEvent(t.Context(), state, OrchestratorEvent{
		Type: EventWorkerUpdate, IssueID: "id1", RunID: current.RunID,
		RunEntry: &RunEntry{TurnCount: 1},
	})
	assert.Equal(t, 1, current.TurnCount)
	assert.Equal(t, RunRunning, current.Phase)
	_ = state
}

// TestReconcileFoldsTheWorkersFinishingReport: the worker reports finishing
// through its run handle; reconcile folds that into Phase and leaves the run
// to its exit, even though the tracker already shows the issue terminal.
func TestReconcileFoldsTheWorkersFinishingReport(t *testing.T) {
	cfg := dependencyAuditConfig()
	state := NewState(cfg)
	entry := &RunEntry{Issue: domain.Issue{ID: "id1", Identifier: "ENG-1", State: "In Progress"}, StartedAt: time.Now()}
	o := New(cfg, tracker.NewMemoryTracker(nil, nil, nil), nil, nil)
	workerCtx := o.startRun(t.Context(), entry)
	state.Running["id1"] = entry
	markFinishing(workerCtx)

	mt := tracker.NewMemoryTracker([]domain.Issue{{ID: "id1", Identifier: "ENG-1", State: "Done"}},
		cfg.Tracker.ActiveStates, []string{"Done"})
	state.TerminalStates = []string{"Done"}
	events := make(chan OrchestratorEvent, 4)
	state = ReconcileTrackerStates(t.Context(), state, mt, events, nil)
	require.Contains(t, state.Running, "id1")
	assert.Equal(t, RunFinishing, state.Running["id1"].Phase)
	assert.Empty(t, events)
}

// TestDeliverExitCarriesTheRunAndMarksItFinishing: the exit names the run it
// belongs to and the run is finishing before the exit is queued.
func TestDeliverExitCarriesTheRunAndMarksItFinishing(t *testing.T) {
	o := New(dependencyAuditConfig(), tracker.NewMemoryTracker(nil, nil, nil), nil, nil)
	entry := &RunEntry{}
	workerCtx := o.startRun(t.Context(), entry)
	o.sendExit(workerCtx, domain.Issue{ID: "id1", Identifier: "ENG-1"}, 0, TerminalFailed, nil)

	ev := <-o.events
	assert.Equal(t, entry.RunID, ev.RunID)
	assert.True(t, entry.observeFinishing())
}

// runCapturingRunner records the run handle of the worker context its first
// turn runs under.
type runCapturingRunner struct {
	agent.Runner
	got chan *runHandle
}

func (r *runCapturingRunner) RunTurn(ctx context.Context, log agent.Logger, onProgress func(agent.TurnResult), sessionID *string, prompt, workspacePath, command, workerHost, logDir string, readTimeoutMs, turnTimeoutMs int, permissionMode agent.PermissionMode) (agent.TurnResult, error) {
	select {
	case r.got <- runFrom(ctx):
	default:
	}
	return r.Runner.RunTurn(ctx, log, onProgress, sessionID, prompt, workspacePath, command, workerHost, logDir, readTimeoutMs, turnTimeoutMs, permissionMode)
}

// finishCheckingTracker records, when the issue is moved to the completion
// state, whether the run had already reported finishing.
type finishCheckingTracker struct {
	*tracker.MemoryTracker
	run        func() *runHandle
	completion string
	markedAt   chan bool
}

func (m *finishCheckingTracker) UpdateIssueState(ctx context.Context, id, state string) error {
	if state == m.completion {
		h := m.run()
		select {
		case m.markedAt <- h != nil && h.finishing.Load():
		default:
		}
	}
	return m.MemoryTracker.UpdateIssueState(ctx, id, state)
}

// TestWorkerMarksRunBeforeMovingToCompletion: the run reports finishing
// before the issue turns terminal in the tracker, so no reconcile tick can
// see a terminal issue with a run it would still stop.
func TestWorkerMarksRunBeforeMovingToCompletion(t *testing.T) {
	cfg := dependencyAuditConfig()
	cfg.Tracker.CompletionState = "Done"
	cfg.Tracker.TerminalStates = []string{"Done"}
	cfg.Agent.MaxTurns = 1
	cfg.Polling.IntervalMs = 20
	runner := &runCapturingRunner{
		Runner: agenttest.NewFakeRunner([]agent.StreamEvent{
			{Type: "system", SessionID: "s1"},
			{Type: "result", SessionID: "s1"},
		}),
		got: make(chan *runHandle, 1),
	}
	var handle *runHandle
	mt := &finishCheckingTracker{
		MemoryTracker: tracker.NewMemoryTracker([]domain.Issue{{ID: "id1", Identifier: "ENG-1", Title: "T", State: "Todo"}},
			cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates),
		completion: "Done",
		markedAt:   make(chan bool, 1),
	}
	mt.run = func() *runHandle {
		if handle == nil {
			select {
			case handle = <-runner.got:
			default:
			}
		}
		return handle
	}
	o := New(cfg, mt, runner, nil)
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

// TestInputRequiredExitSurvivesCancelMidSend (#125): an input-required exit
// marks its run finishing, which reconcile and stall detection both leave
// alone, so it must reach the loop even when the worker's context is
// cancelled while the exit waits for room in the event queue. It used to give
// up on the cancel, leaving the run finishing forever.
func TestInputRequiredExitSurvivesCancelMidSend(t *testing.T) {
	o := New(dependencyAuditConfig(), tracker.NewMemoryTracker(nil, nil, nil), nil, nil)
	running := make(chan struct{}) // the loop is running: loopExited open
	o.loopExited.Store(&running)
	for len(o.events) < cap(o.events) {
		o.events <- OrchestratorEvent{Type: EventDispatchAutomation}
	}
	entry := &RunEntry{}
	ctx, cancel := context.WithCancel(o.startRun(t.Context(), entry))
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1"}
	sent := make(chan struct{})
	go func() {
		o.sendExitWithInputRequired(ctx, &RunEntry{Issue: issue, TerminalReason: TerminalInputRequired}, &InputRequiredEntry{Identifier: "ENG-1"})
		close(sent)
	}()
	time.Sleep(20 * time.Millisecond) // blocked on the full queue
	cancel()
	time.Sleep(20 * time.Millisecond)

	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-o.events:
			if ev.Type == EventWorkerExited {
				assert.Equal(t, entry.RunID, ev.RunID)
				assert.NotNil(t, ev.InputRequiredEntry)
				<-sent
				return
			}
		case <-deadline:
			t.Fatal("the input-required exit was dropped when the worker's context was cancelled mid-send")
		}
	}
}

// TestSnapshotsDoNotCarryTheRunHandle (#125): a snapshot keeps a run's ID and
// phase but not the handle shared with its worker. The clone guard's
// reflection skips unexported fields, so this pins it.
func TestSnapshotsDoNotCarryTheRunHandle(t *testing.T) {
	o := New(dependencyAuditConfig(), tracker.NewMemoryTracker(nil, nil, nil), nil, nil)
	state := NewState(dependencyAuditConfig())
	entry := &RunEntry{Issue: domain.Issue{ID: "id1", Identifier: "ENG-1"}}
	o.startRun(t.Context(), entry)
	state.Running["id1"] = entry

	snap := state.Clone().Running["id1"]
	require.NotNil(t, snap)
	assert.Nil(t, snap.run)
	assert.Equal(t, entry.RunID, snap.RunID)
	assert.Equal(t, RunDispatched, snap.Phase)
	assert.NotNil(t, entry.run, "the live entry keeps its handle")
}
