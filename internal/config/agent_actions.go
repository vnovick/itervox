package config

import (
	"slices"
	"strings"
)

const (
	AgentActionComment      = "comment"
	AgentActionCommentPR    = "comment_pr"
	AgentActionCreateIssue  = "create_issue"
	AgentActionMergePR      = "merge_pr"
	AgentActionMoveState    = "move_state"
	AgentActionProvideInput = "provide_input"
)

var supportedAgentActions = []string{
	AgentActionComment,
	AgentActionCommentPR,
	AgentActionCreateIssue,
	AgentActionMergePR,
	AgentActionMoveState,
	AgentActionProvideInput,
}

var supportedAgentActionSet = map[string]struct{}{
	AgentActionComment:      {},
	AgentActionCommentPR:    {},
	AgentActionCreateIssue:  {},
	AgentActionMergePR:      {},
	AgentActionMoveState:    {},
	AgentActionProvideInput: {},
}

func SupportedAgentActions() []string {
	return slices.Clone(supportedAgentActions)
}

func InvalidAgentActions(actions []string) []string {
	if len(actions) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(actions))
	invalid := make([]string, 0, len(actions))
	for _, action := range actions {
		normalized := strings.TrimSpace(strings.ToLower(action))
		if normalized == "" {
			continue
		}
		if _, ok := supportedAgentActionSet[normalized]; ok {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		invalid = append(invalid, normalized)
	}
	if len(invalid) == 0 {
		return nil
	}
	slices.Sort(invalid)
	return invalid
}

func NormalizeAllowedActions(actions []string) []string {
	if len(actions) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(actions))
	normalized := make([]string, 0, len(actions))
	for _, action := range actions {
		value := strings.TrimSpace(strings.ToLower(action))
		if value == "" {
			continue
		}
		if _, ok := supportedAgentActionSet[value]; !ok {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	if len(normalized) == 0 {
		return nil
	}
	slices.Sort(normalized)
	return normalized
}
