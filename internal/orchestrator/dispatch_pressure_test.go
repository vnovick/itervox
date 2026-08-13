package orchestrator

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
)

// pressureConfig builds the minimal real config the eligibility ladder needs.
func pressureConfig(maxConcurrent int) *config.Config {
	cfg := &config.Config{}
	cfg.Tracker.ActiveStates = []string{"Todo"}
	cfg.Tracker.TerminalStates = []string{"Done"}
	cfg.Agent.Command = "codex"
	cfg.Agent.MaxConcurrentAgents = maxConcurrent
	return cfg
}

// pressureState builds a real State (never a mock — see CLAUDE.md) with the
// given running issue IDs already occupying slots.
func pressureState(cfg *config.Config, running ...string) State {
	state := NewState(cfg)
	for _, id := range running {
		state.Running[id] = &RunEntry{}
	}
	return state
}

// todoIssue builds a dispatch-eligible issue. Title is required:
// IneligibleReason's missing_fields guard rejects any issue without one
// BEFORE reaching the slot or dependency gates, so omitting it silently
// classifies the issue as neither waiting nor blocked.
func todoIssue(id, identifier string) domain.Issue {
	return domain.Issue{ID: id, Identifier: identifier, Title: identifier, State: "Todo"}
}

func blockedIssue(id, identifier, blocker string) domain.Issue {
	issue := todoIssue(id, identifier)
	issue.BlockedBy = []domain.BlockerRef{{Identifier: strPtr(blocker)}}
	return issue
}

// TestDispatchPressureSlotBound: capacity 1, one issue already dispatched and
// a second eligible issue with nowhere to go. Slots are exhausted and real
// work is waiting, so the tick is slot-bound — raising max_concurrent_agents
// would convert directly into throughput.
func TestDispatchPressureSlotBound(t *testing.T) {
	cfg := pressureConfig(1)
	state := pressureState(cfg, "issue-a")
	candidates := []domain.Issue{todoIssue("issue-a", "ENG-A"), todoIssue("issue-b", "ENG-B")}

	got := observeDispatchPressure(DispatchPressure{}, state, candidates, cfg)

	require.Equal(t, int64(1), got.TicksObserved)
	require.Equal(t, int64(1), got.TicksSlotBound, "a saturated tick with eligible work waiting is slot-bound")
	require.Equal(t, int64(0), got.TicksDependencyBound, "slot-bound and dependency-bound are mutually exclusive")
	require.Equal(t, 1, got.EligibleWaiting)
	require.Equal(t, 0, got.BlockedByDependency)
}

// TestDispatchPressureDependencyBound: capacity 5 with a single running
// issue, so four slots sit idle — and they sit idle because the remaining
// candidates are all blocked. Raising max_concurrent_agents here buys
// nothing, which is precisely what this counter is meant to reveal.
func TestDispatchPressureDependencyBound(t *testing.T) {
	cfg := pressureConfig(5)
	state := pressureState(cfg, "issue-x")
	candidates := []domain.Issue{
		todoIssue("issue-x", "ENG-X"),
		blockedIssue("issue-p", "ENG-P", "ENG-X"),
		blockedIssue("issue-q", "ENG-Q", "ENG-X"),
	}

	got := observeDispatchPressure(DispatchPressure{}, state, candidates, cfg)

	require.Equal(t, int64(1), got.TicksDependencyBound, "free slots plus blocked candidates is dependency-bound")
	require.Equal(t, int64(0), got.TicksSlotBound)
	require.Equal(t, 2, got.BlockedByDependency)
	require.Equal(t, 0, got.EligibleWaiting)
}

// TestDispatchPressureSaturatedTickStillCountsBlockers is the regression test
// for the reason-ordering trap that motivates the probe-state design.
//
// ineligibleReasonShared checks AvailableSlots BEFORE the blocker gates, so
// calling IneligibleReason against the LIVE state on a saturated tick returns
// "no_slots" for every candidate — including ones that are genuinely
// dependency-blocked. A classifier built on the live state would therefore
// report BlockedByDependency == 0 exactly when the fleet is busiest.
//
// The assertions below pin both halves: the live-state reason really is
// masked, and the observation nonetheless counts the blocker.
func TestDispatchPressureSaturatedTickStillCountsBlockers(t *testing.T) {
	cfg := pressureConfig(1)
	state := pressureState(cfg, "issue-x")
	blocked := blockedIssue("issue-p", "ENG-P", "ENG-X")
	candidates := []domain.Issue{todoIssue("issue-x", "ENG-X"), blocked}

	// Establish the masking behavior this design works around.
	require.Equal(t, "no_slots", IneligibleReason(blocked, state, cfg),
		"live-state reason must be slot-masked — if this changes, the probe in observeDispatchPressure may no longer be needed")

	got := observeDispatchPressure(DispatchPressure{}, state, candidates, cfg)

	require.Equal(t, 1, got.BlockedByDependency,
		"a dependency-blocked issue must still be counted as blocked on a saturated tick")
	require.Equal(t, 0, got.EligibleWaiting,
		"a blocked issue must never be counted as merely waiting on capacity")
	require.Equal(t, int64(0), got.TicksSlotBound,
		"nothing was actually dispatchable, so the tick is not slot-bound despite zero free slots")
}

// TestDispatchPressureIdleTickChargedToNeither: slots free and nothing
// blocked (the fleet simply has no work). Charging such a tick to either
// counter would make an idle daemon look constrained.
func TestDispatchPressureIdleTickChargedToNeither(t *testing.T) {
	cfg := pressureConfig(4)
	state := pressureState(cfg)

	got := observeDispatchPressure(DispatchPressure{}, state, nil, cfg)

	require.Equal(t, int64(1), got.TicksObserved)
	require.Equal(t, int64(0), got.TicksSlotBound)
	require.Equal(t, int64(0), got.TicksDependencyBound)
}

// TestDispatchPressureAccumulatesAcrossTicks asserts the counters advance
// from the prior value rather than being recomputed per tick, and that
// utilization is a capacity-weighted mean rather than a mean of per-tick
// percentages.
func TestDispatchPressureAccumulatesAcrossTicks(t *testing.T) {
	cfg := pressureConfig(2)
	candidates := []domain.Issue{todoIssue("issue-a", "ENG-A"), todoIssue("issue-b", "ENG-B")}

	// Tick 1: both slots busy, nothing waiting -> 2/2 occupied.
	p := observeDispatchPressure(DispatchPressure{}, pressureState(cfg, "issue-a", "issue-b"), candidates, cfg)
	// Tick 2: one slot busy, one eligible issue waiting is impossible here
	// (a slot is free, so it would have dispatched) -> 1/2 occupied.
	p = observeDispatchPressure(p, pressureState(cfg, "issue-a"), []domain.Issue{todoIssue("issue-a", "ENG-A")}, cfg)

	require.Equal(t, int64(2), p.TicksObserved, "counters must accumulate, not reset")
	require.Equal(t, int64(3), p.SlotsOccupiedTotal)
	require.Equal(t, int64(4), p.SlotsCapacityTotal)
	require.Equal(t, 75, p.UtilizationPercent())
}

// TestDispatchPressureUtilizationZeroBeforeFirstTick guards the divide-by-
// zero path on a daemon that has not completed a tick yet.
func TestDispatchPressureUtilizationZeroBeforeFirstTick(t *testing.T) {
	require.Equal(t, 0, DispatchPressure{}.UtilizationPercent())
}

// TestDispatchPressureCountsInferredBlockers asserts the soft-gated inferred
// edges count toward the dependency-bound signal too, not just tracker-
// declared blockers — an issue held by a gating inferred edge is just as
// undispatchable as one held by a tracker blocker.
func TestDispatchPressureCountsInferredBlockers(t *testing.T) {
	cfg := pressureConfig(5)
	state := pressureState(cfg)
	state.InferredDeps["ENG-P"] = []InferredDepEntry{{Source: "ENG-X", Gating: true}}
	candidates := []domain.Issue{todoIssue("issue-p", "ENG-P")}

	got := observeDispatchPressure(DispatchPressure{}, state, candidates, cfg)

	require.Equal(t, 1, got.BlockedByDependency, "a gating inferred edge must count as dependency-blocked")
	require.Equal(t, int64(1), got.TicksDependencyBound)
}
