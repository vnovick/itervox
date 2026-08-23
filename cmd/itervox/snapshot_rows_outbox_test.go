package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/outbox"
)

// TestOutboxEntryRowsCarryFieldsAndDegraded verifies outboxEntryRows
// (cmd/itervox/snapshot_rows.go) converts every outbox.Entry field onto the
// wire row, preserves global enqueue order (ob.Snapshot()'s own contract —
// row builder must not resort), and derives Degraded from Entry.Degraded()
// rather than trusting a stored field. write-ahead-outbox design, Task 4.
func TestOutboxEntryRowsCarryFieldsAndDegraded(t *testing.T) {
	now := time.Now()
	entries := []outbox.Entry{
		{
			ID:            "e1",
			Kind:          outbox.KindUpdateState,
			Identifier:    "ENG-1",
			TargetState:   "Done",
			Attempts:      2,
			LastError:     "boom",
			EnqueuedAt:    now,
			NextAttemptAt: now.Add(time.Minute),
		},
		{
			ID:            "e2",
			Kind:          outbox.KindCreateComment,
			Identifier:    "ENG-2",
			Attempts:      5, // >= outboxDegradedAttempts threshold
			EnqueuedAt:    now,
			NextAttemptAt: now,
		},
	}

	rows := outboxEntryRows(entries)

	require.Len(t, rows, 2)
	assert.Equal(t, "e1", rows[0].ID, "must preserve ob.Snapshot()'s global enqueue order")
	assert.Equal(t, "update_state", rows[0].Kind)
	assert.Equal(t, "ENG-1", rows[0].Identifier)
	assert.Equal(t, "Done", rows[0].TargetState)
	assert.Equal(t, 2, rows[0].Attempts)
	assert.Equal(t, "boom", rows[0].LastError)
	assert.False(t, rows[0].Degraded, "2 attempts is under the degraded threshold")

	assert.Equal(t, "e2", rows[1].ID)
	assert.Equal(t, "create_comment", rows[1].Kind)
	assert.True(t, rows[1].Degraded, "5 attempts crosses the degraded threshold")
}

func TestOutboxEntryRowsEmptyReturnsNil(t *testing.T) {
	assert.Nil(t, outboxEntryRows(nil))
	assert.Nil(t, outboxEntryRows([]outbox.Entry{}))
}

// TestOutboxSyncingRowsSorted verifies outboxSyncingRows sorts the
// event-loop's map keys deterministically — the wire contract for
// StateSnapshot.OutboxSyncing is a stable, sorted []string (Task 4 plan).
func TestOutboxSyncingRowsSorted(t *testing.T) {
	syncing := map[string]struct{}{
		"ENG-3": {},
		"ENG-1": {},
		"ENG-2": {},
	}

	got := outboxSyncingRows(syncing)

	assert.Equal(t, []string{"ENG-1", "ENG-2", "ENG-3"}, got)
}

func TestOutboxSyncingRowsEmptyReturnsNil(t *testing.T) {
	assert.Nil(t, outboxSyncingRows(nil))
	assert.Nil(t, outboxSyncingRows(map[string]struct{}{}))
}
