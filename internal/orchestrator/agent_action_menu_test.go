package orchestrator

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/vnovick/itervox/internal/agentactions"
	"github.com/vnovick/itervox/internal/config"
)

// TestBuildAgentActionContext_EnumeratesEverySupportedAction guards against
// menu/constant drift (P0-E acceptance). If a new constant is added to
// supportedAgentActions but never gets a menu line here, the agent runs
// without being taught the corresponding CLI subcommand.
func TestBuildAgentActionContext_EnumeratesEverySupportedAction(t *testing.T) {
	allActions := config.SupportedAgentActions()
	got := buildAgentActionContext(allActions, agentactions.Scope{CreateIssueState: "Backlog", MoveIssueState: "Done"}, false)

	mustMention := map[string]string{
		config.AgentActionComment:      "itervox action comment ",
		config.AgentActionCommentPR:    "itervox action comment-pr ",
		config.AgentActionCreateIssue:  "itervox action create-issue ",
		config.AgentActionMergePR:      "itervox action merge-pr ",
		config.AgentActionMoveState:    "itervox action move-state ",
		config.AgentActionProvideInput: "itervox action provide-input ",
	}
	for action, marker := range mustMention {
		if !strings.Contains(got, marker) {
			t.Errorf("menu missing CLI line for action %q (marker %q); got:\n%s", action, marker, got)
		}
	}
}

func TestBuildAgentActionContext_RespectsAllowedActionsFiltering(t *testing.T) {
	got := buildAgentActionContext([]string{config.AgentActionComment}, agentactions.Scope{}, false)
	if !strings.Contains(got, "itervox action comment ") {
		t.Error("expected comment line")
	}
	if strings.Contains(got, "merge-pr") || strings.Contains(got, "comment-pr") {
		t.Error("non-granted actions must not leak into menu")
	}
}

// TestBuildAgentActionContext_ListsCreateIssueLabels (#253): the create-issue
// line is followed by its optional flags, naming the profile's allowed
// labels — or saying none are allowed.
func TestBuildAgentActionContext_ListsCreateIssueLabels(t *testing.T) {
	got := buildAgentActionContext([]string{config.AgentActionCreateIssue},
		agentactions.Scope{CreateIssueState: "Todo", CreateIssueLabels: []string{"repo:web", "needs-design"}}, false)
	assert.Contains(t, got, "`--blocked-by-source`")
	assert.Contains(t, got, "`--label \"...\"` (repeatable)")
	assert.Contains(t, got, "allowed labels: `repo:web`, `needs-design`.")

	got = buildAgentActionContext([]string{config.AgentActionCreateIssue}, agentactions.Scope{CreateIssueState: "Todo"}, false)
	assert.Contains(t, got, "No labels are allowed on new issues for this profile.")
	assert.NotContains(t, got, "--label")

	got = buildAgentActionContext([]string{config.AgentActionComment},
		agentactions.Scope{CreateIssueLabels: []string{"repo:web"}}, false)
	assert.NotContains(t, got, "repo:web", "labels are listed only with create_issue")
}
