package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/outbox"
	"github.com/vnovick/itervox/internal/tracker"
)

// recordingFlusherTracker wraps MemoryTracker so flusher tests can inject
// per-issue failures (fail the next N UpdateIssueState calls for a given
// issue ID) and inspect call order — the flusher calls the RAW tracker
// directly (never a WriteSink), so these tests exercise exactly that path.
type recordingFlusherTracker struct {
	*tracker.MemoryTracker

	mu                  sync.Mutex
	failUpdateRemaining map[string]int
	callOrder           []string

	// fetchStatesErr, when non-nil, is returned by FetchIssueStatesByIDs
	// instead of delegating to MemoryTracker — used to exercise
	// runAbsentIssueReconcileTick's soft-fail path independently of
	// UpdateIssueState/CreateComment delivery.
	fetchStatesErr   error
	fetchStatesCalls int
	lastFetchIDs     []string

	// fetchStatesDelay, when non-zero, is slept (honoring ctx cancellation)
	// before FetchIssueStatesByIDs returns — used to reproduce a slow-but-
	// succeeding tracker call and prove it no longer blocks the flusher's
	// own delivery ticker (Task 5 fix round, Important #1).
	fetchStatesDelay time.Duration
}

func newRecordingFlusherTracker(issues []domain.Issue) *recordingFlusherTracker {
	return &recordingFlusherTracker{
		MemoryTracker:       tracker.NewMemoryTracker(issues, nil, nil),
		failUpdateRemaining: make(map[string]int),
	}
}

// failNextUpdate causes the next n UpdateIssueState calls for issueID to
// fail before falling through to MemoryTracker's real (always-succeeds)
// behavior.
func (r *recordingFlusherTracker) failNextUpdate(issueID string, n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failUpdateRemaining[issueID] = n
}

func (r *recordingFlusherTracker) UpdateIssueState(ctx context.Context, issueID, state string) error {
	r.mu.Lock()
	r.callOrder = append(r.callOrder, "update:"+issueID+":"+state)
	if r.failUpdateRemaining[issueID] > 0 {
		r.failUpdateRemaining[issueID]--
		r.mu.Unlock()
		return errors.New("recordingFlusherTracker: injected update failure")
	}
	r.mu.Unlock()
	return r.MemoryTracker.UpdateIssueState(ctx, issueID, state)
}

func (r *recordingFlusherTracker) CreateComment(ctx context.Context, issueID, body string) (*domain.Comment, error) {
	r.mu.Lock()
	r.callOrder = append(r.callOrder, "comment:"+issueID)
	r.mu.Unlock()
	return r.MemoryTracker.CreateComment(ctx, issueID, body)
}

func (r *recordingFlusherTracker) CallOrder() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.callOrder...)
}

// injectFetchStatesErr causes every subsequent FetchIssueStatesByIDs call to
// return err instead of delegating to MemoryTracker. Pass nil to clear.
func (r *recordingFlusherTracker) injectFetchStatesErr(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fetchStatesErr = err
}

// setFetchStatesDelay causes every subsequent FetchIssueStatesByIDs call to
// sleep d (honoring ctx cancellation) before proceeding. Pass 0 to clear.
func (r *recordingFlusherTracker) setFetchStatesDelay(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fetchStatesDelay = d
}

func (r *recordingFlusherTracker) FetchIssueStatesByIDs(ctx context.Context, issueIDs []string) ([]domain.Issue, error) {
	r.mu.Lock()
	r.fetchStatesCalls++
	r.lastFetchIDs = append([]string(nil), issueIDs...)
	err := r.fetchStatesErr
	delay := r.fetchStatesDelay
	r.mu.Unlock()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	if err != nil {
		return nil, err
	}
	return r.MemoryTracker.FetchIssueStatesByIDs(ctx, issueIDs)
}

// FetchStatesCalls reports how many times FetchIssueStatesByIDs has been
// called on this tracker.
func (r *recordingFlusherTracker) FetchStatesCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fetchStatesCalls
}

// LastFetchIDs returns the issueIDs argument of the most recent
// FetchIssueStatesByIDs call.
func (r *recordingFlusherTracker) LastFetchIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.lastFetchIDs...)
}

// fakeRefresher is a counting outboxRefresher for tests that need to
// observe whether/how-many-times orch.Refresh() would have fired, without
// standing up a full *orchestrator.Orchestrator.
type fakeRefresher struct {
	calls atomic.Int64
}

func (f *fakeRefresher) Refresh() { f.calls.Add(1) }

func newTestOutbox(t *testing.T) *outbox.Outbox {
	t.Helper()
	ob, err := outbox.New("") // empty path — in-memory only, no persistence
	require.NoError(t, err)
	return ob
}

// TestOutboxFlusherPerIssueOrderPreservedAcrossMarkFailed is THE flusher
// ordering test the plan calls for: "per-issue order preserved across
// MarkFailed (head retries before successor flushes)". issue A gets two
// queued update_state entries; the first fails once (MarkFailed schedules
// a backoff retry) — the second must NOT be attempted while the first is
// still pending, even across the tick where the first eventually succeeds.
// Also proves the "success on update_state triggers orch.Refresh" seam:
// Refresh fires exactly once per successful update_state flush, never on
// a failed attempt.
func TestOutboxFlusherPerIssueOrderPreservedAcrossMarkFailed(t *testing.T) {
	ob := newTestOutbox(t)

	tr := newRecordingFlusherTracker([]domain.Issue{{ID: "id-a", Identifier: "ENG-A", State: "In Review"}})
	tr.failNextUpdate("id-a", 1)
	refresher := &fakeRefresher{}

	require.NoError(t, ob.Enqueue(outbox.Entry{Kind: outbox.KindUpdateState, IssueID: "id-a", Identifier: "ENG-A", TargetState: "Done"}))
	require.NoError(t, ob.Enqueue(outbox.Entry{Kind: outbox.KindUpdateState, IssueID: "id-a", Identifier: "ENG-A", TargetState: "Merged"}))

	// Due/MarkFailed take `now` as an explicit parameter (see
	// internal/outbox/outbox.go), so these tests control flusher timing
	// entirely through the `now` passed to runOutboxFlusherTick — no need
	// to reach into the Outbox's internal clock (which is only used by
	// Enqueue, an unexported test seam not visible from this external
	// package). base is captured right after Enqueue so both entries'
	// real (wall-clock) NextAttemptAt are already <= base.
	base := time.Now()

	// Tick 1: only the head entry (target Done) is due. It fails.
	runOutboxFlusherTick(context.Background(), ob, tr, refresher, base)
	assert.Equal(t, []string{"update:id-a:Done"}, tr.CallOrder(),
		"successor entry must not be attempted while the head is still pending")
	assert.EqualValues(t, 0, refresher.calls.Load(), "a failed flush must not trigger Refresh")
	pending := ob.PendingFor("id-a")
	require.Len(t, pending, 2, "both entries remain pending after a failed head attempt")
	assert.Equal(t, 1, pending[0].Attempts)
	assert.Equal(t, "Done", pending[0].TargetState)
	assert.Equal(t, "Merged", pending[1].TargetState)

	// Tick 2, before backoff elapses: the head is not yet due again.
	runOutboxFlusherTick(context.Background(), ob, tr, refresher, base)
	assert.Equal(t, []string{"update:id-a:Done"}, tr.CallOrder(),
		"no re-attempt before NextAttemptAt elapses")

	// Advance past the 10s backoff: head retries and succeeds this time.
	afterBackoff := base.Add(11 * time.Second)
	runOutboxFlusherTick(context.Background(), ob, tr, refresher, afterBackoff)
	assert.Equal(t, []string{"update:id-a:Done", "update:id-a:Done"}, tr.CallOrder())
	assert.EqualValues(t, 1, refresher.calls.Load(), "the successful update_state flush must trigger exactly one Refresh")
	pending = ob.PendingFor("id-a")
	require.Len(t, pending, 1, "the flushed head entry is gone; the successor remains")
	assert.Equal(t, "Merged", pending[0].TargetState)

	// Tick 3: the successor is now the head and immediately due.
	runOutboxFlusherTick(context.Background(), ob, tr, refresher, afterBackoff)
	assert.Equal(t, []string{"update:id-a:Done", "update:id-a:Done", "update:id-a:Merged"}, tr.CallOrder())
	assert.EqualValues(t, 2, refresher.calls.Load())
	assert.Empty(t, ob.PendingFor("id-a"), "both entries flushed")

	issue, err := tr.FetchIssueDetail(context.Background(), "id-a")
	require.NoError(t, err)
	assert.Equal(t, "Merged", issue.State, "the tracker ends up in the last-delivered state")
}

// TestOutboxFlusherCrossIssueIndependence proves a due entry for one issue
// is delivered even while another issue's head entry is still pending
// (cross-issue independence — the flusher does not serialize unrelated
// issues behind each other, only same-issue entries behind their own head).
func TestOutboxFlusherCrossIssueIndependence(t *testing.T) {
	ob := newTestOutbox(t)

	tr := newRecordingFlusherTracker([]domain.Issue{
		{ID: "id-a", Identifier: "ENG-A", State: "In Review"},
		{ID: "id-b", Identifier: "ENG-B", State: "In Review"},
	})
	refresher := &fakeRefresher{}

	require.NoError(t, ob.Enqueue(outbox.Entry{Kind: outbox.KindUpdateState, IssueID: "id-a", Identifier: "ENG-A", TargetState: "Done"}))
	require.NoError(t, ob.Enqueue(outbox.Entry{Kind: outbox.KindUpdateState, IssueID: "id-b", Identifier: "ENG-B", TargetState: "Done"}))

	runOutboxFlusherTick(context.Background(), ob, tr, refresher, time.Now())

	order := tr.CallOrder()
	assert.ElementsMatch(t, []string{"update:id-a:Done", "update:id-b:Done"}, order)
	assert.Empty(t, ob.PendingFor("id-a"))
	assert.Empty(t, ob.PendingFor("id-b"))
	assert.EqualValues(t, 2, refresher.calls.Load())
}

// TestOutboxFlusherCommentSuccessDoesNotTriggerRefresh pins the spec's
// asymmetry: only a successful update_state flush triggers Refresh, never
// a create_comment flush.
func TestOutboxFlusherCommentSuccessDoesNotTriggerRefresh(t *testing.T) {
	ob := newTestOutbox(t)

	tr := newRecordingFlusherTracker([]domain.Issue{{ID: "id-a", Identifier: "ENG-A", State: "In Review"}})
	refresher := &fakeRefresher{}

	require.NoError(t, ob.Enqueue(outbox.Entry{Kind: outbox.KindCreateComment, IssueID: "id-a", Identifier: "ENG-A", Body: "hello"}))

	runOutboxFlusherTick(context.Background(), ob, tr, refresher, time.Now())

	assert.Equal(t, []string{"comment:id-a"}, tr.CallOrder())
	assert.Empty(t, ob.PendingFor("id-a"), "the comment entry flushed")
	assert.EqualValues(t, 0, refresher.calls.Load(), "create_comment must never trigger Refresh")
}

// TestOutboxFlusherStopsMidTickOnCtxCancel proves runOutboxFlusherTick
// honors an already-cancelled ctx: with two distinct issues' entries due,
// a pre-cancelled ctx must deliver none of them (the loop's ctx.Err()
// check runs before every entry, including the first).
func TestOutboxFlusherStopsMidTickOnCtxCancel(t *testing.T) {
	ob := newTestOutbox(t)

	tr := newRecordingFlusherTracker([]domain.Issue{
		{ID: "id-a", Identifier: "ENG-A", State: "In Review"},
		{ID: "id-b", Identifier: "ENG-B", State: "In Review"},
	})
	refresher := &fakeRefresher{}

	require.NoError(t, ob.Enqueue(outbox.Entry{Kind: outbox.KindUpdateState, IssueID: "id-a", Identifier: "ENG-A", TargetState: "Done"}))
	require.NoError(t, ob.Enqueue(outbox.Entry{Kind: outbox.KindUpdateState, IssueID: "id-b", Identifier: "ENG-B", TargetState: "Done"}))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	runOutboxFlusherTick(ctx, ob, tr, refresher, time.Now())

	assert.Empty(t, tr.CallOrder(), "no entry should be delivered once ctx is already cancelled")
	assert.Len(t, ob.PendingFor("id-a"), 1)
	assert.Len(t, ob.PendingFor("id-b"), 1)
}

// TestStartOutboxFlusherStopsOnCtxCancel exercises the real goroutine (not
// just runOutboxFlusherTick): cancel ctx, then enqueue a fresh entry — if
// the flusher goroutine were still ticking it would eventually deliver it.
func TestStartOutboxFlusherStopsOnCtxCancel(t *testing.T) {
	prevInterval := outboxFlushInterval
	outboxFlushInterval = 5 * time.Millisecond
	t.Cleanup(func() { outboxFlushInterval = prevInterval })

	ob := newTestOutbox(t)
	tr := newRecordingFlusherTracker(nil)
	refresher := &fakeRefresher{}

	ctx, cancel := context.WithCancel(context.Background())
	startOutboxFlusher(ctx, ob, tr, refresher)

	// Let a few ticks run against an empty outbox (harmless no-ops).
	time.Sleep(30 * time.Millisecond)
	cancel()
	// Give the goroutine a moment to observe ctx.Done and return.
	time.Sleep(20 * time.Millisecond)

	require.NoError(t, ob.Enqueue(outbox.Entry{Kind: outbox.KindUpdateState, IssueID: "id-x", Identifier: "ENG-X", TargetState: "Done"}))
	// If the goroutine were still running, several ticks worth of time is
	// more than enough for it to have picked this up.
	time.Sleep(50 * time.Millisecond)

	assert.Empty(t, tr.CallOrder(), "flusher goroutine must not deliver anything enqueued after ctx cancel")
	assert.EqualValues(t, 0, refresher.calls.Load())
}

// TestStartOutboxFlusherNilSafe pins that a nil outbox or tracker never
// starts a goroutine (defensive no-op, not a panic) — cmd/itervox only
// calls startOutboxFlusher when cfg.Tracker.Outbox gated construction
// succeeded, but this guards against a future call-site mistake.
func TestStartOutboxFlusherNilSafe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	assert.NotPanics(t, func() {
		startOutboxFlusher(ctx, nil, newRecordingFlusherTracker(nil), &fakeRefresher{})
	})
	assert.NotPanics(t, func() {
		startOutboxFlusher(ctx, newTestOutbox(t), nil, &fakeRefresher{})
	})
}

// --- runAbsentIssueReconcileTick: issue #54 fast-follow -------------------
//
// These tests exercise the batch reconciliation pass directly (not through
// the goroutine), the same "extracted tick function, explicit inputs, no
// real ticker" convention as runOutboxFlusherTick's own tests above.

// TestAbsentIssueReconcileDropsSuperseded proves the human-wins rule: a
// pending update_state entry whose issue the tracker now reports in a
// DIFFERENT state, with UpdatedAt after the entry's EnqueuedAt, is dropped
// as superseded — the scenario is a human moving the issue out of active
// states while the entry is pending, which the orchestrator event loop's
// own reconcilePendingOutboxEntries never observes because the issue is no
// longer in this tick's active-states candidate fetch.
func TestAbsentIssueReconcileDropsSuperseded(t *testing.T) {
	ob := newTestOutbox(t)
	require.NoError(t, ob.Enqueue(outbox.Entry{Kind: outbox.KindUpdateState, IssueID: "id-a", Identifier: "ENG-A", TargetState: "Done"}))
	entry := ob.PendingFor("id-a")[0]

	// The tracker's polled state is a DIFFERENT state than the target, with
	// UpdatedAt strictly after the entry's own EnqueuedAt — a human moved it.
	future := entry.EnqueuedAt.Add(time.Hour)
	tr := newRecordingFlusherTracker([]domain.Issue{
		{ID: "id-a", Identifier: "ENG-A", State: "Backlog", UpdatedAt: &future},
	})

	runAbsentIssueReconcileTick(context.Background(), ob, tr)

	assert.Empty(t, ob.PendingFor("id-a"), "superseded entry must be dropped")
}

// TestAbsentIssueReconcileDropsAlreadyApplied proves the already-applied
// rule fires from the batch pass exactly like it does from the per-tick
// event-loop path: if the tracker's polled state already equals the
// entry's TargetState, the entry is dropped regardless of UpdatedAt.
func TestAbsentIssueReconcileDropsAlreadyApplied(t *testing.T) {
	ob := newTestOutbox(t)
	require.NoError(t, ob.Enqueue(outbox.Entry{Kind: outbox.KindUpdateState, IssueID: "id-a", Identifier: "ENG-A", TargetState: "Done"}))

	// Polled state already matches TargetState; UpdatedAt is nil (must not
	// matter for this rule).
	tr := newRecordingFlusherTracker([]domain.Issue{
		{ID: "id-a", Identifier: "ENG-A", State: "Done"},
	})

	runAbsentIssueReconcileTick(context.Background(), ob, tr)

	assert.Empty(t, ob.PendingFor("id-a"), "already-applied entry must be dropped")
}

// TestAbsentIssueReconcileFetchErrorIsSoftFail proves the soft-fail
// contract: when FetchIssueStatesByIDs errors, nothing is dropped this
// round, and delivery (runOutboxFlusherTick) is completely unaffected —
// reads must never block writes.
func TestAbsentIssueReconcileFetchErrorIsSoftFail(t *testing.T) {
	ob := newTestOutbox(t)
	require.NoError(t, ob.Enqueue(outbox.Entry{Kind: outbox.KindUpdateState, IssueID: "id-a", Identifier: "ENG-A", TargetState: "Done"}))

	// This issue's polled state WOULD trigger already_applied if the fetch
	// succeeded — proving the error path, not just "no matching rule", is
	// what prevents the drop.
	tr := newRecordingFlusherTracker([]domain.Issue{
		{ID: "id-a", Identifier: "ENG-A", State: "Done"},
	})
	tr.injectFetchStatesErr(errors.New("boom: tracker unavailable"))

	runAbsentIssueReconcileTick(context.Background(), ob, tr)

	require.Len(t, ob.PendingFor("id-a"), 1, "a fetch error must drop nothing this round")

	// Delivery is unaffected — proved WITHOUT clearing the injected fetch
	// error first: runOutboxFlusherTick never calls FetchIssueStatesByIDs at
	// all, so it must succeed even while that call keeps failing. Clearing
	// the error before checking delivery would only prove sequencing, not
	// decoupling.
	refresher := &fakeRefresher{}
	runOutboxFlusherTick(context.Background(), ob, tr, refresher, time.Now())
	assert.Empty(t, ob.PendingFor("id-a"), "delivery must proceed unaffected by the earlier (and still-active) reconcile fetch error")
	assert.EqualValues(t, 1, refresher.calls.Load())
}

// TestAbsentIssueReconcileNilUpdatedAtOnlyAppliesAlreadyApplied proves the
// safe-subset rule: when a polled issue's UpdatedAt is nil, only the
// already_applied rule may fire for it — the superseded rule requires a
// real UpdatedAt to compare against EnqueuedAt and must never fire without
// one, even when the polled state differs from TargetState.
func TestAbsentIssueReconcileNilUpdatedAtOnlyAppliesAlreadyApplied(t *testing.T) {
	ob := newTestOutbox(t)
	require.NoError(t, ob.Enqueue(outbox.Entry{Kind: outbox.KindUpdateState, IssueID: "id-a", Identifier: "ENG-A", TargetState: "Done"}))
	require.NoError(t, ob.Enqueue(outbox.Entry{Kind: outbox.KindUpdateState, IssueID: "id-b", Identifier: "ENG-B", TargetState: "Done"}))

	tr := newRecordingFlusherTracker([]domain.Issue{
		// id-a: nil UpdatedAt, state differs from target — must NOT be
		// dropped (the superseded rule cannot safely evaluate).
		{ID: "id-a", Identifier: "ENG-A", State: "Backlog", UpdatedAt: nil},
		// id-b: nil UpdatedAt, state equals target — already_applied still
		// fires because it never reads UpdatedAt.
		{ID: "id-b", Identifier: "ENG-B", State: "Done", UpdatedAt: nil},
	})

	runAbsentIssueReconcileTick(context.Background(), ob, tr)

	assert.Len(t, ob.PendingFor("id-a"), 1, "nil UpdatedAt must not allow the superseded rule to fire")
	assert.Empty(t, ob.PendingFor("id-b"), "already_applied does not need UpdatedAt")
}

// TestAbsentIssueReconcileLeavesAbsentIssuesPending proves an issue missing
// from the tracker's response entirely (deleted/transferred) is left
// pending untouched — the existing dangling posture; operator Discard
// remains the remedy.
func TestAbsentIssueReconcileLeavesAbsentIssuesPending(t *testing.T) {
	ob := newTestOutbox(t)
	require.NoError(t, ob.Enqueue(outbox.Entry{Kind: outbox.KindUpdateState, IssueID: "id-ghost", Identifier: "ENG-GHOST", TargetState: "Done"}))

	tr := newRecordingFlusherTracker(nil) // tracker has no issues at all

	runAbsentIssueReconcileTick(context.Background(), ob, tr)

	assert.Len(t, ob.PendingFor("id-ghost"), 1, "an issue absent from the tracker response stays pending")
}

// TestAbsentIssueReconcileSkipsCreateCommentEntries proves create_comment
// entries never enter the batch fetch at all (no reliable dedupe signal
// exists for them, same reason reconcilePendingOutboxEntries skips them).
func TestAbsentIssueReconcileSkipsCreateCommentEntries(t *testing.T) {
	ob := newTestOutbox(t)
	require.NoError(t, ob.Enqueue(outbox.Entry{Kind: outbox.KindCreateComment, IssueID: "id-a", Identifier: "ENG-A", Body: "hello"}))

	tr := newRecordingFlusherTracker([]domain.Issue{{ID: "id-a", Identifier: "ENG-A", State: "Done"}})

	runAbsentIssueReconcileTick(context.Background(), ob, tr)

	assert.Equal(t, 0, tr.FetchStatesCalls(), "a comment-only outbox must never trigger a states fetch")
	assert.Len(t, ob.PendingFor("id-a"), 1, "create_comment entries are never reconciled here")
}

// TestAbsentIssueReconcileBatchesDistinctIssueIDs proves the fetch batches
// exactly the distinct IssueIDs of pending update_state entries — not one
// call per entry, and not including duplicates for an issue with multiple
// queued entries.
func TestAbsentIssueReconcileBatchesDistinctIssueIDs(t *testing.T) {
	ob := newTestOutbox(t)
	require.NoError(t, ob.Enqueue(outbox.Entry{Kind: outbox.KindUpdateState, IssueID: "id-a", Identifier: "ENG-A", TargetState: "Done"}))
	require.NoError(t, ob.Enqueue(outbox.Entry{Kind: outbox.KindUpdateState, IssueID: "id-a", Identifier: "ENG-A", TargetState: "Merged"}))
	require.NoError(t, ob.Enqueue(outbox.Entry{Kind: outbox.KindUpdateState, IssueID: "id-b", Identifier: "ENG-B", TargetState: "Done"}))

	tr := newRecordingFlusherTracker([]domain.Issue{
		{ID: "id-a", Identifier: "ENG-A", State: "In Review"},
		{ID: "id-b", Identifier: "ENG-B", State: "In Review"},
	})

	runAbsentIssueReconcileTick(context.Background(), ob, tr)

	assert.Equal(t, 1, tr.FetchStatesCalls())
	assert.ElementsMatch(t, []string{"id-a", "id-b"}, tr.LastFetchIDs())
}

// TestOutboxFlusherAbsentReconcileDoesNotBlockDelivery reproduces the review
// finding from Task 5's fix round (Important #1): runAbsentIssueReconcileTick
// used to run INLINE on the flusher's own ticker goroutine, so a slow-but-
// succeeding FetchIssueStatesByIDs (up to outboxFlushCallTimeout) blocked
// the flusher from returning to its select loop in time for the NEXT
// delivery tick — the reviewer's probe found an entry due within 10ms
// wasn't delivered until a 250ms fetch returned. That is a read stalling
// the write path, the exact failure mode the outbox exists to prevent.
//
// This test reproduces that shape directly against the real goroutine
// (startOutboxFlusher, not runOutboxFlusherTick called directly): a
// perpetually-pending entry keeps the absent-reconcile batch non-empty, its
// FetchIssueStatesByIDs is slowed to 250ms, and — while that slow fetch is
// still in flight — a second entry is enqueued and must be delivered on the
// flusher's normal (much shorter) tick cadence, not after the slow fetch
// returns.
func TestOutboxFlusherAbsentReconcileDoesNotBlockDelivery(t *testing.T) {
	prevInterval := outboxFlushInterval
	outboxFlushInterval = 15 * time.Millisecond
	t.Cleanup(func() { outboxFlushInterval = prevInterval })

	ob := newTestOutbox(t)
	tr := newRecordingFlusherTracker([]domain.Issue{
		{ID: "stale-1", Identifier: "ENG-STALE", State: "Backlog"},
		{ID: "target-1", Identifier: "ENG-TARGET", State: "Backlog"},
	})
	// stale-1 never succeeds during this test's window, so it stays pending
	// and pendingUpdateStateIssueIDs is never empty — the absent-reconcile
	// pass always has something to fetch.
	tr.failNextUpdate("stale-1", 1000)
	tr.setFetchStatesDelay(250 * time.Millisecond)
	refresher := &fakeRefresher{}

	require.NoError(t, ob.Enqueue(outbox.Entry{Kind: outbox.KindUpdateState, IssueID: "stale-1", Identifier: "ENG-STALE", TargetState: "Done"}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startOutboxFlusher(ctx, ob, tr, refresher)

	// Let the first tick fire: stale-1's delivery attempt fails (expected),
	// and the absent-issue reconciler's first-ever call fires immediately
	// (tryRun's zero-value fast path), kicking off the slow 250ms fetch on
	// its own goroutine.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) && tr.FetchStatesCalls() == 0 {
		time.Sleep(2 * time.Millisecond)
	}
	require.GreaterOrEqual(t, tr.FetchStatesCalls(), 1, "the slow reconcile fetch must have started")

	// While that fetch is still in flight (well under its 250ms delay),
	// enqueue an entry that becomes due immediately.
	require.NoError(t, ob.Enqueue(outbox.Entry{Kind: outbox.KindUpdateState, IssueID: "target-1", Identifier: "ENG-TARGET", TargetState: "Done"}))

	// Poll for delivery well before the slow fetch could possibly have
	// returned (250ms) — under the old inline design this only succeeded
	// after the fetch completed.
	deliverDeadline := time.Now().Add(150 * time.Millisecond)
	delivered := false
	for time.Now().Before(deliverDeadline) {
		if len(ob.PendingFor("target-1")) == 0 {
			delivered = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	assert.True(t, delivered, "target-1 must be delivered on the flusher's normal tick cadence, not blocked behind the in-flight absent-reconcile fetch")
}

// --- absentIssueReconciler.tryRun/finish: interval pacing + overlap guard --

// TestAbsentIssueReconcilerTryRunRespectsInterval proves the pacer fires
// immediately on first use, then withholds until absentReconcileInterval
// has elapsed (per the injected `now`, never a real sleep), then fires
// again once it has — each successful tryRun is immediately finish()ed so
// only the interval (not the in-flight guard) is under test here.
func TestAbsentIssueReconcilerTryRunRespectsInterval(t *testing.T) {
	r := &absentIssueReconciler{}
	base := time.Now()

	require.True(t, r.tryRun(base), "first call always fires")
	r.finish()

	assert.False(t, r.tryRun(base.Add(1*time.Second)), "well within the interval")
	assert.False(t, r.tryRun(base.Add(absentReconcileInterval-time.Second)), "just under the interval")

	require.True(t, r.tryRun(base.Add(absentReconcileInterval)), "exactly at the interval")
	r.finish()

	// After firing again, the clock resets from that point.
	assert.False(t, r.tryRun(base.Add(absentReconcileInterval+time.Second)), "just after the most recent fire")
	require.True(t, r.tryRun(base.Add(2*absentReconcileInterval)), "a full interval after the most recent fire")
	r.finish()
}

// TestAbsentIssueReconcilerTryRunGuardsAgainstOverlap proves a round in
// flight can never be double-started, even once the interval has long
// since elapsed — the guard this fix round added when
// runAbsentIssueReconcileTick moved to its own goroutine (a slow round no
// longer blocks the flusher's delivery ticker, but without this guard a
// second round could now start concurrently with a still-running one).
func TestAbsentIssueReconcilerTryRunGuardsAgainstOverlap(t *testing.T) {
	r := &absentIssueReconciler{}
	base := time.Now()

	require.True(t, r.tryRun(base), "starts the first round")
	assert.False(t, r.tryRun(base.Add(absentReconcileInterval+time.Minute)),
		"must not start a second round while the first is still in flight, even long past the interval")

	r.finish()
	assert.True(t, r.tryRun(base.Add(absentReconcileInterval+time.Minute)),
		"may start again once the previous round finished")
}
