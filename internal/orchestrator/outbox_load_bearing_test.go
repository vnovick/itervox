package orchestrator_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/outbox"
	"github.com/vnovick/itervox/internal/tracker"
)

// TestOutboxOverlayPreventsRedispatch is THE load-bearing test for Task 3
// (docs/superpowers/specs/2026-08-06-write-ahead-outbox-design.md,
// "Overlay"): with an outbox-backed WriteSink AND the outbox handle wired
// via SetOutbox, a completed issue whose transition is enqueued but never
// flushed (no flusher goroutine runs in this test — flushing is
// out-of-process from the orchestrator by design) must NOT be re-dispatched
// on any later poll tick, even though the memory tracker keeps reporting
// the issue in its original active state forever (nothing ever flushes
// it). The snapshot must also mark the issue's identifier as syncing.
//
// Mutation coverage: removing the tick-top overlay call in onTick
// (event_loop.go's `state.OutboxSyncing = o.reconcileAndOverlayOutbox(...)`)
// makes this test fail — subsequent ticks would see the tracker's
// unchanged "In Progress" state, find the issue eligible again (nothing
// else marks it running/claimed once the worker exits), and dispatch it a
// second time.
func TestOutboxOverlayPreventsRedispatch(t *testing.T) {
	cfg := baseConfig()
	// Fast polling so multiple ticks genuinely elapse within the test
	// window — this is what actually exercises "does NOT get re-dispatched
	// on a LATER tick", not just "wasn't dispatched twice inside a single
	// tick".
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 3
	cfg.Tracker.CompletionState = "Done"

	// Production-shaped: ID (tracker-internal, opaque) != Identifier
	// (human-readable).
	mt := tracker.NewMemoryTracker(
		[]domain.Issue{{ID: "uuid-abc-123", Identifier: "ENG-1", Title: "T", State: "In Progress"}},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)

	ob, err := outbox.New(t.TempDir() + "/outbox.json")
	require.NoError(t, err)

	runner := &succeedOnceRunner{}
	orch := orchestrator.New(cfg, mt, runner, nil)
	orch.SetWriteSink(orchestrator.NewOutboxWriteSink(ob))
	orch.SetOutbox(ob)

	var dispatchCount atomic.Int32
	orch.OnDispatch = func(issueID string) {
		if issueID == "uuid-abc-123" {
			dispatchCount.Add(1)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	runDone := make(chan error, 1)
	go func() { runDone <- orch.Run(ctx) }()

	// Wait for the completion transition to land on the outbox (the
	// worker completes, the event loop's completion handling calls
	// o.writeSink().UpdateIssueState, which — because of SetWriteSink
	// above — enqueues rather than calling the tracker).
	require.Eventually(t, func() bool {
		return len(ob.Snapshot()) > 0
	}, 4*time.Second, 20*time.Millisecond, "completion transition never enqueued on the outbox")

	entries := ob.Snapshot()
	require.Len(t, entries, 1)
	assert.Equal(t, outbox.KindUpdateState, entries[0].Kind)
	assert.Equal(t, "uuid-abc-123", entries[0].IssueID)
	assert.Equal(t, "Done", entries[0].TargetState)

	// Confirm the tracker itself never actually transitioned — nothing in
	// this test flushes the outbox, so if the tracker WERE showing "Done"
	// already, that would mean something bypassed the outbox sink instead
	// of proving the overlay's own effect.
	polled, ferr := mt.FetchIssueStatesByIDs(context.Background(), []string{"uuid-abc-123"})
	require.NoError(t, ferr)
	require.Len(t, polled, 1)
	assert.Equal(t, "In Progress", polled[0].State, "the tracker's own record must remain unflushed")

	// Let several more poll ticks (20ms interval) elapse. Without the
	// overlay, each of these ticks would see "In Progress" (active,
	// unblocked, not running/claimed once the worker exited) and dispatch
	// again.
	time.Sleep(500 * time.Millisecond)

	assert.EqualValues(t, 1, dispatchCount.Load(),
		"a completed-but-unflushed issue must never be re-dispatched")

	snap := orch.Snapshot()
	_, running := snap.Running["uuid-abc-123"]
	assert.False(t, running, "the completed run must have exited (not stuck 'running')")
	_, syncing := snap.OutboxSyncing["ENG-1"]
	assert.True(t, syncing, "the issue row must be marked syncing while its completion write is still pending")

	cancel()
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("orch.Run did not exit after cancel")
	}
}
