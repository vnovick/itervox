package orchestrator

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/vnovick/itervox/internal/tracker"
)

type fakeRateLimitedTracker struct {
	tracker.Tracker
	snap *tracker.RateLimitSnapshot
}

func (f *fakeRateLimitedTracker) RateLimitSnapshot() *tracker.RateLimitSnapshot { return f.snap }

// plainTracker implements no rate-limit reporting at all.
type plainTracker struct{ tracker.Tracker }

// TestShouldShedPollingReads pins the reserve that breaks issue #42's
// feedback loop: below it, polling reads stop so writes can still land.
func TestShouldShedPollingReads(t *testing.T) {
	limited := func(remaining, limit int) tracker.Tracker {
		return &fakeRateLimitedTracker{snap: &tracker.RateLimitSnapshot{
			RequestsRemaining: remaining, RequestsLimit: limit,
		}}
	}

	// Linear's documented ceiling; 10% reserve = 250.
	assert.False(t, shouldShedPollingReads(limited(2500, 2500), 10), "full budget: poll freely")
	assert.False(t, shouldShedPollingReads(limited(251, 2500), 10), "just above the reserve")
	assert.True(t, shouldShedPollingReads(limited(249, 2500), 10), "inside the reserve: shed")
	assert.True(t, shouldShedPollingReads(limited(0, 2500), 10), "exhausted: shed")
}

// TestShouldShedPollingReadsFailsOpen is the load-bearing safety property.
// Shedding on unknown data would stop polling entirely on any adapter that
// does not report counters — turning a diagnostic gap into an outage. Every
// unknown must poll.
func TestShouldShedPollingReadsFailsOpen(t *testing.T) {
	assert.False(t, shouldShedPollingReads(&plainTracker{}, 10),
		"an adapter with no rate-limit reporting must keep polling")
	assert.False(t, shouldShedPollingReads(&fakeRateLimitedTracker{snap: nil}, 10),
		"no snapshot observed yet must keep polling")
	assert.False(t, shouldShedPollingReads(&fakeRateLimitedTracker{
		snap: &tracker.RateLimitSnapshot{RequestsRemaining: 0, RequestsLimit: 0}}, 10),
		"a zero limit is unknown, not exhausted")
	assert.False(t, shouldShedPollingReads(&fakeRateLimitedTracker{
		snap: &tracker.RateLimitSnapshot{RequestsRemaining: 0, RequestsLimit: 2500}}, 0),
		"a zero reserve disables shedding entirely")
}
