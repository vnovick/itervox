package orchestrator

import (
	"cmp"
	"slices"
	"time"
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
// selectLeastRecentlyChecked picks up to budget identifiers from entries,
// ordered by the timestamp lastChecked reports, oldest first. It backs both
// per-tick tracker-request budgets so the two cannot drift apart.
//
// Ordering is load-bearing twice over. It makes the budget fair — every entry
// is reached within ceil(N/budget) ticks rather than some starving forever
// behind a permanently-failing neighbour — and it makes the selection
// deterministic, which ranging over the map directly was not: Go randomizes
// map iteration, so which issues got a request varied tick to tick and could
// be neither reasoned about nor tested.
func selectLeastRecentlyChecked[T any](entries map[string]*T, budget int, lastChecked func(*T) time.Time) []string {
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
		candidates = append(candidates, candidate{identifier: identifier, last: lastChecked(entry).UnixNano()})
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

// selectTrackerReplyCheckBatch picks this tick's input-required reply checks.
func selectTrackerReplyCheckBatch(entries map[string]*InputRequiredEntry, budget int) []string {
	return selectLeastRecentlyChecked(entries, budget, func(e *InputRequiredEntry) time.Time {
		return e.LastReplyCheckAt
	})
}

// selectPendingResumeFetchBatch picks which pending resumes may spend a
// tracker request this tick. The caller still walks every entry for the
// cheap bookkeeping (dropping paused/discarding ones) — only the fetch is
// budgeted.
func selectPendingResumeFetchBatch(entries map[string]*PendingInputResumeEntry, budget int) []string {
	return selectLeastRecentlyChecked(entries, budget, func(e *PendingInputResumeEntry) time.Time {
		return e.LastResumeAttemptAt
	})
}
