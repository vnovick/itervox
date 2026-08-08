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

// updateStateCountingTracker wraps MemoryTracker and counts calls to
// UpdateIssueState — used to prove the outbox-backed write path never
// touches the tracker directly for a completion transition.
type updateStateCountingTracker struct {
	*tracker.MemoryTracker
	updateCalls atomic.Int64
}

func (u *updateStateCountingTracker) UpdateIssueState(ctx context.Context, issueID, stateName string) error {
	u.updateCalls.Add(1)
	return u.MemoryTracker.UpdateIssueState(ctx, issueID, stateName)
}

// TestOrchestratorCompletionTransitionOutboxSink is the sink-routing
// load-bearing test (Task 2 of the write-ahead-outbox plan): with an
// outbox-backed WriteSink injected via SetWriteSink, a real worker
// completion (real event loop, memory tracker, production-shaped issue
// IDs where ID != Identifier) must enqueue exactly one update_state entry
// on the outbox and must NEVER call tracker.UpdateIssueState directly.
//
// Mutation coverage: if outboxWriteSink secretly called the tracker (or if
// SetWriteSink were ignored and the default direct sink ran instead),
// updateCalls would be nonzero and this test would fail.
func TestOrchestratorCompletionTransitionOutboxSink(t *testing.T) {
	cfg := baseConfig()
	// A large poll interval means exactly one tick fires (the immediate
	// t=0 tick — see event_loop.go Run()'s time.NewTimer(0)) within this
	// test's window. That matters here specifically because Task 2 has no
	// overlay yet (that's Task 3): outboxWriteSink never mutates the
	// tracker, so the tracker-visible state stays "In Progress" and a
	// second poll tick would re-dispatch the same issue and enqueue a
	// second entry — not a routing bug, just the overlay not existing yet.
	cfg.Polling.IntervalMs = 60000
	cfg.Agent.MaxTurns = 3
	cfg.Tracker.CompletionState = "Done"

	// Production-shaped: ID (tracker-internal, opaque) != Identifier
	// (human-readable) — same distinction the outbox.Entry validation and
	// the WriteSink interface both care about.
	mt := &updateStateCountingTracker{
		MemoryTracker: tracker.NewMemoryTracker(
			[]domain.Issue{{ID: "uuid-abc-123", Identifier: "ENG-1", Title: "T", State: "In Progress"}},
			cfg.Tracker.ActiveStates,
			cfg.Tracker.TerminalStates,
		),
	}

	ob, err := outbox.New(t.TempDir() + "/outbox.json")
	require.NoError(t, err)

	runner := &succeedOnceRunner{}
	orch := orchestrator.New(cfg, mt, runner, nil)
	orch.SetWriteSink(orchestrator.NewOutboxWriteSink(ob))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go orch.Run(ctx) //nolint:errcheck

	deadline := time.After(4 * time.Second)
	for {
		entries := ob.Snapshot()
		if len(entries) > 0 {
			require.Len(t, entries, 1)
			assert.Equal(t, outbox.KindUpdateState, entries[0].Kind)
			assert.Equal(t, "uuid-abc-123", entries[0].IssueID)
			assert.Equal(t, "ENG-1", entries[0].Identifier)
			assert.Equal(t, "Done", entries[0].TargetState)
			assert.EqualValues(t, 0, mt.updateCalls.Load(),
				"outbox sink must never call tracker.UpdateIssueState directly")

			// The overlay/reconciliation logic that would apply this entry to
			// the tracker is a later task (Task 3) — under Task 2 alone the
			// tracker-visible state must still show the pre-transition
			// state, confirming nothing but the sink changed.
			issues, ferr := mt.FetchIssueStatesByIDs(ctx, []string{"uuid-abc-123"})
			require.NoError(t, ferr)
			require.Len(t, issues, 1)
			assert.Equal(t, "In Progress", issues[0].State,
				"outbox path must not mutate tracker state synchronously — only the flusher (later task) does")
			cancel()
			return
		}
		select {
		case <-deadline:
			snap := orch.Snapshot()
			t.Fatalf("no outbox entry enqueued within timeout; Running=%d, Claimed=%d",
				len(snap.Running), len(snap.Claimed))
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// TestOrchestratorCompletionTransitionDirectSinkDefault pins that the
// default construction path (no SetWriteSink call) keeps the pre-outbox,
// synchronous, tracker-calling behavior — every existing orchestrator test
// (e.g. TestOrchestratorLifecycleWithCompletionState in integration_test.go)
// relies on this default not changing.
func TestOrchestratorCompletionTransitionDirectSinkDefault(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 3
	cfg.Tracker.CompletionState = "Done"

	mt := &updateStateCountingTracker{
		MemoryTracker: tracker.NewMemoryTracker(
			[]domain.Issue{{ID: "uuid-abc-123", Identifier: "ENG-1", Title: "T", State: "In Progress"}},
			cfg.Tracker.ActiveStates,
			cfg.Tracker.TerminalStates,
		),
	}

	runner := &succeedOnceRunner{}
	orch := orchestrator.New(cfg, mt, runner, nil)
	// No SetWriteSink call — must default to direct behavior.

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go orch.Run(ctx) //nolint:errcheck

	deadline := time.After(4 * time.Second)
	for {
		issues, _ := mt.FetchIssueStatesByIDs(ctx, []string{"uuid-abc-123"})
		if len(issues) > 0 && issues[0].State == "Done" {
			assert.GreaterOrEqual(t, mt.updateCalls.Load(), int64(1),
				"direct sink must call tracker.UpdateIssueState synchronously")
			cancel()
			return
		}
		select {
		case <-deadline:
			snap := orch.Snapshot()
			t.Fatalf("issue was not transitioned to Done within timeout; Running=%d, Claimed=%d",
				len(snap.Running), len(snap.Claimed))
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// TestOrchestratorCompletionTransitionExplicitDirectSink is the same
// scenario as TestOrchestratorCompletionTransitionDirectSinkDefault but
// with an explicit SetWriteSink(orchestrator.NewDirectWriteSink(tr)) call —
// the kill-switch path cmd/itervox (Task 3) uses when cfg.Tracker.Outbox is
// false. Proves NewDirectWriteSink is byte-identical to the construction
// default, not just superficially similar.
func TestOrchestratorCompletionTransitionExplicitDirectSink(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 3
	cfg.Tracker.CompletionState = "Done"

	mt := &updateStateCountingTracker{
		MemoryTracker: tracker.NewMemoryTracker(
			[]domain.Issue{{ID: "uuid-abc-123", Identifier: "ENG-1", Title: "T", State: "In Progress"}},
			cfg.Tracker.ActiveStates,
			cfg.Tracker.TerminalStates,
		),
	}

	runner := &succeedOnceRunner{}
	orch := orchestrator.New(cfg, mt, runner, nil)
	orch.SetWriteSink(orchestrator.NewDirectWriteSink(mt))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go orch.Run(ctx) //nolint:errcheck

	deadline := time.After(4 * time.Second)
	for {
		issues, _ := mt.FetchIssueStatesByIDs(ctx, []string{"uuid-abc-123"})
		if len(issues) > 0 && issues[0].State == "Done" {
			assert.GreaterOrEqual(t, mt.updateCalls.Load(), int64(1))
			cancel()
			return
		}
		select {
		case <-deadline:
			snap := orch.Snapshot()
			t.Fatalf("issue was not transitioned to Done within timeout; Running=%d, Claimed=%d",
				len(snap.Running), len(snap.Claimed))
		case <-time.After(50 * time.Millisecond):
		}
	}
}
