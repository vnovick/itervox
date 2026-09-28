package orchestrator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/vnovick/itervox/internal/metrics"
	"github.com/vnovick/itervox/internal/tracker"
)

// BH-M2-4: after the event loop has exited nothing drains o.events, so a
// RecordFailure there used to "succeed" into a dead channel — lost without
// being counted. It must report the drop and count it instead.
func TestRecentFailures_RecordFailureAfterLoopExitIsCounted(t *testing.T) {
	cfg := refreshTestCfg()
	cfg.Polling.IntervalMs = 60_000
	o := New(cfg, tracker.NewMemoryTracker(nil, nil, nil), nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = o.Run(ctx) }()
	cancel()
	<-done

	before, droppedBefore := o.FailureEventsDropped(), metrics.EventsDroppedCount()
	ok := o.RecordFailure(FailureRecord{Kind: FailureKindPersist, Source: "paused", Message: "late write failure"})
	assert.False(t, ok, "a failure recorded after the loop exited is not delivered")
	assert.Equal(t, before+1, o.FailureEventsDropped())
	assert.Equal(t, droppedBefore+1, metrics.EventsDroppedCount())
}
