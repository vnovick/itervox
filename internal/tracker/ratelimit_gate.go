package tracker

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// RateLimitGate coordinates rate-limit backoff across every caller in the
// process instead of each one rediscovering the limit alone (issue #61).
//
// DoWithRateLimitRetry waits out a 429 correctly, but it does so PER CALL. With
// max_concurrent_agents: N, the N workers plus the poller plus the outbox
// flusher each had to hit the limit themselves to learn about it, each burned
// its own retry budget rediscovering the same fact, and — because their
// backoffs were independent — they all resumed at slightly different times,
// hammering the tracker with N probes at the end of each window instead of one.
//
// The gate records "the budget is gone until T" once, and every subsequent
// caller waits for T rather than sending a request that is already known to
// fail.
//
// Three properties are deliberate:
//
//   - **Fail open.** A gate that somehow never clears must not stop the daemon.
//     Every wait is bounded by MaxRateLimitWait, exactly like the per-call
//     backoff, so the worst case is that a caller proceeds and gets its own 429
//     — the pre-gate behaviour, not a deadlock.
//
//   - **Writes go first.** When the gate lifts, writes are admitted ahead of
//     reads, matching the intent of polling.rate_limit_reserve_percent: reads
//     re-derive state the daemon can recompute next tick, while a dropped write
//     is a state transition or comment that may never be retried. This is the
//     read-starves-write loop from #42, at a different layer.
//
//   - **Process-wide, not global-global.** One gate per adapter key, so a
//     GitHub 429 does not stall Linear calls.
type RateLimitGate struct {
	mu     sync.Mutex
	gates  map[string]time.Time // adapter -> instant the budget is expected back
	nowFn  func() time.Time     // injectable for tests
	waitFn func(context.Context, time.Duration) error
}

// NewRateLimitGate constructs an empty gate.
func NewRateLimitGate() *RateLimitGate {
	return &RateLimitGate{
		gates:  make(map[string]time.Time),
		nowFn:  time.Now,
		waitFn: defaultGateWait,
	}
}

func defaultGateWait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Record marks adapter's budget as exhausted until now+retryAfter.
//
// A later reset always wins; an earlier one never shortens an existing gate.
// Shortening would let callers back in before the tracker is ready, which is
// how a coordinated backoff degenerates into the uncoordinated stampede this
// exists to prevent.
//
// retryAfter <= 0 is ignored: with nothing authoritative to record, leaving the
// gate open is better than inventing a window.
func (g *RateLimitGate) Record(adapter string, retryAfter time.Duration) {
	if g == nil || retryAfter <= 0 {
		return
	}
	until := g.nowFn().Add(min(retryAfter, MaxRateLimitWait))
	g.mu.Lock()
	defer g.mu.Unlock()
	if existing, ok := g.gates[adapter]; ok && existing.After(until) {
		return
	}
	g.gates[adapter] = until
}

// Wait blocks until adapter's recorded window has passed, or returns
// immediately when no window is open.
//
// isWrite admits writes ahead of reads: a write waits only for the recorded
// instant, while a read additionally yields a short grace period so the first
// requests through a lifted gate are the ones that cannot be recomputed.
func (g *RateLimitGate) Wait(ctx context.Context, adapter string, isWrite bool) error {
	if g == nil {
		return nil
	}
	d := g.remaining(adapter, isWrite)
	if d <= 0 {
		return nil
	}
	slog.Debug("tracker: waiting on coordinated rate-limit gate",
		"adapter", adapter, "wait", d, "is_write", isWrite)
	return g.waitFn(ctx, d)
}

// writeFirstGrace is how long reads yield to writes after a gate lifts. Short
// enough to be irrelevant to a human watching the dashboard, long enough that
// queued writes win the race against a fleet of readers waking together.
const writeFirstGrace = 250 * time.Millisecond

func (g *RateLimitGate) remaining(adapter string, isWrite bool) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	until, ok := g.gates[adapter]
	if !ok {
		return 0
	}
	d := until.Sub(g.nowFn())
	if d <= 0 {
		// Window has passed — drop it so the map cannot grow unbounded across
		// a long-lived process.
		delete(g.gates, adapter)
		return 0
	}
	if !isWrite {
		d += writeFirstGrace
	}
	// Bounded for the same reason the per-call backoff is: a gate must
	// degrade the daemon, never wedge it.
	return min(d, MaxRateLimitWait)
}

// OpenUntil reports the instant adapter's gate lifts, and whether one is open.
// Exposed for the dashboard/heartbeat so an operator can see that the fleet is
// waiting on a rate limit rather than inferring it from stalled work.
func (g *RateLimitGate) OpenUntil(adapter string) (time.Time, bool) {
	if g == nil {
		return time.Time{}, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	until, ok := g.gates[adapter]
	if !ok || !until.After(g.nowFn()) {
		return time.Time{}, false
	}
	return until, true
}
