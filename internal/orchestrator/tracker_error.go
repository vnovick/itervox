package orchestrator

import (
	"errors"
	"time"

	"github.com/vnovick/itervox/internal/tracker"
)

// CORE-044 — tracker-error visibility.
//
// Tracker errors used to be invisible to anything but a careful reader of the
// daemon log: a failed poll logged Warn, a retry-scheduled worker failure
// logged Info, and HEARTBEAT's "Last error" never looked at the tracker. The
// monitoring README's level=ERROR alert therefore rarely fired during a real
// outage. State.LastTrackerError is the event-loop-owned record of the most
// recent tracker failure, surfaced on the snapshot, the HEARTBEAT and the
// metrics view.

// Tracker error kinds. A rate limit is the tracker asking us to wait — it is
// expected and bounded (DoWithRateLimitRetry fails fast past MaxRateLimitWait
// with a typed *tracker.RateLimitedError) — so it is recorded but never
// escalated. Everything else is an outage.
const (
	TrackerErrorKindOutage      = "outage"
	TrackerErrorKindRateLimited = "rate_limited"
)

// Tracker operations that record a LastTrackerError.
const (
	// TrackerErrorOpPoll is the per-tick candidate fetch.
	TrackerErrorOpPoll = "poll"
	// TrackerErrorOpUpdateState is the failed-state move after retries are
	// exhausted (asyncDiscardAndTransitionTo).
	TrackerErrorOpUpdateState = "update_state"
)

// PollFailureEscalationThreshold is how many consecutive non-rate-limited
// poll failures it takes before the failure logs at Error instead of Warn.
// A single blip (a DNS hiccup, a 502 from a proxy) is noise; three polls in a
// row — at the default 30 s interval, a minute and a half of no dispatch — is
// an outage an operator should be paged for. A policy constant, not config.
const PollFailureEscalationThreshold = 3

// trackerWriteErrorTTL bounds how long a write failure (op update_state)
// stays in LastTrackerError when nothing newer replaces it. A successful poll
// says nothing about the write path, so it does not clear a write failure;
// without a bound, one failed move would read as the "last error" forever.
const trackerWriteErrorTTL = time.Hour

// TrackerErrorInfo is the most recent tracker failure. The zero value (At is
// zero) means none is recorded.
type TrackerErrorInfo struct {
	At      time.Time
	Op      string // TrackerErrorOp*
	Kind    string // TrackerErrorKind*
	Message string
	// ResetAt is the tracker-published instant a rate limit lifts; zero when
	// Kind is not rate_limited or the tracker published none.
	ResetAt time.Time
}

// classifyTrackerError builds the TrackerErrorInfo for err.
func classifyTrackerError(op string, err error, now time.Time) TrackerErrorInfo {
	info := TrackerErrorInfo{At: now, Op: op, Kind: TrackerErrorKindOutage}
	if err != nil {
		info.Message = err.Error()
	}
	var rl *tracker.RateLimitedError
	if errors.As(err, &rl) {
		info.Kind = TrackerErrorKindRateLimited
		info.ResetAt = rl.ResetAt
	}
	return info
}

// recordPollFailure classifies a failed candidate fetch, records it in
// State.LastTrackerError, and logs it at the level its classification
// earns (CORE-044):
//
//   - *tracker.RateLimitedError: Warn, never escalated, and it neither counts
//     toward nor resets ConsecutivePollFailures — the tracker told us when to
//     come back, and DoWithRateLimitRetry already bounded the wait.
//   - anything else: an outage. ConsecutivePollFailures++; Warn below
//     PollFailureEscalationThreshold, Error from it on, so the monitoring
//     README's level=ERROR alert fires on a sustained outage and not on a
//     single blip.
func (o *Orchestrator) recordPollFailure(state State, err error, now time.Time) State {
	info := classifyTrackerError(TrackerErrorOpPoll, err, now)
	state.LastTrackerError = info
	if info.Kind == TrackerErrorKindRateLimited {
		o.logger().Warn("orchestrator: fetch candidates rate limited, waiting for reset",
			"error", err, "reset_at", info.ResetAt,
			"consecutive_failures", state.ConsecutivePollFailures)
		o.publishPollStatus(state, false, true)
		return state
	}
	state.ConsecutivePollFailures++
	// CORE-046: an outage (never a rate limit) also lands in RecentFailures;
	// identical consecutive messages coalesce into one entry.
	appendRecentFailure(&state, FailureRecord{
		Kind:       FailureKindTrackerPoll,
		Source:     TrackerErrorOpPoll,
		Message:    info.Message,
		OccurredAt: now,
	}, now)
	if state.ConsecutivePollFailures >= PollFailureEscalationThreshold {
		o.logger().Error("orchestrator: fetch candidates failed",
			"error", err, "consecutive_failures", state.ConsecutivePollFailures,
			"escalation_threshold", PollFailureEscalationThreshold)
	} else {
		o.logger().Warn("orchestrator: fetch candidates failed",
			"error", err, "consecutive_failures", state.ConsecutivePollFailures,
			"escalation_threshold", PollFailureEscalationThreshold)
	}
	o.publishPollStatus(state, false, false)
	return state
}

// recordPollSuccess resets the outage run and clears a poll LastTrackerError.
// A write failure (op update_state) is left in place — a successful READ says
// nothing about the write path — until it ages past trackerWriteErrorTTL.
func (o *Orchestrator) recordPollSuccess(state State, now time.Time) State {
	if state.ConsecutivePollFailures >= PollFailureEscalationThreshold {
		o.logger().Info("orchestrator: fetch candidates recovered",
			"after_failures", state.ConsecutivePollFailures)
	}
	state.ConsecutivePollFailures = 0
	switch {
	case state.LastTrackerError.At.IsZero():
	case state.LastTrackerError.Op == TrackerErrorOpPoll:
		state.LastTrackerError = TrackerErrorInfo{}
	case now.Sub(state.LastTrackerError.At) > trackerWriteErrorTTL:
		state.LastTrackerError = TrackerErrorInfo{}
	}
	o.publishPollStatus(state, true, false)
	return state
}

// recordTrackerWriteFailure records a failed tracker write that an off-loop
// goroutine reported back on its completion event. Event loop only.
func recordTrackerWriteFailure(state State, op string, err error, now time.Time) State {
	if err == nil {
		return state
	}
	state.LastTrackerError = classifyTrackerError(op, err, now)
	return state
}
