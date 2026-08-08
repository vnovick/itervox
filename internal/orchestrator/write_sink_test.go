package orchestrator

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/outbox"
	"github.com/vnovick/itervox/internal/tracker"
)

// failNTracker wraps MemoryTracker and fails the first failFirstN calls to
// UpdateIssueState / CreateComment (configurable independently), succeeding
// thereafter. Call counts are atomic — directWriteSink's retry loop calls
// from a single goroutine in these tests, but atomics keep the helper safe
// for reuse under -race regardless.
type failNTracker struct {
	*tracker.MemoryTracker
	failFirstNUpdate  int
	failFirstNComment int
	updateCalls       atomic.Int64
	commentCalls      atomic.Int64
}

func newFailNTracker(issues []domain.Issue) *failNTracker {
	return &failNTracker{MemoryTracker: tracker.NewMemoryTracker(issues, nil, nil)}
}

func (f *failNTracker) UpdateIssueState(ctx context.Context, issueID, state string) error {
	n := f.updateCalls.Add(1)
	if int(n) <= f.failFirstNUpdate {
		return fmt.Errorf("write_sink_test: injected update failure (attempt %d)", n)
	}
	return f.MemoryTracker.UpdateIssueState(ctx, issueID, state)
}

func (f *failNTracker) CreateComment(ctx context.Context, issueID, body string) (*domain.Comment, error) {
	n := f.commentCalls.Add(1)
	if int(n) <= f.failFirstNComment {
		return nil, fmt.Errorf("write_sink_test: injected comment failure (attempt %d)", n)
	}
	return f.MemoryTracker.CreateComment(ctx, issueID, body)
}

// withShortTransitionBackoff shrinks transitionRetryBaseDelay for the
// duration of a test so directWriteSink's up-to-4-attempt retry loop (real
// production backoff: 2s/4s/8s between attempts) doesn't make the test
// suite slow. Restores the previous value on return.
func withShortTransitionBackoff(t *testing.T) {
	t.Helper()
	prev := transitionRetryBaseDelay
	transitionRetryBaseDelay = time.Millisecond
	t.Cleanup(func() { transitionRetryBaseDelay = prev })
}

func TestDirectWriteSinkUpdateIssueStateSucceedsFirstAttempt(t *testing.T) {
	withShortTransitionBackoff(t)
	tr := newFailNTracker([]domain.Issue{{ID: "id1", Identifier: "ENG-1", State: "In Progress"}})
	sink := NewDirectWriteSink(tr)

	err := sink.UpdateIssueState(context.Background(), "id1", "ENG-1", "Done")

	require.NoError(t, err)
	assert.EqualValues(t, 1, tr.updateCalls.Load(), "no failure injected — exactly one tracker call")
	issue, ferr := tr.FetchIssueDetail(context.Background(), "id1")
	require.NoError(t, ferr)
	assert.Equal(t, "Done", issue.State)
}

// TestDirectWriteSinkUpdateIssueStateRetriesUntilSuccess pins the moved
// retry loop's core behavior: a transient failure is retried, not
// surfaced immediately. Mutation coverage: if the retry loop were dropped
// from directWriteSink, this test would see exactly 1 call and a non-nil
// error instead of 3 calls and nil.
func TestDirectWriteSinkUpdateIssueStateRetriesUntilSuccess(t *testing.T) {
	withShortTransitionBackoff(t)
	tr := newFailNTracker([]domain.Issue{{ID: "id1", Identifier: "ENG-1", State: "In Progress"}})
	tr.failFirstNUpdate = 2 // fails attempts 1-2, succeeds on attempt 3
	sink := NewDirectWriteSink(tr)

	err := sink.UpdateIssueState(context.Background(), "id1", "ENG-1", "Done")

	require.NoError(t, err)
	assert.EqualValues(t, 3, tr.updateCalls.Load(), "must retry past the first two failures")
}

// TestDirectWriteSinkUpdateIssueStateExhaustsRetries pins the attempt cap:
// a persistent failure is retried exactly maxTransitionAttempts times, then
// the last error surfaces.
func TestDirectWriteSinkUpdateIssueStateExhaustsRetries(t *testing.T) {
	withShortTransitionBackoff(t)
	tr := newFailNTracker([]domain.Issue{{ID: "id1", Identifier: "ENG-1", State: "In Progress"}})
	tr.failFirstNUpdate = 1000 // always fails
	sink := NewDirectWriteSink(tr)

	err := sink.UpdateIssueState(context.Background(), "id1", "ENG-1", "Done")

	require.Error(t, err)
	assert.EqualValues(t, maxTransitionAttempts, tr.updateCalls.Load())
	assert.Contains(t, err.Error(), "attempt 4")
}

// TestDirectWriteSinkUpdateIssueStateStopsOnCtxCancel pins the
// between-attempt cancellation watch: a caller ctx cancelled while the
// retry loop is waiting between attempts stops the loop early instead of
// running all maxTransitionAttempts.
func TestDirectWriteSinkUpdateIssueStateStopsOnCtxCancel(t *testing.T) {
	prev := transitionRetryBaseDelay
	transitionRetryBaseDelay = time.Hour // never elapses within the test
	t.Cleanup(func() { transitionRetryBaseDelay = prev })

	tr := newFailNTracker([]domain.Issue{{ID: "id1", Identifier: "ENG-1", State: "In Progress"}})
	tr.failFirstNUpdate = 1000
	sink := NewDirectWriteSink(tr)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	err := sink.UpdateIssueState(ctx, "id1", "ENG-1", "Done")

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.EqualValues(t, 1, tr.updateCalls.Load(), "only the first (immediate) attempt should have run before ctx cancellation stopped the between-attempt wait")
}

// TestDirectWriteSinkUpdateIssueStateReturnsWithinShortCtxBound is the
// fix-round §1 regression test: at production backoff scale (2s/4s/8s,
// NOT shortened via transitionRetryBaseDelay), a caller-supplied 100ms
// ctx against an always-failing tracker must bound the WHOLE call near
// 100ms — not the ~74s worst case (60s shared callCtx + 14s of
// non-cancellable backoff) that was possible before fix-round §1 when a
// caller passed context.Background(). This proves the fix end-to-end
// (both the call-site bound in event_loop.go and directWriteSink's own
// ctx-honoring) rather than just the isolated mechanism.
func TestDirectWriteSinkUpdateIssueStateReturnsWithinShortCtxBound(t *testing.T) {
	tr := newFailNTracker([]domain.Issue{{ID: "id1", Identifier: "ENG-1", State: "In Progress"}})
	tr.failFirstNUpdate = 1000 // always fails, instantly
	sink := NewDirectWriteSink(tr)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := sink.UpdateIssueState(ctx, "id1", "ENG-1", "Done")
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Less(t, elapsed, time.Second, "must return within the ctx bound, not the ~74s unbounded worst case")
	assert.Less(t, tr.updateCalls.Load(), int64(maxTransitionAttempts), "ctx expiring must stop the loop before it exhausts all attempts")
}

// TestDirectWriteSinkUpdateIssueStateSkipsAttemptWhenCtxAlreadyExpired
// isolates the specific line fix-round §1(b) added — an explicit
// ctx.Err() check at the top of every loop iteration — from the
// pre-existing backoff-select ctx.Done() case. The backoff select can
// only ever stop the loop starting at attempt 2 (there is no backoff wait
// before attempt 1). If the caller's ctx is ALREADY past its deadline
// before UpdateIssueState is even called, only the new top-of-loop check
// can prevent attempt 1 itself from running.
//
// Mutation coverage: if the ctx.Err() check were removed (leaving the
// select's ctx.Done() case intact), this test's assertion of zero tracker
// calls would fail — see task-2-report.md fix-round §1 mutation evidence.
func TestDirectWriteSinkUpdateIssueStateSkipsAttemptWhenCtxAlreadyExpired(t *testing.T) {
	tr := newFailNTracker([]domain.Issue{{ID: "id1", Identifier: "ENG-1", State: "In Progress"}})
	tr.failFirstNUpdate = 1000 // injected-failure fake: would always fail if called
	sink := NewDirectWriteSink(tr)

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second)) // already expired
	defer cancel()

	start := time.Now()
	err := sink.UpdateIssueState(ctx, "id1", "ENG-1", "Done")
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.EqualValues(t, 0, tr.updateCalls.Load(), "an already-expired ctx must prevent even the first attempt")
	assert.Less(t, elapsed, time.Second)
}

func TestDirectWriteSinkCreateCommentNoRetry(t *testing.T) {
	tr := newFailNTracker([]domain.Issue{{ID: "id1", Identifier: "ENG-1", State: "In Progress"}})
	tr.failFirstNComment = 1000 // always fails
	sink := NewDirectWriteSink(tr)

	err := sink.CreateComment(context.Background(), "id1", "ENG-1", "hello")

	require.Error(t, err, "comment path has never retried — pre-existing behavior")
	assert.EqualValues(t, 1, tr.commentCalls.Load(), "exactly one attempt, no retry")
}

func TestDirectWriteSinkCreateCommentSuccess(t *testing.T) {
	tr := newFailNTracker([]domain.Issue{{ID: "id1", Identifier: "ENG-1", State: "In Progress"}})
	sink := NewDirectWriteSink(tr)

	err := sink.CreateComment(context.Background(), "id1", "ENG-1", "hello")

	require.NoError(t, err)
	assert.EqualValues(t, 1, tr.commentCalls.Load())
}

// TestOutboxWriteSinkUpdateIssueStateEnqueuesOnceNoTrackerCalls is the
// load-bearing routing test: with an outbox-backed sink, UpdateIssueState
// must enqueue exactly one update_state entry and must NEVER touch the
// tracker directly. Mutation coverage: if outboxWriteSink secretly called
// the tracker, tr.updateCalls would be nonzero and this test would fail.
func TestOutboxWriteSinkUpdateIssueStateEnqueuesOnceNoTrackerCalls(t *testing.T) {
	tr := newFailNTracker([]domain.Issue{{ID: "id1", Identifier: "ENG-1", State: "In Progress"}})
	ob, err := outbox.New(t.TempDir() + "/outbox.json")
	require.NoError(t, err)
	sink := NewOutboxWriteSink(ob)

	sinkErr := sink.UpdateIssueState(context.Background(), "id1", "ENG-1", "Done")

	require.NoError(t, sinkErr)
	assert.EqualValues(t, 0, tr.updateCalls.Load(), "outbox sink must never call the tracker directly")

	entries := ob.Snapshot()
	require.Len(t, entries, 1)
	assert.Equal(t, outbox.KindUpdateState, entries[0].Kind)
	assert.Equal(t, "id1", entries[0].IssueID)
	assert.Equal(t, "ENG-1", entries[0].Identifier)
	assert.Equal(t, "Done", entries[0].TargetState)
}

// TestOutboxWriteSinkCreateCommentEnqueuesOnceNoTrackerCalls mirrors the
// UpdateIssueState routing test for the comment path.
func TestOutboxWriteSinkCreateCommentEnqueuesOnceNoTrackerCalls(t *testing.T) {
	tr := newFailNTracker([]domain.Issue{{ID: "id1", Identifier: "ENG-1", State: "In Progress"}})
	ob, err := outbox.New(t.TempDir() + "/outbox.json")
	require.NoError(t, err)
	sink := NewOutboxWriteSink(ob)

	sinkErr := sink.CreateComment(context.Background(), "id1", "ENG-1", "hello")

	require.NoError(t, sinkErr)
	assert.EqualValues(t, 0, tr.commentCalls.Load(), "outbox sink must never call the tracker directly")

	entries := ob.Snapshot()
	require.Len(t, entries, 1)
	assert.Equal(t, outbox.KindCreateComment, entries[0].Kind)
	assert.Equal(t, "id1", entries[0].IssueID)
	assert.Equal(t, "ENG-1", entries[0].Identifier)
	assert.Equal(t, "hello", entries[0].Body)
}

// TestOutboxWriteSinkUpdateIssueStateEnqueueErrorPropagates pins that a
// validation failure from outbox.Enqueue surfaces verbatim to the
// WriteSink caller — the same place worker.go / event_loop.go already
// handle a transition error (log + pause, see runWorker's completion-state
// branch and asyncDiscardAndTransitionTo's Warn log).
func TestOutboxWriteSinkUpdateIssueStateEnqueueErrorPropagates(t *testing.T) {
	ob, err := outbox.New(t.TempDir() + "/outbox.json")
	require.NoError(t, err)
	sink := NewOutboxWriteSink(ob)

	// Empty identifier is rejected by outbox.Enqueue's validation.
	sinkErr := sink.UpdateIssueState(context.Background(), "id1", "", "Done")

	require.Error(t, sinkErr)
	assert.Empty(t, ob.Snapshot(), "a validation failure must never persist an entry")
}

// TestOutboxWriteSinkCreateCommentEnqueueErrorPropagates mirrors the above
// for the comment path (empty Body is rejected).
func TestOutboxWriteSinkCreateCommentEnqueueErrorPropagates(t *testing.T) {
	ob, err := outbox.New(t.TempDir() + "/outbox.json")
	require.NoError(t, err)
	sink := NewOutboxWriteSink(ob)

	sinkErr := sink.CreateComment(context.Background(), "id1", "ENG-1", "")

	require.Error(t, sinkErr)
	assert.Empty(t, ob.Snapshot())
}

// TestOrchestratorDefaultWriteSinkIsDirect pins the construction default:
// New() must wire a direct sink so every pre-existing orchestrator test
// keeps old (synchronous, tracker-calling) behavior unless it explicitly
// opts into an outbox sink via SetWriteSink. Verified behaviorally (not by
// type assertion, since the field is unexported) — a completion transition
// through the default-constructed orchestrator must reach the tracker.
func TestOrchestratorDefaultWriteSinkIsDirect(t *testing.T) {
	withShortTransitionBackoff(t)
	tr := newFailNTracker([]domain.Issue{{ID: "id1", Identifier: "ENG-1", State: "In Progress"}})
	o := &Orchestrator{tracker: tr}

	err := o.writeSink().UpdateIssueState(context.Background(), "id1", "ENG-1", "Done")

	require.NoError(t, err)
	assert.EqualValues(t, 1, tr.updateCalls.Load(), "default sink (nil o.sink fallback) must call the tracker directly")
}

// TestOrchestratorSetWriteSinkOverridesDefault pins that SetWriteSink is
// the seam cmd/itervox (Task 3) and this package's own tests use to opt
// into an outbox-backed sink.
func TestOrchestratorSetWriteSinkOverridesDefault(t *testing.T) {
	tr := newFailNTracker([]domain.Issue{{ID: "id1", Identifier: "ENG-1", State: "In Progress"}})
	ob, err := outbox.New(t.TempDir() + "/outbox.json")
	require.NoError(t, err)
	o := &Orchestrator{tracker: tr}
	o.SetWriteSink(NewOutboxWriteSink(ob))

	sinkErr := o.writeSink().UpdateIssueState(context.Background(), "id1", "ENG-1", "Done")

	require.NoError(t, sinkErr)
	assert.EqualValues(t, 0, tr.updateCalls.Load())
	assert.Len(t, ob.Snapshot(), 1)
}
