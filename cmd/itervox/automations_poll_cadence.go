package main

import (
	"log/slog"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/tracker"
)

// automationPollBackoffPercent is the share of the tracker's request budget
// below which the automation event poll stretches its interval (#155). It is
// twice the default write reserve (polling.rate_limit_reserve_percent), so
// the poll slows down before the orchestrator starts shedding its own reads.
const automationPollBackoffPercent = 2 * config.DefaultRateLimitReservePercent

// automationPollMaxBackoff caps the stretch: at most this many times the
// configured interval (2 minutes at the 15s default).
const automationPollMaxBackoff = 8

// automationPollCadence is the automation event poll's interval (#155):
// polling.automation_interval_ms, doubled after each poll that finds the
// tracker's remaining request budget below automationPollBackoffPercent of
// its limit (up to automationPollMaxBackoff times), and back to the
// configured interval after the first poll that finds it recovered. Owned by
// the automation scheduler goroutine; not shared.
type automationPollCadence struct {
	base   time.Duration
	factor int
	// low / recovered are the log lines for a stretch and for a return to
	// the base interval (the PR-event poll, #173, shares this cadence).
	low, recovered string
}

func newAutomationPollCadence(intervalMs int) *automationPollCadence {
	return newPollCadence(intervalMs,
		"automation: tracker request budget is low, slowing the automation event poll",
		"automation: tracker request budget recovered, automation event poll back to its interval")
}

func newPollCadence(intervalMs int, low, recovered string) *automationPollCadence {
	if intervalMs <= 0 {
		intervalMs = config.DefaultAutomationPollIntervalMs
	}
	return &automationPollCadence{base: time.Duration(intervalMs) * time.Millisecond, factor: 1, low: low, recovered: recovered}
}

// nextDelay is the wait before the next poll, given the budget src (a
// tracker, or any tracker.RateLimiter) reports now.
func (c *automationPollCadence) nextDelay(src any) time.Duration {
	snap, low := trackerBudgetLow(src, automationPollBackoffPercent)
	switch {
	case low:
		if f := min(c.factor*2, automationPollMaxBackoff); f != c.factor {
			c.factor = f
			slog.Warn(c.low,
				"requests_remaining", snap.RequestsRemaining, "requests_limit", snap.RequestsLimit,
				"interval", c.base*time.Duration(c.factor))
		}
	case c.factor > 1:
		c.factor = 1
		slog.Info(c.recovered, "interval", c.base)
	}
	return c.base * time.Duration(c.factor)
}

// trackerBudgetLow reports whether the tracker's remaining request budget is
// below percent of its limit. Like shouldShedPollingReads it fails open: an
// adapter without counters, or a snapshot without a limit, is never low.
func trackerBudgetLow(src any, percent int) (*tracker.RateLimitSnapshot, bool) {
	limiter, ok := src.(tracker.RateLimiter)
	if !ok {
		return nil, false
	}
	snap := limiter.RateLimitSnapshot()
	if snap == nil || snap.RequestsLimit <= 0 {
		return snap, false
	}
	return snap, snap.RequestsRemaining < snap.RequestsLimit*percent/100
}
