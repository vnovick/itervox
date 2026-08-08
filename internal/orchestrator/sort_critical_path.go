package orchestrator

import (
	"sort"

	"github.com/vnovick/itervox/internal/domain"
)

// SortForDispatchCriticalPath sorts issues for dispatch using the
// critical-path ordering (config.DependenciesOrderingCriticalPath, the
// default): priority band ASC (nil last, identical comparator to
// SortForDispatch) -> TransitiveDependents DESC (issues that unblock more
// downstream work dispatch first) -> LongestChain DESC (among equal fan-out,
// the issue heading the longer downstream chain dispatches first) ->
// created_at ASC (nil last) -> identifier ASC.
//
// m is looked up per-issue via map access, which defaults to the zero value
// for identifiers absent from m (e.g. an empty GraphMetrics). That makes this
// function a strict superset of SortForDispatch: when every issue has zero
// TransitiveDependents/LongestChain, the two extra tiebreakers never fire and
// the output is byte-identical to SortForDispatch's.
func SortForDispatchCriticalPath(issues []domain.Issue, m GraphMetrics) []domain.Issue {
	out := make([]domain.Issue, len(issues))
	copy(out, issues)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch {
		case a.Priority == nil && b.Priority == nil:
			// fall through
		case a.Priority == nil:
			return false
		case b.Priority == nil:
			return true
		case *a.Priority != *b.Priority:
			return *a.Priority < *b.Priority
		}
		da, db := m.TransitiveDependents[a.Identifier], m.TransitiveDependents[b.Identifier]
		if da != db {
			return da > db
		}
		la, lb := m.LongestChain[a.Identifier], m.LongestChain[b.Identifier]
		if la != lb {
			return la > lb
		}
		switch {
		case a.CreatedAt == nil && b.CreatedAt == nil:
			// fall through
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
