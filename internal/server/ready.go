package server

import (
	"net/http"
	"time"
)

// readyPath is the readiness probe. Like healthPath it is unauthenticated
// and exempt from the Host guard; see hostGuard for why.
const readyPath = "/api/v1/ready"

// ReadyTickBudget bounds how long one event-loop iteration (a poll tick or an
// event) may run before /ready calls the loop stale. An explicit policy
// constant, NOT derived from tracker.MaxRateLimitWait: that caps a SINGLE
// rate-limit wait, while one tracker call can take a gate wait plus up to
// four retry waits, and one tick makes several tracker calls (reconcile, a
// paginated candidate fetch, resumes). Ten minutes is far past any healthy
// tick and well inside what an operator tolerates before a restart.
const ReadyTickBudget = 10 * time.Minute

// readyMinStaleAfter floors the idle-staleness threshold so a very short
// polling.interval_ms (tests, demos) cannot make a GC pause or a slow disk
// write flap the probe.
const readyMinStaleAfter = 30 * time.Second

// ReadinessSignals are the /ready inputs (CORE-043). Everything the event
// loop contributes is published through atomics it alone writes, so building
// this never takes cfgMu, snapMu or State.
type ReadinessSignals struct {
	// StartedAt is when this daemon generation started; with LoopStarted
	// false it anchors the startup grace of one poll interval.
	StartedAt   time.Time
	LoopStarted bool
	// LastLoopIdle is when the event loop last finished an iteration.
	LastLoopIdle time.Time
	// TickStarted is when the iteration now running began, zero when the loop
	// is idle in its select. Readiness is phase-aware: inside an iteration
	// the loop is stale only past ReadyTickBudget, so a slow tracker call is
	// not read as a wedge.
	TickStarted  time.Time
	PollInterval time.Duration
	// LastPollOK is whether the most recent candidate poll succeeded.
	LastPollOK bool
	// PollRateLimited is whether the most recent poll failed with a tracker
	// rate limit (expected and bounded: degraded, never unready).
	PollRateLimited bool
	// PollShedding is whether the last tick skipped its poll to keep the
	// tracker budget for writes (degraded, not a failure).
	PollShedding bool
	// ConsecutivePollFailures counts consecutive non-rate-limited poll
	// failures; at PollFailureThreshold the daemon is not ready.
	ConsecutivePollFailures int
	PollFailureThreshold    int
	// ConfigInvalid is whether the last WORKFLOW.md reload failed validation.
	ConfigInvalid bool
	// RateLimitedUntil is the tracker rate-limit gate's reset while the gate
	// is open (tracker.SharedRateLimitGate().OpenUntil), nil when closed.
	RateLimitedUntil *time.Time
	// Draining is whether the daemon is draining for shutdown or a
	// WORKFLOW.md reload (CORE-057). A draining daemon is not ready, so a
	// load balancer stops routing new requests to it.
	Draining bool
}

// ReadyResponse is the /api/v1/ready body: booleans and one timestamp, no
// error text, identifiers or configuration, so exposing it without a token
// (and to any Host) discloses nothing beyond "up / degraded".
type ReadyResponse struct {
	Ready                   bool       `json:"ready"`
	LoopFresh               bool       `json:"loop_fresh"`
	LastPollOK              bool       `json:"last_poll_ok"`
	ConfigInvalid           bool       `json:"config_invalid"`
	Degraded                bool       `json:"degraded"`
	TrackerRateLimitedUntil *time.Time `json:"tracker_rate_limited_until"`
	Draining                bool       `json:"draining"`
}

// EvaluateReadiness applies the readiness policy to sig at now.
//
//   - loop_fresh: before the loop's first iteration, fresh during a startup
//     grace of one poll interval (floored at 30 s). Inside an iteration, fresh until it has run
//     ReadyTickBudget. Idle, fresh while the last iteration ended within
//     3x the poll interval (floored at 30 s).
//   - ready = loop_fresh AND fewer than PollFailureThreshold consecutive
//     (non-rate-limited) poll failures AND not draining (CORE-057).
//   - degraded: a rate-limited poll or an open rate-limit gate, a tick that
//     shed its poll to keep the tracker budget for writes, a failure run
//     below the threshold, or an invalid WORKFLOW.md. None of these make the
//     daemon unready: it is still working (on its last valid config, or
//     waiting out a published reset), and pulling it out of a load balancer
//     would only hide the dashboard an operator needs to fix it.
func EvaluateReadiness(sig ReadinessSignals, now time.Time) ReadyResponse {
	interval := max(sig.PollInterval, time.Second)
	var fresh bool
	switch {
	case !sig.LoopStarted:
		// BH-M2-5: the same floor as the idle threshold below.
		fresh = !sig.StartedAt.IsZero() && now.Sub(sig.StartedAt) <= max(interval, readyMinStaleAfter)
	case !sig.TickStarted.IsZero():
		fresh = now.Sub(sig.TickStarted) <= ReadyTickBudget
	default:
		fresh = now.Sub(sig.LastLoopIdle) <= max(3*interval, readyMinStaleAfter)
	}
	threshold := sig.PollFailureThreshold
	if threshold <= 0 {
		threshold = 3
	}
	failing := sig.ConsecutivePollFailures >= threshold
	return ReadyResponse{
		Ready:         fresh && !failing && !sig.Draining,
		LoopFresh:     fresh,
		LastPollOK:    sig.LastPollOK,
		ConfigInvalid: sig.ConfigInvalid,
		Degraded: sig.PollRateLimited || sig.PollShedding || sig.RateLimitedUntil != nil ||
			sig.ConsecutivePollFailures > 0 || sig.ConfigInvalid,
		TrackerRateLimitedUntil: sig.RateLimitedUntil,
		Draining:                sig.Draining,
	}
}

// handleReady is the readiness probe: 200 when ready, 503 otherwise, with the
// same body either way. It reads only s.readiness — never the snapshot, whose
// generation timestamp is stamped at request time and proves nothing about
// the loop.
func (s *Server) handleReady(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.readiness == nil {
		writeJSON(w, http.StatusServiceUnavailable, ReadyResponse{})
		return
	}
	resp := EvaluateReadiness(s.readiness(), time.Now())
	status := http.StatusOK
	if !resp.Ready {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, resp)
}
