package orchestrator

import (
	"time"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/outbox"
)

// reconcileAndOverlayOutbox runs the write-ahead-outbox's per-tick
// tracker-authoritative reconciliation, then overlays each surviving
// pending update_state entry's TargetState onto its issue in `issues`
// (mutated in place, by index) — see
// docs/superpowers/specs/2026-08-06-write-ahead-outbox-design.md,
// "Overlay" and "Reconciliation". Called from onTick right after
// FetchCandidateIssues, before ReconcileInferredDeps / CandidateSeen /
// BuildTickGraph / the dispatch-eligibility loop run over `issues` — so
// every one of those consumers sees the overlaid state, and a
// completed-but-unflushed issue is never re-dispatched (the load-bearing
// property this exists for).
//
// Order matters: reconciliation runs BEFORE the overlay, even though the
// spec presents them in the opposite order. Decision 1 (the spec's
// "Decisions" section) is "tracker-authoritative + pending overlay": a
// human's tracker-side change wins the moment no local write is pending
// for that issue. If the overlay ran first, a "superseded_by_tracker"
// entry would still overlay its now-stale TargetState onto this tick's
// candidate before reconciliation got a chance to drop it — the tick would
// show the human's real tracker state overridden by a target that's about
// to be discarded anyway. Reconciling first means the overlay only ever
// considers entries that are still genuinely pending after this tick's
// tracker-authoritative check.
//
// Nil-safe: when o.outbox is nil (cfg.Tracker.Outbox=false, the kill
// switch — SetOutbox is never called), both steps no-op and this returns
// an empty set.
//
// Returns the set of issue identifiers overlaid this tick, for the caller
// to publish onto State.OutboxSyncing (empty when o.outbox is nil).
func (o *Orchestrator) reconcileAndOverlayOutbox(issues []domain.Issue, now time.Time) map[string]struct{} {
	syncing := make(map[string]struct{})
	if o.outbox == nil {
		return syncing
	}

	for i := range issues {
		issue := &issues[i]
		if issue.ID == "" {
			continue
		}

		o.reconcilePendingOutboxEntries(*issue, now)

		target, ok := lastPendingUpdateStateTarget(o.outbox.PendingFor(issue.ID))
		if !ok {
			continue
		}
		issue.State = target
		if issue.Identifier != "" {
			syncing[issue.Identifier] = struct{}{}
		}
	}
	return syncing
}

// reconcilePendingOutboxEntries drops update_state entries for issue that
// the tracker's own polled data (this tick's fetch — no extra reads) has
// already resolved, per outbox.ReconcileVerdict's two reconciliation rules
// (already_applied / superseded_by_tracker — see that function's doc for
// the full rule text; it is the single shared implementation, also used by
// cmd/itervox's flusher-owned absent-issue batch pass, issue #54's
// fast-follow).
//
// now is accepted for symmetry with the rest of the tick's reconcile-style
// helpers (ReconcileStalls, ReconcileTrackerStates) but unused here — both
// rules compare against the entry's own EnqueuedAt / the polled issue's
// UpdatedAt, not wall-clock now.
//
// create_comment entries are never reconciled here — no reliable dedupe
// signal exists for a posted comment (unlike a state transition, "was this
// comment already posted" can't be read back off the polled issue). They
// only leave the outbox via a successful flush or an explicit operator
// discard (a later task's DELETE /api/v1/outbox/{id} endpoint).
func (o *Orchestrator) reconcilePendingOutboxEntries(issue domain.Issue, _ time.Time) {
	for _, entry := range o.outbox.PendingFor(issue.ID) {
		if entry.Kind != outbox.KindUpdateState {
			continue
		}
		if drop, reason := outbox.ReconcileVerdict(entry, issue.State, issue.UpdatedAt); drop {
			o.outbox.Drop(entry.ID, reason)
		}
	}
}

// lastPendingUpdateStateTarget returns the TargetState of the most
// recently enqueued (last in FIFO order) update_state entry in pending, if
// any. Per-issue FIFO ordering means the flusher still delivers entries
// strictly in enqueue order (earliest first) — but for the OVERLAY (what
// the dashboard should show as "about to be true"), a later enqueue
// represents more recent intent than an earlier one still in flight ahead
// of it, so later intent wins here even though delivery order is
// unchanged. Example: an issue is enqueued Done, then (before that flushes)
// re-enqueued In Review — the overlay shows In Review, matching what the
// operator most recently asked for, even though the flusher will still
// deliver the Done transition first.
func lastPendingUpdateStateTarget(pending []outbox.Entry) (string, bool) {
	for i := len(pending) - 1; i >= 0; i-- {
		if pending[i].Kind == outbox.KindUpdateState {
			return pending[i].TargetState, true
		}
	}
	return "", false
}
