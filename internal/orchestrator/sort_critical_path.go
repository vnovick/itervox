package orchestrator

import (
	"cmp"
	"sort"

	"github.com/vnovick/itervox/internal/domain"
)

// The dispatch-ordering comparators below all return the standard
// three-way result (-1 = a sorts first, +1 = b sorts first, 0 = tie) so the
// ordering modes can be expressed as a chain of shared stages rather than
// three near-identical copies of the same tiebreak ladder. The ONLY
// difference between critical_path and critical_path_strict is the position
// of cmpPriority in that chain — keeping the stages shared is what makes
// that claim checkable by reading twelve lines instead of diffing two
// hand-written comparators.

// cmpPriority orders by priority band ascending with nil last.
func cmpPriority(a, b domain.Issue) int {
	switch {
	case a.Priority == nil && b.Priority == nil:
		return 0
	case a.Priority == nil:
		return 1
	case b.Priority == nil:
		return -1
	}
	return cmp.Compare(*a.Priority, *b.Priority)
}

// cmpGraph orders by graph leverage: TransitiveDependents descending (an
// issue that unblocks more downstream work sorts first), then LongestChain
// descending (among equal fan-out, the issue heading the longer downstream
// chain sorts first).
//
// m is looked up via map access, so identifiers absent from m default to the
// zero value and this comparator reports a tie. That is what makes an empty
// GraphMetrics collapse every graph-aware mode back onto the non-graph
// tiebreakers.
func cmpGraph(a, b domain.Issue, m GraphMetrics) int {
	if da, db := m.TransitiveDependents[a.Identifier], m.TransitiveDependents[b.Identifier]; da != db {
		return cmp.Compare(db, da)
	}
	return cmp.Compare(m.LongestChain[b.Identifier], m.LongestChain[a.Identifier])
}

// cmpCreatedAt orders oldest-first with nil last.
func cmpCreatedAt(a, b domain.Issue) int {
	switch {
	case a.CreatedAt == nil && b.CreatedAt == nil:
		return 0
	case a.CreatedAt == nil:
		return 1
	case b.CreatedAt == nil:
		return -1
	case a.CreatedAt.Equal(*b.CreatedAt):
		return 0
	case a.CreatedAt.Before(*b.CreatedAt):
		return -1
	}
	return 1
}

// sortForDispatchBy returns a sorted copy of issues under cmpFn. The input
// slice is never mutated.
func sortForDispatchBy(issues []domain.Issue, cmpFn func(a, b domain.Issue) int) []domain.Issue {
	out := make([]domain.Issue, len(issues))
	copy(out, issues)
	sort.SliceStable(out, func(i, j int) bool {
		return cmpFn(out[i], out[j]) < 0
	})
	return out
}

// SortForDispatchCriticalPath sorts issues for dispatch using the
// critical-path ordering (config.DependenciesOrderingCriticalPath, the
// default): priority band ASC (nil last, identical comparator to
// SortForDispatch) -> TransitiveDependents DESC (issues that unblock more
// downstream work dispatch first) -> LongestChain DESC (among equal fan-out,
// the issue heading the longer downstream chain dispatches first) ->
// created_at ASC (nil last) -> identifier ASC.
//
// Because priority compares FIRST, the graph metrics only break ties WITHIN a
// single priority band: a P1 leaf that unblocks nothing still dispatches
// ahead of a P2 root that unblocks a dozen issues. That is deliberate — it
// keeps operator-set priority authoritative — but it also means this mode's
// graph awareness is inert on a tracker where priorities are consistently
// distinct. Use DependenciesOrderingCriticalPathStrict when structural
// leverage should outrank the priority field instead.
//
// m is looked up per-issue via map access, which defaults to the zero value
// for identifiers absent from m (e.g. an empty GraphMetrics). That makes this
// function a strict superset of SortForDispatch: when every issue has zero
// TransitiveDependents/LongestChain, the two extra tiebreakers never fire and
// the output is byte-identical to SortForDispatch's.
func SortForDispatchCriticalPath(issues []domain.Issue, m GraphMetrics) []domain.Issue {
	return sortForDispatchBy(issues, func(a, b domain.Issue) int {
		if c := cmpPriority(a, b); c != 0 {
			return c
		}
		if c := cmpGraph(a, b, m); c != 0 {
			return c
		}
		if c := cmpCreatedAt(a, b); c != 0 {
			return c
		}
		return cmp.Compare(a.Identifier, b.Identifier)
	})
}

// SortForDispatchCriticalPathStrict sorts issues for dispatch using the
// strict critical-path ordering (config.DependenciesOrderingCriticalPathStrict):
// TransitiveDependents DESC -> LongestChain DESC -> priority band ASC (nil
// last) -> created_at ASC (nil last) -> identifier ASC.
//
// This is SortForDispatchCriticalPath with priority demoted from the first
// comparator to the third, so structural leverage decides the order and
// priority only breaks ties between issues of EQUAL graph leverage. The
// practical effect: a blocker that gates a dozen downstream issues dispatches
// before an unrelated urgent leaf, on the reasoning that finishing the
// blocker is what makes the rest of the fleet dispatchable at all.
//
// This is the mode to choose when throughput across the dependency graph
// matters more than honoring the tracker's priority field on any individual
// issue. It is NOT the default, because it deliberately overrides an explicit
// operator signal — a P1 marked urgent for a reason external to the graph
// (a customer escalation, a deadline) will wait behind a high-fan-out P3.
//
// Like SortForDispatchCriticalPath, an empty GraphMetrics makes cmpGraph tie
// on every pair, at which point this degrades to exactly SortForDispatch's
// priority -> created_at -> identifier order.
func SortForDispatchCriticalPathStrict(issues []domain.Issue, m GraphMetrics) []domain.Issue {
	return sortForDispatchBy(issues, func(a, b domain.Issue) int {
		if c := cmpGraph(a, b, m); c != 0 {
			return c
		}
		if c := cmpPriority(a, b); c != 0 {
			return c
		}
		if c := cmpCreatedAt(a, b); c != 0 {
			return c
		}
		return cmp.Compare(a.Identifier, b.Identifier)
	})
}
