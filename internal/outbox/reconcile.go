package outbox

import "time"

// ReconcileVerdict evaluates whether a pending KindUpdateState Entry should
// be dropped, given the tracker's currently polled state and UpdatedAt for
// e.IssueID. It is the single implementation of the write-ahead-outbox's
// tracker-authoritative reconciliation rules — see
// docs/superpowers/specs/2026-08-06-write-ahead-outbox-design.md,
// "Reconciliation", and issue #54's fast-follow.
//
// Two call sites share this logic (extracted here — internal/outbox owns
// the Entry type — so the rules exist in exactly one place instead of being
// hand-duplicated across packages):
//   - internal/orchestrator's per-tick overlay path
//     (reconcilePendingOutboxEntries in outbox_overlay.go), which only ever
//     examines issues present in the CURRENT tick's active-states candidate
//     fetch.
//   - cmd/itervox's flusher-owned absent-issue batch pass
//     (runAbsentIssueReconcileTick in outbox_flusher.go), which examines
//     EVERY issue with a pending update_state entry regardless of whether
//     it's a candidate this tick — closing the gap the per-tick path leaves
//     when a human moves an issue OUT of active states while a write is
//     pending for it.
//
// Rules, in priority order:
//  1. polledState == e.TargetState -> already applied (the write landed,
//     possibly via a path other than the caller, or a race with the
//     caller's own success) -> (true, "already_applied").
//  2. polledUpdatedAt is non-nil AND after e.EnqueuedAt AND polledState !=
//     e.TargetState -> the tracker moved to a DIFFERENT state after this
//     write was queued (most likely a human) -> the human wins ->
//     (true, "superseded_by_tracker").
//  3. otherwise -> (false, "") — keep, unchanged; the flusher keeps trying.
//
// A nil polledUpdatedAt means rule 2 can never fire for this call: only
// rule 1 can safely evaluate without a real UpdatedAt to compare against
// EnqueuedAt. Both known Tracker adapters (Linear, GitHub) populate
// UpdatedAt on every issue FetchIssueStatesByIDs returns, but the Tracker
// interface itself does not guarantee it, so this stays a defensive nil
// check rather than an assumption.
//
// This function only implements the drop rules for KindUpdateState entries.
// create_comment entries are never reconciled this way (no reliable dedupe
// signal exists for "was this comment already posted" — the caller is
// responsible for filtering to KindUpdateState before calling this at all;
// ReconcileVerdict does not itself check e.Kind).
func ReconcileVerdict(e Entry, polledState string, polledUpdatedAt *time.Time) (drop bool, reason string) {
	switch {
	case polledState == e.TargetState:
		return true, "already_applied"
	case supersededByTracker(e, polledState, polledUpdatedAt):
		return true, "superseded_by_tracker"
	default:
		return false, ""
	}
}

// supersededByTracker reports whether the tracker's own state genuinely
// moved away from this entry's baseline, to something that is not what the
// entry asks for.
//
// A fresher UpdatedAt alone is NOT sufficient evidence, because the outbox
// bumps UpdatedAt itself: entries are per-issue FIFO, and the completion
// path enqueues the session comment (worker.go) before the completion
// transition for the same issue, so the comment always flushes first and
// advances UpdatedAt without touching State. Treating that as a human edit
// dropped the queued transition, left the issue in its active state, and
// re-dispatched work that had already succeeded.
//
// Requiring polledState != e.FromState is what separates the two: a comment
// flush leaves State equal to the baseline, a human move does not.
//
// An empty FromState means the enqueue site could not observe the issue's
// state, so there is no baseline and the two cases are indistinguishable.
// Fail safe by keeping the entry: a redundant transition is recoverable,
// a silently dropped one is not.
func supersededByTracker(e Entry, polledState string, polledUpdatedAt *time.Time) bool {
	if polledUpdatedAt == nil || !polledUpdatedAt.After(e.EnqueuedAt) {
		return false
	}
	if e.FromState == "" {
		return false
	}
	return polledState != e.TargetState && polledState != e.FromState
}
