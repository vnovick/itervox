package orchestrator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/outbox"
)

// timePtr is a small helper for building domain.Issue.UpdatedAt values in
// these whitebox reconciliation/overlay tests.
func timePtr(t time.Time) *time.Time { return &t }

// TestReconcileAndOverlayOutboxNilOutboxIsNoOp is the explicit kill-switch
// nil-safety test: an Orchestrator that never had SetOutbox called (the
// cfg.Tracker.Outbox=false path) must leave `issues` completely untouched
// and return an empty syncing set — every read site in this file must be
// nil-guarded, not just "happens to not panic in this particular test".
func TestReconcileAndOverlayOutboxNilOutboxIsNoOp(t *testing.T) {
	o := &Orchestrator{}
	issues := []domain.Issue{{ID: "id-a", Identifier: "ENG-A", State: "In Progress"}}

	syncing := o.reconcileAndOverlayOutbox(issues, time.Now())

	assert.Empty(t, syncing)
	assert.Equal(t, "In Progress", issues[0].State, "nil outbox must never mutate issue state")
}

// TestReconcileAndOverlayOutboxAppliesLastPendingTarget is the load-bearing
// overlay behavior at the unit level: two pending update_state entries for
// the same issue (neither reconciled away — the polled state matches
// neither target), the overlay must use the LAST enqueued entry's
// TargetState (most recent intent), and mark the issue's identifier as
// syncing.
func TestReconcileAndOverlayOutboxAppliesLastPendingTarget(t *testing.T) {
	ob, err := outbox.New("")
	require.NoError(t, err)
	o := &Orchestrator{}
	o.SetOutbox(ob)

	require.NoError(t, ob.Enqueue(outbox.Entry{Kind: outbox.KindUpdateState, IssueID: "id-a", Identifier: "ENG-A", TargetState: "Done"}))
	require.NoError(t, ob.Enqueue(outbox.Entry{Kind: outbox.KindUpdateState, IssueID: "id-a", Identifier: "ENG-A", TargetState: "Merged"}))

	issues := []domain.Issue{{ID: "id-a", Identifier: "ENG-A", State: "In Progress"}}
	syncing := o.reconcileAndOverlayOutbox(issues, time.Now())

	assert.Equal(t, "Merged", issues[0].State, "overlay must use the most-recently-enqueued target")
	_, ok := syncing["ENG-A"]
	assert.True(t, ok, "the overlaid issue's identifier must be in the syncing set")
	assert.Len(t, ob.PendingFor("id-a"), 2, "reconciliation must not have dropped either entry (neither matches nor is superseded)")
}

// TestReconcileAndOverlayOutboxAlreadyAppliedDropped covers reconciliation
// rule 1: polled state == TargetState drops the entry (already_applied),
// and — because that was the issue's only pending entry — the overlay has
// nothing left to apply, so the issue is not marked syncing.
func TestReconcileAndOverlayOutboxAlreadyAppliedDropped(t *testing.T) {
	ob, err := outbox.New("")
	require.NoError(t, err)
	o := &Orchestrator{}
	o.SetOutbox(ob)

	require.NoError(t, ob.Enqueue(outbox.Entry{Kind: outbox.KindUpdateState, IssueID: "id-a", Identifier: "ENG-A", TargetState: "Done"}))

	// The tracker's own polled data already shows "Done" — the write
	// landed (e.g. via a path other than the flusher, or a race with a
	// flush success on a previous tick).
	issues := []domain.Issue{{ID: "id-a", Identifier: "ENG-A", State: "Done"}}
	syncing := o.reconcileAndOverlayOutbox(issues, time.Now())

	assert.Empty(t, ob.PendingFor("id-a"), "already_applied entry must be dropped")
	assert.Equal(t, "Done", issues[0].State)
	_, ok := syncing["ENG-A"]
	assert.False(t, ok, "no pending entry survives, so nothing should be marked syncing")
}

// TestReconcileAndOverlayOutboxSupersededDropped covers reconciliation
// rule 2 (decision 1 — "the human wins"): the tracker's polled UpdatedAt is
// after the entry's EnqueuedAt AND the polled state differs from
// TargetState — the entry is dropped as superseded_by_tracker, and the
// overlay must NOT apply the now-discarded target on top of the human's
// real tracker-side change.
func TestReconcileAndOverlayOutboxSupersededDropped(t *testing.T) {
	ob, err := outbox.New("")
	require.NoError(t, err)
	o := &Orchestrator{}
	o.SetOutbox(ob)

	require.NoError(t, ob.Enqueue(outbox.Entry{Kind: outbox.KindUpdateState, IssueID: "id-a", Identifier: "ENG-A", TargetState: "Done"}))
	enqueued := ob.PendingFor("id-a")
	require.Len(t, enqueued, 1)

	// A human moved the issue to "In Review" in the tracker AFTER this
	// write was enqueued.
	humanChange := enqueued[0].EnqueuedAt.Add(1 * time.Hour)
	issues := []domain.Issue{{ID: "id-a", Identifier: "ENG-A", State: "In Review", UpdatedAt: timePtr(humanChange)}}

	syncing := o.reconcileAndOverlayOutbox(issues, time.Now())

	assert.Empty(t, ob.PendingFor("id-a"), "superseded entry must be dropped")
	assert.Equal(t, "In Review", issues[0].State, "the human's tracker-side state must win — overlay must not reapply the discarded target")
	_, ok := syncing["ENG-A"]
	assert.False(t, ok)
}

// TestReconcileAndOverlayOutboxKeepsWhenPolledUpdateIsOlder proves the
// "keep" branch: the polled UpdatedAt predates the entry's EnqueuedAt (the
// tracker snapshot is stale relative to our own write), so the entry must
// survive reconciliation AND the overlay must still apply its target.
func TestReconcileAndOverlayOutboxKeepsWhenPolledUpdateIsOlder(t *testing.T) {
	ob, err := outbox.New("")
	require.NoError(t, err)
	o := &Orchestrator{}
	o.SetOutbox(ob)

	require.NoError(t, ob.Enqueue(outbox.Entry{Kind: outbox.KindUpdateState, IssueID: "id-a", Identifier: "ENG-A", TargetState: "Done"}))
	enqueued := ob.PendingFor("id-a")
	require.Len(t, enqueued, 1)

	olderUpdate := enqueued[0].EnqueuedAt.Add(-1 * time.Hour)
	issues := []domain.Issue{{ID: "id-a", Identifier: "ENG-A", State: "In Progress", UpdatedAt: timePtr(olderUpdate)}}

	syncing := o.reconcileAndOverlayOutbox(issues, time.Now())

	assert.Len(t, ob.PendingFor("id-a"), 1, "an older polled UpdatedAt must not drop the entry — the flusher keeps trying")
	assert.Equal(t, "Done", issues[0].State, "overlay still applies while the entry survives reconciliation")
	_, ok := syncing["ENG-A"]
	assert.True(t, ok)
}

// TestReconcileAndOverlayOutboxCreateCommentNeverReconciledOrOverlaid pins
// that create_comment entries are excluded from both reconciliation (no
// reliable dedupe signal — see reconcilePendingOutboxEntries's doc
// comment) and the overlay (only update_state entries carry a TargetState
// to overlay).
func TestReconcileAndOverlayOutboxCreateCommentNeverReconciledOrOverlaid(t *testing.T) {
	ob, err := outbox.New("")
	require.NoError(t, err)
	o := &Orchestrator{}
	o.SetOutbox(ob)

	require.NoError(t, ob.Enqueue(outbox.Entry{Kind: outbox.KindCreateComment, IssueID: "id-a", Identifier: "ENG-A", Body: "hello"}))

	// Even though nothing about the polled issue looks like "Done" (a
	// state a create_comment entry doesn't even have), reconciliation must
	// leave it alone regardless of what the tracker reports.
	issues := []domain.Issue{{ID: "id-a", Identifier: "ENG-A", State: "In Progress"}}
	syncing := o.reconcileAndOverlayOutbox(issues, time.Now())

	assert.Len(t, ob.PendingFor("id-a"), 1, "create_comment entries are never reconciled")
	assert.Equal(t, "In Progress", issues[0].State, "a create_comment entry has no TargetState to overlay")
	_, ok := syncing["ENG-A"]
	assert.False(t, ok, "a create_comment-only pending set must not mark the issue syncing")
}

// TestReconcileAndOverlayOutboxSkipsIssuesWithoutID guards the defensive
// `issue.ID == ""` skip in reconcileAndOverlayOutbox — PendingFor is keyed
// by IssueID, so an issue with no ID can never have pending entries and
// must not be looked up (a no-op, not a bug, but worth pinning so a future
// refactor doesn't accidentally call PendingFor("") and match nothing by
// coincidence rather than by design).
func TestReconcileAndOverlayOutboxSkipsIssuesWithoutID(t *testing.T) {
	ob, err := outbox.New("")
	require.NoError(t, err)
	o := &Orchestrator{}
	o.SetOutbox(ob)

	issues := []domain.Issue{{ID: "", Identifier: "ENG-A", State: "In Progress"}}
	syncing := o.reconcileAndOverlayOutbox(issues, time.Now())

	assert.Empty(t, syncing)
	assert.Equal(t, "In Progress", issues[0].State)
}
