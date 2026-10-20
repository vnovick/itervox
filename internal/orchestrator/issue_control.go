package orchestrator

import (
	"errors"
	"log/slog"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/metrics"
)

// ErrBusy is returned by the issue-control methods below when a
// non-blocking send to the orchestrator's event channel found it full. It
// means only "the request was not enqueued; try again" — it never means the
// identifier was not found. Callers (internal/server's handlers, via the
// OrchestratorClient adapter in cmd/itervox) must map it to HTTP 503 with a
// Retry-After hint, not 404 (CORE-005).
var ErrBusy = errors.New("orchestrator: event channel full")

// ErrNotFound is returned by CancelIssue, ResumeIssue, TerminateIssue and
// ReanalyzeIssue when a synchronous state lookup — performed BEFORE any
// attempt to send an event — establishes that the identifier is not in the
// state the call requires (e.g. ResumeIssue on an issue that isn't paused).
// ProvideInput and DismissInput never return this: they perform no lookup,
// so for them a non-nil error is ErrBusy (CORE-005) — or, for ProvideInput,
// ErrDraining while the daemon drains (CORE-057).
var ErrNotFound = errors.New("orchestrator: issue not found in the required state")

// CancelIssue cancels a running or retry-queued issue and marks it as paused
// so it is not automatically retried. The issue stays paused until ResumeIssue
// is called.
//   - If a live worker exists, it is cancelled immediately.
//   - If the issue is in the retry queue (no live worker), an EventCancelRetry
//     is sent to the event loop which removes the retry entry and pauses the issue.
//
// Returns nil if an action was taken, ErrNotFound if the issue is neither
// running nor queued for retry, or ErrBusy if the event channel was full
// (retry-queue cancel path only — the live-worker cancel path never sends an
// event).
// Safe to call from any goroutine.
func (o *Orchestrator) CancelIssue(identifier string) error {
	// Mark as user-cancelled BEFORE cancelling the worker so the exit handler
	// in the event loop sees the flag when the EventWorkerExited arrives.
	o.userCancelledMu.Lock()
	o.userCancelledIDs[identifier] = struct{}{}
	o.userCancelledMu.Unlock()

	cancelled := o.cancelRunningWorker(identifier, func() {
		// Worker wasn't running — clear the marker so it doesn't accidentally pause.
		o.userCancelledMu.Lock()
		delete(o.userCancelledIDs, identifier)
		o.userCancelledMu.Unlock()
	})
	if cancelled {
		return nil
	}

	// No live worker — check if the issue is in the retry queue.
	o.snapMu.RLock()
	var retryIssueID string
	for issueID, entry := range o.lastSnap.RetryAttempts {
		if entry != nil && entry.Identifier == identifier {
			retryIssueID = issueID
			break
		}
	}
	o.snapMu.RUnlock()

	if retryIssueID == "" {
		return ErrNotFound
	}

	select {
	case o.events <- OrchestratorEvent{Type: EventCancelRetry, IssueID: retryIssueID, Identifier: identifier}:
	default:
		metrics.EventDropped() // CORE-045
		return ErrBusy         // channel full; caller can retry
	}
	slog.Info("orchestrator: retry-queue cancel queued", "identifier", identifier)
	return nil
}

// ResumeIssue removes a paused issue from the pause set, allowing it to be
// dispatched again on the next tick.
// Returns nil if the issue was paused and the resume was queued, ErrNotFound
// if the issue was not paused, or ErrBusy if the event channel was full.
// Safe to call from any goroutine.
func (o *Orchestrator) ResumeIssue(identifier string) error {
	if o.isDraining() { // CORE-057
		return ErrDraining
	}
	o.snapMu.RLock()
	_, isPaused := o.lastSnap.PausedIdentifiers[identifier]
	o.snapMu.RUnlock()
	if !isPaused {
		return ErrNotFound
	}
	// Route state mutation through the event loop so the change is applied to
	// state.PausedIdentifiers (the event loop's source of truth), not just to
	// the lastSnap copy — which would be overwritten on the next storeSnap.
	select {
	case o.events <- OrchestratorEvent{Type: EventResumeIssue, Identifier: identifier}:
	default:
		metrics.EventDropped() // CORE-045
		return ErrBusy         // channel full; caller can retry
	}
	slog.Info("orchestrator: issue resume queued", "identifier", identifier)
	return nil
}

// TerminateIssue discards an issue without adding it to PausedIdentifiers:
//   - If a worker is running, it is cancelled; the claim is released when
//     the event loop processes the worker's exit.
//   - If the issue is paused, it is removed from PausedIdentifiers (and its
//     captured session dropped) instead of being resumed.
//
// In both cases the event loop then moves the issue in the tracker via
// asyncDiscardAndTransition: to the first tracker.backlog_states entry, else
// the first tracker.active_states entry. The move is skipped when neither
// list is set or the issue UUID is unknown (a legacy paused entry). Only the
// active_states fallback leaves the issue eligible for re-dispatch.
//
// Returns nil if any action was taken (worker cancel or paused-removal
// queued), ErrNotFound if the issue is neither running nor paused, or
// ErrBusy if the event channel was full.
// Safe to call from any goroutine.
func (o *Orchestrator) TerminateIssue(identifier string) error {
	// Clean up the per-issue backend override so stale entries don't persist.
	o.issueBackendsMu.Lock()
	delete(o.issueBackends, identifier)
	o.issueBackendsMu.Unlock()

	// Read both paused and running state under a single RLock so the two
	// checks are consistent with the same snapshot.
	o.snapMu.RLock()
	issueID, isPaused := o.lastSnap.PausedIdentifiers[identifier]
	var isRunning bool
	for _, entry := range o.lastSnap.Running {
		if entry.Issue.Identifier == identifier {
			isRunning = true
			break
		}
	}
	o.snapMu.RUnlock()

	if isPaused {
		// Route state mutation through the event loop (same reason as ResumeIssue).
		select {
		case o.events <- OrchestratorEvent{Type: EventTerminatePaused, Identifier: identifier, IssueID: issueID}:
		default:
			metrics.EventDropped() // CORE-045
			return ErrBusy         // channel full; caller can retry
		}
		slog.Info("orchestrator: paused issue terminate queued", "identifier", identifier)
		return nil
	}

	if !isRunning {
		return ErrNotFound
	}

	// Route the running-worker terminate through the event loop so it is
	// serialised with EventWorkerExited. The event loop handler will re-verify
	// that the worker is still in state.Running before setting userTerminatedIDs
	// and calling cancel — eliminating the TOCTOU window where a natural exit
	// races with the user cancel and causes wasTerminatedByUser to misfire (GO-R5-3).
	select {
	case o.events <- OrchestratorEvent{Type: EventTerminateRunning, Identifier: identifier}:
	default:
		metrics.EventDropped() // CORE-045
		return ErrBusy         // channel full; caller can retry
	}
	slog.Info("orchestrator: running issue terminate queued", "identifier", identifier)
	return nil
}

// ReanalyzeIssue moves a paused issue from the pause set to the ForceReanalyze queue
// so that the next dispatch cycle runs the agent again, bypassing the open-PR guard.
// Returns ErrNotFound if the issue is not currently paused, ErrBusy if the
// event channel is full, or nil once the re-analysis is queued.
// Safe to call from any goroutine.
func (o *Orchestrator) ReanalyzeIssue(identifier string) error {
	if o.isDraining() { // CORE-057
		return ErrDraining
	}
	// Read-only check: is the issue actually paused?
	o.snapMu.RLock()
	_, paused := o.lastSnap.PausedIdentifiers[identifier]
	o.snapMu.RUnlock()
	if !paused {
		return ErrNotFound
	}
	// Route state mutation through the event loop — avoids concurrent map access
	// between this goroutine and the event loop which reads state.ForceReanalyze.
	select {
	case o.events <- OrchestratorEvent{Type: EventForceReanalyze, Identifier: identifier}:
	default:
		metrics.EventDropped() // CORE-045
		// Event channel full; caller can retry.
		return ErrBusy
	}
	slog.Info("orchestrator: issue queued for forced re-analysis", "identifier", identifier)
	return nil
}

// SetIssueProfile sets (or clears) a named agent profile override for a specific issue.
// Pass an empty profileName to reset the issue to the default profile.
// Safe to call from any goroutine.
func (o *Orchestrator) SetIssueProfile(identifier, profileName string) {
	o.issueProfilesMu.Lock()
	if profileName == "" {
		delete(o.issueProfiles, identifier)
	} else {
		o.issueProfiles[identifier] = profileName
	}
	// An operator's choice replaces a pending reviewer injection: the next
	// run is theirs, not a (read-only) reviewer's (#79).
	delete(o.reviewerInjectedProfiles, identifier)
	o.issueProfilesMu.Unlock()
	slog.Info("orchestrator: issue profile updated", "identifier", identifier, "profile", profileName)
	if o.OnStateChange != nil {
		o.OnStateChange()
	}
}

// SetIssueBackend sets (or clears) a per-issue backend override.
// Pass an empty backend to reset the issue to the default backend.
// Safe to call from any goroutine.
func (o *Orchestrator) SetIssueBackend(identifier, backend string) {
	o.issueBackendsMu.Lock()
	if backend == "" {
		delete(o.issueBackends, identifier)
	} else {
		o.issueBackends[identifier] = backend
	}
	o.issueBackendsMu.Unlock()
	slog.Info("orchestrator: issue backend updated", "identifier", identifier, "backend", backend)
	if o.OnStateChange != nil {
		o.OnStateChange()
	}
}

// GetRunningIssue returns a copy of the domain.Issue for the currently running
// worker identified by identifier, or nil if no such worker is running.
// Safe to call from any goroutine.
func (o *Orchestrator) GetRunningIssue(identifier string) *domain.Issue {
	o.snapMu.RLock()
	defer o.snapMu.RUnlock()
	for _, entry := range o.lastSnap.Running {
		if entry.Issue.Identifier == identifier {
			issue := entry.Issue
			return &issue
		}
	}
	return nil
}

// cancelRunningWorker looks up a live worker cancel func by identifier and calls
// it if found, returning true. If no live cancel func is registered (the worker
// is not running), cleanupFn is called (to clear the caller's side-channel
// marker) and false is returned.
// Must NOT be called with workerCancelsMu held.
func (o *Orchestrator) cancelRunningWorker(identifier string, cleanupFn func()) bool {
	o.workerCancelsMu.Lock()
	cancel, ok := o.workerCancels[identifier]
	o.workerCancelsMu.Unlock()
	if ok {
		cancel()
		return true
	}
	if cleanupFn != nil {
		cleanupFn()
	}
	return false
}

// ProvideInput sends the user's message to an input-required issue, resuming
// the agent session. It performs NO lookup — it is a bare non-blocking send —
// so the only way it can fail is a full event channel: it returns ErrBusy in
// that case and nil once the message is queued. Whether the issue was
// actually in the input-required queue is the event loop's decision, not
// this call's (CORE-005); a queued send for an issue that turns out not to
// be waiting for input is a no-op there.
// Safe to call from any goroutine.
func (o *Orchestrator) ProvideInput(identifier, message string) error {
	if o.isDraining() { // CORE-057: the resume would start a worker
		return ErrDraining
	}
	select {
	case o.events <- OrchestratorEvent{
		Type:       EventProvideInput,
		Identifier: identifier,
		Message:    message,
	}:
		return nil
	default:
		metrics.EventDropped() // CORE-045
		slog.Warn("orchestrator: provide-input event channel full", "identifier", identifier)
		return ErrBusy
	}
}

// DismissInput moves an input-required issue to paused state without
// providing input. Like ProvideInput, it performs no lookup: the only
// failure mode is a full event channel (ErrBusy); nil once queued
// (CORE-005).
// Safe to call from any goroutine.
func (o *Orchestrator) DismissInput(identifier string) error {
	select {
	case o.events <- OrchestratorEvent{
		Type:       EventDismissInput,
		Identifier: identifier,
	}:
		return nil
	default:
		metrics.EventDropped() // CORE-045
		slog.Warn("orchestrator: dismiss-input event channel full", "identifier", identifier)
		return ErrBusy
	}
}
