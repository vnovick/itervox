package orchestrator

import (
	"sort"

	"github.com/vnovick/itervox/internal/domain"
)

// SortForDispatchWithOutdegree wraps SortForDispatch with an optional
// outdegree tiebreaker: when preferHighOutdegree is true, the comparator
// inserts the per-issue "how many candidates does this one block
// transitively" count between the priority and createdAt comparisons. The
// outdegree map is precomputed per-tick (cheap O(N²) for the typical
// backlog of <100 issues) so the comparator stays O(1) per pair. P2.
func SortForDispatchWithOutdegree(issues []domain.Issue, preferHighOutdegree bool) []domain.Issue {
	if !preferHighOutdegree {
		return SortForDispatch(issues)
	}
	outdegree := computeBlockerOutdegree(issues)
	out := make([]domain.Issue, len(issues))
	copy(out, issues)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch {
		case a.Priority == nil && b.Priority == nil:
		case a.Priority == nil:
			return false
		case b.Priority == nil:
			return true
		case *a.Priority != *b.Priority:
			return *a.Priority < *b.Priority
		}
		oa, ob := outdegree[a.Identifier], outdegree[b.Identifier]
		if oa != ob {
			return oa > ob
		}
		switch {
		case a.CreatedAt == nil && b.CreatedAt == nil:
		case a.CreatedAt == nil:
			return false
		case b.CreatedAt == nil:
			return true
		case !a.CreatedAt.Equal(*b.CreatedAt):
			return a.CreatedAt.Before(*b.CreatedAt)
		}
		return a.Identifier < b.Identifier
	})
	return out
}

// computeBlockerOutdegree counts, for each issue in the candidate set, how
// many other issues currently list it as a blocker. This is the direct
// outdegree; transitive closure would be a future refinement.
func computeBlockerOutdegree(issues []domain.Issue) map[string]int {
	out := make(map[string]int, len(issues))
	for _, issue := range issues {
		for _, b := range issue.BlockedBy {
			if b.Identifier == nil {
				continue
			}
			out[*b.Identifier]++
		}
	}
	return out
}
