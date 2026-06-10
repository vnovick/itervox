package orchestrator

import (
	"strings"
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

// LedgerJanitorCounts groups the per-map prune totals so the two ledger
// janitors (B2 terminal, B9.b absent) share a single return shape. Caller
// folds the counts into the slog "janitor pass" line.
type LedgerJanitorCounts struct {
	InputRequired int
	Retry         int
	Queue         int
	Paused        int
	Profile       int
	Backend       int
}

// pruneMap walks `m`, derives the identifier for each entry via `identOf`,
// and deletes any entry whose identifier is rejected by `keep`. Returns the
// removed-count so callers can log non-zero passes. Generic over the value
// type so the same shape covers map[string]*InputRequiredEntry,
// map[string]*RetryEntry, map[string]string, etc.
func pruneMap[V any](
	m map[string]V,
	identOf func(key string, val V) string,
	keep func(ident string) bool,
) int {
	removed := 0
	for key, val := range m {
		if keep(identOf(key, val)) {
			continue
		}
		delete(m, key)
		removed++
	}
	return removed
}

// identOfKey returns the map key as the identifier. Used for ledgers that
// key directly by identifier (InputRequiredIssues, IssueProfiles, etc.).
func identOfKey[V any](key string, _ V) string { return key }

// identOfRetry resolves the identifier from a RetryEntry. RetryAttempts is
// keyed by issueID but identifier-presence checks expect the human-readable
// identifier. Falls back to the issueID when the entry doesn't carry one
// (defensive: legacy persistence shapes).
func identOfRetry(issueID string, entry *RetryEntry) string {
	if entry != nil && entry.Identifier != "" {
		return entry.Identifier
	}
	return issueID
}

// identOfPROpened pulls the issue identifier out of the composite dedup key
// `<identifier>|<prURL>|<automationID>`. Returns the map key itself when the
// separator is missing so the entry survives — better to keep a malformed
// row than to silently drop dedup evidence.
func identOfPROpened(key string, _ struct{}) string {
	if sep := strings.IndexByte(key, '|'); sep > 0 {
		return key[:sep]
	}
	return key
}

// pausedCleanup is the cascade-delete companion to pruneMap for
// PausedIdentifiers / PausedSessions. The paused-sessions map is keyed by
// both identifier and issueID, so a delete needs to clear both keys.
func pausedCleanup(state *State, keep func(ident string) bool) int {
	removed := 0
	for ident, issueID := range state.PausedIdentifiers {
		if keep(ident) {
			continue
		}
		delete(state.PausedIdentifiers, ident)
		delete(state.PausedSessions, ident)
		delete(state.PausedSessions, issueID)
		removed++
	}
	return removed
}

// pruneAutomationQueue drops AutomationQueue entries whose embedded
// issue.Identifier is rejected by `keep`, and rebuilds AutomationQueueOrder
// in lockstep so the FIFO surface stays consistent.
func pruneAutomationQueue(state *State, keep func(ident string) bool) int {
	if len(state.AutomationQueue) == 0 {
		return 0
	}
	keepEntry := func(entry *AutomationQueueEntry) bool {
		return entry != nil && keep(entry.Issue.Identifier)
	}
	removed := 0
	newOrder := make([]string, 0, len(state.AutomationQueueOrder))
	for _, key := range state.AutomationQueueOrder {
		if keepEntry(state.AutomationQueue[key]) {
			newOrder = append(newOrder, key)
			continue
		}
		if _, ok := state.AutomationQueue[key]; ok {
			delete(state.AutomationQueue, key)
			removed++
		}
	}
	state.AutomationQueueOrder = newOrder
	// Sweep any map-only entries (defensive: shouldn't happen but tests
	// may exercise the path).
	for key, entry := range state.AutomationQueue {
		if keepEntry(entry) {
			continue
		}
		delete(state.AutomationQueue, key)
		removed++
	}
	return removed
}

// pruneTerminalRuntimeLedgers sweeps ledger maps for issues whose current
// tracker state is terminal (CompletionState / FailedState / any
// TerminalStates member). v0.2.0 todolist5 B2.
//
// Without this sweep, an issue that the agent moved to "Done" via direct
// tracker API leaves residue in these ledgers indefinitely.
//
// `terminalIdentifiers` is the set of identifiers whose snapshot state is
// terminal — caller builds it from PrevIssueStates. Identifiers absent from
// the snapshot are NOT pruned here (B9.b handles absence separately).
//
// INVARIANT: must only be called from the single event-loop goroutine.
func pruneTerminalRuntimeLedgers(state *State, terminalIdentifiers map[string]struct{}) LedgerJanitorCounts {
	if len(terminalIdentifiers) == 0 {
		return LedgerJanitorCounts{}
	}
	keep := func(ident string) bool {
		_, terminal := terminalIdentifiers[ident]
		return !terminal
	}
	// v0.2.0 todolist5 B4 — PROpenedDispatched dedup keys carry the
	// identifier as the first segment. Pruned in the same pass so a
	// re-opened issue starts with a fresh dispatch budget.
	pruneMap(state.PROpenedDispatched, identOfPROpened, keep)
	counts := LedgerJanitorCounts{
		InputRequired: pruneMap(state.InputRequiredIssues, identOfKey, keep),
		Retry:         pruneMap(state.RetryAttempts, identOfRetry, keep),
		Queue:         pruneAutomationQueue(state, keep),
		Paused:        pausedCleanup(state, keep),
	}
	// codex-B2 — surface per-identifier removals as status-history rows so
	// the per-issue timeline explains the disappearance.
	if counts.InputRequired+counts.Retry+counts.Queue+counts.Paused > 0 {
		now := time.Now()
		for ident := range terminalIdentifiers {
			appendIssueStatusChange(state, IssueStatusChange{
				Identifier: ident,
				ToState:    state.PrevIssueStates[ident],
				Source:     StatusSourceJanitor,
				Reason:     JanitorReasonIssueTerminal,
				At:         now,
			})
		}
	}
	return counts
}

// pruneAbsentTrackerIssues sweeps ledger entries for identifiers absent from
// both the current tick's candidate set and the previous tick's set. The
// two-tick grace window tolerates a single transient tracker miss.
//
// In-flight workers (state.Running) are deliberately NOT touched — a worker
// may be mid-write to a workspace / branch / PR and killing it
// asynchronously is a data-loss hazard. EventWorkerExited cleans Running.
//
// v0.2.0 todolist5 B9.b.
//
// INVARIANT: must only be called from the single event-loop goroutine.
func pruneAbsentTrackerIssues(state *State, currentActive map[string]struct{}) LedgerJanitorCounts {
	// Persistence-replay safety: on the very first ticks after a daemon
	// restart, PrevActiveIdentifiers and PrevIssueStates are empty.
	// Persistence-loaded ledger entries would be pruned immediately if we
	// ran the sweep before observation history exists.
	if len(state.PrevActiveIdentifiers) == 0 && len(state.PrevIssueStates) == 0 {
		return LedgerJanitorCounts{}
	}
	keep := buildPresentPredicate(state, currentActive)
	return LedgerJanitorCounts{
		InputRequired: pruneMap(state.InputRequiredIssues, identOfKey, keep),
		Retry:         pruneMap(state.RetryAttempts, identOfRetry, keep),
		Queue:         pruneAutomationQueue(state, keep),
		Paused:        pausedCleanup(state, keep),
		Profile:       pruneMap(state.IssueProfiles, identOfKey, keep),
		Backend:       pruneMap(state.IssueBackends, identOfKey, keep),
	}
}

// buildPresentPredicate returns the "is this identifier still observed?"
// closure used by both ledger janitors. An identifier counts as present
// when ANY of the following holds:
//   - currentActive (this tick's poll) contains it
//   - PrevActiveIdentifiers (previous tick) contains it
//   - state.Running has a worker for it (in-flight, never prune sibling ledgers)
//   - PrevIssueStates has a last-observed state for it
//   - DependencyAudit references it (backlog-targeted automations operate on
//     identifiers absent from the active poll but still tracker-resident)
func buildPresentPredicate(state *State, currentActive map[string]struct{}) func(string) bool {
	auditIdents := dependencyAuditIdentifiers(state)
	return func(id string) bool {
		if _, ok := currentActive[id]; ok {
			return true
		}
		if _, ok := state.PrevActiveIdentifiers[id]; ok {
			return true
		}
		if _, ok := state.Running[id]; ok {
			return true
		}
		if _, ok := state.PrevIssueStates[id]; ok {
			return true
		}
		_, ok := auditIdents[id]
		return ok
	}
}

// dependencyAuditIdentifiers flattens DependencyAudit entries to a set of
// identifiers so the presence predicate can use a constant-time lookup.
func dependencyAuditIdentifiers(state *State) map[string]struct{} {
	if len(state.DependencyAudit) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(state.DependencyAudit))
	for _, audit := range state.DependencyAudit {
		if audit == nil || audit.Identifier == "" {
			continue
		}
		out[audit.Identifier] = struct{}{}
	}
	return out
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
