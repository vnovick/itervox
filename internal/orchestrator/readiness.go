package orchestrator

import "time"

// Readiness is the event loop's liveness as published for the /api/v1/ready
// probe (CORE-043). Every field is read from an atomic the event loop alone
// writes, so Readiness() takes neither cfgMu nor snapMu nor touches State —
// a wedged loop (stuck holding nothing) and a busy loop (inside a slow tracker
// call) are both observable from another goroutine.
type Readiness struct {
	// LoopStarted is false until Run's loop is about to wait for the first
	// time.
	LoopStarted bool
	// LastLoopIdle is when the loop last finished an iteration (a tick or an
	// event, including its storeSnap).
	LastLoopIdle time.Time
	// TickStarted is when the iteration now running began, zero while the
	// loop waits in its select. Covers onTick (the synchronous tracker poll)
	// and handleEvent alike, so readiness can tell "busy" from "wedged".
	TickStarted time.Time
	// PollInterval is polling.interval_ms (read-only after startup).
	PollInterval time.Duration
	// LastPollOK is whether the most recent candidate poll succeeded.
	LastPollOK bool
	// PollRateLimited is whether the most recent poll failed with a
	// *tracker.RateLimitedError.
	PollRateLimited bool
	// PollShedding is whether the most recent tick skipped its candidate
	// poll because the tracker budget is in the write reserve (#42-E,
	// BH-M2-6). No poll happened, so LastPollOK is false; it is not a
	// failure either.
	PollShedding bool
	// ConsecutivePollFailures mirrors State.ConsecutivePollFailures.
	ConsecutivePollFailures int
	// PollFailureThreshold is PollFailureEscalationThreshold.
	PollFailureThreshold int
	// Draining is whether the loop is draining for shutdown or reload
	// (CORE-057); /ready then reports not-ready so a load balancer drains too.
	Draining bool
}

// Readiness returns the published readiness signals. Safe from any goroutine.
func (o *Orchestrator) Readiness() Readiness {
	r := Readiness{
		PollInterval:            time.Duration(o.cfg.Polling.IntervalMs) * time.Millisecond,
		LastPollOK:              o.lastPollOK.Load(),
		PollRateLimited:         o.pollRateLimited.Load(),
		PollShedding:            o.pollShedding.Load(),
		ConsecutivePollFailures: int(o.pollFailuresCount.Load()),
		PollFailureThreshold:    PollFailureEscalationThreshold,
		Draining:                o.draining.Load(),
	}
	if n := o.loopIdleNano.Load(); n != 0 {
		r.LoopStarted = true
		r.LastLoopIdle = time.Unix(0, n)
	}
	if n := o.tickStartedNano.Load(); n != 0 {
		r.TickStarted = time.Unix(0, n)
	}
	return r
}

// markLoopBusy stamps the start of an event-loop iteration. Event loop only.
func (o *Orchestrator) markLoopBusy() {
	o.tickStartedNano.Store(time.Now().UnixNano())
}

// markLoopIdle stamps the end of an event-loop iteration (and the loop's
// start, before its first wait). Event loop only.
func (o *Orchestrator) markLoopIdle() {
	o.tickStartedNano.Store(0)
	o.loopIdleNano.Store(time.Now().UnixNano())
}

// publishPollStatus mirrors the latest poll outcome into the readiness
// atomics. Event loop only (called from onTick).
func (o *Orchestrator) publishPollStatus(state State, ok, rateLimited bool) {
	o.pollShedding.Store(false)
	o.lastPollOK.Store(ok)
	o.pollRateLimited.Store(rateLimited)
	o.pollFailuresCount.Store(int32(min(state.ConsecutivePollFailures, 1<<30)))
}

// publishPollShedding records a tick that skipped its candidate poll to keep
// the tracker budget for writes (BH-M2-6). Event loop only.
func (o *Orchestrator) publishPollShedding(state State) {
	o.lastPollOK.Store(false)
	o.pollRateLimited.Store(false)
	o.pollShedding.Store(true)
	o.pollFailuresCount.Store(int32(min(state.ConsecutivePollFailures, 1<<30)))
}
