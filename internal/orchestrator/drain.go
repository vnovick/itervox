package orchestrator

import (
	"errors"
	"log/slog"
	"sync"
)

// CORE-057 — graceful drain.
//
// A drain stops ADMISSION and nothing else: the event loop keeps running,
// keeps polling and reconciling, and keeps consuming EventWorkerExited /
// EventWorkerUpdate so an in-flight turn finishes exactly as it would have.
// Worker contexts are NOT cancelled by a drain; cmd/itervox cancels the loop
// context (which SIGKILLs the agent process groups through cmd.Cancel) only
// once Drained() closes, the drain grace expires, or a second signal forces
// the stop.
//
// Every admission path is gated:
//   - tick dispatch, fireRetries, pending-input resumes and automation
//     dispatch through AvailableSlots, which reports 0 while draining (retries
//     reschedule, automations queue — both are then persisted or re-derived
//     from the tracker on the next start);
//   - dispatch() and dispatchReviewerForIssue refuse outright, covering the
//     paths that do not consult AvailableSlots (the backend_fallback re-dispatch
//     in handleFailedExit, auto-review, the review chain, EventDispatchReviewer);
//   - the HTTP controls ResumeIssue, ReanalyzeIssue, ProvideInput and
//     DispatchReviewer answer ErrDraining (HTTP 409) instead of queueing.

// ErrDraining is returned by the issue-control methods that would admit new
// work while the daemon drains for shutdown or reload. cmd/itervox maps it to
// server.ErrDraining (HTTP 409).
var ErrDraining = errors.New("orchestrator: daemon is draining; not admitting new work")

// errLoopExited is returned by RequestDrain when the event loop is gone.
var errLoopExited = errors.New("orchestrator: event loop has exited")

// drainSignal is a lazily-created, close-once channel, so an Orchestrator
// built as a struct literal in a test still has a usable Drained().
type drainSignal struct {
	initOnce  sync.Once
	closeOnce sync.Once
	ch        chan struct{}
}

func (d *drainSignal) c() chan struct{} {
	d.initOnce.Do(func() { d.ch = make(chan struct{}) })
	return d.ch
}

func (d *drainSignal) fire() {
	ch := d.c()
	d.closeOnce.Do(func() { close(ch) })
}

// RequestDrain asks the event loop to stop admitting work. It ONLY sends
// EventDrain — State.Draining and the readiness flag are set by the loop when
// it handles the event. It blocks until the event is accepted, and returns an
// error only when the loop has already exited. Safe from any goroutine.
func (o *Orchestrator) RequestDrain() error {
	// M4-close D2: announce the request before queueing the event, so every
	// admission point the loop reaches from now on refuses (syncDrainRequest)
	// even if the loop picks a tick before it dequeues EventDrain.
	o.drainRequested.Store(true)
	var exited chan struct{}
	if p := o.loopExited.Load(); p != nil {
		exited = *p
	}
	select {
	case o.events <- OrchestratorEvent{Type: EventDrain}:
		return nil
	case <-exited: // nil before Run: blocks, the buffered send wins
		return errLoopExited
	}
}

// Drained is closed by the event loop once a drain is in progress and no
// worker is running. It is never closed without a drain.
func (o *Orchestrator) Drained() <-chan struct{} {
	return o.drained.c()
}

// AdmissionClosed reports whether a drain has been requested or applied.
// Off-loop admission paths that start agents without going through the event
// loop (the deps analyzer, M4-close BH-M4-3) refuse while it is true. Safe
// from any goroutine.
func (o *Orchestrator) AdmissionClosed() bool {
	return o.draining.Load() || o.drainRequested.Load()
}

// isDraining is the lock-free drain flag for off-loop callers (HTTP issue
// controls, readiness). Only the event loop stores it.
func (o *Orchestrator) isDraining() bool {
	return o.draining.Load()
}

// applyDrain handles EventDrain. Event loop only.
func (o *Orchestrator) applyDrain(state *State) {
	if state.Draining {
		return
	}
	state.Draining = true
	o.draining.Store(true)
	slog.Info("orchestrator: draining — admission stopped, in-flight turns continue",
		"running", len(state.Running), "retry_queue", len(state.RetryAttempts),
		"automation_queue", len(state.AutomationQueue))
}

// syncDrainRequest applies a drain that RequestDrain has announced but whose
// EventDrain the loop has not dequeued yet. Every admission point calls it
// before its own gate (dispatch and the reviewer check State.Draining;
// automation and pending-input-resume launches check AvailableSlots, which is
// 0 while draining), as does the top of each loop iteration, so no worker
// starts after a drain request regardless of select ordering (M4-close D2).
// Event loop only.
func (o *Orchestrator) syncDrainRequest(state *State) {
	if !state.Draining && o.drainRequested.Load() {
		o.applyDrain(state)
	}
}

// observeDrained closes Drained() once a drain has no running worker left.
// Event loop only; called after every loop iteration.
func (o *Orchestrator) observeDrained(state State) {
	if state.Draining && len(state.Running) == 0 {
		o.drained.fire()
	}
}
