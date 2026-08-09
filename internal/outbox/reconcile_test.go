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
// EnqueuedAt, with a polled state different from TargetState, drops as
// superseded_by_tracker — the human-wins decision.
func TestReconcileVerdictSuperseded(t *testing.T) {
	enqueuedAt := time.Now()
	e := outbox.Entry{TargetState: "Done", EnqueuedAt: enqueuedAt}

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
	e := outbox.Entry{TargetState: "Done", EnqueuedAt: enqueuedAt}

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
	e := outbox.Entry{TargetState: "Done", EnqueuedAt: enqueuedAt}

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
