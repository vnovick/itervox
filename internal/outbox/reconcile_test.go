package outbox_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/vnovick/itervox/internal/outbox"
)

func timePtr(t time.Time) *time.Time { return &t }

// TestReconcileVerdictAlreadyApplied pins rule 1: polled state equals
// TargetState drops unconditionally, regardless of UpdatedAt.
func TestReconcileVerdictAlreadyApplied(t *testing.T) {
	enqueuedAt := time.Now()
	e := outbox.Entry{TargetState: "Done", EnqueuedAt: enqueuedAt}

	drop, reason := outbox.ReconcileVerdict(e, "Done", nil)
	assert.True(t, drop)
	assert.Equal(t, "already_applied", reason)
}

// TestReconcileVerdictSuperseded pins rule 2: a newer UpdatedAt than
// EnqueuedAt, with a polled state different from BOTH TargetState and the
// entry's FromState baseline, drops as superseded_by_tracker — the
// human-wins decision.
//
// FromState is load-bearing here and was added to this fixture when rule 2
// gained its baseline requirement. A fresher UpdatedAt alone is no longer
// sufficient: the outbox bumps UpdatedAt itself when it flushes the session
// comment that the completion path enqueues ahead of the transition, and
// reading that as a human edit dropped the transition and re-dispatched
// completed work. See supersededByTracker.
func TestReconcileVerdictSuperseded(t *testing.T) {
	enqueuedAt := time.Now()
	e := outbox.Entry{TargetState: "Done", FromState: "In Progress", EnqueuedAt: enqueuedAt}

	newer := enqueuedAt.Add(time.Hour)
	drop, reason := outbox.ReconcileVerdict(e, "In Review", timePtr(newer))
	assert.True(t, drop)
	assert.Equal(t, "superseded_by_tracker", reason)
}

// TestReconcileVerdictKeepsWhenPolledUpdateIsOlder proves an UpdatedAt
// older than (or equal to) EnqueuedAt never triggers the superseded rule —
// the tracker snapshot is stale relative to our own write.
func TestReconcileVerdictKeepsWhenPolledUpdateIsOlder(t *testing.T) {
	enqueuedAt := time.Now()
	// FromState is set and DIFFERENT from the polled state so the stale
	// timestamp is the only thing preventing a supersede. Without a
	// baseline, supersededByTracker returns early and this test would pass
	// for a reason unrelated to the UpdatedAt guard it names.
	e := outbox.Entry{TargetState: "Done", FromState: "Todo", EnqueuedAt: enqueuedAt}

	older := enqueuedAt.Add(-time.Hour)
	drop, reason := outbox.ReconcileVerdict(e, "In Progress", timePtr(older))
	assert.False(t, drop)
	assert.Empty(t, reason)
}

// TestReconcileVerdictNilUpdatedAtNeverSupersedes is the safe-subset rule:
// without a real UpdatedAt to compare against EnqueuedAt, only the
// already_applied rule may fire — a nil UpdatedAt with a differing polled
// state must never be treated as superseded.
func TestReconcileVerdictNilUpdatedAtNeverSupersedes(t *testing.T) {
	enqueuedAt := time.Now()
	// Baseline set, and the polled state differs from both it and the
	// target — so a nil UpdatedAt is the only reason this must not
	// supersede. Leaving FromState empty would short-circuit first and the
	// nil guard would go unexercised.
	e := outbox.Entry{TargetState: "Done", FromState: "Todo", EnqueuedAt: enqueuedAt}

	drop, reason := outbox.ReconcileVerdict(e, "Backlog", nil)
	assert.False(t, drop)
	assert.Empty(t, reason)
}

// TestReconcileVerdictKeepsWhenStatesMatchButNoNewerUpdate proves the
// "otherwise keep" default: polled state differs from target and either
// there's no UpdatedAt or it doesn't postdate EnqueuedAt.
func TestReconcileVerdictKeepsOtherwise(t *testing.T) {
	enqueuedAt := time.Now()
	e := outbox.Entry{TargetState: "Done", EnqueuedAt: enqueuedAt}

	sameInstant := enqueuedAt
	drop, reason := outbox.ReconcileVerdict(e, "In Progress", timePtr(sameInstant))
	assert.False(t, drop, "UpdatedAt equal to (not strictly after) EnqueuedAt must not supersede")
	assert.Empty(t, reason)
}

// TestReconcileVerdictKeepsWhenOnlyOurOwnWriteBumpedUpdatedAt pins the
// default completion path. worker.go enqueues the session comment before
// the completion transition, both for the same issue, so the comment is the
// per-issue FIFO head and flushes first. Posting it bumps the tracker's
// UpdatedAt without changing State. Rule 2 must not read that as "a human
// moved this issue" — the queued transition is still valid and must be
// kept, or the issue never reaches its completion state and is
// re-dispatched with the work already done.
func TestReconcileVerdictKeepsWhenOnlyOurOwnWriteBumpedUpdatedAt(t *testing.T) {
	enqueuedAt := time.Now()
	e := outbox.Entry{TargetState: "Done", FromState: "In Progress", EnqueuedAt: enqueuedAt}

	// Our own comment flushed after the transition was queued.
	afterOurComment := enqueuedAt.Add(time.Minute)
	drop, reason := outbox.ReconcileVerdict(e, "In Progress", timePtr(afterOurComment))
	assert.False(t, drop, "an UpdatedAt bump with no state change must not supersede")
	assert.Empty(t, reason)
}

// TestReconcileVerdictSupersededRequiresActualStateChange keeps the
// human-wins semantics rule 2 exists for: the tracker state genuinely moved
// away from what we observed at enqueue time, to something that is not our
// target. That is a human, and the human wins.
func TestReconcileVerdictSupersededRequiresActualStateChange(t *testing.T) {
	enqueuedAt := time.Now()
	e := outbox.Entry{TargetState: "Done", FromState: "In Progress", EnqueuedAt: enqueuedAt}

	newer := enqueuedAt.Add(time.Hour)
	drop, reason := outbox.ReconcileVerdict(e, "Cancelled", timePtr(newer))
	assert.True(t, drop)
	assert.Equal(t, "superseded_by_tracker", reason)
}

// TestReconcileVerdictUnknownFromStateNeverSupersedes is the fail-safe
// degradation for an entry that carries no baseline. Every in-tree enqueue
// site now supplies one, so this guards the contract rather than a live
// caller: a persisted entry written by an older build, or a future call site
// that cannot observe the state, must not be superseded on a bare UpdatedAt
// bump. Keeping such a write costs a redundant transition; dropping one
// silently loses it.
//
// Note the cost of that choice, documented so it is a decision and not an
// oversight: a baseline-less entry can ALSO never yield to a human's move,
// so it retries until it lands or an operator discards it.
func TestReconcileVerdictUnknownFromStateNeverSupersedes(t *testing.T) {
	enqueuedAt := time.Now()
	e := outbox.Entry{TargetState: "Done", EnqueuedAt: enqueuedAt} // FromState unset

	newer := enqueuedAt.Add(time.Hour)
	drop, reason := outbox.ReconcileVerdict(e, "Cancelled", timePtr(newer))
	assert.False(t, drop, "no enqueue-time baseline: must keep rather than guess")
	assert.Empty(t, reason)
}
