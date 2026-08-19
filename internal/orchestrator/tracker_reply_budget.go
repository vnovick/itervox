package orchestrator

import (
	"cmp"
	"slices"
)

// trackerReplyCheckPerTickBudget caps how many FetchIssueDetail calls
// checkTrackerReplies may spend in a single tick.
//
// Without a cap the loop cost one tracker request per input-required issue
// per tick, so its request rate scaled with the size of the stuck-issue
// backlog — the very thing this loop exists to clear. Issue #42 measured it
// at 15% of the traffic on a deployment that sat at zero remaining Linear
// budget for 35 minutes of every hour; at the default 30s poll (120
// ticks/hour) a 19-entry backlog alone costs ~2,280 requests/hour against a
// 2,500/hour ceiling.
//
// 5 per tick makes the cost constant in the backlog size — ~600 requests/hour
// at the default poll — while still reaching every entry within
// ceil(N/5) ticks, which for that same 19-entry backlog is ~2 minutes. That
// is well inside human reply latency, which is what this loop is watching
// for. The dependency-audit refresh takes the same shape (see
// DefaultDependencyAuditRefreshBatchSize).
const trackerReplyCheckPerTickBudget = 5

// selectTrackerReplyCheckBatch picks up to budget identifiers to spend this
// tick's reply-check requests on, least-recently-checked first.
//
// Ordering is load-bearing twice over. It makes the budget fair — every entry
// is reached in bounded time rather than some starving — and it makes the
// selection deterministic, which ranging over the map directly was not: Go
// randomizes map iteration, so which issues got checked varied tick to tick
// and could not be reasoned about or tested.
func selectTrackerReplyCheckBatch(entries map[string]*InputRequiredEntry, budget int) []string {
	if budget <= 0 || len(entries) == 0 {
		return nil
	}
	type candidate struct {
		identifier string
		last       int64
	}
	candidates := make([]candidate, 0, len(entries))
	for identifier, entry := range entries {
		if entry == nil {
			continue
		}
		candidates = append(candidates, candidate{identifier: identifier, last: entry.LastReplyCheckAt.UnixNano()})
	}
	slices.SortFunc(candidates, func(a, b candidate) int {
		// Identifier breaks ties so equal timestamps — including the zero
		// value shared by every never-checked entry — stay deterministic.
		if c := cmp.Compare(a.last, b.last); c != 0 {
			return c
		}
		return cmp.Compare(a.identifier, b.identifier)
	})
	out := make([]string, 0, min(budget, len(candidates)))
	for _, c := range candidates {
		if len(out) == budget {
			break
		}
		out = append(out, c.identifier)
	}
	return out
}
