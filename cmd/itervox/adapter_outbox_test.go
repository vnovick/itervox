package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/outbox"
	"github.com/vnovick/itervox/internal/tracker"
)

// TestOrchestratorAdapterRetryOutboxEntry_CallsOutboxDirectly verifies the
// adapter routes to the Outbox handle directly (bypassing the orchestrator
// event loop entirely — unlike SetDepsOverride) — write-ahead-outbox
// design, Task 4. Also the mutation-catcher for "retry endpoint wired to
// Drop": if this called ob.Drop instead of ob.RetryNow, the entry would
// vanish from Snapshot() instead of having its NextAttemptAt reset, and
// the second assertion below would fail.
func TestOrchestratorAdapterRetryOutboxEntry_CallsOutboxDirectly(t *testing.T) {
	ob, err := outbox.New("")
	require.NoError(t, err)
	require.NoError(t, ob.Enqueue(outbox.Entry{
		Kind:        outbox.KindUpdateState,
		IssueID:     "id-1",
		Identifier:  "ENG-1",
		TargetState: "Done",
	}))
	entries := ob.Snapshot()
	require.Len(t, entries, 1)
	id := entries[0].ID

	adapter := &orchestratorAdapter{ob: ob}

	assert.True(t, adapter.RetryOutboxEntry(id))
	// Entry must still be present after Retry (not dropped).
	require.Len(t, ob.Snapshot(), 1)

	assert.False(t, adapter.RetryOutboxEntry("does-not-exist"))
}

// TestOrchestratorAdapterDropOutboxEntry_RemovesFromOutbox verifies Discard
// actually discards the entry (the mutation-catcher: wiring the Discard
// button to RetryNow instead of Drop would leave the entry present).
func TestOrchestratorAdapterDropOutboxEntry_RemovesFromOutbox(t *testing.T) {
	ob, err := outbox.New("")
	require.NoError(t, err)
	require.NoError(t, ob.Enqueue(outbox.Entry{
		Kind:        outbox.KindUpdateState,
		IssueID:     "id-1",
		Identifier:  "ENG-1",
		TargetState: "Done",
	}))
	id := ob.Snapshot()[0].ID

	adapter := &orchestratorAdapter{ob: ob}
	adapter.DropOutboxEntry(id)

	assert.Empty(t, ob.Snapshot())

	// Idempotent: dropping an already-gone / unknown id must not panic.
	assert.NotPanics(t, func() {
		adapter.DropOutboxEntry("does-not-exist")
	})
}

func TestPostOperatorCommentEnqueuesPlainBody(t *testing.T) {
	ob, err := outbox.New(t.TempDir() + "/outbox.json")
	require.NoError(t, err)
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", State: "In Progress"}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, []string{"In Progress"}, []string{"Done"})
	adapter := &orchestratorAdapter{ob: ob, tr: mt}

	queued, err := adapter.PostOperatorComment(context.Background(), "ENG-1", "Looks good, ship it")

	require.NoError(t, err)
	assert.True(t, queued, "with an outbox the comment is queued, not posted")
	entries := ob.Snapshot()
	require.Len(t, entries, 1)
	assert.Equal(t, outbox.KindCreateComment, entries[0].Kind)
	assert.Equal(t, "id1", entries[0].IssueID)
	assert.Equal(t, "ENG-1", entries[0].Identifier)
	assert.Equal(t, "Looks good, ship it", entries[0].Body, "an operator comment is a plain human comment")
	assert.NotContains(t, entries[0].Body, tracker.ManagedCommentMarker)
	assert.NotEmpty(t, entries[0].CommentKey, "the outbox assigns the idempotency key")

	detail, err := mt.FetchIssueDetail(context.Background(), "id1")
	require.NoError(t, err)
	assert.Empty(t, detail.Comments, "nothing reaches the tracker until the flusher runs")
}

func TestPostOperatorCommentDirectWhenOutboxOff(t *testing.T) {
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", State: "In Progress"}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, []string{"In Progress"}, []string{"Done"})
	adapter := &orchestratorAdapter{tr: mt} // ob == nil: tracker.outbox: false

	queued, err := adapter.PostOperatorComment(context.Background(), "ENG-1", "direct")

	require.NoError(t, err)
	assert.False(t, queued)
	detail, err := mt.FetchIssueDetail(context.Background(), "id1")
	require.NoError(t, err)
	require.Len(t, detail.Comments, 1)
	assert.Equal(t, "direct", detail.Comments[0].Body)
}

func TestPostOperatorCommentUnknownIssue(t *testing.T) {
	mt := tracker.NewMemoryTracker(nil, nil, nil)
	adapter := &orchestratorAdapter{tr: mt}

	_, err := adapter.PostOperatorComment(context.Background(), "ENG-404", "x")

	require.Error(t, err)
	assert.ErrorIs(t, err, tracker.ErrNotFound, "the adapter must let tracker.ErrNotFound through so the route can answer 404")
}
