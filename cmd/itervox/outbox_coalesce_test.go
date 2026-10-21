package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/outbox"
)

const testPushInterval = 10 * time.Second

func enqueueMove(t *testing.T, ob *outbox.Outbox, from, to string) {
	t.Helper()
	require.NoError(t, ob.Enqueue(outbox.Entry{
		Kind: outbox.KindUpdateState, IssueID: "id-a", Identifier: "ENG-A",
		TargetState: to, FromState: from,
	}))
}

// TestCoalescedPushThreeStateMovesProduceOneTrackerWrite (#115 acceptance):
// three state moves to one issue inside one push interval reach the tracker
// as exactly one write, carrying the last state.
func TestCoalescedPushThreeStateMovesProduceOneTrackerWrite(t *testing.T) {
	ob := newTestOutbox(t)
	ob.SetPushInterval(testPushInterval)
	tr := newRecordingFlusherTracker([]domain.Issue{{ID: "id-a", Identifier: "ENG-A", State: "Todo"}})
	refresher := &fakeRefresher{}

	enqueueMove(t, ob, "Todo", "In Progress")
	enqueueMove(t, ob, "In Progress", "In Review")
	enqueueMove(t, ob, "In Review", "Done")
	enqueued := time.Now()

	runOutboxFlusherTick(context.Background(), ob, tr, refresher, enqueued)
	assert.Empty(t, tr.CallOrder(), "nothing is pushed before the interval runs out")

	runOutboxFlusherTick(context.Background(), ob, tr, refresher, enqueued.Add(testPushInterval+time.Second))
	assert.Equal(t, []string{"update:id-a:Done"}, tr.CallOrder(), "one tracker write, with the last state")
	assert.Empty(t, ob.Snapshot())

	issue, err := tr.FetchIssueDetail(context.Background(), "id-a")
	require.NoError(t, err)
	assert.Equal(t, "Done", issue.State)
}

// TestCoalescedPushCrashBetweenQueueAndPushReplaysAfterRestart (#115
// acceptance): writes queued for a push that never happened (the process
// died inside the interval) are in outbox.json; a restarted outbox pushes
// them, still coalesced and in order, and the store overlay sees them again.
func TestCoalescedPushCrashBetweenQueueAndPushReplaysAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	before, err := outbox.New(path)
	require.NoError(t, err)
	before.SetPushInterval(testPushInterval)
	enqueueMove(t, before, "Todo", "In Progress")
	require.NoError(t, before.Enqueue(outbox.Entry{
		Kind: outbox.KindCreateComment, IssueID: "id-a", Identifier: "ENG-A", Body: "session summary",
	}))
	enqueueMove(t, before, "In Progress", "Done")
	enqueued := time.Now()
	// Crash: no flush, no shutdown hook — the handle is simply abandoned
	// and a new one opens the same file, as a restarted daemon does.

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), `"target_state": "Done"`, "the queued write is durable before any push")

	after, err := outbox.New(path)
	require.NoError(t, err)
	after.SetPushInterval(testPushInterval)
	assert.Equal(t, map[string]string{"id-a": "Done"}, after.PendingStates(), "the restart sees the queued move")

	tr := newRecordingFlusherTracker([]domain.Issue{{ID: "id-a", Identifier: "ENG-A", State: "Todo"}})
	runOutboxFlusherTick(context.Background(), after, tr, &fakeRefresher{}, enqueued.Add(testPushInterval+time.Second))
	assert.Equal(t, []string{"comment:id-a", "update:id-a:Done"}, tr.CallOrder(),
		"everything queued before the crash is pushed after the restart")
	assert.Empty(t, after.Snapshot())
}

// TestOutboxFlusherPushesUrgentWriteOnWake: an urgent write reaches the
// tracker without waiting for the push interval or the flusher's ticker.
func TestOutboxFlusherPushesUrgentWriteOnWake(t *testing.T) {
	prev := outboxFlushInterval
	outboxFlushInterval = time.Hour // the ticker never fires in this test
	t.Cleanup(func() { outboxFlushInterval = prev })

	ob := newTestOutbox(t)
	ob.SetPushInterval(time.Hour)
	tr := newRecordingFlusherTracker([]domain.Issue{{ID: "id-a", Identifier: "ENG-A", State: "In Progress"}})
	ctx, cancel := context.WithCancel(context.Background())
	done := startOutboxFlusher(ctx, ob, tr, &fakeRefresher{}, "")
	t.Cleanup(func() { cancel(); <-done })

	require.NoError(t, ob.Enqueue(outbox.Entry{
		Kind: outbox.KindCreateComment, IssueID: "id-a", Identifier: "ENG-A",
		Body: "Which file should I edit?", Urgent: true,
	}))
	require.Eventually(t, func() bool { return len(tr.CallOrder()) == 1 }, 5*time.Second, 10*time.Millisecond,
		"the urgent comment is pushed at once")
	assert.Equal(t, []string{"comment:id-a"}, tr.CallOrder())
}

// overlayRecordingTracker captures the pending-state hook wireCoalescedPush
// installs on a tracker store.
type overlayRecordingTracker struct {
	*recordingFlusherTracker
	pending func() map[string]string
}

func (o *overlayRecordingTracker) SetPendingStates(fn func() map[string]string) { o.pending = fn }

// TestWireCoalescedPushInstallsIntervalAndStoreOverlay: the daemon wiring
// applies tracker.push_interval_ms to the outbox and hands the store the
// outbox's queued moves, so the board shows a move the moment it is queued.
func TestWireCoalescedPushInstallsIntervalAndStoreOverlay(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tracker.PushIntervalMs = 10000
	ob := newTestOutbox(t)
	tr := &overlayRecordingTracker{recordingFlusherTracker: newRecordingFlusherTracker(nil)}

	wireCoalescedPush(cfg, ob, tr)
	enqueueMove(t, ob, "Todo", "Done")

	assert.Empty(t, ob.Due(time.Now()), "the move waits for the push interval")
	require.NotNil(t, tr.pending, "the store overlay is installed")
	assert.Equal(t, map[string]string{"id-a": "Done"}, tr.pending())
}

// retryOnFailureRecorder plays an operator pressing Retry the moment a
// delivery failure is reported, so the failed entry is due again before the
// tick that failed it has ended.
type retryOnFailureRecorder struct {
	fakeRefresher
	ob      *outbox.Outbox
	retries int
}

func (r *retryOnFailureRecorder) RecordFailure(orchestrator.FailureRecord) bool {
	if r.retries < 20 { // bound the damage if the guard is missing
		r.retries++
		for _, e := range r.ob.Snapshot() {
			r.ob.RetryNow(e.ID)
		}
	}
	return true
}

// TestOutboxFlusherTickAttemptsEachEntryOnce: a tick that pushes whole
// batches still attempts each entry at most once, even when the entry is due
// again before the tick ends; the next tick retries it.
func TestOutboxFlusherTickAttemptsEachEntryOnce(t *testing.T) {
	ob := newTestOutbox(t)
	tr := newRecordingFlusherTracker([]domain.Issue{{ID: "id-a", Identifier: "ENG-A", State: "Todo"}})
	tr.failNextUpdate("id-a", 100)
	enqueueMove(t, ob, "Todo", "Done")

	// A tick whose now is ahead of the wall clock, so RetryNow's "now" is due.
	runOutboxFlusherTick(context.Background(), ob, tr, &retryOnFailureRecorder{ob: ob}, time.Now().Add(time.Hour))
	assert.Equal(t, []string{"update:id-a:Done"}, tr.CallOrder())
}
