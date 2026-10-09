package skills

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/vnovick/itervox/internal/config"
)

func refInventory() *Inventory {
	return &Inventory{
		Skills: []Skill{
			{Name: "verify-before-done", Provider: "claude", Source: "project"},
			{Name: "personal-notes", Provider: "claude", Source: "user"},
			{Name: "codex-only", Provider: "codex", Source: "user"},
		},
		Plugins: []Plugin{{Name: "demo-plugin", Provider: "claude", Skills: []Skill{{Name: "skill-a"}}}},
		Subagents: []Subagent{
			{Name: "code-reviewer", Provider: "claude", Source: "project"},
			{Name: "reviewer", Provider: "claude", Source: "plugin:demo-plugin"},
		},
	}
}

func refIssueKeys(issues []InventoryIssue) []string {
	out := make([]string, 0, len(issues))
	for _, i := range issues {
		out = append(out, i.ID+":"+i.Affected[0]+":"+i.Affected[1])
	}
	return out
}

func TestExtractPromptRefsRecognisedForms(t *testing.T) {
	text := "Ask @agent-code-reviewer first. Then run `/verify-before-done`.\n" +
		"Use the `skill-a` skill, the `demo-plugin:reviewer` subagent and the `planner` agent.\n" +
		"Plain prose about a reviewer skill, an email me@agent-x.com, a path /usr/bin, and `notaref` here never count.\n" +
		"Repeat: @agent-code-reviewer."
	assert.Equal(t, []PromptRef{
		{Kind: "subagent", Name: "code-reviewer"},
		{Kind: "skill", Name: "verify-before-done"},
		{Kind: "skill", Name: "skill-a"},
		{Kind: "subagent", Name: "demo-plugin:reviewer"},
		{Kind: "subagent", Name: "planner"},
	}, ExtractPromptRefs(text))
}

// TestValidateProfileRefsWarnsOnMissingOnly pins #86: a prompt that names a
// missing skill or subagent produces a warning; a valid reference does not.
func TestValidateProfileRefsWarnsOnMissingOnly(t *testing.T) {
	profiles := map[string]config.AgentProfile{
		"implementer": {
			Command: "claude",
			Soul:    "You are the implementer.",
			Instructions: "Before finishing run `/verify-before-done`, use the `skill-a` skill and the `demo-plugin:skill-a` skill.\n" +
				"Hand off to @agent-code-reviewer and the `demo-plugin:reviewer` subagent.\n" +
				"Then run `/verify-befor-done` and ask @agent-security-auditor.",
		},
		"clean": {Command: "claude", Instructions: "Ask @agent-reviewer. Run `/VERIFY-BEFORE-DONE`."},
	}
	issues := ValidateProfileRefs(refInventory(), profiles, nil)
	assert.Equal(t, []string{
		"MISSING_SKILL_REF:implementer:verify-befor-done",
		"MISSING_SUBAGENT_REF:implementer:security-auditor",
	}, refIssueKeys(issues), "only the typo'd skill and the unknown subagent warn; case-insensitive matches pass")
	for _, i := range issues {
		assert.Equal(t, "warn", i.Severity)
	}
}

// TestValidateProfileRefsPerBackend: a Codex profile resolves against Codex
// skills only and has no subagents.
func TestValidateProfileRefsPerBackend(t *testing.T) {
	profiles := map[string]config.AgentProfile{
		"codex-impl":     {Command: "codex", Instructions: "Use `/codex-only`, `/verify-before-done` and @agent-code-reviewer."},
		"backend-set":    {Command: "/opt/bin/agent", Backend: "codex", Instructions: "Use `/codex-only`."},
		"claude-default": {Instructions: "Use `/codex-only`."},
	}
	issues := ValidateProfileRefs(refInventory(), profiles, nil)
	assert.Equal(t, []string{
		"MISSING_SKILL_REF:claude-default:codex-only",
		"MISSING_SKILL_REF:codex-impl:verify-before-done",
		"MISSING_SUBAGENT_REF:codex-impl:code-reviewer",
	}, refIssueKeys(issues))
	for _, i := range issues {
		if i.ID == "MISSING_SUBAGENT_REF" {
			assert.Contains(t, i.Description, "Codex, which has no subagents")
		}
	}
}

// TestValidateProfileRefsSSHInfo: with SSH hosts configured, a reference that
// resolves only via user or plugin scope gets an info issue; project-scoped
// references do not.
func TestValidateProfileRefsSSHInfo(t *testing.T) {
	profiles := map[string]config.AgentProfile{
		"impl": {Instructions: "Run `/personal-notes`, `/verify-before-done`, and ask @agent-reviewer."},
	}
	assert.Empty(t, ValidateProfileRefs(refInventory(), profiles, nil), "no SSH hosts → nothing to say")
	issues := ValidateProfileRefs(refInventory(), profiles, []string{"build-box"})
	assert.Equal(t, []string{
		"USER_SCOPE_REF_ON_SSH:impl:personal-notes",
		"USER_SCOPE_REF_ON_SSH:impl:reviewer",
	}, refIssueKeys(issues))
	assert.Equal(t, "info", issues[0].Severity)
	assert.Contains(t, issues[0].Description, "build-box")
}

func TestAnalyzeIncludesProfileRefIssues(t *testing.T) {
	issues := Analyze(refInventory(), AnalyzeInputs{
		Profiles: map[string]config.AgentProfile{"impl": {Instructions: "Ask @agent-ghost."}},
	})
	found := false
	for _, i := range issues {
		if i.ID == "MISSING_SUBAGENT_REF" {
			found = true
		}
	}
	assert.True(t, found, "Analyze surfaces reference issues to the dashboard: %+v", issues)
}
