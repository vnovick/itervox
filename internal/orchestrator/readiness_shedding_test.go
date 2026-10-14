package orchestrator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/vnovick/itervox/internal/tracker"
)

type sheddingTracker struct {
	*tracker.MemoryTracker
	snap *tracker.RateLimitSnapshot
}

func (s *sheddingTracker) RateLimitSnapshot() *tracker.RateLimitSnapshot { return s.snap }

// BH-M2-6: a tick that sheds its polling read (tracker budget in the write
// reserve) used to return before publishing any poll status, so /ready kept
// reporting the last real poll's last_poll_ok=true indefinitely. A shedding
// tick now publishes "no poll, shedding" — degraded, never a failure.
func TestReadinessPublishesReadShedding(t *testing.T) {
	tr := &sheddingTracker{MemoryTracker: tracker.NewMemoryTracker(nil, nil, nil)}
	cfg := refreshTestCfg()
	cfg.Polling.RateLimitReservePercent = 10
	o := &Orchestrator{tracker: tr, events: make(chan OrchestratorEvent, 8), cfg: cfg}
	state := NewState(cfg)

	state = o.onTick(context.Background(), state)
	assert.True(t, o.Readiness().LastPollOK, "a real poll succeeded")
	assert.False(t, o.Readiness().PollShedding)

	tr.snap = &tracker.RateLimitSnapshot{RequestsRemaining: 10, RequestsLimit: 2500}
	state = o.onTick(context.Background(), state)
	r := o.Readiness()
	assert.False(t, r.LastPollOK, "no poll happened this tick")
	assert.True(t, r.PollShedding)
	assert.Zero(t, r.ConsecutivePollFailures, "shedding is not a failure")

	tr.snap = nil
	_ = o.onTick(context.Background(), state)
	r = o.Readiness()
	assert.True(t, r.LastPollOK)
	assert.False(t, r.PollShedding)
}

// TestReadinessRecordsLastSuccessfulPollTime (#117): with no tracker store in
// front, the sync status's last pull is the last successful candidate poll.
// A failed or rate-limited poll keeps the previous success's time.
func TestReadinessRecordsLastSuccessfulPollTime(t *testing.T) {
	cfg := refreshTestCfg()
	o := &Orchestrator{tracker: tracker.NewMemoryTracker(nil, nil, nil), events: make(chan OrchestratorEvent, 8), cfg: cfg}
	state := NewState(cfg)
	assert.True(t, o.Readiness().LastPollOKAt.IsZero(), "no poll yet")

	_ = o.onTick(context.Background(), state)
	okAt := o.Readiness().LastPollOKAt
	assert.False(t, okAt.IsZero(), "a successful poll is stamped")

	o.publishPollStatus(state, false, false)
	o.publishPollStatus(state, false, true)
	assert.Equal(t, okAt, o.Readiness().LastPollOKAt, "failures keep the last success's time")
}
