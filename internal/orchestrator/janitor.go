package orchestrator

import (
	"time"
)

// issueStatusHistoryRetention bounds how long a per-issue status-history slice
// (and the matching PrevIssueStates entry) may live after the issue stops
// appearing in the candidate set. Entries inside this window are kept even
// if the issue is no longer fetched, so a brief tracker hiccup does not lose
// observation history. Configurable via WORKFLOW.md is deferred to a future
// release; the constant matches the value documented in the v0.2.0 audit.
const issueStatusHistoryRetention = 7 * 24 * time.Hour

// pruneIssueStatusHistory removes IssueStatusHistory + PrevIssueStates outer-map
// entries whose identifier is absent from `candidates` AND whose most-recent
// recorded change is older than `retention`. Identifiers still in `candidates`
// survive regardless of age — live issues are never pruned by this pass.
//
// The two maps are pruned in lockstep: any identifier dropped from one must be
// dropped from the other, otherwise the next observation would silently lose
// the from-state hint.
//
// Returns (statusRemoved, prevRemoved) so onTick can log non-zero passes for
// observability without forcing a log line on every quiet tick.
//
// INVARIANT: must only be called from the single event-loop goroutine.
func pruneIssueStatusHistory(state *State, candidates map[string]struct{}, now time.Time, retention time.Duration) (int, int) {
	statusRemoved := 0
	prevRemoved := 0
	if retention <= 0 {
		retention = issueStatusHistoryRetention
	}

	for id, history := range state.IssueStatusHistory {
		if _, live := candidates[id]; live {
			continue
		}
		mostRecent := mostRecentChangeAt(history)
		if !mostRecent.IsZero() && now.Sub(mostRecent) < retention {
			continue
		}
		delete(state.IssueStatusHistory, id)
		statusRemoved++
		if _, has := state.PrevIssueStates[id]; has {
			delete(state.PrevIssueStates, id)
			prevRemoved++
		}
	}

	// PrevIssueStates may carry identifiers that never produced a history
	// entry (e.g. an issue observed exactly once before leaving the candidate
	// set). Without history, there is no timestamp to apply the retention
	// rule against, so the absence-from-candidates check stands alone for
	// these entries.
	for id := range state.PrevIssueStates {
		if _, live := candidates[id]; live {
			continue
		}
		if _, hasHistory := state.IssueStatusHistory[id]; hasHistory {
			continue
		}
		delete(state.PrevIssueStates, id)
		prevRemoved++
	}

	return statusRemoved, prevRemoved
}

// mostRecentChangeAt returns the most-recent At from a history slice. Returns
// a zero time when the slice is empty or every At is zero (treated as "very
// old" by the caller so retention does not preserve the entry indefinitely).
func mostRecentChangeAt(history []IssueStatusChange) time.Time {
	var latest time.Time
	for i := range history {
		at := history[i].At
		if at.After(latest) {
			latest = at
		}
	}
	return latest
}

// pruneTerminalDependencyAudit removes DependencyAudit entries whose
// last-observed issue state is terminal AND no live AutomationQueue entry
// references the identifier AND no worker is currently running for it.
//
// A queued blockers_resolved automation that has not fired yet must keep its
// audit row, otherwise the dispatch-time blocker check would lose the
// resolved-blocker context. Same rationale applies to running workers: cancel
// paths and ReconcileStalls may inspect the audit row before clearing it.
//
// Returns the count of removed rows so onTick can log non-zero passes.
//
// INVARIANT: must only be called from the single event-loop goroutine.
func pruneTerminalDependencyAudit(state *State) int {
	if len(state.DependencyAudit) == 0 {
		return 0
	}
	queueIdentifiers := automationQueueIdentifiers(state)
	removed := 0
	for key, entry := range state.DependencyAudit {
		if entry == nil {
			delete(state.DependencyAudit, key)
			removed++
			continue
		}
		if !isTerminalState(entry.IssueState, *state) {
			continue
		}
		if _, queued := queueIdentifiers[entry.Identifier]; queued {
			continue
		}
		if entry.IssueID != "" {
			if _, queuedByID := queueIdentifiers[entry.IssueID]; queuedByID {
				continue
			}
		}
		if _, running := state.Running[entry.Identifier]; running {
			continue
		}
		delete(state.DependencyAudit, key)
		removed++
	}
	return removed
}

// automationQueueIdentifiers collects every issue identifier and ID currently
// referenced by the AutomationQueue, so the dependency-audit janitor can keep
// audit rows alive while a queued automation still needs them.
func automationQueueIdentifiers(state *State) map[string]struct{} {
	if len(state.AutomationQueue) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(state.AutomationQueue))
	for _, entry := range state.AutomationQueue {
		if entry == nil {
			continue
		}
		if entry.Issue.Identifier != "" {
			out[entry.Issue.Identifier] = struct{}{}
		}
		if entry.Issue.ID != "" {
			out[entry.Issue.ID] = struct{}{}
		}
	}
	return out
}
