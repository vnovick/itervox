package tracker

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testGate builds a gate with a controllable clock and a wait that records
// rather than sleeps, so the coordination logic is tested without real time.
func testGate(now *time.Time) (*RateLimitGate, *[]time.Duration) {
	var waits []time.Duration
	g := NewRateLimitGate()
	g.nowFn = func() time.Time { return *now }
	g.waitFn = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}
	return g, &waits
}

// TestGateMakesOneDiscoveryServeTheFleet is issue #61's core claim: after ONE
// caller hits a 429, every other caller waits instead of sending a request
// already known to fail.
func TestGateMakesOneDiscoveryServeTheFleet(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	g, waits := testGate(&now)

	g.Record("linear", 30*time.Second) // one caller discovers the limit

	for i := 0; i < 5; i++ {
		require.NoError(t, g.Wait(context.Background(), "linear", true))
	}

	assert.Len(t, *waits, 5, "every subsequent caller must wait on the shared window")
	for _, w := range *waits {
		assert.InDelta(t, float64(30*time.Second), float64(w), float64(time.Second),
			"each waits for the recorded window, not its own rediscovered backoff")
	}
}

// TestGateAdmitsWritesAheadOfReads pins the write-first ordering. Reads
// re-derive state the daemon recomputes next tick; a starved write may never be
// retried — the read-starves-write loop from #42 at a different layer.
func TestGateAdmitsWritesAheadOfReads(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	g, waits := testGate(&now)
	g.Record("linear", 10*time.Second)

	require.NoError(t, g.Wait(context.Background(), "linear", true))  // write
	require.NoError(t, g.Wait(context.Background(), "linear", false)) // read

	require.Len(t, *waits, 2)
	assert.Less(t, (*waits)[0], (*waits)[1],
		"a read must yield to writes when the gate lifts, not race them")
}

// TestGateIsClosedWhenNoLimitObserved pins that the common path is free: with
// no window open, nothing waits.
func TestGateIsClosedWhenNoLimitObserved(t *testing.T) {
	now := time.Now()
	g, waits := testGate(&now)

	require.NoError(t, g.Wait(context.Background(), "linear", false))

	assert.Empty(t, *waits, "no observed rate limit must cost no wait")
}

// TestGateExpiresAndFailsOpen pins that a passed window stops gating, and that
// the entry is dropped so the map cannot grow across a long-lived process.
func TestGateExpiresAndFailsOpen(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	g, waits := testGate(&now)
	g.Record("linear", 5*time.Second)

	now = now.Add(6 * time.Second) // window has passed

	require.NoError(t, g.Wait(context.Background(), "linear", false))
	assert.Empty(t, *waits, "an expired window must not gate")

	_, open := g.OpenUntil("linear")
	assert.False(t, open, "an expired window must be reported closed")

	g.mu.Lock()
	_, present := g.gates["linear"]
	g.mu.Unlock()
	assert.False(t, present, "expired entries must be dropped, not accumulated")
}

// TestGateNeverShortensAnOpenWindow pins that an earlier reset cannot let
// callers back in before the tracker is ready — that would degenerate the
// coordinated backoff into the stampede it exists to prevent.
func TestGateNeverShortensAnOpenWindow(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	g, _ := testGate(&now)

	g.Record("linear", 40*time.Second)
	g.Record("linear", 5*time.Second) // a later, shorter observation

	until, open := g.OpenUntil("linear")
	require.True(t, open)
	assert.True(t, until.After(now.Add(30*time.Second)),
		"a shorter observation must not shorten an open window")
}

// TestGateIsPerAdapter pins that a GitHub limit does not stall Linear calls.
func TestGateIsPerAdapter(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	g, waits := testGate(&now)

	g.Record("github", 30*time.Second)
	require.NoError(t, g.Wait(context.Background(), "linear", false))

	assert.Empty(t, *waits, "one adapter's rate limit must not gate another's")
}

// TestGateWaitIsBounded pins the fail-open property: a pathological window
// must degrade the daemon, never wedge it.
func TestGateWaitIsBounded(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	g, waits := testGate(&now)

	g.Record("linear", 24*time.Hour)

	require.NoError(t, g.Wait(context.Background(), "linear", true))
	require.Len(t, *waits, 1)
	assert.LessOrEqual(t, (*waits)[0], MaxRateLimitWait,
		"no caller may block longer than the per-call cap")
}

// TestGateIsSafeUnderConcurrency runs the real (unstubbed) gate under parallel
// Record/Wait/OpenUntil, since its entire purpose is cross-goroutine use.
func TestGateIsSafeUnderConcurrency(t *testing.T) {
	g := NewRateLimitGate()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			g.Record("linear", time.Duration(i)*time.Millisecond)
			_, _ = g.OpenUntil("linear")
			_ = g.Wait(context.Background(), "github", i%2 == 0) // never gated
		}(i)
	}
	wg.Wait()
}

// TestGateWaitRespectsContextCancellation pins that a shutdown does not have to
// outlast a rate-limit window.
func TestGateWaitRespectsContextCancellation(t *testing.T) {
	g := NewRateLimitGate()
	g.Record("linear", MaxRateLimitWait)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := g.Wait(ctx, "linear", true)
	assert.ErrorIs(t, err, context.Canceled,
		"a cancelled context must abandon the wait rather than block shutdown")
}
