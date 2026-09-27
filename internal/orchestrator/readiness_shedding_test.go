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
