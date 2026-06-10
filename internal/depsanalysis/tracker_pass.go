package depsanalysis

import (
	"context"
	"fmt"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// AnalyzerIssue is the compact issue shape passed to the analyzer agent. We
// drop the bulkier fields (comments, branches, timestamps) the analyzer does
// not need; smaller prompts cost less and parse faster.
type AnalyzerIssue struct {
	Identifier  string `json:"identifier"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	State       string `json:"state,omitempty"`
}

// FetchIssues retrieves the full issue set across the given state names from
// the tracker, returning a compact issue list and the tracker-declared edges
// derived from each issue's BlockedBy relations.
//
// The plan calls for "all issues" — callers compute stateNames as the union
// of cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates, and
// cfg.Tracker.BacklogStates (deduplicated). Pass that union here.
func FetchIssues(ctx context.Context, t tracker.Tracker, stateNames []string) ([]AnalyzerIssue, []TrackerEdge, error) {
	if t == nil {
		return nil, nil, fmt.Errorf("depsanalysis: tracker is nil")
	}
	if len(stateNames) == 0 {
		return nil, nil, nil
	}
	issues, err := t.FetchIssuesByStates(ctx, stateNames)
	if err != nil {
		return nil, nil, fmt.Errorf("depsanalysis: fetch issues: %w", err)
	}
	analyzer := make([]AnalyzerIssue, 0, len(issues))
	var edges []TrackerEdge
	for _, issue := range issues {
		if issue.Identifier == "" {
			continue
		}
		analyzer = append(analyzer, AnalyzerIssue{
			Identifier:  issue.Identifier,
			Title:       issue.Title,
			Description: dereferenceString(issue.Description),
			State:       issue.State,
		})
		for _, blocker := range issue.BlockedBy {
			src := identifierFromBlocker(blocker)
			if src == "" {
				continue
			}
			edges = append(edges, TrackerEdge{Source: src, Target: issue.Identifier})
		}
	}
	return analyzer, edges, nil
}

// DedupeStateNames returns the input slice with case-insensitive duplicates
// removed (the first occurrence wins). Used to assemble the "all issues"
// state filter without confusing the tracker adapter.
func DedupeStateNames(states ...[]string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, group := range states {
		for _, s := range group {
			if s == "" {
				continue
			}
			key := s
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, s)
		}
	}
	return out
}

func dereferenceString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func identifierFromBlocker(b domain.BlockerRef) string {
	if b.Identifier != nil && *b.Identifier != "" {
		return *b.Identifier
	}
	if b.ID != nil && *b.ID != "" {
		return *b.ID
	}
	return ""
}
