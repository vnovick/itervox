package workflow_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/workflow"
	"gopkg.in/yaml.v3"
)

// assertValidWorkflowYAML extracts the YAML front matter from a workflow
// file's contents and confirms it parses cleanly. The original B10 symptom
// was a mid-block indent mismatch that produced "mapping values are not
// allowed in this context" at parse time. v0.2.0 todolist5 B10.
func assertValidWorkflowYAML(t *testing.T, contents string) {
	t.Helper()
	body := strings.TrimPrefix(contents, "---\n")
	end := strings.Index(body, "\n---\n")
	if end < 0 {
		t.Fatalf("expected closing front-matter delimiter, got:\n%s", contents)
	}
	front := body[:end]
	var into map[string]any
	if err := yaml.Unmarshal([]byte(front), &into); err != nil {
		t.Fatalf("front matter must be valid YAML after Patch*; got error %v\n--- front matter ---\n%s", err, front)
	}
}

func TestLoadBasicWorkflow(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "workflows", "basic.md")
	wf, err := workflow.Load(path)
	require.NoError(t, err)
	assert.NotNil(t, wf.Config)
	assert.Contains(t, wf.PromptTemplate, "issue.identifier")
	trackerKind, ok := wf.Config["tracker"].(map[string]interface{})
	require.True(t, ok, "tracker should be a map")
	assert.Equal(t, "linear", trackerKind["kind"])
}

func TestLoadNoFrontMatter(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "workflows", "no-front-matter.md")
	wf, err := workflow.Load(path)
	require.NoError(t, err)
	assert.Empty(t, wf.Config)
	assert.Contains(t, wf.PromptTemplate, "no front matter")
}

func TestLoadMissingFile(t *testing.T) {
	_, err := workflow.Load("/nonexistent/path/WORKFLOW.md")
	require.Error(t, err)
	var wfErr *workflow.Error
	require.ErrorAs(t, err, &wfErr)
	assert.Equal(t, workflow.ErrMissingFile, wfErr.Code)
}

func TestLoadInvalidYAML(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "workflows", "invalid-yaml.md")
	_, err := workflow.Load(path)
	require.Error(t, err)
	var wfErr *workflow.Error
	require.ErrorAs(t, err, &wfErr)
	assert.Equal(t, workflow.ErrParseError, wfErr.Code)
}

func TestLoadFrontMatterNotAMap(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "WORKFLOW.md")
	content := "---\n- item1\n- item2\n---\n\nPrompt body.\n"
	require.NoError(t, os.WriteFile(f, []byte(content), 0o644))

	_, err := workflow.Load(f)
	require.Error(t, err)
	var wfErr *workflow.Error
	require.ErrorAs(t, err, &wfErr)
	assert.Equal(t, workflow.ErrFrontMatterNotAMap, wfErr.Code)
}

func TestLoadEmptyFrontMatter(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "WORKFLOW.md")
	content := "---\n---\n\nSome prompt.\n"
	require.NoError(t, os.WriteFile(f, []byte(content), 0o644))

	wf, err := workflow.Load(f)
	require.NoError(t, err)
	assert.Empty(t, wf.Config)
	assert.Equal(t, "Some prompt.", wf.PromptTemplate)
}

func TestLoadPromptIsTrimmed(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "WORKFLOW.md")
	content := "---\ntracker:\n  kind: linear\n---\n\n\n  hello  \n\n"
	require.NoError(t, os.WriteFile(f, []byte(content), 0o644))

	wf, err := workflow.Load(f)
	require.NoError(t, err)
	assert.Equal(t, "hello", wf.PromptTemplate)
}

func TestPatchIntField(t *testing.T) {
	content := "---\nagent:\n  max_concurrent_agents: 3\n  max_turns: 60\n---\n\nPrompt body.\n"
	tmp := t.TempDir()
	f := filepath.Join(tmp, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(f, []byte(content), 0o644))

	require.NoError(t, workflow.PatchIntField(f, "max_concurrent_agents", 7))

	data, err := os.ReadFile(f)
	require.NoError(t, err)
	got := string(data)
	assert.Contains(t, got, "max_concurrent_agents: 7")
	assert.Contains(t, got, "max_turns: 60") // unchanged
	assert.Contains(t, got, "Prompt body.")  // body preserved
}

func TestPatchIntFieldKeyNotFound(t *testing.T) {
	content := "---\nagent:\n  max_turns: 60\n---\n"
	tmp := t.TempDir()
	f := filepath.Join(tmp, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(f, []byte(content), 0o644))

	err := workflow.PatchIntField(f, "max_concurrent_agents", 5)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestPatchIntFieldPreservesComments(t *testing.T) {
	content := "---\nagent:\n  max_concurrent_agents: 3 # set at runtime\n---\n"
	tmp := t.TempDir()
	f := filepath.Join(tmp, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(f, []byte(content), 0o644))

	require.NoError(t, workflow.PatchIntField(f, "max_concurrent_agents", 10))

	data, err := os.ReadFile(f)
	require.NoError(t, err)
	assert.Contains(t, string(data), "max_concurrent_agents: 10 # set at runtime")
}

// TestPatchIntFieldConcurrent verifies T-46 (gaps_280426 06.G-01): concurrent
// PatchIntField calls on the same path serialize via editMu, so the final
// file always contains exactly one of the requested values rather than a
// torn / partially-overwritten line. Pre-fix, this test would race the
// read-modify-write on a busy filesystem and could produce an unexpected
// final value or a corrupt file.
func TestPatchIntFieldConcurrent(t *testing.T) {
	content := "---\nagent:\n  max_concurrent_agents: 0\n---\n"
	tmp := t.TempDir()
	f := filepath.Join(tmp, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(f, []byte(content), 0o644))

	const concurrency = 10
	values := make([]int, concurrency)
	for i := range values {
		values[i] = i + 1
	}

	var wg sync.WaitGroup
	for _, v := range values {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			require.NoError(t, workflow.PatchIntField(f, "max_concurrent_agents", n))
		}(v)
	}
	wg.Wait()

	// Final file content must contain exactly one of the written values
	// (whichever goroutine ran last under the lock) — not a malformed line.
	data, err := os.ReadFile(f)
	require.NoError(t, err)
	got := string(data)
	matched := false
	for _, v := range values {
		expected := fmt.Sprintf("max_concurrent_agents: %d", v)
		if strings.Contains(got, expected) {
			matched = true
			break
		}
	}
	assert.True(t, matched, "expected file to contain one of the written values, got:\n%s", got)
}

func TestPatchProfilesBlock_Create(t *testing.T) {
	// File with no profiles block — adds one under agent:
	content := "---\nagent:\n  max_concurrent_agents: 3\n  command: claude\n---\n\nPrompt body.\n"
	tmp := t.TempDir()
	f := filepath.Join(tmp, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(f, []byte(content), 0o644))

	disabled := false
	profiles := map[string]workflow.ProfileEntry{
		"fast":     {Command: "claude --model claude-haiku-4-5-20251001", AllowedActions: []string{"comment", "provide_input"}},
		"thorough": {Command: "claude --model claude-opus-4-6"},
		"codex":    {Command: "run-codex-wrapper", Backend: "codex", Enabled: &disabled},
		"triage":   {Command: "claude --model claude-sonnet-4-6", AllowedActions: []string{"create_issue"}, CreateIssueState: "Todo"},
	}
	require.NoError(t, workflow.PatchProfilesBlock(f, profiles))

	data, err := os.ReadFile(f)
	require.NoError(t, err)
	got := string(data)
	assert.Contains(t, got, "  profiles:")
	assert.Contains(t, got, "    fast:")
	assert.Contains(t, got, "      command: claude --model claude-haiku-4-5-20251001")
	assert.Contains(t, got, "      allowed_actions:")
	assert.Contains(t, got, "        - comment")
	assert.Contains(t, got, "        - provide_input")
	assert.Contains(t, got, "    codex:")
	assert.Contains(t, got, "      command: run-codex-wrapper")
	assert.Contains(t, got, "      backend: codex")
	assert.Contains(t, got, "      enabled: false")
	assert.Contains(t, got, "    triage:")
	assert.Contains(t, got, "      create_issue_state: \"Todo\"")
	assert.Contains(t, got, "    thorough:")
	assert.Contains(t, got, "      command: claude --model claude-opus-4-6")
	assert.NotContains(t, got, "    fast:\n      enabled: false")
	// Other fields preserved
	assert.Contains(t, got, "max_concurrent_agents: 3")
	assert.Contains(t, got, "command: claude")
	// Body preserved
	assert.Contains(t, got, "Prompt body.")
}

func TestPatchProfilesBlock_Replace(t *testing.T) {
	// File with existing profiles — replaces them.
	content := "---\nagent:\n  max_concurrent_agents: 5\n  profiles:\n    old:\n      command: claude --model old\n---\n\nBody.\n"
	tmp := t.TempDir()
	f := filepath.Join(tmp, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(f, []byte(content), 0o644))

	disabled := false
	profiles := map[string]workflow.ProfileEntry{
		"fast": {
			Command:          "run-codex-wrapper",
			Backend:          "codex",
			Enabled:          &disabled,
			AllowedActions:   []string{"move_state", "create_issue"},
			CreateIssueState: "Todo",
		},
	}
	require.NoError(t, workflow.PatchProfilesBlock(f, profiles))

	data, err := os.ReadFile(f)
	require.NoError(t, err)
	got := string(data)
	assert.Contains(t, got, "    fast:")
	assert.Contains(t, got, "      command: run-codex-wrapper")
	assert.Contains(t, got, "      backend: codex")
	assert.Contains(t, got, "      enabled: false")
	assert.Contains(t, got, "      allowed_actions:")
	assert.Contains(t, got, "        - create_issue")
	assert.Contains(t, got, "        - move_state")
	assert.Contains(t, got, "      create_issue_state: \"Todo\"")
	// Old profile gone
	assert.NotContains(t, got, "old:")
	// Other fields preserved
	assert.Contains(t, got, "max_concurrent_agents: 5")
	assert.Contains(t, got, "Body.")
}

func TestPatchProfilesBlock_QuotesCreateIssueState(t *testing.T) {
	content := "---\nagent:\n  command: claude\n---\n\nBody.\n"
	tmp := t.TempDir()
	f := filepath.Join(tmp, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(f, []byte(content), 0o644))

	profiles := map[string]workflow.ProfileEntry{
		"triage": {
			Command:          "claude --model claude-sonnet-4-6",
			AllowedActions:   []string{"create_issue"},
			CreateIssueState: "Todo: needs clarification #1",
		},
	}
	require.NoError(t, workflow.PatchProfilesBlock(f, profiles))

	data, err := os.ReadFile(f)
	require.NoError(t, err)
	assert.Contains(t, string(data), "      create_issue_state: \"Todo: needs clarification #1\"")
}

func TestPatchProfilesBlock_Delete(t *testing.T) {
	// Passing nil profiles removes the block.
	content := "---\nagent:\n  max_concurrent_agents: 2\n  profiles:\n    fast:\n      command: claude --model fast\n---\n\nBody.\n"
	tmp := t.TempDir()
	f := filepath.Join(tmp, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(f, []byte(content), 0o644))

	require.NoError(t, workflow.PatchProfilesBlock(f, nil))

	data, err := os.ReadFile(f)
	require.NoError(t, err)
	got := string(data)
	assert.NotContains(t, got, "profiles:")
	assert.NotContains(t, got, "fast:")
	// Other fields preserved
	assert.Contains(t, got, "max_concurrent_agents: 2")
	assert.Contains(t, got, "Body.")
}

func TestPatchAutomationsBlock_Create(t *testing.T) {
	content := "---\nagent:\n  command: claude\n---\n\nPrompt body.\n"
	tmp := t.TempDir()
	f := filepath.Join(tmp, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(f, []byte(content), 0o644))

	automations := []workflow.AutomationEntry{
		{
			ID:           "weekday-review",
			Enabled:      true,
			Profile:      "reviewer",
			Instructions: "Review backlog issues and comment with missing details.",
			Trigger: workflow.AutomationTriggerEntry{
				Type:     "cron",
				Cron:     "0 9 * * 1-5",
				Timezone: "Asia/Jerusalem",
			},
			Filter: workflow.AutomationFilterEntry{
				MatchMode:       "any",
				States:          []string{"Backlog", "Todo"},
				LabelsAny:       []string{"bug"},
				IdentifierRegex: "^ENG-",
				Limit:           2,
			},
		},
		{
			ID:           "qa-state-entry",
			Enabled:      true,
			Profile:      "qa",
			Instructions: "Run QA when the issue enters Ready for QA.",
			Trigger: workflow.AutomationTriggerEntry{
				Type:  "issue_entered_state",
				State: "Ready for QA",
			},
		},
		{
			ID:           "input-responder",
			Enabled:      true,
			Profile:      "input-responder",
			Instructions: "Answer narrow blocked-run questions.",
			Trigger: workflow.AutomationTriggerEntry{
				Type: "input_required",
			},
			Filter: workflow.AutomationFilterEntry{
				InputContextRegex: "continue|branch",
				MaxAgeMinutes:     30,
			},
			Policy: workflow.AutomationPolicyEntry{
				AutoResume: true,
			},
		},
		{
			ID:      "rate-limit-switch",
			Enabled: true,
			Profile: "fallback-codex",
			Trigger: workflow.AutomationTriggerEntry{
				Type: "rate_limited",
			},
			Policy: workflow.AutomationPolicyEntry{
				AutoResume:      true,
				SwitchToProfile: "fallback-codex",
				SwitchToBackend: "codex",
				CooldownMinutes: 45,
			},
		},
	}
	require.NoError(t, workflow.PatchAutomationsBlock(f, automations))

	data, err := os.ReadFile(f)
	require.NoError(t, err)
	got := string(data)
	assert.Contains(t, got, "automations:")
	assert.Contains(t, got, `type: cron`)
	assert.Contains(t, got, `cron: "0 9 * * 1-5"`)
	assert.Contains(t, got, `timezone: "Asia/Jerusalem"`)
	assert.Contains(t, got, "profile: reviewer")
	assert.Contains(t, got, `instructions: "Review backlog issues and comment with missing details."`)
	assert.Contains(t, got, `match_mode: "any"`)
	assert.Contains(t, got, `states: ["Backlog","Todo"]`)
	assert.Contains(t, got, `labels_any: ["bug"]`)
	assert.Contains(t, got, `identifier_regex: "^ENG-"`)
	assert.Contains(t, got, "limit: 2")
	assert.Contains(t, got, `type: issue_entered_state`)
	assert.Contains(t, got, `state: "Ready for QA"`)
	assert.Contains(t, got, `type: input_required`)
	assert.Contains(t, got, `input_context_regex: "continue|branch"`)
	assert.Contains(t, got, `max_age_minutes: 30`)
	assert.Contains(t, got, `auto_resume: true`)
	assert.Contains(t, got, `type: rate_limited`)
	assert.Contains(t, got, `auto_switch: true`)
	assert.Contains(t, got, `switch_to_profile: "fallback-codex"`)
	assert.Contains(t, got, `switch_to_backend: "codex"`)
	assert.Contains(t, got, `cooldown_minutes: 45`)
	assert.Contains(t, got, "Prompt body.")
}

func TestPatchProfilesBlock_PreservesOtherKeys(t *testing.T) {
	// Other agent keys and comments are unchanged.
	content := "---\n# Top comment\ntracker:\n  kind: linear\nagent:\n  # agent comment\n  max_concurrent_agents: 3\n  max_turns: 60\n  profiles:\n    old:\n      command: claude\nserver:\n  port: 8090\n---\n\nPrompt.\n"
	tmp := t.TempDir()
	f := filepath.Join(tmp, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(f, []byte(content), 0o644))

	profiles := map[string]workflow.ProfileEntry{
		"fast": {Command: "run-codex-wrapper", Backend: "codex"},
	}
	require.NoError(t, workflow.PatchProfilesBlock(f, profiles))

	data, err := os.ReadFile(f)
	require.NoError(t, err)
	got := string(data)
	// New profile present
	assert.Contains(t, got, "    fast:")
	assert.Contains(t, got, "      command: run-codex-wrapper")
	assert.Contains(t, got, "      backend: codex")
	// Old profile gone
	assert.NotContains(t, got, "    old:")
	// Other top-level keys preserved
	assert.Contains(t, got, "tracker:")
	assert.Contains(t, got, "  kind: linear")
	assert.Contains(t, got, "server:")
	assert.Contains(t, got, "  port: 8090")
	// Comments preserved
	assert.Contains(t, got, "# Top comment")
	assert.Contains(t, got, "# agent comment")
	// Body preserved
	assert.Contains(t, got, "Prompt.")
}

// --- workflow.Error ---

func TestWorkflowErrorMessage(t *testing.T) {
	err := &workflow.Error{Code: workflow.ErrMissingFile, Path: "/some/path.md"}
	assert.Equal(t, "missing_workflow_file: /some/path.md", err.Error())
}

func TestWorkflowErrorMessageWithCause(t *testing.T) {
	inner := fmt.Errorf("inner cause")
	err := &workflow.Error{Code: workflow.ErrParseError, Path: "/w.md", Cause: inner}
	msg := err.Error()
	assert.Contains(t, msg, "workflow_parse_error")
	assert.Contains(t, msg, "/w.md")
	assert.Contains(t, msg, "inner cause")
}

func TestWorkflowErrorUnwrap(t *testing.T) {
	inner := fmt.Errorf("root")
	err := &workflow.Error{Code: workflow.ErrParseError, Path: "p", Cause: inner}
	assert.Equal(t, inner, err.Unwrap())
}

func TestWorkflowErrorUnwrapNoCause(t *testing.T) {
	err := &workflow.Error{Code: workflow.ErrMissingFile, Path: "p"}
	assert.Nil(t, err.Unwrap())
}

// --- PatchAgentBoolField ---

func writeTmp(t *testing.T, content string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "WORKFLOW.md")
	require.NoError(t, os.WriteFile(f, []byte(content), 0o644))
	return f
}

func TestPatchAgentBoolFieldSetTrue(t *testing.T) {
	f := writeTmp(t, "---\nagent:\n  verbose: false\n---\n\nBody.\n")
	require.NoError(t, workflow.PatchAgentBoolField(f, "verbose", true))

	data, _ := os.ReadFile(f)
	assert.Contains(t, string(data), "  verbose: true")
	assert.Contains(t, string(data), "Body.")
}

func TestPatchAgentBoolFieldSetFalseRemovesKey(t *testing.T) {
	f := writeTmp(t, "---\nagent:\n  verbose: true\n---\n\nBody.\n")
	require.NoError(t, workflow.PatchAgentBoolField(f, "verbose", false))

	data, _ := os.ReadFile(f)
	assert.NotContains(t, string(data), "verbose")
}

func TestPatchAgentBoolFieldInsertWhenMissing(t *testing.T) {
	f := writeTmp(t, "---\nagent:\n  max_turns: 50\n---\n\nBody.\n")
	require.NoError(t, workflow.PatchAgentBoolField(f, "auto_resume", true))

	data, _ := os.ReadFile(f)
	assert.Contains(t, string(data), "  auto_resume: true")
}

// v0.2.0 todolist5 B10 — toggling a bool field in a workflow whose `agent:`
// block uses 4-space indent (the default produced by yaml.v3 serialisation
// on many Go encoders) MUST preserve that indent. Before the fix, the
// patcher hardcoded 2-space indent and produced "  inline_input: true\n
// auto_review: false" — invalid YAML at line 3.
func TestPatchAgentBoolFieldPreserves4SpaceIndent(t *testing.T) {
	source := "---\nagent:\n    auto_review: false\n    max_turns: 50\n---\n\nBody.\n"
	f := writeTmp(t, source)
	require.NoError(t, workflow.PatchAgentBoolField(f, "inline_input", true))

	data, err := os.ReadFile(f)
	require.NoError(t, err)
	got := string(data)
	assert.Contains(t, got, "    inline_input: true", "new key must adopt the 4-space indent the block already uses")
	// The file must still parse as valid YAML — the actual symptom on
	// inhabited workflows was a parse error at the mismatch line. This
	// catches both an outright indent mismatch and any subtle prefix-overlap
	// regression that might survive the textual contains check.
	assertValidWorkflowYAML(t, got)
}

// v0.2.0 todolist5 B10 — toggling off (delete) on a 4-space file must also
// preserve the file's indent for siblings.
func TestPatchAgentBoolFieldSetFalsePreserves4SpaceIndent(t *testing.T) {
	source := "---\nagent:\n    inline_input: true\n    auto_review: false\n---\n\nBody.\n"
	f := writeTmp(t, source)
	require.NoError(t, workflow.PatchAgentBoolField(f, "inline_input", false))

	data, err := os.ReadFile(f)
	require.NoError(t, err)
	got := string(data)
	assert.NotContains(t, got, "inline_input")
	assert.Contains(t, got, "    auto_review: false", "remaining sibling must keep its original 4-space indent")
	assertValidWorkflowYAML(t, got)
}

func TestPatchAgentBoolFieldNoFrontMatterErrors(t *testing.T) {
	f := writeTmp(t, "No front matter here.\n")
	err := workflow.PatchAgentBoolField(f, "verbose", true)
	require.Error(t, err)
}

func TestPatchAgentBoolFieldMissingFileErrors(t *testing.T) {
	err := workflow.PatchAgentBoolField("/no/such/file.md", "verbose", true)
	require.Error(t, err)
}

// --- PatchAgentStringField ---

func TestPatchAgentStringFieldSet(t *testing.T) {
	f := writeTmp(t, "---\nagent:\n  backend: claude\n---\n\nBody.\n")
	require.NoError(t, workflow.PatchAgentStringField(f, "backend", "codex"))

	data, _ := os.ReadFile(f)
	// PatchAgentStringField stores strings quoted.
	assert.Contains(t, string(data), "backend")
	assert.Contains(t, string(data), "codex")
}

func TestPatchAgentStringFieldRemoveWhenEmpty(t *testing.T) {
	f := writeTmp(t, "---\nagent:\n  backend: codex\n---\n\nBody.\n")
	require.NoError(t, workflow.PatchAgentStringField(f, "backend", ""))

	data, _ := os.ReadFile(f)
	assert.NotContains(t, string(data), "backend")
}

func TestPatchAgentStringFieldInsertWhenMissing(t *testing.T) {
	f := writeTmp(t, "---\nagent:\n  max_turns: 40\n---\n\nBody.\n")
	require.NoError(t, workflow.PatchAgentStringField(f, "backend", "codex"))

	data, _ := os.ReadFile(f)
	assert.Contains(t, string(data), "backend")
	assert.Contains(t, string(data), "codex")
}

func TestPatchAgentStringFieldNoFrontMatterErrors(t *testing.T) {
	f := writeTmp(t, "Just body.\n")
	err := workflow.PatchAgentStringField(f, "backend", "codex")
	require.Error(t, err)
}

func TestPatchAgentStringFieldMissingFileErrors(t *testing.T) {
	err := workflow.PatchAgentStringField("/no/such/file.md", "backend", "codex")
	require.Error(t, err)
}

func TestPatchAgentStringSliceFieldInsertWhenMissing(t *testing.T) {
	f := writeTmp(t, "---\nagent:\n  command: claude\n---\n\nBody.\n")
	require.NoError(t, workflow.PatchAgentStringSliceField(f, "ssh_hosts", []string{"worker-1", "worker-2"}))

	data, _ := os.ReadFile(f)
	assert.Contains(t, string(data), `ssh_hosts: ["worker-1","worker-2"]`)
}

func TestPatchAgentStringMapFieldSetAndRemove(t *testing.T) {
	f := writeTmp(t, "---\nagent:\n  command: claude\n---\n\nBody.\n")
	require.NoError(t, workflow.PatchAgentStringMapField(f, "ssh_host_descriptions", map[string]string{
		"worker-1":      "fast box",
		"worker-2:2222": "gpu box",
	}))

	data, _ := os.ReadFile(f)
	assert.Contains(t, string(data), `ssh_host_descriptions:`)
	assert.Contains(t, string(data), `"worker-1": "fast box"`)
	assert.Contains(t, string(data), `"worker-2:2222": "gpu box"`)

	require.NoError(t, workflow.PatchAgentStringMapField(f, "ssh_host_descriptions", nil))
	data, _ = os.ReadFile(f)
	assert.NotContains(t, string(data), "ssh_host_descriptions:")
}

func TestPatchReviewerConfig_SetAndClear(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(f, []byte("---\ntracker:\n  kind: linear\nagent:\n  command: claude\n---\n\nBody.\n"), 0o644))

	require.NoError(t, workflow.PatchReviewerConfig(f, "reviewer", true))

	data, err := os.ReadFile(f)
	require.NoError(t, err)
	got := string(data)
	assert.Contains(t, got, `reviewer_profile: "reviewer"`)
	assert.Contains(t, got, `auto_review: true`)

	require.NoError(t, workflow.PatchReviewerConfig(f, "", false))

	data, err = os.ReadFile(f)
	require.NoError(t, err)
	got = string(data)
	assert.NotContains(t, got, "reviewer_profile:")
	assert.NotContains(t, got, "auto_review:")
	assert.Contains(t, got, "  command: claude")
}

func TestPatchTrackerStates_InsertsMissingKeysInsideTrackerBlock(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(f, []byte("---\ntracker:\n  kind: linear\nagent:\n  command: claude\n---\n\nBody.\n"), 0o644))

	require.NoError(t, workflow.PatchTrackerStates(f, []string{"Todo"}, []string{"Done"}, "In Review"))

	data, err := os.ReadFile(f)
	require.NoError(t, err)
	got := string(data)
	assert.Contains(t, got, "tracker:\n  kind: linear\n  active_states: [\"Todo\"]\n  terminal_states: [\"Done\"]\n  completion_state: \"In Review\"")
	assert.Contains(t, got, "agent:\n  command: claude")
}

func TestPatchAgentMaxRetries_ReplaceAndInsert(t *testing.T) {
	tmp := t.TempDir()

	// Existing key — replace in place.
	f1 := filepath.Join(tmp, "with.md")
	require.NoError(t, os.WriteFile(f1, []byte("---\nagent:\n  command: claude\n  max_retries: 3\n---\nBody.\n"), 0o644))
	require.NoError(t, workflow.PatchAgentMaxRetries(f1, 7))
	data, err := os.ReadFile(f1)
	require.NoError(t, err)
	assert.Contains(t, string(data), "max_retries: 7")
	assert.NotContains(t, string(data), "max_retries: 3")

	// Missing key — insert inside agent block. Operator might not have set
	// it before; the UI should still be able to write a value.
	f2 := filepath.Join(tmp, "without.md")
	require.NoError(t, os.WriteFile(f2, []byte("---\nagent:\n  command: claude\n---\nBody.\n"), 0o644))
	require.NoError(t, workflow.PatchAgentMaxRetries(f2, 5))
	data, err = os.ReadFile(f2)
	require.NoError(t, err)
	assert.Contains(t, string(data), "max_retries: 5")
}

func TestPatchTrackerFailedState_SetAndClear(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(f, []byte("---\ntracker:\n  kind: linear\nagent:\n  command: claude\n---\nBody.\n"), 0o644))

	require.NoError(t, workflow.PatchTrackerFailedState(f, "Backlog"))
	data, err := os.ReadFile(f)
	require.NoError(t, err)
	assert.Contains(t, string(data), `failed_state: "Backlog"`)

	// Empty string — operator picked "Pause (do not move)" — must remove
	// the key entirely so config.Load() reads back empty default.
	require.NoError(t, workflow.PatchTrackerFailedState(f, ""))
	data, err = os.ReadFile(f)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "failed_state:")
	assert.Contains(t, string(data), "  kind: linear") // sibling preserved
}
