package linear

import (
	"strings"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

const pageSize = 50

// normalizeIssue converts a raw Linear API issue map into a domain.Issue.
// Returns nil if the issue map is missing required fields.
func normalizeIssue(raw map[string]any) *domain.Issue {
	id, _ := raw["id"].(string)
	identifier, _ := raw["identifier"].(string)
	title, _ := raw["title"].(string)
	if id == "" || identifier == "" || title == "" {
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
	invRel, ok := raw["inverseRelations"].(map[string]any)
	if !ok {
		return nil
	}
	nodes, ok := invRel["nodes"].([]any)
	if !ok {
		return nil
	}
	result := make([]domain.BlockerRef, 0, len(nodes))
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
		ref := domain.BlockerRef{}
		if id, ok := blockerIssue["id"].(string); ok && id != "" {
			ref.ID = &id
		}
		if ident, ok := blockerIssue["identifier"].(string); ok && ident != "" {
			ref.Identifier = &ident
		}
		if u, ok := blockerIssue["url"].(string); ok && u != "" {
			ref.URL = &u
		}
		if s, ok := blockerIssue["state"].(map[string]any); ok {
			if name, ok := s["name"].(string); ok && name != "" {
				ref.State = &name
			}
		}
		result = append(result, ref)
	}
	return result
}
