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
	text := "Ask @agent-code-reviewer first (or @agent-planner).\n" +
		"Use the `verify-before-done` skill, then the `skill-a`, `demo-plugin:skill-b` and `go-hygiene` skills.\n" +
		"Hand off to the `demo-plugin:reviewer` subagent.\n" +
		"Repeat: @agent-code-reviewer."
	assert.Equal(t, []PromptRef{
		{Kind: "subagent", Name: "code-reviewer"},
		{Kind: "subagent", Name: "planner"},
		{Kind: "skill", Name: "verify-before-done"},
		{Kind: "skill", Name: "skill-a"},
		{Kind: "skill", Name: "demo-plugin:skill-b"},
		{Kind: "skill", Name: "go-hygiene"},
		{Kind: "subagent", Name: "demo-plugin:reviewer"},
	}, ExtractPromptRefs(text))
}

// TestExtractPromptRefsIgnoresOrdinaryProse pins the false-positive corpus
// found in review: none of these is a skill or subagent reference.
func TestExtractPromptRefsIgnoresOrdinaryProse(t *testing.T) {
	for _, text := range []string{
		"Use the `claude` agent CLI.",
		"The `codex` agent is not available here.",
		"Set `max_concurrent_agents` agents to 3.",
		"Do not use the `foo` agent configuration",
		"Read `SKILL.md` skill files.",
		"Run `/tmp` cleanup, check the `/health` endpoint, and use `/model` to switch.",
		"See https://example.com/@agent-foo/profile and email me@agent-x.com.",
		"Plain prose about a reviewer skill and the code-reviewer subagent.",
		"```\nExample: ask @agent-ghost and use the `ghost` skill.\n```",
	} {
		assert.Empty(t, ExtractPromptRefs(text), "must not be a reference: %q", text)
	}
}

// TestValidateProfileRefsWarnsOnMissingOnly pins #86: a prompt that names a
// missing skill or subagent produces a warning; a valid reference does not.
func TestValidateProfileRefsWarnsOnMissingOnly(t *testing.T) {
	profiles := map[string]config.AgentProfile{
		"implementer": {
			Command: "claude",
			Soul:    "You are the implementer.",
			Instructions: "Before finishing use the `verify-before-done` skill, and the `skill-a` and `demo-plugin:skill-a` skills.\n" +
				"Hand off to @agent-code-reviewer and the `demo-plugin:reviewer` subagent.\n" +
				"Then use the `verify-befor-done` skill and ask @agent-security-auditor.",
		},
		"clean": {Command: "claude", Instructions: "Ask @agent-reviewer. Use the `VERIFY-BEFORE-DONE` skill."},
	}
	issues := ValidateProfileRefs(refInventory(), profiles, RefBackendDefaults{}, nil)
	assert.Equal(t, []string{
		"MISSING_SKILL_REF:implementer:verify-befor-done",
		"MISSING_SUBAGENT_REF:implementer:security-auditor",
	}, refIssueKeys(issues), "only the typo'd skill and the unknown subagent warn; case-insensitive matches pass")
	for _, i := range issues {
		assert.Equal(t, "warn", i.Severity)
	}
}

// TestProfileBackendMatchesDispatchResolver mirrors the orchestrator's
// resolveDispatchTarget cases found in review.
func TestProfileBackendMatchesDispatchResolver(t *testing.T) {
	cases := []struct {
		name     string
		defaults RefBackendDefaults
		profile  config.AgentProfile
		want     string
	}{
		{"default claude", RefBackendDefaults{Command: "claude"}, config.AgentProfile{}, "claude"},
		{"env assignment before codex", RefBackendDefaults{}, config.AgentProfile{Command: "OPENAI_API_KEY=x codex exec"}, "codex"},
		{"env prefix before codex", RefBackendDefaults{}, config.AgentProfile{Command: "env FOO=1 codex"}, "codex"},
		{"CODEX_HOME env, claude binary", RefBackendDefaults{}, config.AgentProfile{Command: "CODEX_HOME=/x claude --model opus"}, "claude"},
		{"codex-named wrapper without backend runs on the default runner", RefBackendDefaults{}, config.AgentProfile{Command: "/opt/bin/codex-wrapper.sh"}, "claude"},
		{"wrapper with backend codex", RefBackendDefaults{}, config.AgentProfile{Command: "/opt/bin/agent.sh", Backend: "codex"}, "codex"},
		{"conflicting backend is refused", RefBackendDefaults{}, config.AgentProfile{Command: "claude --model opus", Backend: "codex"}, "claude"},
		{"agent.command codex inherited", RefBackendDefaults{Command: "codex"}, config.AgentProfile{}, "codex"},
		{"agent.backend on a wrapper default", RefBackendDefaults{Command: "./run-agent", Backend: "codex"}, config.AgentProfile{}, "codex"},
		{"profile command replaces default backend", RefBackendDefaults{Command: "codex"}, config.AgentProfile{Command: "claude"}, "claude"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, profileBackend(tc.profile, tc.defaults), tc.name)
	}
}

// TestValidateProfileRefsPerBackend: a Codex profile resolves against Codex
// skills only and has no subagents.
func TestValidateProfileRefsPerBackend(t *testing.T) {
	profiles := map[string]config.AgentProfile{
		"codex-impl":     {Command: "codex", Instructions: "Use the `codex-only` and `verify-before-done` skills, and ask @agent-code-reviewer."},
		"backend-set":    {Command: "/opt/bin/agent", Backend: "codex", Instructions: "Use the `codex-only` skill."},
		"claude-default": {Instructions: "Use the `codex-only` skill."},
	}
	issues := ValidateProfileRefs(refInventory(), profiles, RefBackendDefaults{Command: "claude"}, nil)
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
		"impl": {Instructions: "Use the `personal-notes` and `verify-before-done` skills, and ask @agent-reviewer."},
	}
	assert.Empty(t, ValidateProfileRefs(refInventory(), profiles, RefBackendDefaults{}, nil), "no SSH hosts → nothing to say")
	issues := ValidateProfileRefs(refInventory(), profiles, RefBackendDefaults{}, []string{"build-box"})
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
