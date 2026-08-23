package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/logbuffer"
	"github.com/vnovick/itervox/internal/tracker"
)

// ReconcileStalls checks all running sessions for stall timeout violations.
// If stall_timeout_ms <= 0, stall detection is disabled and this is a no-op.
// The optional logBuf receives a stall-warning entry before the worker is killed.
//
// cfg.Agent.StallTimeoutMs is read without cfgMu because it is set only once at
// startup — there is no HTTP setter for this field, so there is no concurrent
// writer and no data race. See CLAUDE.md "cfgMu scope" for the full list of
// fields that require locking.
// cfg.Agent.MaxRetryBackoffMs is also read without cfgMu for the same reason:
// no HTTP setter exists for this field.
// CleanupWorker cancels and de-registers a worker by identifier. Provided by
// the orchestrator (`o.cancelAndCleanupWorker`) so reconcile can release the
// cancel func from workerCancels even if the EventWorkerExited send drops
// under load. Tests may pass nil, in which case reconcile falls back to the
// entry's local WorkerCancel and the workerCancels-map cleanup is skipped
// (acceptable in tests because tests don't populate the map).
type CleanupWorker = func(identifier string)

func cancelOrFallback(cleanup CleanupWorker, identifier string, fallback context.CancelFunc) {
	if cleanup != nil {
		cleanup(identifier)
		return
	}
	if fallback != nil {
		fallback()
	}
}

func ReconcileStalls(state State, cfg *config.Config, now time.Time, events chan OrchestratorEvent, cleanup CleanupWorker, logBuf ...*logbuffer.Buffer) State {
	if cfg.Agent.StallTimeoutMs <= 0 {
		return state
	}
	stallDur := time.Duration(cfg.Agent.StallTimeoutMs) * time.Millisecond

	var buf *logbuffer.Buffer
	if len(logBuf) > 0 {
		buf = logBuf[0]
	}

	for id, entry := range state.Running {
		var elapsed time.Duration
		if entry.LastEventAt != nil {
			elapsed = now.Sub(*entry.LastEventAt)
		} else {
			elapsed = now.Sub(entry.StartedAt)
		}
		if elapsed > stallDur {
			slog.Warn("stall detected: killing worker",
				"issue_id", id,
				"issue_identifier", entry.Issue.Identifier,
				"elapsed_ms", elapsed.Milliseconds(),
			)
			// Emit stall warning to per-issue log buffer before killing (#6).
			if buf != nil {
				msg := fmt.Sprintf("worker: ⚠ stall detected — no output for %.0fs, killing worker", elapsed.Seconds())
				buf.Add(entry.Issue.Identifier, makeBufLine("WARN", msg))
			}
			cancelOrFallback(cleanup, entry.Issue.Identifier, entry.WorkerCancel)
			delete(state.Running, id)
			delete(state.Claimed, id)
			prevAttempt := 0
			if re, ok := state.RetryAttempts[id]; ok {
				prevAttempt = re.Attempt
			}
			state = ScheduleRetry(state, id, prevAttempt+1, entry.Issue.Identifier, "stall_timeout", now, BackoffMs(prevAttempt+1, cfg.Agent.MaxRetryBackoffMs))
			// Include the RunEntry so handleEvent can record stall history.
			// Claim and retry management have already been performed inline above;
			// the event loop will see TerminalStalled and only call recordHistory.
			stalledEntry := RunEntry{
				Issue:          entry.Issue,
				StartedAt:      entry.StartedAt,
				TurnCount:      entry.TurnCount,
				TotalTokens:    entry.TotalTokens,
				InputTokens:    entry.InputTokens,
				OutputTokens:   entry.OutputTokens,
				SessionID:      entry.SessionID,
				WorkerHost:     entry.WorkerHost,
				Backend:        entry.Backend,
				ProfileName:    entry.ProfileName,
				TerminalReason: TerminalStalled,
			}
			select {
			case events <- OrchestratorEvent{Type: EventWorkerExited, IssueID: id, RunEntry: &stalledEntry}:
			case <-time.After(100 * time.Millisecond):
				slog.Warn("orchestrator: event send timed out in reconcile", "issue_id", id)
			}
		}
	}
	return state
}

// ReconcileTrackerStates fetches current states for all running issues and
// reconciles: terminal→cleanup, active→update snapshot, neither→stop no cleanup.
// If the fetch fails, workers are kept and the error is logged.
// The optional logBuf receives per-issue explanatory messages when workers are stopped.
// State.ActiveStates and State.TerminalStates are used for comparison; these are
// snapshotted from cfg under cfgMu at the start of each tick, so no lock is needed here.
func ReconcileTrackerStates(ctx context.Context, state State, tr tracker.Tracker, events chan OrchestratorEvent, cleanup CleanupWorker, logBuf ...*logbuffer.Buffer) State {
	var buf *logbuffer.Buffer
	if len(logBuf) > 0 {
		buf = logBuf[0]
	}
	if len(state.Running) == 0 {
		return state
	}

	ids := make([]string, 0, len(state.Running))
	for id := range state.Running {
		ids = append(ids, id)
	}

	refreshed, err := tr.FetchIssueStatesByIDs(ctx, ids)
	if err != nil {
		slog.Warn("reconciliation: tracker refresh failed, keeping workers", "error", err)
		return state
	}

	byID := make(map[string]string, len(refreshed))
	for _, issue := range refreshed {
		byID[issue.ID] = issue.State
	}

	now := time.Now()
	for id, entry := range state.Running {
		refreshedState, found := byID[id]
		if !found {
			slog.Info("reconciliation: issue not found in tracker, stopping worker",
				"issue_id", id, "issue_identifier", entry.Issue.Identifier)
			if buf != nil {
				buf.Add(entry.Issue.Identifier, makeBufLine("WARN", "worker: ⚠ issue no longer found in tracker — worker stopped"))
			}
			cancelOrFallback(cleanup, entry.Issue.Identifier, entry.WorkerCancel)
			delete(state.Running, id)
			delete(state.Claimed, id)
			select {
			case events <- OrchestratorEvent{Type: EventWorkerExited, IssueID: id}:
			case <-time.After(100 * time.Millisecond):
				slog.Warn("orchestrator: event send timed out in reconcile", "issue_id", id)
			}
			continue
		}

		// Automation-dispatched workers (Kind=="automation") run by design on
		// non-active and terminal-state issues — see CRIT-3 invariant in
		// automation.go and event_loop.go:1031. The isActiveState gate that
		// reconciles regular workers must NOT apply to them: triggers like
		// issue_moved_to_backlog or input_required scheduled on Backlog/Done
		// issues need the worker to keep running until it finishes naturally.
		// Without this guard, ReconcileTrackerStates races automation
		// dispatch and clears state.Running before the worker exits,
		// stranding the AutomationID/TriggerType from history (F-1).
		if entry.Kind == "automation" {
			entry.Issue.State = refreshedState
			entry.LastEventAt = &now
			continue
		}
		if isTerminalState(refreshedState, state) {
			slog.Info("reconciliation: terminal state, stopping worker",
				"issue_id", id, "issue_identifier", entry.Issue.Identifier, "state", refreshedState)
			if buf != nil {
				buf.Add(entry.Issue.Identifier, makeBufLine("INFO", fmt.Sprintf("worker: issue moved to terminal state %q — worker stopped", refreshedState)))
			}
			cancelOrFallback(cleanup, entry.Issue.Identifier, entry.WorkerCancel)
			delete(state.Running, id)
			delete(state.Claimed, id)
			select {
			case events <- OrchestratorEvent{
				Type:    EventWorkerExited,
				IssueID: id,
				RunEntry: &RunEntry{
					Issue: entry.Issue,
					// Kind MUST be carried through. The exit handler routes on
					// it via runEligibleForAutoReview, which treats an empty
					// Kind as "a plain worker finished" and queues an
					// auto-review. Dropping it here meant a REVIEWER stopped
					// by this very branch looked like a fresh worker success,
					// so the handler dispatched another reviewer, which this
					// branch stopped again — an unbounded re-dispatch loop
					// that spawns agents (and burns tracker/API budget) for as
					// long as the issue sits in a terminal state. Reproduced
					// at 23-30 RunTurn calls per 1.5s against an expected 2.
					Kind:           entry.Kind,
					ProfileName:    entry.ProfileName,
					BranchName:     entry.BranchName,
					TerminalReason: TerminalCanceledByReconciliation,
				},
			}:
			case <-time.After(100 * time.Millisecond):
				slog.Warn("orchestrator: event send timed out in reconcile", "issue_id", id)
			}
		} else if isActiveState(refreshedState, state) {
			entry.Issue.State = refreshedState
			entry.LastEventAt = &now
		} else {
			slog.Info("reconciliation: non-active state, stopping worker without cleanup",
				"issue_id", id, "issue_identifier", entry.Issue.Identifier, "state", refreshedState)
			if buf != nil {
				buf.Add(entry.Issue.Identifier, makeBufLine("WARN", fmt.Sprintf("worker: ⚠ issue state changed to %q (not in active_states) — worker stopped", refreshedState)))
			}
			cancelOrFallback(cleanup, entry.Issue.Identifier, entry.WorkerCancel)
			delete(state.Running, id)
			delete(state.Claimed, id)
			select {
			case events <- OrchestratorEvent{Type: EventWorkerExited, IssueID: id}:
			case <-time.After(100 * time.Millisecond):
				slog.Warn("orchestrator: event send timed out in reconcile", "issue_id", id)
			}
		}
	}
	return state
}
