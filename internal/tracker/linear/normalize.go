package linear

import (
	"strings"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

const pageSize = 50

// blockerOriginSubIssue is the domain.BlockerRef.Origin value set on refs
// derived from a parent issue's children (see extractBlockers below). Its
// string value ("sub_issue") intentionally matches
// internal/orchestrator/automation_queue.go::DependencySourceSubIssue so the
// two packages agree on the label without linear importing orchestrator
// (package-boundary rule, see blockerRefKey's doc comment).
const blockerOriginSubIssue = domain.BlockerOriginSubIssue

// normalizeIssue converts a raw Linear API issue map into a domain.Issue.
// Returns nil if the issue map is missing required fields OR if the issue
// is in Linear's trash (codex-B9). Trashed issues poll-side filtering means
// daemon never dispatches against an issue an operator has moved to the
// archive bucket.
func normalizeIssue(raw map[string]any) *domain.Issue {
	id, _ := raw["id"].(string)
	identifier, _ := raw["identifier"].(string)
	title, _ := raw["title"].(string)
	if id == "" || identifier == "" || title == "" {
		return nil
	}
	if trashed, ok := raw["trashed"].(bool); ok && trashed {
		return nil
	}

	issue := &domain.Issue{
		ID:         id,
		Identifier: identifier,
		Title:      title,
		State:      stateName(raw),
		Labels:     extractLabels(raw),
		BlockedBy:  extractBlockers(raw),
		CreatedAt:  tracker.ParseTime(raw["createdAt"]),
		UpdatedAt:  tracker.ParseTime(raw["updatedAt"]),
	}

	if desc, ok := raw["description"].(string); ok && desc != "" {
		issue.Description = &desc
	}
	if prio, ok := tracker.ToIntVal(raw["priority"]); ok {
		issue.Priority = &prio
	}
	if branch, ok := raw["branchName"].(string); ok && branch != "" {
		issue.BranchName = &branch
	}
	if u, ok := raw["url"].(string); ok && u != "" {
		issue.URL = &u
	}

	return issue
}

func stateName(raw map[string]any) string {
	if s, ok := raw["state"].(map[string]any); ok {
		if name, ok := s["name"].(string); ok {
			return name
		}
	}
	return ""
}

func extractLabels(raw map[string]any) []string {
	labels, ok := raw["labels"].(map[string]any)
	if !ok {
		return nil
	}
	nodes, ok := labels["nodes"].([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(nodes))
	for _, n := range nodes {
		node, ok := n.(map[string]any)
		if !ok {
			continue
		}
		name, ok := node["name"].(string)
		if !ok || name == "" {
			continue
		}
		result = append(result, strings.ToLower(name))
	}
	return result
}

func extractBlockers(raw map[string]any) []domain.BlockerRef {
	var result []domain.BlockerRef
	seen := make(map[string]struct{})

	if invRel, ok := raw["inverseRelations"].(map[string]any); ok {
		if nodes, ok := invRel["nodes"].([]any); ok {
			for _, n := range nodes {
				node, ok := n.(map[string]any)
				if !ok {
					continue
				}
				relType, _ := node["type"].(string)
				if !strings.EqualFold(strings.TrimSpace(relType), "blocks") {
					continue
				}
				blockerIssue, ok := node["issue"].(map[string]any)
				if !ok {
					continue
				}
				ref := blockerRefFromNode(blockerIssue)
				key := blockerRefKey(ref)
				if _, dup := seen[key]; dup {
					continue
				}
				seen[key] = struct{}{}
				result = append(result, ref)
			}
		}
	}

	// Linear sub-issues gate their parent: a child issue not yet in a
	// terminal state is a hard blocker on the parent (see
	// docs/superpowers/specs/2026-08-05-tracker-edge-widening-design.md,
	// "Linear sub-issues"). Children are captured uniformly regardless of
	// state — resolution of terminal-state blockers is downstream's job.
	if children, ok := raw["children"].(map[string]any); ok {
		if nodes, ok := children["nodes"].([]any); ok {
			for _, n := range nodes {
				node, ok := n.(map[string]any)
				if !ok {
					continue
				}
				ref := blockerRefFromNode(node)
				key := blockerRefKey(ref)
				if _, dup := seen[key]; dup {
					continue
				}
				seen[key] = struct{}{}
				// Provenance marker: this ref came from raw["children"], not
				// an explicit "blocks" relation. dependencySourceForBlocker
				// (internal/orchestrator/dependency_audit.go) maps
				// Origin == "sub_issue" to DependencySourceSubIssue so
				// operators can distinguish sub-issue gating from a
				// tracker_relation blocker in the dependency audit Sources.
				ref.Origin = blockerOriginSubIssue
				result = append(result, ref)
			}
		}
	}

	return result
}

// blockerRefFromNode builds a domain.BlockerRef from a raw Linear issue node
// shape (`{ id identifier url state { name } }`), shared by both the
// inverseRelations `issue` sub-object and the children node itself.
func blockerRefFromNode(node map[string]any) domain.BlockerRef {
	ref := domain.BlockerRef{}
	if id, ok := node["id"].(string); ok && id != "" {
		ref.ID = &id
	}
	if ident, ok := node["identifier"].(string); ok && ident != "" {
		ref.Identifier = &ident
	}
	if u, ok := node["url"].(string); ok && u != "" {
		ref.URL = &u
	}
	if s, ok := node["state"].(map[string]any); ok {
		if name, ok := s["name"].(string); ok && name != "" {
			ref.State = &name
		}
	}
	return ref
}

// blockerRefKey mirrors the id > identifier > url priority used by
// internal/orchestrator/dependency_audit.go::blockerKey, reimplemented
// locally per package-boundary rules (internal/tracker/linear must not
// import internal/orchestrator).
func blockerRefKey(ref domain.BlockerRef) string {
	if ref.ID != nil && *ref.ID != "" {
		return "id:" + *ref.ID
	}
	if ref.Identifier != nil && *ref.Identifier != "" {
		return "identifier:" + *ref.Identifier
	}
	if ref.URL != nil && *ref.URL != "" {
		return "url:" + *ref.URL
	}
	return "unknown"
}
