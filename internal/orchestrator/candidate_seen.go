package orchestrator

import (
	"sort"
	"time"

	"github.com/vnovick/itervox/internal/domain"
)

// CandidateSeenRow is a snapshot of "what tracker polling saw this tick" —
// one row per candidate issue identifier plus its tracker-reported UpdatedAt
// (zero when the tracker gave none). It is deliberately minimal: nothing in
// the orchestrator consumes it beyond publishing it on the snapshot for
// cmd/itervox's deps auto-analyze scheduler (analyzer-autonomy Task 4 fix
// round), which needs a real "current backlog" signal — State.InferredDeps /
// State.DependencyAudit are NOT usable as that signal because both are empty
// until at least one dependency relation already exists, which is exactly
// wrong for detecting a fresh backlog with zero relations yet.
type CandidateSeenRow struct {
	Identifier string
	UpdatedAt  time.Time
}

// candidateSeenRows builds this tick's CandidateSeen rows from the freshly
// fetched candidate-issue set, sorted by Identifier for a stable, order-
// independent snapshot. Pure function — no orchestrator state read or
// written — mirrors ReconcileInferredDeps's shape (inferred_deps.go) so the
// event-loop call site is a single assignment.
func candidateSeenRows(issues []domain.Issue) []CandidateSeenRow {
	rows := make([]CandidateSeenRow, 0, len(issues))
	for _, issue := range issues {
		if issue.Identifier == "" {
			continue
		}
		row := CandidateSeenRow{Identifier: issue.Identifier}
		if issue.UpdatedAt != nil {
			row.UpdatedAt = *issue.UpdatedAt
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Identifier < rows[j].Identifier })
	return rows
}
