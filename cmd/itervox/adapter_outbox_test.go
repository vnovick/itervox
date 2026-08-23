package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/outbox"
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
