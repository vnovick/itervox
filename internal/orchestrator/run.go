package orchestrator

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"
)

// RunPhase is where a run is in its lifecycle (#125):
//
//	dispatched → running → finishing → exited
//
// The event loop owns it. A run is dispatched when its Running entry is
// created, running once its worker reports progress, finishing once the worker
// has started to wrap up (it is about to move the issue to the completion
// state, or is sending its exit), and exited when the loop has handled its
// exit. Reconcile and stall detection leave a finishing run to its own exit.
type RunPhase string

// Run lifecycle phases.
const (
	RunDispatched RunPhase = "dispatched"
	RunRunning    RunPhase = "running"
	RunFinishing  RunPhase = "finishing"
	RunExited     RunPhase = "exited"
)

// runHandle is what a run's worker shares with the event loop. The worker
// reports "finishing" here rather than by event because the report must be
// visible to the very tick whose tracker fetch sees the issue it just moved:
// an event could still be queued behind that tick.
type runHandle struct {
	id        string
	finishing atomic.Bool
}

type runHandleKey struct{}

// startRun gives a new Running entry its run identity and returns the worker
// context that carries it. Event loop only.
func (o *Orchestrator) startRun(ctx context.Context, entry *RunEntry) context.Context {
	h := &runHandle{id: generateRunID()}
	entry.RunID = h.id
	entry.SessionID = h.id
	entry.Phase = RunDispatched
	entry.run = h
	return context.WithValue(ctx, runHandleKey{}, h)
}

// runFrom returns the run a worker context belongs to, or nil when the worker
// was started without one (tests that call runWorker directly).
func runFrom(ctx context.Context) *runHandle {
	h, _ := ctx.Value(runHandleKey{}).(*runHandle)
	return h
}

// runIDFrom returns the ID of the run a worker context belongs to, or "".
func runIDFrom(ctx context.Context) string {
	if h := runFrom(ctx); h != nil {
		return h.id
	}
	return ""
}

// markFinishing reports that the worker's run has started to finish. Safe to
// call from the worker goroutine; a no-op without a run.
func markFinishing(ctx context.Context) {
	if h := runFrom(ctx); h != nil {
		h.finishing.Store(true)
	}
}

// observeFinishing reports whether the run is finishing, folding a report from
// its worker into Phase. Event loop only.
func (e *RunEntry) observeFinishing() bool {
	if e.Phase != RunFinishing && e.run != nil && e.run.finishing.Load() {
		e.Phase = RunFinishing
	}
	return e.Phase == RunFinishing
}

// isStaleFor reports whether an event naming runID belongs to a run other than
// this one. An empty ID on either side matches (entries and events built by
// hand in tests carry none).
func (e *RunEntry) isStaleFor(runID string) bool {
	return runID != "" && e.RunID != "" && e.RunID != runID
}

// handleStaleExit settles the exit of a run that is no longer its issue's
// current run (#125): reconcile or stall detection stopped it and the issue
// was dispatched again before its exit arrived. The stopper already released
// the claim and scheduled any retry, so the exit changes no state; a stall is
// still recorded in history, which its own exit would otherwise do.
func (o *Orchestrator) handleStaleExit(ev OrchestratorEvent, current *RunEntry) {
	reason := TerminalReason("")
	if ev.RunEntry != nil {
		reason = ev.RunEntry.TerminalReason
	}
	slog.Info("orchestrator: ignoring the exit of a superseded run",
		"issue_id", ev.IssueID, "run_id", ev.RunID, "current_run_id", current.RunID, "reason", reason)
	if reason == TerminalStalled {
		o.recordHistory(ev.RunEntry, ev.RunEntry.Issue, time.Now(), "stalled")
	}
}
