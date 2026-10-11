package orchestrator

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
