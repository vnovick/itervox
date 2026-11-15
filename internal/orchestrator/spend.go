package orchestrator

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"sort"
	"time"
)

// Spend is what one issue (or one issue on one day) has used (#136):
// tokens across every run and retry, and the estimated cost of its Claude
// runs (Codex reports none).
type Spend struct {
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
	// CostKnown is set once a run of the issue reported a cost.
	CostKnown bool `json:"cost_known,omitempty"`
}

// Tokens is input plus output tokens.
func (s Spend) Tokens() int { return s.InputTokens + s.OutputTokens }

func (s Spend) zero() bool { return s.Tokens() == 0 && !s.CostKnown }

// Add adds d to s.
func (s *Spend) Add(d Spend) {
	s.InputTokens += d.InputTokens
	s.OutputTokens += d.OutputTokens
	s.CostUSD += d.CostUSD
	s.CostKnown = s.CostKnown || d.CostKnown
}

// SpendLedger is the per-issue spend ledger (#136): totals per issue, which
// agent.budget is checked against, and per day, for the cost report.
// Event-loop state, persisted to issue_spend.json so a restart neither
// resets a budget nor loses the report.
type SpendLedger struct {
	Issues map[string]Spend            `json:"issues"`
	Days   map[string]map[string]Spend `json:"days"` // "2006-01-02" (UTC) → identifier → spend
}

// spendDaysKept bounds the per-day history.
const spendDaysKept = 90

func (l *SpendLedger) record(identifier string, at time.Time, d Spend) {
	if d.zero() || identifier == "" {
		return
	}
	if l.Issues == nil {
		l.Issues = map[string]Spend{}
	}
	if l.Days == nil {
		l.Days = map[string]map[string]Spend{}
	}
	s := l.Issues[identifier]
	s.Add(d)
	l.Issues[identifier] = s
	day := at.UTC().Format(time.DateOnly)
	if l.Days[day] == nil {
		l.Days[day] = map[string]Spend{}
		l.pruneDays()
	}
	ds := l.Days[day][identifier]
	ds.Add(d)
	l.Days[day][identifier] = ds
}

func (l *SpendLedger) pruneDays() {
	if len(l.Days) <= spendDaysKept {
		return
	}
	days := make([]string, 0, len(l.Days))
	for d := range l.Days {
		days = append(days, d)
	}
	sort.Strings(days)
	for _, d := range days[:len(days)-spendDaysKept] {
		delete(l.Days, d)
	}
}

func (l SpendLedger) clone() SpendLedger {
	c := SpendLedger{Issues: maps.Clone(l.Issues)}
	if l.Days != nil {
		c.Days = make(map[string]map[string]Spend, len(l.Days))
		for d, m := range l.Days {
			c.Days[d] = maps.Clone(m)
		}
	}
	return c
}

// overBudget reports whether spend has reached budget.
func overBudget(spend Spend, tokens int, costUSD float64) bool {
	return (tokens > 0 && spend.Tokens() >= tokens) || (costUSD > 0 && spend.CostKnown && spend.CostUSD >= costUSD)
}

// issueOverBudget reports whether identifier has reached agent.budget.
func (o *Orchestrator) issueOverBudget(state *State, identifier string) bool {
	b := o.cfg.Agent.Budget
	return b.Enabled() && overBudget(state.Spend.Issues[identifier], b.Tokens, b.CostUSD)
}

// stopOverBudgetRun stops entry's run once its issue has reached the budget
// (#136): its exit then pauses the issue with budget_exceeded. Event loop.
func (o *Orchestrator) stopOverBudgetRun(state *State, entry *RunEntry) {
	ident := entry.Issue.Identifier
	if state.BudgetStops[ident] == entry.RunID || !o.issueOverBudget(state, ident) {
		return
	}
	if state.BudgetStops == nil {
		state.BudgetStops = map[string]string{}
	}
	state.BudgetStops[ident] = entry.RunID
	spend := state.Spend.Issues[ident]
	o.logger().Warn("orchestrator: issue reached its budget, stopping it",
		"issue_identifier", ident, "tokens", spend.Tokens(), "cost_usd", spend.CostUSD,
		"budget_tokens", o.cfg.Agent.Budget.Tokens, "budget_cost_usd", o.cfg.Agent.Budget.CostUSD)
	if o.logBuf != nil {
		o.logBuf.Add(ident, makeBufLine("WARN", fmt.Sprintf(
			"worker: budget reached (%d tokens, $%.2f) — stopping and pausing the issue", spend.Tokens(), spend.CostUSD)))
	}
	o.userCancelledMu.Lock()
	o.userCancelledIDs[ident] = struct{}{} // the exit pauses the issue
	o.userCancelledMu.Unlock()
	o.saveSpendToDisk(state)
	if entry.WorkerCancel != nil {
		entry.WorkerCancel()
	}
}

// refuseOverBudget pauses issue instead of dispatching it when it has
// reached the budget (#136): a resume without a raised budget pauses it
// again. Reports whether it refused. Event loop.
func (o *Orchestrator) refuseOverBudget(state *State, issueID, identifier string) bool {
	if !o.issueOverBudget(state, identifier) {
		return false
	}
	state.PausedIdentifiers[identifier] = issueID
	setPauseReason(state, identifier, PauseReasonBudgetExceeded)
	delete(state.Claimed, issueID)
	o.savePausedToDisk(maps.Clone(state.PausedIdentifiers))
	o.savePauseReasonsToDisk(maps.Clone(state.PauseReasons))
	o.logger().Warn("orchestrator: issue is over its budget, paused instead of dispatched",
		"issue_identifier", identifier)
	return true
}

// SetSpendFile sets where the spend ledger is persisted (#136). Must be
// called before Run; empty disables persistence.
func (o *Orchestrator) SetSpendFile(path string) {
	o.autoSwitchedMu.Lock()
	o.spendFile = path
	o.autoSwitchedMu.Unlock()
}

// saveSpendToDisk persists the spend ledger. Event loop only.
func (o *Orchestrator) saveSpendToDisk(state *State) {
	o.autoSwitchedMu.RLock()
	path := o.spendFile
	o.autoSwitchedMu.RUnlock()
	if path == "" || state == nil {
		return
	}
	data, err := json.Marshal(spendEnvelope{Version: spendFileVersion, SpendLedger: state.Spend})
	if err != nil {
		slog.Warn("orchestrator: failed to marshal the spend ledger", "error", err)
		return
	}
	o.persistLedger(ledgerSpend, path, data, 0o644)
}

func (o *Orchestrator) loadSpendFromDisk(state State) State {
	o.autoSwitchedMu.RLock()
	path := o.spendFile
	o.autoSwitchedMu.RUnlock()
	if path == "" {
		return state
	}
	l, err := ReadSpendFile(path)
	if err != nil {
		slog.Warn("orchestrator: spend ledger not loaded; budgets start from zero", "path", path, "error", err)
		return state
	}
	state.Spend = l
	return state
}

const spendFileVersion = 1

type spendEnvelope struct {
	Version int `json:"version"`
	SpendLedger
}

// ReadSpendFile reads a persisted spend ledger (#136); a missing file is an
// empty ledger.
func ReadSpendFile(path string) (SpendLedger, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return SpendLedger{}, nil
	}
	if err != nil {
		return SpendLedger{}, err
	}
	var env spendEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return SpendLedger{}, fmt.Errorf("orchestrator: %s: %w", path, err)
	}
	if env.Version != spendFileVersion {
		return SpendLedger{}, fmt.Errorf("orchestrator: %s: unsupported version %d", path, env.Version)
	}
	return env.SpendLedger, nil
}
