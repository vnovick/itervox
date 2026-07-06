package orchestrator

import (
	"log/slog"

	"github.com/vnovick/itervox/internal/domain"
)

// CancelIssue cancels a running or retry-queued issue and marks it as paused
// so it is not automatically retried. The issue stays paused until ResumeIssue
// is called.
//   - If a live worker exists, it is cancelled immediately.
//   - If the issue is in the retry queue (no live worker), an EventCancelRetry
//     is sent to the event loop which removes the retry entry and pauses the issue.
//
// Returns true if an action was taken, false if the issue is neither running
// nor queued for retry.
// Safe to call from any goroutine.
func (o *Orchestrator) CancelIssue(identifier string) bool {
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
		return true
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
		return false
	}

	select {
	case o.events <- OrchestratorEvent{Type: EventCancelRetry, IssueID: retryIssueID, Identifier: identifier}:
	default:
		return false // channel full; caller can retry
	}
	slog.Info("orchestrator: retry-queue cancel queued", "identifier", identifier)
	return true
}

// ResumeIssue removes a paused issue from the pause set, allowing it to be
// dispatched again on the next tick.
// Returns true if the issue was paused and is now resumed, false if not found.
// Safe to call from any goroutine.
func (o *Orchestrator) ResumeIssue(identifier string) bool {
	o.snapMu.RLock()
	_, isPaused := o.lastSnap.PausedIdentifiers[identifier]
	o.snapMu.RUnlock()
	if !isPaused {
		return false
	}
	// Route state mutation through the event loop so the change is applied to
	// state.PausedIdentifiers (the event loop's source of truth), not just to
	// the lastSnap copy — which would be overwritten on the next storeSnap.
	select {
	case o.events <- OrchestratorEvent{Type: EventResumeIssue, Identifier: identifier}:
	default:
		return false // channel full; caller can retry
	}
	slog.Info("orchestrator: issue resume queued", "identifier", identifier)
	return true
}

// TerminateIssue hard-stops an issue without adding it to PausedIdentifiers:
//   - If a worker is running, it is cancelled and the claim is released
//     (the issue will be re-dispatched on the next poll cycle).
//   - If the issue is paused, it is removed from PausedIdentifiers so it can
//     be re-dispatched without a manual resume.
//
// Returns true if any action was taken (worker cancelled or paused removed).
// Safe to call from any goroutine.
func (o *Orchestrator) TerminateIssue(identifier string) bool {
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
			return false // channel full; caller can retry
		}
		slog.Info("orchestrator: paused issue terminate queued", "identifier", identifier)
		return true
	}

	if !isRunning {
		return false
	}

	// Route the running-worker terminate through the event loop so it is
	// serialised with EventWorkerExited. The event loop handler will re-verify
	// that the worker is still in state.Running before setting userTerminatedIDs
	// and calling cancel — eliminating the TOCTOU window where a natural exit
	// races with the user cancel and causes wasTerminatedByUser to misfire (GO-R5-3).
	select {
	case o.events <- OrchestratorEvent{Type: EventTerminateRunning, Identifier: identifier}:
	default:
		return false // channel full; caller can retry
	}
	slog.Info("orchestrator: running issue terminate queued", "identifier", identifier)
	return true
}

// ReanalyzeIssue moves a paused issue from the pause set to the ForceReanalyze queue
// so that the next dispatch cycle runs the agent again, bypassing the open-PR guard.
// Returns false if the issue is not currently paused or the event channel is full.
// Safe to call from any goroutine.
func (o *Orchestrator) ReanalyzeIssue(identifier string) bool {
	// Read-only check: is the issue actually paused?
	o.snapMu.RLock()
	_, paused := o.lastSnap.PausedIdentifiers[identifier]
	o.snapMu.RUnlock()
	if !paused {
		return false
	}
	// Route state mutation through the event loop — avoids concurrent map access
	// between this goroutine and the event loop which reads state.ForceReanalyze.
	select {
	case o.events <- OrchestratorEvent{Type: EventForceReanalyze, Identifier: identifier}:
	default:
		// Event channel full; caller can retry.
		return false
	}
	slog.Info("orchestrator: issue queued for forced re-analysis", "identifier", identifier)
	return true
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
// the agent session. Returns false if the issue is not in the input-required queue.
// Safe to call from any goroutine.
func (o *Orchestrator) ProvideInput(identifier, message string) bool {
	select {
	case o.events <- OrchestratorEvent{
		Type:       EventProvideInput,
		Identifier: identifier,
		Message:    message,
	}:
		return true
	default:
		slog.Warn("orchestrator: provide-input event channel full", "identifier", identifier)
		return false
	}
}

// DismissInput moves an input-required issue to paused state without providing
// input. Returns false if the issue is not in the input-required queue.
// Safe to call from any goroutine.
func (o *Orchestrator) DismissInput(identifier string) bool {
	select {
	case o.events <- OrchestratorEvent{
		Type:       EventDismissInput,
		Identifier: identifier,
	}:
		return true
	default:
		slog.Warn("orchestrator: dismiss-input event channel full", "identifier", identifier)
		return false
	}
}
