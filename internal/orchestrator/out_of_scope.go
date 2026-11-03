package orchestrator

import (
	"log/slog"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// leftTrackerScope reports whether a fresh by-ID read of an issue no longer
// matches the tracker's filter (#198), evaluated in memory with the same
// predicate the scoped reads compile. Only a definitive no counts: a
// tracker without a filter, or an answer that waits on an unresolved
// assignee, keeps the issue.
func leftTrackerScope(tr tracker.Tracker, issue domain.Issue) bool {
	m, ok := tr.(tracker.ScopeMatcher)
	if !ok {
		return false
	}
	in, known := m.MatchesFilter(issue)
	return known && !in
}

// observeOutOfScope records, from a successful candidate poll, the running
// issues that left the tracker's scope (#182): still in an active state
// (ReconcileTrackerStates reads them by ID, unscoped) but absent from the
// scoped poll — reassigned away from an assignee scope, moved to a project
// outside the project filter, or no longer matching the tracker filter
// (#198: the poll is that filter compiled by the tracker).
//
// Leaving the scope is not a cancel: the running worker finishes. The marker
// keeps a new run from starting — the poll no longer offers the issue, and
// fireRetries releases a retry whose issue carries it — and the dashboard
// shows the run as out of scope. A marked issue that is no longer running
// keeps its marker only while a retry for it is pending; one back in the
// poll loses it.
//
// Event-loop state, session-scoped, not persisted.
func (o *Orchestrator) observeOutOfScope(state *State, candidates []domain.Issue) {
	polled := make(map[string]struct{}, len(candidates))
	for i := range candidates {
		polled[candidates[i].ID] = struct{}{}
	}
	next := make(map[string]struct{})
	for id, r := range state.Running {
		if r == nil {
			continue
		}
		if _, ok := polled[id]; ok || !isActiveState(r.Issue.State, *state) {
			continue
		}
		next[r.Issue.Identifier] = struct{}{}
		if _, already := state.OutOfScope[r.Issue.Identifier]; !already {
			slog.Info("orchestrator: running issue left the tracker scope; the run finishes, no new run is dispatched",
				"identifier", r.Issue.Identifier, "state", r.Issue.State)
			if o.logBuf != nil {
				o.logBuf.Add(r.Issue.Identifier, makeBufLine("INFO",
					"worker: issue left the tracker scope (reassigned or moved) — this run finishes, no new run is dispatched"))
			}
		}
	}
	for id, retry := range state.RetryAttempts {
		if retry == nil {
			continue
		}
		if _, was := state.OutOfScope[retry.Identifier]; !was {
			continue
		}
		if _, back := polled[id]; !back {
			next[retry.Identifier] = struct{}{}
		}
	}
	state.OutOfScope = next
}
