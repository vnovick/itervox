package orchestrator

import (
	"strings"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
)

// IneligibleBlockedByPrefix / IneligibleInferredBlockedByPrefix are the
// reason-string prefixes IneligibleReason emits for the two dependency gates.
// They are exported constants rather than inline literals specifically so the
// producer (ineligibleReasonShared) and the consumer
// (observeDispatchPressure's classifier) cannot drift apart: a rename that
// updated only one side would otherwise silently reclassify every
// dependency-blocked issue as "other" and quietly zero out the
// dependency-bound signal.
const (
	IneligibleBlockedByPrefix         = "blocked_by:"
	IneligibleInferredBlockedByPrefix = "inferred_blocked_by:"
)

// DispatchPressure is the session-scoped record of WHY the agent fleet was
// not saturated on each tick. It exists to answer one operator question that
// the instantaneous capacity gauge cannot: "if I raise
// agent.max_concurrent_agents, will anything actually get faster?"
//
// The answer depends on which constraint is binding:
//
//   - Slot-bound ticks: work was eligible and ready, but every slot was
//     occupied. Raising max_concurrent_agents converts directly into
//     throughput.
//   - Dependency-bound ticks: slots were free and went unused because the
//     remaining candidates were held by blockers. Raising
//     max_concurrent_agents buys nothing; the fix is upstream (resolve or
//     decompose the blockers).
//
// The two counters are mutually exclusive by construction — one requires zero
// available slots and the other requires at least one — so a tick is charged
// to at most one of them. Ticks where the fleet simply had nothing to do are
// charged to neither, which is what keeps an idle daemon from inflating the
// dependency-bound signal.
//
// All fields are event-loop-owned State, mutated only by the single
// orchestrator goroutine, exactly like AutomationQueueBackpressure. This is
// bounded runtime-session history: it accumulates from daemon start and is
// deliberately NOT persisted across restarts, since the question it answers
// ("is my current configuration binding?") is about the running fleet.
type DispatchPressure struct {
	// TicksObserved is the total ticks measured, the denominator for the
	// two ratios below.
	TicksObserved int64
	// TicksSlotBound counts ticks with zero available slots AND at least one
	// otherwise-eligible issue waiting.
	TicksSlotBound int64
	// TicksDependencyBound counts ticks with at least one free slot AND at
	// least one candidate held by a dependency gate.
	TicksDependencyBound int64
	// SlotsOccupiedTotal / SlotsCapacityTotal accumulate running-worker count
	// and configured capacity per tick. Their ratio is mean fleet
	// utilization. Kept as a running sum rather than a precomputed average so
	// capacity changes mid-session weight correctly.
	SlotsOccupiedTotal int64
	SlotsCapacityTotal int64
	// BlockedByDependency / EligibleWaiting are the MOST RECENT tick's
	// counts, not cumulative — they describe the fleet right now, whereas the
	// Ticks* counters describe the session.
	BlockedByDependency int
	EligibleWaiting     int
}

// UtilizationPercent returns mean fleet utilization across the session as a
// 0-100 value, or 0 when nothing has been measured yet (rather than dividing
// by zero on a daemon that has not completed a tick).
func (d DispatchPressure) UtilizationPercent() int {
	if d.SlotsCapacityTotal <= 0 {
		return 0
	}
	return int(d.SlotsOccupiedTotal * 100 / d.SlotsCapacityTotal)
}

// observeDispatchPressure returns prev advanced by one tick's observation. It
// is a pure function over already-dispatched state — call it AFTER the
// dispatch loop, so issues that just started are counted as running rather
// than as waiting.
//
// Classification reuses IneligibleReason instead of re-deriving the
// eligibility ladder, because a second hand-written copy of that ladder would
// drift the moment a new gate is added. The one obstacle to that reuse is
// ordering: ineligibleReasonShared checks AvailableSlots BEFORE the blocker
// gates, so on a saturated tick every candidate reports "no_slots" and the
// dependency signal is masked exactly when it matters most.
//
// The fix is to classify against a probe copy of State with the slot gate
// neutralized, which reports each issue's REAL blocking reason independent of
// capacity. State is a value type, so the copy is cheap; its maps are shared
// with the original but this function only reads them, and the sole write is
// to the probe's own int field.
func observeDispatchPressure(prev DispatchPressure, state State, candidates []domain.Issue, cfg *config.Config) DispatchPressure {
	out := prev
	out.TicksObserved++
	out.SlotsOccupiedTotal += int64(len(state.Running))
	out.SlotsCapacityTotal += int64(state.MaxConcurrentAgents)

	// Raise capacity above any reachable running count so AvailableSlots is
	// always positive on the probe and the slot gate cannot short-circuit
	// ahead of the dependency gates.
	probe := state
	probe.MaxConcurrentAgents = len(state.Running) + len(candidates) + 1

	blocked, waiting := 0, 0
	for _, issue := range candidates {
		if _, running := state.Running[issue.ID]; running {
			continue
		}
		reason := IneligibleReason(issue, probe, cfg)
		switch {
		case reason == "":
			// Eligible under the probe but not dispatched: the real tick
			// must have run out of slots before reaching it.
			waiting++
		case strings.HasPrefix(reason, IneligibleBlockedByPrefix),
			strings.HasPrefix(reason, IneligibleInferredBlockedByPrefix):
			blocked++
		}
		// Every other reason (paused, discarding, input_required, terminal,
		// per_state_limit, ...) is deliberately uncounted: those are operator
		// or lifecycle states, not evidence about which resource is binding.
	}
	out.BlockedByDependency = blocked
	out.EligibleWaiting = waiting

	switch {
	case AvailableSlots(state) <= 0 && waiting > 0:
		out.TicksSlotBound++
	case AvailableSlots(state) > 0 && blocked > 0:
		out.TicksDependencyBound++
	}

	return out
}
