package github

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/vnovick/itervox/internal/templates"
)

// TestAgentTaskTemplateBlockersParse (#83): an issue created from the
// agent-ready template, with its Blockers line filled in, yields those
// blockers through the GitHub adapter's own parser; the unfilled template
// (including the phrase examples in its comments) yields none.
func TestAgentTaskTemplateBlockersParse(t *testing.T) {
	body := string(templates.AgentTaskBody)
	assert.Empty(t, extractBlockers(map[string]any{"number": 50, "body": body}),
		"an unfilled template must not declare a blocker")

	filled := strings.Replace(body, "\nBlocked by #\n", "\nBlocked by #12, #15\n", 1)
	assert.NotEqual(t, body, filled, "the template's Blockers line is where a reader fills in blockers")
	blockers := extractBlockers(map[string]any{"number": 50, "body": filled})
	var ids []string
	for _, b := range blockers {
		ids = append(ids, *b.Identifier)
	}
	assert.Equal(t, []string{"#12", "#15"}, ids)

	dependsForm := strings.Replace(body, "\nBlocked by #\n", "\nDepends on #7\n", 1)
	got := extractBlockers(map[string]any{"number": 50, "body": dependsForm})
	if assert.Len(t, got, 1) {
		assert.Equal(t, "#7", *got[0].Identifier, "the other phrase the comment suggests works too")
	}
}

// TestAgentTaskGitHubTemplateHasNoLabels: the GitHub template sets no labels,
// so a new issue is not dispatchable before it is filled in.
func TestAgentTaskGitHubTemplateHasNoLabels(t *testing.T) {
	tpl := string(templates.AgentTaskGitHubTemplate())
	assert.True(t, strings.HasPrefix(tpl, "---\nname: Agent task\n"))
	front, _, _ := strings.Cut(strings.TrimPrefix(tpl, "---\n"), "\n---\n")
	assert.NotContains(t, front, "labels")
	for _, section := range []string{"## Goal", "## Acceptance criteria", "## Likely files or areas", "## How to verify", "## Out of scope", "## Blockers"} {
		assert.Contains(t, tpl, section)
	}
}
