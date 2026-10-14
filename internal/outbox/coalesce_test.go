package outbox

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// batchedOutbox is an in-memory outbox with a 10s push interval and a clock
// the test moves.
func batchedOutbox(t *testing.T) (*Outbox, *time.Time) {
	t.Helper()
	ob, err := New("")
	require.NoError(t, err)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	ob.SetNow(func() time.Time { return now })
	ob.SetPushInterval(10 * time.Second)
	return ob, &now
}

func stateMove(issueID, from, to string) Entry {
	return Entry{Kind: KindUpdateState, IssueID: issueID, Identifier: "ENG-" + issueID, TargetState: to, FromState: from}
}

func comment(issueID, body string) Entry {
	return Entry{Kind: KindCreateComment, IssueID: issueID, Identifier: "ENG-" + issueID, Body: body}
}

// TestCoalesceKeepsLastStateMoveAndEarliestBaseline: three moves inside one
// interval leave one entry, carrying the last target and the first move's
// FromState (what the tracker still shows), due at the first move's
// deadline — later writes never postpone the batch.
func TestCoalesceKeepsLastStateMoveAndEarliestBaseline(t *testing.T) {
	ob, now := batchedOutbox(t)
	start := *now

	require.NoError(t, ob.Enqueue(stateMove("a", "Todo", "In Progress")))
	*now = start.Add(3 * time.Second)
	require.NoError(t, ob.Enqueue(stateMove("a", "In Progress", "In Review")))
	*now = start.Add(6 * time.Second)
	require.NoError(t, ob.Enqueue(stateMove("a", "In Review", "Done")))

	entries := ob.Snapshot()
	require.Len(t, entries, 1)
	assert.Equal(t, "Done", entries[0].TargetState)
	assert.Equal(t, "Todo", entries[0].FromState, "baseline is the state the tracker still shows")
	assert.Equal(t, 2, entries[0].Coalesced)
	assert.Equal(t, start.Add(10*time.Second), entries[0].NextAttemptAt, "the batch keeps its first write's deadline")

	assert.Empty(t, ob.Due(start.Add(9*time.Second)), "nothing is pushed inside the interval")
	assert.Len(t, ob.Due(start.Add(10*time.Second)), 1)
	assert.Equal(t, map[string]string{"a": "Done"}, ob.PendingStates())
}

// TestCoalesceKeepsCommentsInOrder: comments are never merged or reordered;
// the surviving state move takes the place of the last one.
func TestCoalesceKeepsCommentsInOrder(t *testing.T) {
	ob, _ := batchedOutbox(t)

	require.NoError(t, ob.Enqueue(stateMove("a", "Todo", "In Progress")))
	require.NoError(t, ob.Enqueue(comment("a", "first")))
	require.NoError(t, ob.Enqueue(comment("a", "second")))
	require.NoError(t, ob.Enqueue(stateMove("a", "In Progress", "Done")))
	require.NoError(t, ob.Enqueue(comment("b", "other issue")))

	var got []string
	for _, e := range ob.Snapshot() {
		if e.Kind == KindCreateComment {
			got = append(got, e.IssueID+":"+e.Body)
		} else {
			got = append(got, e.IssueID+":->"+e.TargetState)
		}
	}
	assert.Equal(t, []string{"a:first", "a:second", "a:->Done", "b:other issue"}, got)
}

// TestCoalesceNeverReplacesAttemptedWrite: a move the flusher already tried
// may have landed, so a later move is queued behind it, not merged into it.
func TestCoalesceNeverReplacesAttemptedWrite(t *testing.T) {
	ob, now := batchedOutbox(t)
	start := *now
	require.NoError(t, ob.Enqueue(stateMove("a", "Todo", "In Progress")))

	*now = start.Add(10 * time.Second)
	due := ob.Due(*now)
	require.Len(t, due, 1)
	ob.MarkFailed(due[0].ID, errors.New("timeout"), *now)

	require.NoError(t, ob.Enqueue(stateMove("a", "In Progress", "Done")))
	entries := ob.Snapshot()
	require.Len(t, entries, 2)
	assert.Equal(t, "In Progress", entries[0].TargetState)
	assert.Equal(t, 1, entries[0].Attempts)
	assert.Equal(t, "Done", entries[1].TargetState)
	assert.Equal(t, "In Progress", entries[1].FromState)
	assert.Zero(t, entries[1].Coalesced)
}

// TestUrgentWritePullsIssueBatchForward: an urgent write is due at once and
// takes the issue's waiting writes with it (per-issue FIFO would otherwise
// hold it behind them); another issue's batch keeps waiting, and the flusher
// is woken.
func TestUrgentWritePullsIssueBatchForward(t *testing.T) {
	ob, now := batchedOutbox(t)
	require.NoError(t, ob.Enqueue(stateMove("a", "Todo", "In Progress")))
	require.NoError(t, ob.Enqueue(comment("b", "batched")))
	select {
	case <-ob.Wake():
		t.Fatal("a non-urgent write must not wake the flusher")
	default:
	}

	urgent := comment("a", "question")
	urgent.Urgent = true
	require.NoError(t, ob.Enqueue(urgent))

	select {
	case <-ob.Wake():
	default:
		t.Fatal("an urgent write must wake the flusher")
	}
	for _, e := range ob.Snapshot() {
		if e.IssueID == "a" {
			assert.Equal(t, *now, e.NextAttemptAt, "%s is due at once", e.Kind)
		} else {
			assert.True(t, e.NextAttemptAt.After(*now), "issue b's batch keeps waiting")
		}
	}
	due := ob.Due(*now)
	require.Len(t, due, 1)
	assert.Equal(t, "In Progress", due[0].TargetState, "the issue's queue is pushed in order")
}

// TestPushIntervalZeroKeepsImmediateDelivery: with batching off every write
// is due at once and nothing is merged — the pre-#115 behaviour.
func TestPushIntervalZeroKeepsImmediateDelivery(t *testing.T) {
	ob, now := batchedOutbox(t)
	ob.SetPushInterval(0)
	require.NoError(t, ob.Enqueue(stateMove("a", "Todo", "In Progress")))
	require.NoError(t, ob.Enqueue(stateMove("a", "In Progress", "Done")))
	entries := ob.Snapshot()
	require.Len(t, entries, 2)
	assert.Equal(t, *now, entries[0].NextAttemptAt)
	assert.Equal(t, *now, entries[1].NextAttemptAt)
}

// TestEnqueueRollbackRestoresCoalescedEntries: when the persist fails,
// Enqueue's atomic contract covers batching too — the replaced state move
// and the rescheduled entries come back exactly as they were. The file's
// directory is replaced by a regular file so the write fails even as root.
func TestEnqueueRollbackRestoresCoalescedEntries(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "itervox")
	require.NoError(t, os.Mkdir(dir, 0o700))
	ob, err := New(filepath.Join(dir, "outbox.json"))
	require.NoError(t, err)
	ob.SetPushInterval(10 * time.Second)
	require.NoError(t, ob.Enqueue(stateMove("a", "Todo", "In Progress")))
	require.NoError(t, ob.Enqueue(comment("a", "note")))
	before := ob.Snapshot()

	require.NoError(t, os.RemoveAll(dir))
	require.NoError(t, os.WriteFile(dir, []byte("not a directory"), 0o600))

	urgentMove := stateMove("a", "In Progress", "Done")
	urgentMove.Urgent = true
	require.Error(t, ob.Enqueue(urgentMove), "the persist failure surfaces")
	assert.Equal(t, before, ob.Snapshot(), "the replaced move and the rescheduled batch are restored")
}

func fieldEdit(issueID, payload string) Entry {
	return Entry{Kind: KindUpdateFields, IssueID: issueID, Identifier: "ENG-" + issueID, Payload: payload}
}

// TestCoalesceMergesWaitingFieldEdits (#118): field edits for one issue
// inside one interval merge into one entry, key by key with the later value
// winning, at the end of the issue's queue (after its comments), due at the
// batch's first deadline; an edit already handed to the flusher is not
// merged into.
func TestCoalesceMergesWaitingFieldEdits(t *testing.T) {
	ob, now := batchedOutbox(t)
	start := *now
	require.NoError(t, ob.Enqueue(fieldEdit("a", `{"title":"One","labels":["cli"]}`)))
	require.NoError(t, ob.Enqueue(comment("a", "note")))
	*now = start.Add(4 * time.Second)
	require.NoError(t, ob.Enqueue(fieldEdit("a", `{"title":"Two","priority":2}`)))
	require.NoError(t, ob.Enqueue(fieldEdit("b", `{"title":"Other"}`)))

	entries := ob.Snapshot()
	require.Len(t, entries, 3)
	assert.Equal(t, KindCreateComment, entries[0].Kind)
	merged := entries[1]
	assert.Equal(t, KindUpdateFields, merged.Kind)
	assert.JSONEq(t, `{"title":"Two","labels":["cli"],"priority":2}`, merged.Payload)
	assert.Equal(t, 1, merged.Coalesced)
	assert.Equal(t, start.Add(10*time.Second), merged.NextAttemptAt, "the batch's first deadline")
	assert.JSONEq(t, `{"title":"Other"}`, entries[2].Payload, "other issues are untouched")

	// Once attempted, an edit is not merged into: it may have landed.
	ob.MarkFailed(merged.ID, errors.New("boom"), *now)
	require.NoError(t, ob.Enqueue(fieldEdit("a", `{"title":"Three"}`)))
	assert.Len(t, ob.PendingFor("a"), 3)
}

// TestEnqueueValidatesSyncKinds (#118): create_issue and update_fields need
// a JSON payload; a create gets an idempotency key like a comment does.
func TestEnqueueValidatesSyncKinds(t *testing.T) {
	ob, err := New("")
	require.NoError(t, err)
	assert.Error(t, ob.Enqueue(Entry{Kind: KindUpdateFields, IssueID: "a", Identifier: "ENG-1", Payload: "not json"}))
	assert.Error(t, ob.Enqueue(Entry{Kind: KindCreateIssue, IssueID: "local:ITX-1", Identifier: "ITX-1"}))
	require.NoError(t, ob.Enqueue(Entry{Kind: KindCreateIssue, IssueID: "local:ITX-1", Identifier: "ITX-1", Payload: `{"local":"ITX-1"}`}))
	assert.NotEmpty(t, ob.Snapshot()[0].CommentKey)
}
