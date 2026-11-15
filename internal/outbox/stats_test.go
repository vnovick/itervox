package outbox

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type statsRateLimit struct{ reset time.Time }

func (e statsRateLimit) Error() string               { return "rate limited" }
func (e statsRateLimit) RateLimitResetAt() time.Time { return e.reset }

// TestStatsSummarisesQueueForSyncStatus pins #117's pending-write counts:
// batched (waiting for the push interval), urgent, retrying, degraded and
// rate-limited writes, the oldest enqueue time, and the next push due, which
// only an issue's queue head can set.
func TestStatsSummarisesQueueForSyncStatus(t *testing.T) {
	ob, now := batchedOutbox(t)
	start := *now

	assert.Equal(t, Stats{}, ob.Stats(start), "an empty outbox has nothing pending")

	// b: a comment that fails five times (degraded), then a batched move
	// behind it whose own deadline is earlier than the head's retry: it
	// must not set NextPushAt, because only the head can go.
	*now = start.Add(time.Second)
	require.NoError(t, ob.Enqueue(comment("b", "hello")))
	bHead := ob.PendingFor("b")[0].ID
	for range outboxDegradedAttempts {
		ob.MarkFailed(bHead, errors.New("linear: 502"), start.Add(2*time.Second))
	}
	require.NoError(t, ob.Enqueue(stateMove("b", "Todo", "Done")))
	// c: a rate-limited comment, deferred to start+1h.
	require.NoError(t, ob.Enqueue(comment("c", "hi")))
	ob.MarkFailed(ob.PendingFor("c")[0].ID, statsRateLimit{start.Add(time.Hour)}, start.Add(2*time.Second))
	// a: a batched state move, due at start+13s.
	*now = start.Add(3 * time.Second)
	require.NoError(t, ob.Enqueue(stateMove("a", "Todo", "Done")))
	// d: an urgent comment, due at once.
	require.NoError(t, ob.Enqueue(Entry{Kind: KindCreateComment, IssueID: "d", Identifier: "ENG-d", Body: "question", Urgent: true}))

	st := ob.Stats(start.Add(4 * time.Second))
	assert.Equal(t, 5, st.Pending)
	assert.Equal(t, 1, st.Urgent)
	assert.Equal(t, 2, st.Batched, "a's move and b's move wait for the push interval")
	assert.Equal(t, 1, st.Retrying)
	assert.Equal(t, 1, st.Degraded)
	assert.Equal(t, 1, st.RateLimited)
	assert.Equal(t, start.Add(time.Second), st.OldestEnqueuedAt)
	assert.Equal(t, start.Add(3*time.Second), st.NextPushAt, "the urgent head is due first")

	ob.MarkFlushed(ob.PendingFor("d")[0].ID)
	st = ob.Stats(start.Add(4 * time.Second))
	assert.Equal(t, start.Add(13*time.Second), st.NextPushAt,
		"a's batch deadline, not b's earlier start+11s: b's queued move is not a head and cannot go before b's failing comment")
}
