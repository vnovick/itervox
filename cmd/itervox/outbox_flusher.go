package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/outbox"
	"github.com/vnovick/itervox/internal/tracker"
)

// outboxRefresher is the narrow surface the flusher needs from
// *orchestrator.Orchestrator — same "interface named for what the caller
// needs, not what the concrete type offers" convention as
// deps_auto_analyze.go's depsAutoAnalyzeEnqueuer. Lets tests inject a fake
// that just counts calls instead of standing up a full Orchestrator (with
// its own tracker/runner/workspace) to observe the post-flush refresh
// signal.
type outboxRefresher interface {
	Refresh()
}

// outboxFlushInterval is how often startOutboxFlusher polls the outbox for
// due entries (spec: "Own ticker (5s)"). A var (not const) so whitebox
// tests can shrink it instead of waiting through real 5s ticks to observe
// multiple ticker fires / ctx-cancel behavior — same convention as
// write_sink.go's transitionRetryBaseDelay.
var outboxFlushInterval = 5 * time.Second

// outboxFlushCallTimeout bounds each individual tracker call the flusher
// makes (spec: "per-call timeout (30s)").
const outboxFlushCallTimeout = 30 * time.Second

// absentReconcileInterval is how often runAbsentIssueReconcileTick's
// batch reconciliation pass may run — issue #54's fast-follow.
//
// Problem: internal/orchestrator/outbox_overlay.go's
// reconcilePendingOutboxEntries only ever examines issues present in the
// CURRENT tick's active-states candidate fetch. A human moving an issue OUT
// of active states while an update_state entry is pending for it is
// therefore invisible to that path — the entry keeps retrying and the
// flusher eventually overrides the human's move (the "human wins" decision
// documented on reconcilePendingOutboxEntries never gets a chance to apply).
//
// Fix: this batch pass runs in the FLUSHER goroutine, not the orchestrator
// event loop — CLAUDE.md's single-goroutine state machine rule is exactly
// why this can't live in the event loop (the event loop must never do
// network reads outside FetchCandidateIssues). It fetches current tracker
// state for every distinct issue with a pending update_state entry
// (regardless of whether that issue happens to be a candidate this tick)
// and applies the SAME two reconciliation rules
// reconcilePendingOutboxEntries applies, closing the gap.
//
// Gated via absentIssueReconciler.due(now), not a second time.Ticker, so
// tests can drive the pacing with controlled `now` values instead of
// sleeping through real 60s waits.
const absentReconcileInterval = 60 * time.Second

// absentIssueReconciler paces runAbsentIssueReconcileTick to roughly once
// per absentReconcileInterval even though it is checked on every (much more
// frequent, 5s default) outboxFlushInterval tick. Owned by the flusher
// goroutine; safe for the single caller startOutboxFlusher has, and mutex-
// guarded so tests can also drive it directly and concurrently-safely.
type absentIssueReconciler struct {
	mu      sync.Mutex
	lastRun time.Time // zero value: never run yet.
}

// due reports whether at least absentReconcileInterval has elapsed since the
// last call that returned true, and if so records now as the new lastRun.
// The first call on a fresh absentIssueReconciler always returns true
// (lastRun starts zero) — the pass runs promptly once at startup rather
// than waiting a full interval before ever reconciling anything.
func (r *absentIssueReconciler) due(now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.lastRun.IsZero() && now.Sub(r.lastRun) < absentReconcileInterval {
		return false
	}
	r.lastRun = now
	return true
}

// startOutboxFlusher runs the write-ahead-outbox delivery goroutine: every
// outboxFlushInterval it asks ob for this tick's due entries and delivers
// each to tr (the RAW tracker — the flusher IS the delivery mechanism, not
// a WriteSink; see write_sink.go's outboxWriteSink, which only enqueues and
// never calls the tracker itself). Started from run() in main.go beside
// startAutomations, only when cfg.Tracker.Outbox is true.
//
// The flusher shares no budget or locks with the orchestrator's polling
// reads — that separation (an independent goroutine that cannot be starved
// by read-path rate-limit exhaustion) is the structural fix this whole
// feature exists for (see the design spec's "Problem" section, #42-E).
//
// ob and tr must be non-nil; a nil ob or tr is a caller error (cmd/itervox
// only calls this when cfg.Tracker.Outbox gated construction succeeded) and
// this no-ops defensively rather than panicking a daemon goroutine.
func startOutboxFlusher(ctx context.Context, ob *outbox.Outbox, tr tracker.Tracker, orch outboxRefresher) {
	if ob == nil || tr == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(outboxFlushInterval)
		defer ticker.Stop()
		absentReconciler := &absentIssueReconciler{}
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				now := time.Now()
				runOutboxFlusherTick(ctx, ob, tr, orch, now)
				if absentReconciler.due(now) {
					runAbsentIssueReconcileTick(ctx, ob, tr)
				}
			}
		}
	}()
}

// runOutboxFlusherTick performs one flusher tick: it delivers every entry
// ob.Due(now) returns, sequentially, stopping early if ctx is cancelled
// mid-tick. Extracted from startOutboxFlusher's goroutine body so tests can
// drive a single tick directly with an injected `now` (and an injected
// ob.SetNow clock for backoff assertions) instead of waiting on a real
// ticker — same convention as cmd/itervox/deps_auto_analyze.go's
// runDepsAutoAnalyzeTick.
//
// Sequential, not concurrent: ob.Due already returns at most one entry per
// issue (the FIFO head), so cross-issue delivery could in principle run in
// parallel, but the spec is explicit ("for each entry (sequentially...)")
// and a single in-flight tracker call at a time keeps flusher behavior easy
// to reason about and trivially serializes with any other tracker caller.
func runOutboxFlusherTick(ctx context.Context, ob *outbox.Outbox, tr tracker.Tracker, orch outboxRefresher, now time.Time) {
	for _, entry := range ob.Due(now) {
		if ctx.Err() != nil {
			return
		}
		flushOutboxEntry(ctx, ob, tr, orch, entry, now)
	}
}

// flushOutboxEntry delivers a single outbox entry to tr and records the
// result on ob: MarkFlushed on success (plus orch.Refresh() for
// KindUpdateState, so the next poll observes the transition promptly — the
// orchestrator's own next tick's overlay/reconciliation then cleans up the
// now-empty PendingFor for this issue), MarkFailed (with the tracker error,
// which schedules the next backoff attempt) on failure.
func flushOutboxEntry(ctx context.Context, ob *outbox.Outbox, tr tracker.Tracker, orch outboxRefresher, entry outbox.Entry, now time.Time) {
	callCtx, cancel := context.WithTimeout(ctx, outboxFlushCallTimeout)
	defer cancel()

	var flushErr error
	switch entry.Kind {
	case outbox.KindUpdateState:
		flushErr = tr.UpdateIssueState(callCtx, entry.IssueID, entry.TargetState)
	case outbox.KindCreateComment:
		_, flushErr = tr.CreateComment(callCtx, entry.IssueID, entry.Body)
	default:
		// validateEntry (internal/outbox) rejects unknown kinds at Enqueue
		// time, so this is unreachable in practice — guarded defensively so
		// a future EntryKind addition fails loudly (a stuck entry logged
		// every tick) instead of silently retrying forever with no tracker
		// call ever attempted.
		slog.Error("outbox flusher: entry has unknown kind, cannot deliver",
			"id", entry.ID, "issue_id", entry.IssueID, "kind", entry.Kind)
		return
	}

	if flushErr != nil {
		ob.MarkFailed(entry.ID, flushErr, now)
		slog.Warn("outbox flusher: flush failed, will retry",
			"id", entry.ID, "issue_id", entry.IssueID, "identifier", entry.Identifier,
			"kind", entry.Kind, "attempts", entry.Attempts+1, "error", flushErr)
		return
	}

	ob.MarkFlushed(entry.ID)
	if entry.Kind == outbox.KindUpdateState && orch != nil {
		orch.Refresh()
	}
}

// runAbsentIssueReconcileTick batch-fetches current tracker state for every
// distinct issue with a pending update_state outbox entry (via
// tr.FetchIssueStatesByIDs) and applies the same two reconciliation rules
// internal/orchestrator/outbox_overlay.go's reconcilePendingOutboxEntries
// applies per-tick over the active-states candidate fetch:
//
//   - polled state == TargetState -> already applied (the write landed via
//     some other path, or a race with the flusher's own success) -> drop.
//   - polled UpdatedAt is after the entry's EnqueuedAt AND polled state !=
//     TargetState -> the tracker moved to a DIFFERENT state after this
//     write was queued (most likely a human) -> the human wins -> drop.
//   - otherwise: keep, unchanged.
//
// A nil UpdatedAt on a returned issue (not expected from Linear or GitHub
// today — both populate it — but not guaranteed by the Tracker interface)
// means only the already_applied rule can safely apply to it: the
// superseded rule needs a real UpdatedAt to compare against EnqueuedAt, and
// the `issue.UpdatedAt != nil` guard below is what enforces that safe
// subset.
//
// Soft-fail: a FetchIssueStatesByIDs error is logged at Debug and this tick
// is skipped entirely — reads must never block writes, the flusher's
// delivery path (runOutboxFlusherTick) is completely unaffected by a failure
// here. Issues absent from the tracker's response (e.g. deleted or
// transferred) are left pending untouched — the existing dangling posture;
// the operator remedy is the Outbox panel's Discard / DELETE
// /api/v1/outbox/{id}.
//
// create_comment entries are never examined here, for the same reason
// reconcilePendingOutboxEntries skips them: there is no reliable dedupe
// signal for "was this comment already posted" to read back off a polled
// issue.
func runAbsentIssueReconcileTick(ctx context.Context, ob *outbox.Outbox, tr tracker.Tracker) {
	ids := pendingUpdateStateIssueIDs(ob)
	if len(ids) == 0 {
		return
	}

	callCtx, cancel := context.WithTimeout(ctx, outboxFlushCallTimeout)
	defer cancel()

	issues, err := tr.FetchIssueStatesByIDs(callCtx, ids)
	if err != nil {
		slog.Debug("outbox flusher: absent-issue reconcile fetch failed, skipping this round",
			"error", err, "issue_count", len(ids))
		return
	}

	byID := make(map[string]domain.Issue, len(issues))
	for _, issue := range issues {
		byID[issue.ID] = issue
	}

	for _, entry := range ob.Snapshot() {
		if entry.Kind != outbox.KindUpdateState {
			continue
		}
		issue, found := byID[entry.IssueID]
		if !found {
			continue // absent from the tracker's response — leave pending.
		}
		switch {
		case issue.State == entry.TargetState:
			ob.Drop(entry.ID, "already_applied")
		case issue.UpdatedAt != nil && issue.UpdatedAt.After(entry.EnqueuedAt) && issue.State != entry.TargetState:
			ob.Drop(entry.ID, "superseded_by_tracker")
		}
	}
}

// pendingUpdateStateIssueIDs returns the distinct IssueIDs of every pending
// update_state entry in ob, in first-seen order. Used to build the batch
// FetchIssueStatesByIDs request for runAbsentIssueReconcileTick.
func pendingUpdateStateIssueIDs(ob *outbox.Outbox) []string {
	seen := make(map[string]struct{})
	var ids []string
	for _, entry := range ob.Snapshot() {
		if entry.Kind != outbox.KindUpdateState {
			continue
		}
		if _, ok := seen[entry.IssueID]; ok {
			continue
		}
		seen[entry.IssueID] = struct{}{}
		ids = append(ids, entry.IssueID)
	}
	return ids
}
