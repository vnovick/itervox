package orchestrator

// RunTotals is the daemon-session cumulative token and estimated-cost
// accounting behind the snapshot's `totals` (CORE-091). Event-loop state; it
// is deliberately NOT persisted (like IssueStatusHistory, it describes this
// daemon session) and resets on restart.
//
// Tokens: every RunEntry's token counts are cumulative for its run, so each
// EventWorkerUpdate adds only the increase over what that run had already
// contributed.
//
// Cost: Claude's total_cost_usd is a counter of one CLI PROCESS. Whether a
// `--resume` restores the session's earlier cost into it (the 2.1.283 bundle
// has a cost-state restore path) or starts at zero is not established — no
// model calls were made to observe it — so the accounting is correct under
// both models (M6-close CORE-091 a/b). SessionCost keeps the last value seen
// per agent session id:
//   - a value >= the last one continues the same counter (the same process,
//     or a resume that restored the cost): only the increase is added;
//   - a value < the last one means a new process started a new counter (a
//     resume that did not restore it, or a restarted CLI): that whole value
//     is new spend and is added, and becomes the new baseline.
//
// The one case neither model can tell apart — a new process whose first
// report already exceeds the previous process's total — is counted as the
// increase only (an under-count, never a double count).
//
// Codex reports no cost: its runs count as cost-unknown (CodexRuns) so the
// dashboard can say "Claude runs only". SessionCost entries are pruned once
// nothing can resume the session (pruneSessionCost); totals are kept.
type RunTotals struct {
	InputTokens  int
	OutputTokens int
	// CostUSD is the estimated total; CostKnown is false until a Claude run
	// has reported a cost (the snapshot then shows null, not 0).
	CostUSD    float64
	CostKnown  bool
	ClaudeRuns int
	CodexRuns  int
	// SessionCost is the last total_cost_usd seen per agent session id.
	SessionCost map[string]float64
}

// TotalsSnapshot is RunTotals as the snapshot reports it.
type TotalsSnapshot struct {
	InputTokens      int
	OutputTokens     int
	CostUSDEstimated *float64 // nil until a Claude run reports cost
	ClaudeRuns       int
	CodexRuns        int
}

// Snapshot returns the reportable view of t.
func (t RunTotals) Snapshot() TotalsSnapshot { return t.snapshot() }

func (t RunTotals) snapshot() TotalsSnapshot {
	s := TotalsSnapshot{InputTokens: t.InputTokens, OutputTokens: t.OutputTokens, ClaudeRuns: t.ClaudeRuns, CodexRuns: t.CodexRuns}
	if t.CostKnown {
		c := t.CostUSD
		s.CostUSDEstimated = &c
	}
	return s
}

// accountRunUpdate folds one EventWorkerUpdate into the totals. entry is the
// live RunEntry BEFORE the update's token counts are applied to it; update
// is the event's RunEntry. Event loop only.
func (t *RunTotals) accountRunUpdate(entry, update *RunEntry) {
	if update.TotalTokens > 0 {
		t.InputTokens += max(0, update.InputTokens-entry.totalsInput)
		t.OutputTokens += max(0, update.OutputTokens-entry.totalsOutput)
		entry.totalsInput = max(entry.totalsInput, update.InputTokens)
		entry.totalsOutput = max(entry.totalsOutput, update.OutputTokens)
		if !entry.totalsCounted {
			entry.totalsCounted = true
			switch entry.Backend {
			case "codex":
				t.CodexRuns++
			default:
				t.ClaudeRuns++
			}
		}
	}
	if update.CostUSD == nil {
		return
	}
	session := update.AgentSessionID
	if session == "" {
		session = entry.AgentSessionID
	}
	if session == "" {
		session = "run:" + entry.Issue.ID + ":" + entry.SessionID
	}
	cost := *update.CostUSD
	if t.SessionCost == nil {
		t.SessionCost = make(map[string]float64)
	}
	last := t.SessionCost[session] // 0 for an unseen session
	if cost >= last {
		t.CostUSD += cost - last // the same counter went up
	} else {
		t.CostUSD += cost // a new process's counter: all of it is new spend
	}
	t.SessionCost[session] = cost
	t.CostKnown = true
}

// pruneSessionCost drops the cost baseline of every agent session that
// nothing can resume any more — not a running run, a paused session or an
// input-required / pending-resume entry (M6-close CORE-091 c). The fleet
// totals are unaffected. Event loop only (janitor pass).
func pruneSessionCost(state *State) {
	if len(state.Totals.SessionCost) == 0 {
		return
	}
	live := make(map[string]struct{})
	for _, r := range state.Running {
		if r.AgentSessionID != "" {
			live[r.AgentSessionID] = struct{}{}
		}
	}
	for _, p := range state.PausedSessions {
		if p != nil && p.SessionID != "" {
			live[p.SessionID] = struct{}{}
		}
	}
	for _, e := range state.InputRequiredIssues {
		if e != nil && e.SessionID != "" {
			live[e.SessionID] = struct{}{}
		}
	}
	for _, e := range state.PendingInputResumes {
		if e != nil && e.SessionID != "" {
			live[e.SessionID] = struct{}{}
		}
	}
	for session := range state.Totals.SessionCost {
		if _, ok := live[session]; !ok {
			delete(state.Totals.SessionCost, session)
		}
	}
}
