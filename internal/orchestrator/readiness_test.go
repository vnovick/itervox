package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// CORE-043 — the event loop publishes lock-free readiness signals.

// blockingPollTracker blocks FetchCandidateIssues until release is closed,
// standing in for a slow tracker call inside onTick.
type blockingPollTracker struct {
	*tracker.MemoryTracker
	entered chan struct{}
	release chan struct{}
}

func (b *blockingPollTracker) FetchCandidateIssues(ctx context.Context) ([]domain.Issue, error) {
	select {
	case b.entered <- struct{}{}:
	default:
	}
	select {
	case <-b.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return nil, nil
}

func TestReadinessPublishesTickPhase(t *testing.T) {
	cfg := refreshTestCfg()
	cfg.Polling.IntervalMs = 60_000
	tr := &blockingPollTracker{
		MemoryTracker: tracker.NewMemoryTracker(nil, nil, nil),
		entered:       make(chan struct{}, 1),
		release:       make(chan struct{}),
	}
	o := New(cfg, tr, nil, nil)
	before := o.Readiness()
	assert.False(t, before.LoopStarted)
	assert.Equal(t, time.Minute, before.PollInterval)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = o.Run(ctx) }()
	defer func() { cancel(); <-done }()

	select {
	case <-tr.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first tick never polled")
	}
	mid := o.Readiness()
	assert.True(t, mid.LoopStarted)
	assert.False(t, mid.TickStarted.IsZero(), "inside onTick the tick start is published")

	close(tr.release)
	require.Eventually(t, func() bool {
		r := o.Readiness()
		return r.TickStarted.IsZero() && r.LastPollOK
	}, 5*time.Second, 10*time.Millisecond, "after the tick the loop is idle and the poll ok")
	assert.WithinDuration(t, time.Now(), o.Readiness().LastLoopIdle, 5*time.Second)
}

func TestReadinessRateLimitedPollDoesNotCount(t *testing.T) {
	tr := &scriptedPollTracker{MemoryTracker: tracker.NewMemoryTracker(nil, nil, nil)}
	tr.setErr(&tracker.RateLimitedError{Adapter: "linear", ResetAt: time.Now().Add(time.Hour)})
	o, _ := newPollTestOrchestrator(t, tr)
	state := NewState(o.cfg)
	for range 3 {
		state = o.onTick(context.Background(), state)
	}
	r := o.Readiness()
	assert.True(t, r.PollRateLimited)
	assert.False(t, r.LastPollOK)
	assert.Zero(t, r.ConsecutivePollFailures)

	tr.setErr(errors.New("outage"))
	for range 3 {
		state = o.onTick(context.Background(), state)
	}
	r = o.Readiness()
	assert.False(t, r.PollRateLimited)
	assert.Equal(t, 3, r.ConsecutivePollFailures)
	assert.Equal(t, PollFailureEscalationThreshold, r.PollFailureThreshold)

	tr.setErr(nil)
	_ = o.onTick(context.Background(), state)
	r = o.Readiness()
	assert.True(t, r.LastPollOK)
	assert.Zero(t, r.ConsecutivePollFailures)
}
