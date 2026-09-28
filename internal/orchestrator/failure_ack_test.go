package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestFailureAcksKeepMaxAndPrune (CORE-175): the event loop keeps the
// latest upTo per issue, ignores an ack with nothing to ack, and drops an
// ack once the issue's worker failures left the RecentFailures ring. Acks
// are accepted while draining (they start no work).
func TestFailureAcksKeepMaxAndPrune(t *testing.T) {
	o := New(testConfig(), nil, nil, nil)
	state := NewState(o.cfg)
	state.Draining = true
	t0 := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	state.RecentFailures = []FailureRecord{
		{Kind: FailureKindWorkerFailed, Identifier: "ENG-1", OccurredAt: t0},
		{Kind: FailureKindWorkerStalled, Identifier: "ENG-2", OccurredAt: t0},
	}
	ack := func(id string, at time.Time) {
		state = o.handleEvent(context.Background(), state, OrchestratorEvent{Type: EventAckFailures, Identifier: id, AckUpTo: at})
	}
	ack("ENG-1", t0)
	ack("ENG-1", t0.Add(-time.Hour)) // older: ignored
	ack("ENG-2", t0)
	assert.True(t, state.FailureAcks["ENG-1"].Equal(t0))
	assert.Contains(t, state.FailureAcks, "ENG-2")

	// ENG-2's failure ages out of the ring → its ack goes at the next prune.
	state.RecentFailures = state.RecentFailures[:1]
	pruneFailureAcks(&state)
	assert.NotContains(t, state.FailureAcks, "ENG-2")
	assert.Contains(t, state.FailureAcks, "ENG-1")
}
