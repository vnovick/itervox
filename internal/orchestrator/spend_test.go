package orchestrator

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
)

// #136 — per-issue budgets and the spend ledger.

func budgetOrchestrator(t *testing.T, tokens int) (*Orchestrator, domain.Issue) {
	t.Helper()
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "T", State: "In Progress"}
	o, _ := breakerOrchestrator(t, config.BackendFallbackConfig{}, issue)
	o.cfg.Agent.Budget = config.BudgetConfig{Tokens: tokens}
	o.SetSpendFile(filepath.Join(t.TempDir(), "issue_spend.json"))
	return o, issue
}

func reportTokens(state State, o *Orchestrator, issue domain.Issue, in, out int) State {
	run := state.Running[issue.ID]
	return o.handleEvent(context.Background(), state, OrchestratorEvent{
		Type: EventWorkerUpdate, IssueID: issue.ID, RunID: run.RunID,
		RunEntry: &RunEntry{Issue: issue, TotalTokens: in + out, InputTokens: in, OutputTokens: out},
	})
}

// TestIssueOverBudgetIsPausedWithBudgetExceeded: once an issue's runs have
// used its budget, the running run is stopped and the issue is paused with
// budget_exceeded.
func TestIssueOverBudgetIsPausedWithBudgetExceeded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	o, issue := budgetOrchestrator(t, 1000)
	state := NewState(o.cfg)
	state = o.dispatch(ctx, state, issue, 0)
	require.Contains(t, state.Running, issue.ID)

	state = reportTokens(state, o, issue, 400, 100)
	assert.NotContains(t, state.BudgetStops, "ENG-1", "under budget: still running")
	assert.Equal(t, 500, state.Spend.Issues["ENG-1"].Tokens())

	state = reportTokens(state, o, issue, 900, 200) // cumulative for the run
	assert.Equal(t, 1100, state.Spend.Issues["ENG-1"].Tokens())
	require.Contains(t, state.BudgetStops, "ENG-1", "over budget: the run is being stopped")

	state, ev := nextWorkerExit(t, ctx, o, state)
	state = o.handleEvent(ctx, state, ev)
	assert.NotContains(t, state.Running, issue.ID)
	assert.Contains(t, state.PausedIdentifiers, "ENG-1")
	assert.Equal(t, PauseReasonBudgetExceeded, state.PauseReasons["ENG-1"])
	assert.NotContains(t, state.BudgetStops, "ENG-1")
	assert.Empty(t, state.RetryAttempts, "no retry for a run stopped by its budget")

	day := time.Now().UTC().Format(time.DateOnly)
	assert.Equal(t, 1100, state.Spend.Days[day]["ENG-1"].Tokens(), "the day's report has it too")
}

// TestRaisingTheBudgetResumesTheIssue: a resume without more budget pauses
// the issue again at once; with a raised budget it runs.
func TestRaisingTheBudgetResumesTheIssue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	o, issue := budgetOrchestrator(t, 1000)
	state := NewState(o.cfg)
	state.Spend.record("ENG-1", time.Now(), Spend{InputTokens: 900, OutputTokens: 200})
	state.PausedIdentifiers["ENG-1"] = issue.ID
	setPauseReason(&state, "ENG-1", PauseReasonBudgetExceeded)

	resume := func() {
		state = o.handleEvent(ctx, state, OrchestratorEvent{Type: EventResumeIssue, Identifier: "ENG-1"})
		require.NotContains(t, state.PausedIdentifiers, "ENG-1")
		state = o.dispatch(ctx, state, issue, 0)
	}
	resume()
	assert.NotContains(t, state.Running, issue.ID, "still over budget")
	assert.Equal(t, PauseReasonBudgetExceeded, state.PauseReasons["ENG-1"], "paused again")
	assert.NotContains(t, state.Claimed, issue.ID)

	o.cfg.Agent.Budget.Tokens = 5000 // the operator raises the budget
	resume()
	assert.Contains(t, state.Running, issue.ID, "the issue continues")
	assert.NotContains(t, state.PauseReasons, "ENG-1")
}

// The ledger survives a restart, so a restart neither resets a budget nor
// loses the cost report.
func TestSpendLedgerSurvivesARestart(t *testing.T) {
	o, _ := budgetOrchestrator(t, 0)
	state := NewState(o.cfg)
	at := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	cost := Spend{InputTokens: 10, OutputTokens: 5, CostUSD: 0.25, CostKnown: true}
	state.Spend.record("ENG-1", at, cost)
	state.Spend.record("ENG-1", at.Add(24*time.Hour), Spend{InputTokens: 1})
	o.saveSpendToDisk(&state)

	fresh := o.loadSpendFromDisk(NewState(o.cfg))
	assert.Equal(t, Spend{InputTokens: 11, OutputTokens: 5, CostUSD: 0.25, CostKnown: true}, fresh.Spend.Issues["ENG-1"])
	assert.Equal(t, cost, fresh.Spend.Days["2026-10-11"]["ENG-1"])
	assert.Equal(t, 1, fresh.Spend.Days["2026-10-12"]["ENG-1"].InputTokens)
}

func TestSpendBudgetRules(t *testing.T) {
	assert.False(t, overBudget(Spend{InputTokens: 999}, 1000, 0))
	assert.True(t, overBudget(Spend{InputTokens: 600, OutputTokens: 400}, 1000, 0))
	assert.False(t, overBudget(Spend{CostUSD: 5}, 0, 1), "a cost no run reported is not counted")
	assert.True(t, overBudget(Spend{CostUSD: 1, CostKnown: true}, 0, 1))
	assert.False(t, overBudget(Spend{InputTokens: 1e9, CostUSD: 1e9, CostKnown: true}, 0, 0), "no budget, no limit")

	var l SpendLedger
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for d := range spendDaysKept + 5 {
		l.record("ENG-1", start.AddDate(0, 0, d), Spend{InputTokens: 1})
	}
	assert.Len(t, l.Days, spendDaysKept, "the per-day history is bounded")
	assert.NotContains(t, l.Days, "2026-01-01")
	assert.Equal(t, spendDaysKept+5, l.Issues["ENG-1"].InputTokens, "issue totals keep everything")
}

// TestOverBudgetIssueIsNotStartedByAnAutomationOrAResume: the automation
// and answered-question paths start workers without dispatch(); each pauses
// an over-budget issue instead.
func TestOverBudgetIssueIsNotStartedByAnAutomationOrAResume(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	o, issue := budgetOrchestrator(t, 1000)
	over := func() State {
		state := NewState(o.cfg)
		state.Spend.record("ENG-1", time.Now(), Spend{InputTokens: 1000})
		return state
	}

	state := over()
	started, held := o.startAutomationRunOrHold(ctx, &state, issue, time.Now(), AutomationDispatch{ProfileName: "any"})
	assert.False(t, started || held, "the automation run is dropped")
	assert.NotContains(t, state.Running, issue.ID)
	assert.Equal(t, PauseReasonBudgetExceeded, state.PauseReasons["ENG-1"])

	state = over()
	state.PendingInputResumes["ENG-1"] = &PendingInputResumeEntry{
		IssueID: issue.ID, Identifier: "ENG-1", SessionID: "s1", UserMessage: "go ahead",
	}
	state = o.processPendingInputResumes(ctx, state, time.Now())
	assert.NotContains(t, state.Running, issue.ID)
	assert.NotContains(t, state.PendingInputResumes, "ENG-1")
	assert.Equal(t, PauseReasonBudgetExceeded, state.PauseReasons["ENG-1"])
}

// TestOverBudgetIssueGetsNoReviewer: a reviewer dispatch pauses an
// over-budget issue instead of starting the reviewer.
func TestOverBudgetIssueGetsNoReviewer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	o, issue := budgetOrchestrator(t, 1000)
	state := NewState(o.cfg)
	state.Spend.record("ENG-1", time.Now(), Spend{InputTokens: 1000})
	o.dispatchReviewerForIssue(ctx, &state, issue, "reviewer", time.Now())
	assert.NotContains(t, state.Running, issue.ID)
	assert.Equal(t, PauseReasonBudgetExceeded, state.PauseReasons["ENG-1"])
}

// TestStaleBudgetStopDoesNotShieldALaterRun: a stop mark left by a run whose
// exit never closed it does not keep a later run of the issue from being
// stopped at the budget.
func TestStaleBudgetStopDoesNotShieldALaterRun(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	o, issue := budgetOrchestrator(t, 1000)
	state := NewState(o.cfg)
	state.BudgetStops = map[string]string{"ENG-1": "an-earlier-run"}
	state = o.dispatch(ctx, state, issue, 0)
	require.Contains(t, state.Running, issue.ID)

	state = reportTokens(state, o, issue, 1000, 100)
	assert.Equal(t, state.Running[issue.ID].RunID, state.BudgetStops["ENG-1"], "this run is being stopped")
	state, ev := nextWorkerExit(t, ctx, o, state)
	state = o.handleEvent(ctx, state, ev)
	assert.Equal(t, PauseReasonBudgetExceeded, state.PauseReasons["ENG-1"])
}

// scriptedTurnRunner returns one fixed turn result, after reporting a
// partial through onProgress (#136 settlement tests).
type scriptedTurnRunner struct {
	partial *agent.TurnResult
	result  agent.TurnResult
}

func (r *scriptedTurnRunner) RunTurn(_ context.Context, _ agent.Logger, onProgress func(agent.TurnResult), _ *string, _, _, _, _, _ string, _, _ int, _ agent.PermissionMode) (agent.TurnResult, error) {
	if r.partial != nil && onProgress != nil {
		onProgress(*r.partial)
	}
	return r.result, nil
}

// runScriptedToExit dispatches issue through the real dispatch and worker
// path and handles its events up to and including its exit. Updates for
// which drop returns true are discarded, as a full event channel would.
func runScriptedToExit(t *testing.T, o *Orchestrator, issue domain.Issue, state State, drop func(OrchestratorEvent) bool) State {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	state = o.dispatch(ctx, state, issue, 0)
	require.Contains(t, state.Running, issue.ID)
	for {
		select {
		case ev := <-o.events:
			if ev.Type == EventWorkerUpdate && drop != nil && drop(ev) {
				continue
			}
			state = o.handleEvent(ctx, state, ev)
			if ev.Type == EventWorkerExited {
				return state
			}
		case <-ctx.Done():
			t.Fatal("timed out waiting for the worker's exit")
		}
	}
}

// TestFailedTurnOverCostBudgetIsSettledAndPaused: a turn that fails after
// spending $2 under a $1 cost budget is recorded at $2 (and its tokens),
// and the issue is paused with budget_exceeded instead of being retried.
func TestFailedTurnOverCostBudgetIsSettledAndPaused(t *testing.T) {
	o, issue := budgetOrchestrator(t, 0)
	o.cfg.Agent.Budget = config.BudgetConfig{CostUSD: 1}
	o.runner = &scriptedTurnRunner{result: agent.TurnResult{
		Failed: true, FailureText: "boom", SessionID: "s1",
		InputTokens: 100, OutputTokens: 20, TotalTokens: 120, CostUSD: usd(2),
	}}
	state := runScriptedToExit(t, o, issue, NewState(o.cfg), nil)

	assert.Equal(t, Spend{InputTokens: 100, OutputTokens: 20, CostUSD: 2, CostKnown: true}, state.Spend.Issues["ENG-1"],
		"the failed turn's usage is settled at its exit")
	if cost := state.Totals.snapshot().CostUSDEstimated; assert.NotNil(t, cost) {
		assert.InDelta(t, 2.0, *cost, 1e-9)
	}
	assert.Equal(t, 100, state.Totals.InputTokens)
	assert.Contains(t, state.PausedIdentifiers, "ENG-1")
	assert.Equal(t, PauseReasonBudgetExceeded, state.PauseReasons["ENG-1"])
	assert.Empty(t, state.RetryAttempts, "no retry beyond the budget")
	assert.NotContains(t, state.Claimed, issue.ID)
	history := o.RunHistory()
	require.NotEmpty(t, history)
	assert.Equal(t, 120, history[len(history)-1].TotalTokens, "the run's record has the settled tokens")
}

// TestSettledUsageIsNotDoubleCounted: a successful run whose progress
// updates already reported its usage adds nothing more at its exit; when
// the end-of-turn update was dropped, the exit settles the rest — once.
func TestSettledUsageIsNotDoubleCounted(t *testing.T) {
	for _, tc := range []struct {
		name string
		drop bool
	}{
		{"updates delivered", false},
		{"end-of-turn update dropped", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			endOfTurn := 0
			drop := func(ev OrchestratorEvent) bool {
				if ev.RunEntry != nil && ev.RunEntry.CostUSD != nil {
					endOfTurn++
					return tc.drop
				}
				return false
			}
			o, issue := budgetOrchestrator(t, 0)
			o.cfg.Agent.Budget = config.BudgetConfig{CostUSD: 10}
			o.runner = &scriptedTurnRunner{
				partial: &agent.TurnResult{InputTokens: 60, OutputTokens: 5, TotalTokens: 65},
				result: agent.TurnResult{
					SessionID: "s1", ResultText: "done",
					InputTokens: 100, OutputTokens: 20, TotalTokens: 120, CostUSD: usd(0.5),
				},
			}
			state := runScriptedToExit(t, o, issue, NewState(o.cfg), drop)
			require.Equal(t, 1, endOfTurn, "the worker sent its end-of-turn update")
			history := o.RunHistory()
			require.NotEmpty(t, history)
			require.Equal(t, "succeeded", history[len(history)-1].Status)

			assert.Equal(t, Spend{InputTokens: 100, OutputTokens: 20, CostUSD: 0.5, CostKnown: true}, state.Spend.Issues["ENG-1"])
			got := state.Totals.snapshot()
			require.NotNil(t, got.CostUSDEstimated)
			assert.InDelta(t, 0.5, *got.CostUSDEstimated, 1e-9)
			assert.Equal(t, 100, got.InputTokens)
			assert.Equal(t, 20, got.OutputTokens)
			assert.Equal(t, 1, got.ClaudeRuns)
			assert.NotContains(t, state.PauseReasons, "ENG-1")
		})
	}
}
