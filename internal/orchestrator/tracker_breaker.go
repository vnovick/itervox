package orchestrator

import (
	"errors"
	"time"

	"github.com/vnovick/itervox/internal/tracker"
)

// #187 — tracker transport circuit breaker.
//
// A tracker that times out (or is unreachable) costs every poll its full read
// deadline. After TrackerBreakerThreshold consecutive polls with a transport
// failure the breaker opens: the loop skips its tracker reads (the poll, due
// retries' reads, pending input resumes' reads) until OpenUntil and reports
// the poll degraded on the snapshot, HEARTBEAT.md and /api/v1/ready. The
// first poll after the cooldown is the probe: success closes the breaker,
// another transport failure re-opens it for a fresh cooldown.
//
// The shape mirrors BackendHealth (CORE-053) kept deliberately small: one
// breaker for the one tracker, no per-host keys, no persistence (a restart
// probes the tracker anyway on its first poll). Rate limits are not transport
// failures: the tracker answered, and the #42 read shedding and the shared
// rate-limit gate already handle them.

// TrackerBreakerThreshold is how many consecutive polls with a transport
// failure open the breaker. The same three-in-a-row policy as
// PollFailureEscalationThreshold: one timeout is noise, three is an outage.
const TrackerBreakerThreshold = 3

// Breaker cooldown bounds. The cooldown is two poll intervals, so an open
// breaker skips about two polls, clamped so a very short interval (tests,
// demos) still gives a struggling tracker a real pause and a very long one
// does not leave the daemon blind for longer than five minutes.
const (
	trackerBreakerCooldownMin = 30 * time.Second
	trackerBreakerCooldownMax = 5 * time.Minute
)

// TrackerBreakerState is the event-loop state of the tracker breaker.
type TrackerBreakerState struct {
	// ConsecutiveTransportFailures counts polls in a row that hit a
	// transport failure; any poll without one resets it.
	ConsecutiveTransportFailures int
	// OpenUntil is when an open breaker lets the next poll probe the
	// tracker. Zero while closed.
	OpenUntil time.Time
	// OpenedAt is when the breaker last opened; zero while closed.
	OpenedAt time.Time
}

// Open reports whether the breaker is open at now (reads are skipped).
func (b TrackerBreakerState) Open(now time.Time) bool {
	return !b.OpenUntil.IsZero() && now.Before(b.OpenUntil)
}

// Degraded reports whether the poll is degraded: the breaker is open, or it
// opened and its probe has not succeeded yet.
func (b TrackerBreakerState) Degraded() bool {
	return !b.OpenedAt.IsZero()
}

// trackerBreakerCooldown is the cooldown for the configured poll interval.
func (o *Orchestrator) trackerBreakerCooldown() time.Duration {
	if o.trackerBreakerCooldownOverride > 0 {
		return o.trackerBreakerCooldownOverride
	}
	interval := time.Duration(o.cfg.Polling.IntervalMs) * time.Millisecond
	return min(max(2*interval, trackerBreakerCooldownMin), trackerBreakerCooldownMax)
}

// isTrackerTransportError reports whether err means the tracker could not be
// reached or did not answer in time: a timeout, a network error, or a 5xx.
// A rate limit, an auth failure or a GraphQL error is an answer, not a
// transport failure; a cancellation is the daemon stopping.
func isTrackerTransportError(err error) bool {
	if err == nil {
		return false
	}
	var st *tracker.APIStatusError
	if errors.As(err, &st) {
		return st.Status >= 500
	}
	switch tracker.ClassifyError(err).Kind {
	case tracker.ErrorClassTimeout, tracker.ErrorClassNetwork:
		return true
	}
	return false
}

// observeTrackerTransport folds one poll's outcome into the breaker. failed
// is whether any of the poll's reads hit a transport failure. Event loop.
func (o *Orchestrator) observeTrackerTransport(state *State, failed bool, now time.Time) {
	b := &state.TrackerBreaker
	if !failed {
		if b.Degraded() {
			o.logger().Info("orchestrator: tracker reachable again, poll no longer degraded",
				"degraded_for", now.Sub(b.OpenedAt).Round(time.Second).String())
		}
		*b = TrackerBreakerState{}
		o.pollDegraded.Store(false)
		return
	}
	b.ConsecutiveTransportFailures++
	if b.ConsecutiveTransportFailures < TrackerBreakerThreshold {
		return
	}
	cooldown := o.trackerBreakerCooldown()
	b.OpenUntil = now.Add(cooldown)
	if b.OpenedAt.IsZero() {
		b.OpenedAt = now
	}
	o.pollDegraded.Store(true)
	o.logger().Warn("orchestrator: tracker poll degraded: skipping tracker reads after consecutive transport failures",
		"consecutive_failures", b.ConsecutiveTransportFailures,
		"threshold", TrackerBreakerThreshold,
		"cooldown", cooldown.String(),
		"open_until", b.OpenUntil)
}

// trackerReadsBlocked reports whether the tracker breaker is open at now, so
// an on-loop tracker read should be skipped (and left due). Event loop.
func trackerReadsBlocked(state State, now time.Time) bool {
	return state.TrackerBreaker.Open(now)
}
