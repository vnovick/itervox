package agent

import "sync"

// maxTrackedSessionCosts bounds sessionCostLedger. An evicted session that
// is resumed later reports only its new turns' cost, which the
// orchestrator's accounting reads as a fresh counter and counts in full.
const maxTrackedSessionCosts = 1024

// sessionCostLedger turns per-turn cost into the session-cumulative figure
// TurnResult.CostUSD carries (CORE-091: Claude's total_cost_usd is
// cumulative for the session, and the orchestrator counts its increase).
// OpenCode reports cost per step and the custom protocol per turn, so their
// runners add each turn's cost to the session's running total here. Safe
// for concurrent turns.
type sessionCostLedger struct {
	mu    sync.Mutex
	total map[string]float64
	order []string // insertion order, for eviction
}

// add records turnCost for session and returns the session's cumulative
// cost. With no session id the turn's own cost is returned.
func (l *sessionCostLedger) add(session string, turnCost float64) float64 {
	if session == "" {
		return turnCost
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.total == nil {
		l.total = make(map[string]float64)
	}
	prev, seen := l.total[session]
	if !seen {
		if len(l.order) >= maxTrackedSessionCosts {
			delete(l.total, l.order[0])
			l.order = l.order[1:]
		}
		l.order = append(l.order, session)
	}
	l.total[session] = prev + turnCost
	return prev + turnCost
}
