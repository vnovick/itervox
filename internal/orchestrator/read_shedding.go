package orchestrator

import (
	"log/slog"

	"github.com/vnovick/itervox/internal/config"

	"github.com/vnovick/itervox/internal/tracker"
)

// DefaultRateLimitReservePercent is the share of the tracker's hourly request
// budget held back for WRITES. Below it, the orchestrator stops spending
// requests on polling reads.
//
// Issue #42-E: reads and writes drew from one undifferentiated budget, so on a
// busy project the polling loops exhausted the hour in its first ~25 minutes
// and every state transition, comment and dependency audit failed for the
// remaining ~35. Worse, the loops that consume the budget scale with the
// number of STUCK issues, and unsticking one requires a write — so reads
// starved the very operations that would have drained the queue. That is the
// feedback loop #42 describes, and a reserve is what breaks it.
//
// 10% of Linear's documented 2,500/hour is 250 requests — comfortably more
// than a fleet's writes in the window it takes the budget to reset, while
// costing polling freshness only when the budget is nearly gone anyway.
const DefaultRateLimitReservePercent = config.DefaultRateLimitReservePercent

// shouldShedPollingReads reports whether the remaining tracker budget has
// fallen into the write reserve.
//
// Deliberately fails OPEN — an adapter that reports no counters, a zero limit,
// or a snapshot not yet populated all return false. Shedding on unknown data
// would silently stop the daemon polling on any adapter that does not report
// rate limits (today: the in-memory tracker, and any future adapter), turning
// a diagnostic gap into a total outage. Over-polling degrades; not polling
// stops.
func shouldShedPollingReads(tr tracker.Tracker, reservePercent int) bool {
	limiter, ok := tr.(tracker.RateLimiter)
	if !ok || reservePercent <= 0 {
		return false
	}
	snap := limiter.RateLimitSnapshot()
	if snap == nil || snap.RequestsLimit <= 0 {
		return false
	}
	reserve := snap.RequestsLimit * reservePercent / 100
	return snap.RequestsRemaining < reserve
}

// logReadShedding reports the decision once per tick, at Warn, because an
// operator seeing stalled polling needs to know the cause is budget rather
// than a hung daemon.
func logReadShedding(tr tracker.Tracker) {
	limiter, ok := tr.(tracker.RateLimiter)
	if !ok {
		return
	}
	if snap := limiter.RateLimitSnapshot(); snap != nil {
		slog.Warn("orchestrator: tracker budget in the write reserve — shedding polling reads until it resets",
			"requests_remaining", snap.RequestsRemaining,
			"requests_limit", snap.RequestsLimit,
			"reset", snap.Reset)
	}
}

// rateLimitReservePercent returns the configured write-reserve percentage,
// falling back to the default for an unset or out-of-range value.
//
// Read under cfgMu-free access because Polling config has no runtime setter:
// every write is at load time (see CLAUDE.md's cfgMu allowlist, which does not
// include it).
func (o *Orchestrator) rateLimitReservePercent() int {
	if o == nil || o.cfg == nil {
		return DefaultRateLimitReservePercent
	}
	pct := o.cfg.Polling.RateLimitReservePercent
	// An explicit 0 disables shedding — a deliberate escape hatch for an
	// operator who would rather over-poll than ever pause dispatch. Anything
	// at or above 100 would shed permanently, which is never intended.
	if pct < 0 || pct >= 100 {
		return DefaultRateLimitReservePercent
	}
	return pct
}
