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

func TestExtractPromptRefsListsAndFences(t *testing.T) {
	text := "Use the `a`, `b`, and `c` skills; then the `d`/`e` + `f` skills.\n" +
		"Text with ``` mid-line does not open a fence. Ask @agent-after-inline.\n" +
		"~~~\nAsk @agent-in-tilde-fence.\n~~~\n" +
		"   ```go\nUse the `in-backtick-fence` skill.\n   ```\n" +
		"Finally ask @agent-last."
	assert.Equal(t, []PromptRef{
		{Kind: "skill", Name: "a"}, {Kind: "skill", Name: "b"}, {Kind: "skill", Name: "c"},
		{Kind: "skill", Name: "d"}, {Kind: "skill", Name: "e"}, {Kind: "skill", Name: "f"},
		{Kind: "subagent", Name: "after-inline"},
		{Kind: "subagent", Name: "last"},
	}, ExtractPromptRefs(text))
}

// TestExtractPromptRefsFenceRules pins CommonMark fence handling found in
// review: fences inside list items and blockquotes, longer fences that a
// shorter run does not close, closing lines that carry text, and backtick
// info strings.
func TestExtractPromptRefsFenceRules(t *testing.T) {
	for name, text := range map[string]string{
		"list item, four-space indent":            "1. Step one:\n\n    ```\n    Ask @agent-ghost.\n    ```\n",
		"fence on the list marker line":           "- ```\n  Ask @agent-ghost.\n  ```\n",
		"blockquote":                              "> ```\n> Ask @agent-ghost.\n> ```\n",
		"nested blockquote":                       "> > ~~~\n> > Use the `ghost` skill.\n> > ~~~\n",
		"four-backtick fence not closed by three": "````\n```\nAsk @agent-ghost.\n```\n````\n",
		"closing line with text does not close":   "```\n``` not a close\nAsk @agent-ghost.\n```\n",
		"tilde fence not closed by backticks":     "~~~\n```\nAsk @agent-ghost.\n~~~\n",
		"unclosed fence runs to the end":          "```\nAsk @agent-ghost.",
	} {
		assert.Empty(t, ExtractPromptRefs(text), name)
	}
	// A backtick run whose "info string" holds a backtick is inline code,
	// not a fence, so the text after it is still checked.
	assert.Equal(t, []PromptRef{{Kind: "subagent", Name: "real"}},
		ExtractPromptRefs("```inline ` code```\nAsk @agent-real."))
	// Text after a properly closed long fence is checked again.
	assert.Equal(t, []PromptRef{{Kind: "subagent", Name: "after"}},
		ExtractPromptRefs("````md\n```\n@agent-ghost\n```\n````\nAsk @agent-after."))
}

// TestExtractPromptRefsFenceContainers pins the container cases found in
// review 4: container-looking lines inside a fence do not close it, and a
// fence opened in a quote or list item ends with that container.
func TestExtractPromptRefsFenceContainers(t *testing.T) {
	for name, tc := range map[string]struct {
		text string
		want []PromptRef
	}{
		"list-marker fence line inside a fence": {
			"```markdown\n- ```\nAsk @agent-ghost-list.\n```\nAsk @agent-real.\n", nil,
		},
		"four-space fence line inside a fence": {
			"```\n    ```\nAsk @agent-ghost-indent4.\n```\nAsk @agent-real.\n", nil,
		},
		"quote fence ends with the quote": {
			"> ```\n> some code\n\nAsk @agent-real.\n\n```\nAsk @agent-ghost-b.\n```\n", nil,
		},
		"list fence ends with the item": {
			"- ```\n  code @agent-ghost\nAsk @agent-real.\n", nil,
		},
		"list fence closed at the item's indent": {
			"- ```\n  code @agent-ghost\n  ```\n  Ask @agent-real.\n", nil,
		},
	} {
		want := tc.want
		if want == nil {
			want = []PromptRef{{Kind: "subagent", Name: "real"}}
		}
		assert.Equal(t, want, ExtractPromptRefs(tc.text), name)
	}
}

// TestExtractPromptRefsAgentMentionBoundaries pins the "@agent-" start and
// end rules found in review.
func TestExtractPromptRefsAgentMentionBoundaries(t *testing.T) {
	text := "Ask `@agent-in-code`, **@agent-bold**, _@agent-em_, __@agent-strong__, <@agent-angle> and >@agent-quoted."
	assert.Equal(t, []PromptRef{
		{Kind: "subagent", Name: "in-code"},
		{Kind: "subagent", Name: "bold"},
		{Kind: "subagent", Name: "em"},
		{Kind: "subagent", Name: "strong"},
		{Kind: "subagent", Name: "angle"},
		{Kind: "subagent", Name: "quoted"},
	}, ExtractPromptRefs(text))
	for _, text := range []string{
		"Ask @agent-foo_bar.",
		"Ask @agent-fooBar.",
		"Ask @agent-foo9_x.",
		"Ask @agent-foo__bar.",
		"Ask @agent-fooé.",
		"Ask @agent-foo-.",
		"Mail ops_@agent-corp.com.",
		"See https://x.io/a_@agent-path and x*@agent-y.",
	} {
		assert.Empty(t, ExtractPromptRefs(text), "invalid name must be skipped, not truncated: %q", text)
	}
}

// TestExtractPromptRefsDocumentedListLimits pins the list forms the package
// doc says are not followed, so the documentation stays true.
func TestExtractPromptRefsDocumentedListLimits(t *testing.T) {
	assert.Equal(t, []PromptRef{{Kind: "skill", Name: "b"}},
		ExtractPromptRefs("Use `a` and the `b` skills."))
	assert.Equal(t, []PromptRef{{Kind: "skill", Name: "a"}},
		ExtractPromptRefs("Use the `a` skill (or `b`)."))
	assert.Equal(t, []PromptRef{{Kind: "skill", Name: "b"}},
		ExtractPromptRefs("Use `a`,\n`b` skills."))
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
		"~~~\nAsk @agent-ghost\n~~~",
		"Put it in the `codex` skills directory, the `parser` skills module and the `max-retries` skills option.",
		"Give `x` skill-level guidance; `README` skill notes; `1` skill point.",
		"Mention `foo`\n\nskills header",
		"Edit the `planner` subagent file.",
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
		"clean": {Command: "claude", Instructions: "Ask @agent-reviewer. Use the `verify-before-done` skill."},
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
		{"padded backend is used verbatim and falls back to claude", RefBackendDefaults{}, config.AgentProfile{Command: "./wrap", Backend: " codex"}, "claude"},
		{"backend hint in the command", RefBackendDefaults{}, config.AgentProfile{Command: "@@itervox-backend=codex ./wrap"}, "codex"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, ProfileBackend(tc.profile, tc.defaults), tc.name)
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

// TestValidateProfileRefsSSHInfoSkipsProjectPlugins: a plugin installed under
// the repository's .claude/plugins travels with the repo, so its skills and
// agents get no USER_SCOPE_REF_ON_SSH note; a user-scope plugin's do.
func TestValidateProfileRefsSSHInfoSkipsProjectPlugins(t *testing.T) {
	inv := &Inventory{
		Plugins: []Plugin{
			{Name: "repo-plugin", Provider: "claude", Source: "project", Skills: []Skill{{Name: "repo-skill"}}},
			{Name: "home-plugin", Provider: "claude", Source: "user", Skills: []Skill{{Name: "home-skill"}}},
		},
		Subagents: []Subagent{
			{Name: "repo-agent", Provider: "claude", Source: "plugin:repo-plugin"},
			{Name: "home-agent", Provider: "claude", Source: "plugin:home-plugin"},
		},
	}
	profiles := map[string]config.AgentProfile{
		"impl": {Instructions: "Use the `repo-skill` and `home-skill` skills; ask @agent-repo-agent and @agent-home-agent."},
	}
	assert.Equal(t, []string{
		"USER_SCOPE_REF_ON_SSH:impl:home-skill",
		"USER_SCOPE_REF_ON_SSH:impl:home-agent",
	}, refIssueKeys(ValidateProfileRefs(inv, profiles, RefBackendDefaults{}, []string{"build-box"})))
}
