package orchestrator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
)

func costUpdate(issueID, session string, in, out int, cost *float64) OrchestratorEvent {
	return OrchestratorEvent{Type: EventWorkerUpdate, IssueID: issueID, RunEntry: &RunEntry{
		AgentSessionID: session, InputTokens: in, OutputTokens: out, TotalTokens: in + out, CostUSD: cost, TurnCount: 1,
	}}
}

func usd(v float64) *float64 { return &v }

// TestCostTotalsDoNotDoubleCountResumedSession (CORE-091): Claude's
// total_cost_usd is cumulative for the SESSION, so a run that resumes a
// session reports the prior runs' cost again. Totals add only the delta
// over the last value seen for that session; a lower value is a new
// baseline (never a negative delta); Codex runs contribute tokens and are
// counted as cost-unknown; tokens are per-run cumulative and add deltas.
func TestCostTotalsDoNotDoubleCountResumedSession(t *testing.T) {
	o := New(testConfig(), nil, nil, nil)
	ctx := context.Background()
	state := NewState(o.cfg)
	assert.Nil(t, state.Totals.snapshot().CostUSDEstimated, "no Claude cost reported yet: unknown, not 0")

	// Run 1 (claude, session s1): two end-of-turn updates, cumulative.
	state.Running["id1"] = &RunEntry{Issue: domain.Issue{ID: "id1", Identifier: "ENG-1"}, Backend: "claude"}
	state = o.handleEvent(ctx, state, costUpdate("id1", "s1", 100, 10, usd(0.10)))
	state = o.handleEvent(ctx, state, costUpdate("id1", "s1", 300, 30, usd(0.25)))
	delete(state.Running, "id1") // the run ended

	// Run 2 resumes session s1: its first report includes run 1's 0.25.
	state.Running["id1"] = &RunEntry{Issue: domain.Issue{ID: "id1", Identifier: "ENG-1"}, Backend: "claude"}
	state = o.handleEvent(ctx, state, costUpdate("id1", "s1", 50, 5, usd(0.40)))
	// A lower value is a new process's counter (M6-close): +0.10, never -0.30.
	state = o.handleEvent(ctx, state, costUpdate("id1", "s1", 60, 6, usd(0.10)))
	state = o.handleEvent(ctx, state, costUpdate("id1", "s1", 70, 7, usd(0.15)))

	// Run 3 (codex): tokens only.
	state.Running["id2"] = &RunEntry{Issue: domain.Issue{ID: "id2", Identifier: "ENG-2"}, Backend: "codex"}
	state = o.handleEvent(ctx, state, costUpdate("id2", "c1", 1000, 100, nil))

	got := state.Totals.snapshot()
	require.NotNil(t, got.CostUSDEstimated)
	// 0.25 (run 1) + 0.15 (run 2's delta over 0.25) + 0.10 (new counter) + 0.05 = 0.55
	assert.InDelta(t, 0.55, *got.CostUSDEstimated, 1e-9, "a resumed session's cost must not be counted twice")
	assert.Equal(t, 300+70+1000, got.InputTokens)
	assert.Equal(t, 30+7+100, got.OutputTokens)
	assert.Equal(t, 2, got.ClaudeRuns)
	assert.Equal(t, 1, got.CodexRuns)

	// Repeating the same update adds nothing (a re-delivered event).
	again := o.handleEvent(ctx, state, costUpdate("id2", "c1", 1000, 100, nil)).Totals.snapshot()
	assert.Equal(t, got, again)

	// Clone does not share the per-session map.
	c := state.Clone()
	c.Totals.SessionCost["s1"] = 99
	assert.NotEqual(t, 99.0, state.Totals.SessionCost["s1"])
}

// TestCostTotalsCountSpendAfterADrop (M6-close CORE-091 a/b): Claude's
// total_cost_usd is cumulative per PROCESS. When a report drops below the
// session's last value, a new process started a new counter (a --resume that
// does not restore the cost, or a restarted CLI), so that value IS new spend
// and is counted — it used to be swallowed as a mere baseline. Under the
// cumulative-across-resume model (no drop) only the increase is counted.
// Both models therefore add up correctly.
func TestCostTotalsCountSpendAfterADrop(t *testing.T) {
	o := New(testConfig(), nil, nil, nil)
	ctx := context.Background()
	state := NewState(o.cfg)
	state.Running["id1"] = &RunEntry{Issue: domain.Issue{ID: "id1", Identifier: "ENG-1"}, Backend: "claude"}
	state = o.handleEvent(ctx, state, costUpdate("id1", "s1", 10, 1, usd(0.30)))
	delete(state.Running, "id1")

	// Per-process model: the resumed run's counter restarts at its own spend.
	state.Running["id1"] = &RunEntry{Issue: domain.Issue{ID: "id1", Identifier: "ENG-1"}, Backend: "claude"}
	state = o.handleEvent(ctx, state, costUpdate("id1", "s1", 20, 2, usd(0.05)))
	state = o.handleEvent(ctx, state, costUpdate("id1", "s1", 30, 3, usd(0.12)))
	got := state.Totals.snapshot()
	require.NotNil(t, got.CostUSDEstimated)
	assert.InDelta(t, 0.30+0.12, *got.CostUSDEstimated, 1e-9, "spend after a drop is counted as a new process's spend")
}

// TestCostTotalsSessionCostPruned (M6-close CORE-091 c): the per-session
// baseline is dropped once no running, paused or input-required entry can
// resume that session; the fleet totals are kept.
func TestCostTotalsSessionCostPruned(t *testing.T) {
	o := New(testConfig(), nil, nil, nil)
	ctx := context.Background()
	state := NewState(o.cfg)
	for _, id := range []string{"a", "b", "c", "d"} {
		state.Running[id] = &RunEntry{Issue: domain.Issue{ID: id, Identifier: "ENG-" + id}, Backend: "claude"}
		state = o.handleEvent(ctx, state, costUpdate(id, "s-"+id, 1, 1, usd(0.10)))
	}
	delete(state.Running, "b") // finished: nothing can resume s-b
	delete(state.Running, "c") // paused with its session
	state.PausedSessions["ENG-c"] = &PausedSessionInfo{IssueID: "c", SessionID: "s-c"}
	delete(state.Running, "d") // waiting on input
	state.InputRequiredIssues["ENG-d"] = &InputRequiredEntry{IssueID: "d", Identifier: "ENG-d", SessionID: "s-d"}

	pruneSessionCost(&state)
	assert.Contains(t, state.Totals.SessionCost, "s-a", "running")
	assert.NotContains(t, state.Totals.SessionCost, "s-b", "no longer resumable")
	assert.Contains(t, state.Totals.SessionCost, "s-c", "paused session can resume")
	assert.Contains(t, state.Totals.SessionCost, "s-d", "input-required session can resume")
	got := state.Totals.snapshot()
	require.NotNil(t, got.CostUSDEstimated)
	assert.InDelta(t, 0.40, *got.CostUSDEstimated, 1e-9, "fleet totals are kept")
}
