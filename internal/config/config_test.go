package config_test

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/config"
)

func workflowWithContent(t *testing.T, content string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "WORKFLOW.md")
	require.NoError(t, os.WriteFile(f, []byte(content), 0o644))
	return f
}

func minimal(extras string) string {
	return "---\ntracker:\n  kind: linear\n  api_key: test-key\n  project_slug: my-project\n" + extras + "---\n\nPrompt.\n"
}

func minimalV2(extras string) string {
	return "---\nitervox_schema_version: 2\ntracker:\n  kind: linear\n  api_key: test-key\n  project_slug: my-project\n" + extras + "---\n\nPrompt.\n"
}

func schema2ProfileFileFields(t *testing.T, indent string) string {
	t.Helper()
	dir := t.TempDir()
	soulPath := filepath.Join(dir, "SOUL.md")
	instructionsPath := filepath.Join(dir, "INSTRUCTIONS.md")
	require.NoError(t, os.WriteFile(soulPath, []byte("# SOUL"), 0o644))
	require.NoError(t, os.WriteFile(instructionsPath, []byte("# INSTRUCTIONS"), 0o644))
	return indent + "soul_file: " + strconv.Quote(soulPath) + "\n" +
		indent + "instructions_file: " + strconv.Quote(instructionsPath) + "\n"
}

func minimalV2WithProfileFiles(t *testing.T, extras string) string {
	t.Helper()
	lines := strings.Split(extras, "\n")
	out := make([]string, 0, len(lines)*2)
	inProfiles := false
	for _, line := range lines {
		if line == "  profiles:" {
			inProfiles = true
		} else if inProfiles && line != "" && !strings.HasPrefix(line, " ") {
			inProfiles = false
		}
		out = append(out, line)
		if inProfiles && strings.HasPrefix(line, "      command:") {
			out = append(out, strings.TrimSuffix(schema2ProfileFileFields(t, "      "), "\n"))
		}
	}
	return minimalV2(strings.Join(out, "\n"))
}

func TestDefaults(t *testing.T) {
	path := workflowWithContent(t, minimal(""))
	cfg, err := config.Load(path)
	require.NoError(t, err)

	assert.Equal(t, "linear", cfg.Tracker.Kind)
	assert.Equal(t, "https://api.linear.app/graphql", cfg.Tracker.Endpoint)
	assert.Equal(t, []string{"Todo", "In Progress"}, cfg.Tracker.ActiveStates)
	assert.Equal(t, []string{"Closed", "Cancelled", "Canceled", "Duplicate", "Done"}, cfg.Tracker.TerminalStates)
	assert.Equal(t, 30000, cfg.Polling.IntervalMs)
	assert.Equal(t, 10, cfg.Agent.MaxConcurrentAgents)
	assert.Equal(t, 20, cfg.Agent.MaxTurns)
	assert.Equal(t, 300000, cfg.Agent.MaxRetryBackoffMs)
	assert.Equal(t, 5, cfg.Agent.MaxRetries)
	assert.Equal(t, "", cfg.Tracker.FailedState)
	assert.True(t, cfg.Tracker.Outbox, "tracker.outbox must default true")
	assert.Equal(t, "claude", cfg.Agent.Command)
	assert.Equal(t, 3600000, cfg.Agent.TurnTimeoutMs)
	assert.Equal(t, 30000, cfg.Agent.ReadTimeoutMs)
	assert.Equal(t, 300000, cfg.Agent.StallTimeoutMs)
	assert.Equal(t, 60000, cfg.Hooks.TimeoutMs)
	require.NotNil(t, cfg.Server.Port)
	assert.Equal(t, config.DefaultServerPort, *cfg.Server.Port)
}

// server.port has three meaningful spellings: absent (fixed default, stable
// dashboard URL across restarts and reloads), explicit 0 (OS picks — the
// multi-daemon knob), and an explicit port. Absent used to mean ephemeral,
// which re-rolled the port on every config reload; this pins the new contract.
func TestServerPortDefaultAndExplicitZero(t *testing.T) {
	t.Run("absent defaults to fixed port", func(t *testing.T) {
		cfg, err := config.Load(workflowWithContent(t, minimal("")))
		require.NoError(t, err)
		require.NotNil(t, cfg.Server.Port)
		assert.Equal(t, config.DefaultServerPort, *cfg.Server.Port)
	})
	t.Run("explicit zero is preserved", func(t *testing.T) {
		cfg, err := config.Load(workflowWithContent(t, minimal("server:\n  port: 0\n")))
		require.NoError(t, err)
		require.NotNil(t, cfg.Server.Port)
		assert.Zero(t, *cfg.Server.Port)
	})
	t.Run("explicit port is preserved", func(t *testing.T) {
		cfg, err := config.Load(workflowWithContent(t, minimal("server:\n  port: 9321\n")))
		require.NoError(t, err)
		require.NotNil(t, cfg.Server.Port)
		assert.Equal(t, 9321, *cfg.Server.Port)
	})
	t.Run("negative port falls back to the default", func(t *testing.T) {
		cfg, err := config.Load(workflowWithContent(t, minimal("server:\n  port: -1\n")))
		require.NoError(t, err)
		require.NotNil(t, cfg.Server.Port)
		assert.Equal(t, config.DefaultServerPort, *cfg.Server.Port)
	})
}

// TestServerAllowUnauthenticatedAlias covers the #48 rename:
// server.allow_unauthenticated is the preferred key, the legacy
// server.allow_unauthenticated_lan still parses as a deprecated alias, and
// the new key wins when both are present in the same WORKFLOW.md.
func TestServerAllowUnauthenticatedAlias(t *testing.T) {
	t.Run("new key parses", func(t *testing.T) {
		cfg, err := config.Load(workflowWithContent(t, minimal("server:\n  allow_unauthenticated: true\n")))
		require.NoError(t, err)
		assert.True(t, cfg.Server.AllowUnauthenticatedLAN)
	})
	t.Run("new key absent defaults false", func(t *testing.T) {
		cfg, err := config.Load(workflowWithContent(t, minimal("")))
		require.NoError(t, err)
		assert.False(t, cfg.Server.AllowUnauthenticatedLAN)
	})
	t.Run("old key still parses and warns", func(t *testing.T) {
		cfg, err := config.Load(workflowWithContent(t, minimal("server:\n  allow_unauthenticated_lan: true\n")))
		require.NoError(t, err)
		assert.True(t, cfg.Server.AllowUnauthenticatedLAN)
	})
	t.Run("new key wins on conflict", func(t *testing.T) {
		cfg, err := config.Load(workflowWithContent(t, minimal(
			"server:\n  allow_unauthenticated: false\n  allow_unauthenticated_lan: true\n")))
		require.NoError(t, err)
		assert.False(t, cfg.Server.AllowUnauthenticatedLAN)

		cfg2, err := config.Load(workflowWithContent(t, minimal(
			"server:\n  allow_unauthenticated: true\n  allow_unauthenticated_lan: false\n")))
		require.NoError(t, err)
		assert.True(t, cfg2.Server.AllowUnauthenticatedLAN)
	})
}

func TestTrackerKindRequired(t *testing.T) {
	content := "---\nitervox_schema_version: 2\ntracker:\n  api_key: key\n  project_slug: slug\n---\n\nPrompt.\n"
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err) // Load no longer validates
	err = config.ValidateDispatch(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tracker.kind")
}

func TestEnvVarResolution(t *testing.T) {
	t.Setenv("TEST_API_KEY", "resolved-key")
	content := "---\ntracker:\n  kind: linear\n  api_key: $TEST_API_KEY\n  project_slug: proj\n---\n\nPrompt.\n"
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, "resolved-key", cfg.Tracker.APIKey)
}

func TestEnvVarMissingBecomesEmpty(t *testing.T) {
	_ = os.Unsetenv("NONEXISTENT_VAR_XYZ")
	content := "---\ntracker:\n  kind: linear\n  api_key: $NONEXISTENT_VAR_XYZ\n  project_slug: proj\n---\n\nPrompt.\n"
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, "", cfg.Tracker.APIKey)
}

func TestTildeExpansionOnWorkspaceRoot(t *testing.T) {
	content := "---\ntracker:\n  kind: linear\n  api_key: key\n  project_slug: proj\nworkspace:\n  root: ~/itervox_ws\n---\n\nPrompt.\n"
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	home, _ := os.UserHomeDir()
	assert.Equal(t, home+"/itervox_ws", cfg.Workspace.Root)
}

func TestAgentCommandNotTildeExpanded(t *testing.T) {
	content := "---\ntracker:\n  kind: linear\n  api_key: key\n  project_slug: proj\nagent:\n  command: ~/bin/claude\n---\n\nPrompt.\n"
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	// agent.command is NOT path-expanded per spec
	assert.Equal(t, "~/bin/claude", cfg.Agent.Command)
}

func TestMaxRetriesExplicit(t *testing.T) {
	content := minimal("agent:\n  max_retries: 10\n")
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, 10, cfg.Agent.MaxRetries)
}

func TestMaxRetriesZeroMeansUnlimited(t *testing.T) {
	content := minimal("agent:\n  max_retries: 0\n")
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, 0, cfg.Agent.MaxRetries)
}

func TestDefaultAutomationQueueLength(t *testing.T) {
	path := workflowWithContent(t, minimal(""))
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, 100, cfg.Agent.MaxAutomationQueueLength)
}

func TestZeroAutomationQueueLengthFallsBackToDefault(t *testing.T) {
	content := minimal("agent:\n  max_automation_queue_length: 0\n")
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, 100, cfg.Agent.MaxAutomationQueueLength)
}

func TestNegativeAutomationQueueLengthFallsBackToDefault(t *testing.T) {
	content := minimal("agent:\n  max_automation_queue_length: -1\n")
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, 100, cfg.Agent.MaxAutomationQueueLength)
}

func TestPositiveAutomationQueueLengthIsHonored(t *testing.T) {
	content := minimal("agent:\n  max_automation_queue_length: 25\n")
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, 25, cfg.Agent.MaxAutomationQueueLength)
}

func TestFailedStateExplicit(t *testing.T) {
	content := "---\ntracker:\n  kind: linear\n  api_key: test-key\n  project_slug: my-project\n  failed_state: \"Backlog\"\n---\n\nPrompt.\n"
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, "Backlog", cfg.Tracker.FailedState)
}

// TestTrackerOutboxExplicitFalse pins the kill switch: `tracker.outbox: false`
// must round-trip to false (default is true — see TestDefaults).
func TestTrackerOutboxExplicitFalse(t *testing.T) {
	content := minimal("  outbox: false\n")
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.False(t, cfg.Tracker.Outbox)
}

// TestTrackerOutboxExplicitTrue pins the explicit-true spelling round-trips
// (not just the absent-key default from TestDefaults).
func TestTrackerOutboxExplicitTrue(t *testing.T) {
	content := minimal("  outbox: true\n")
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.True(t, cfg.Tracker.Outbox)
}

func TestMaxConcurrentAgentsByStateNormalized(t *testing.T) {
	content := minimal("agent:\n  max_concurrent_agents_by_state:\n    Todo: 3\n    IN PROGRESS: 2\n")
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, 3, cfg.Agent.MaxConcurrentAgentsByState["todo"])
	assert.Equal(t, 2, cfg.Agent.MaxConcurrentAgentsByState["in progress"])
	_, hasTodo := cfg.Agent.MaxConcurrentAgentsByState["Todo"]
	assert.False(t, hasTodo, "original-case key should not be present")
}

func TestMaxConcurrentAgentsByStateInvalidIgnored(t *testing.T) {
	content := minimal("agent:\n  max_concurrent_agents_by_state:\n    todo: -1\n    inprog: 0\n")
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Empty(t, cfg.Agent.MaxConcurrentAgentsByState)
}

func TestHooksTimeoutNonPositiveFallsBackToDefault(t *testing.T) {
	content := minimal("hooks:\n  timeout_ms: 0\n")
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, 60000, cfg.Hooks.TimeoutMs)
}

// F3 — hooks.after_run_required roundtrip: explicit true parses; the field
// defaults false so existing configs keep best-effort hook semantics.
func TestAfterRunRequiredExplicit(t *testing.T) {
	content := minimal("hooks:\n  after_run: make test\n  after_run_required: true\n")
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.True(t, cfg.Hooks.AfterRunRequired)
}

func TestAfterRunRequiredDefaultsFalse(t *testing.T) {
	path := workflowWithContent(t, minimal(""))
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.False(t, cfg.Hooks.AfterRunRequired)
}

// SRV-1 — agent.allow_unchecked_merge roundtrip: explicit true parses; the
// field defaults false so the merge_pr unarmed-gate refusal is the default
// behavior for existing configs.
func TestAllowUncheckedMergeExplicit(t *testing.T) {
	content := minimal("agent:\n  allow_unchecked_merge: true\n")
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.True(t, cfg.Agent.AllowUncheckedMerge)
}

func TestAllowUncheckedMergeDefaultsFalse(t *testing.T) {
	path := workflowWithContent(t, minimal(""))
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.False(t, cfg.Agent.AllowUncheckedMerge)
}

func TestWorkspaceRootDefault(t *testing.T) {
	path := workflowWithContent(t, minimal(""))
	cfg, err := config.Load(path)
	require.NoError(t, err)
	// Primary default: ~/.itervox/workspaces
	// Fallback (no home dir): <os.TempDir()>/itervox_workspaces
	// Both paths end in "workspaces".
	assert.Contains(t, cfg.Workspace.Root, "workspaces")
}

func TestPromptTemplate(t *testing.T) {
	path := workflowWithContent(t, minimal(""))
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, "Prompt.", cfg.PromptTemplate)
}

func TestAgentProfileBackendField(t *testing.T) {
	content := minimal(`agent:
  profiles:
    codex-fast:
      command: codex --model o4-mini
      backend: codex
    inferred:
      command: codex --model o3
`)
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.NotNil(t, cfg.Agent.Profiles)
	assert.Equal(t, "codex", cfg.Agent.Profiles["codex-fast"].Backend)
	assert.Equal(t, "", cfg.Agent.Profiles["inferred"].Backend)
}

func TestWorkflowSchemaVersionParsed(t *testing.T) {
	content := "---\nitervox_schema_version: 2\ntracker:\n  kind: linear\n  api_key: test-key\n  project_slug: my-project\n---\n\nPrompt.\n"
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)

	assert.Equal(t, 2, cfg.SchemaVersion)
}

func TestWorkflowSchemaMissingFailsDispatchValidation(t *testing.T) {
	path := workflowWithContent(t, minimal(""))
	cfg, err := config.Load(path)
	require.NoError(t, err)

	err = config.ValidateDispatch(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "itervox_schema_version")
	assert.Contains(t, err.Error(), "itervox init --update --workflow")
}

func TestMissingWorkflowSchemaMessageExact(t *testing.T) {
	path := workflowWithContent(t, minimal(""))
	cfg, err := config.Load(path)
	require.NoError(t, err)

	err = config.ValidateDispatch(cfg)
	require.Error(t, err)
	assert.EqualError(t, err, "WORKFLOW.md is missing itervox_schema_version.\nRun:\n  itervox init --update --workflow "+path+"\n\nThis creates .itervox/agents/<profile>/SOUL.md and INSTRUCTIONS.md,\nrewrites profile references, and keeps a WORKFLOW.md.bak backup.")
}

func TestWorkflowSchemaStaleFailsDispatchValidation(t *testing.T) {
	content := "---\nitervox_schema_version: 1\ntracker:\n  kind: linear\n  api_key: test-key\n  project_slug: my-project\n---\n\nPrompt.\n"
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)

	err = config.ValidateDispatch(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "legacy inline profile-prompt schema")
	assert.Contains(t, err.Error(), "itervox init --update --workflow")
}

func TestLegacyWorkflowSchemaMessageExact(t *testing.T) {
	content := "---\nitervox_schema_version: 1\ntracker:\n  kind: linear\n  api_key: test-key\n  project_slug: my-project\n---\n\nPrompt.\n"
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)

	err = config.ValidateDispatch(cfg)
	require.Error(t, err)
	assert.EqualError(t, err, "WORKFLOW.md uses the legacy inline profile-prompt schema.\nRun:\n  itervox init --update --workflow "+path+"\n\nThis creates .itervox/agents/<profile>/SOUL.md and INSTRUCTIONS.md,\nrewrites profile references, and keeps a WORKFLOW.md.bak backup.")
}

func TestSchema2ProfileLoadsSoulAndInstructionsFilesRelativeToWorkflow(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".itervox", "agents", "implementer"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".itervox", "agents", "implementer", "SOUL.md"), []byte("Soul {{ issue.identifier }}"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".itervox", "agents", "implementer", "INSTRUCTIONS.md"), []byte("Instructions {{ issue.title }}"), 0o644))
	path := filepath.Join(dir, "WORKFLOW.md")
	content := `---
itervox_schema_version: 2
tracker:
  kind: linear
  api_key: test-key
  project_slug: my-project
agent:
  profiles:
    implementer:
      command: claude
      soul_file: .itervox/agents/implementer/SOUL.md
      instructions_file: .itervox/agents/implementer/INSTRUCTIONS.md
---

Prompt.
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.Contains(t, cfg.Agent.Profiles, "implementer")
	profile := cfg.Agent.Profiles["implementer"]
	assert.Equal(t, ".itervox/agents/implementer/SOUL.md", profile.SoulFile)
	assert.Equal(t, ".itervox/agents/implementer/INSTRUCTIONS.md", profile.InstructionsFile)
	assert.Equal(t, "Soul {{ issue.identifier }}", profile.Soul)
	assert.Equal(t, "Instructions {{ issue.title }}", profile.Instructions)
}

func TestSchema2ProfileRejectsMissingInstructionFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".itervox", "agents", "implementer"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".itervox", "agents", "implementer", "SOUL.md"), []byte("Soul"), 0o644))
	path := filepath.Join(dir, "WORKFLOW.md")
	content := `---
itervox_schema_version: 2
tracker:
  kind: linear
  api_key: test-key
  project_slug: my-project
agent:
  profiles:
    implementer:
      command: claude
      soul_file: .itervox/agents/implementer/SOUL.md
      instructions_file: .itervox/agents/implementer/INSTRUCTIONS.md
---

Prompt.
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	_, err := config.Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), ".itervox/agents/implementer/INSTRUCTIONS.md")
}

func TestSchema2ProfileValidatesFilesBeforeCommand(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	content := `---
itervox_schema_version: 2
tracker:
  kind: linear
  api_key: test-key
  project_slug: my-project
agent:
  profiles:
    implementer:
      soul_file: .itervox/agents/implementer/SOUL.md
      instructions_file: .itervox/agents/implementer/INSTRUCTIONS.md
---

Prompt.
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	_, err := config.Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), ".itervox/agents/implementer/SOUL.md")
}

func TestSchema2ProfileRejectsInlinePrompt(t *testing.T) {
	content := `---
itervox_schema_version: 2
tracker:
  kind: linear
  api_key: test-key
  project_slug: my-project
agent:
  profiles:
    implementer:
      command: claude
      prompt: inline legacy prompt
---

Prompt.
`
	path := workflowWithContent(t, content)

	_, err := config.Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "legacy inline profile-prompt schema")
	assert.Contains(t, err.Error(), "itervox init --update --workflow")
}

func TestSchema2InlineProfilePromptMessageExact(t *testing.T) {
	content := `---
itervox_schema_version: 2
tracker:
  kind: linear
  api_key: test-key
  project_slug: my-project
agent:
  profiles:
    implementer:
      command: claude
      prompt: inline legacy prompt
---

Prompt.
`
	path := workflowWithContent(t, content)

	_, err := config.Load(path)
	require.Error(t, err)
	assert.EqualError(t, err, "WORKFLOW.md uses the legacy inline profile-prompt schema.\nRun:\n  itervox init --update --workflow "+path+"\n\nThis creates .itervox/agents/<profile>/SOUL.md and INSTRUCTIONS.md,\nrewrites profile references, and keeps a WORKFLOW.md.bak backup.")
}

func TestSchema2ProfileRejectsInlinePromptWithoutCommand(t *testing.T) {
	content := `---
itervox_schema_version: 2
tracker:
  kind: linear
  api_key: test-key
  project_slug: my-project
agent:
  profiles:
    implementer:
      prompt: inline legacy prompt
---

Prompt.
`
	path := workflowWithContent(t, content)

	_, err := config.Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "legacy inline profile-prompt schema")
}

func TestFutureWorkflowSchemaRejectedBeforeProfileParsing(t *testing.T) {
	content := `---
itervox_schema_version: 99
tracker:
  kind: linear
  api_key: test-key
  project_slug: my-project
agent:
  profiles:
    implementer:
      command: claude
---

Prompt.
`
	path := workflowWithContent(t, content)

	_, err := config.Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported itervox_schema_version 99")
	assert.NotContains(t, err.Error(), "soul_file")
}

func TestWorkflowSchemaErrorPrecedesRemovedAgentMode(t *testing.T) {
	content := `---
itervox_schema_version: 99
tracker:
  kind: linear
  api_key: test-key
  project_slug: my-project
agent:
  agent_mode: teams
---

Prompt.
`
	path := workflowWithContent(t, content)

	_, err := config.Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported itervox_schema_version 99")
	assert.NotContains(t, err.Error(), "agent.agent_mode")
}

func TestMissingWorkflowSchemaPrecedesRemovedAgentMode(t *testing.T) {
	content := `---
tracker:
  kind: linear
  api_key: test-key
  project_slug: my-project
agent:
  agent_mode: teams
---

Prompt.
`
	path := workflowWithContent(t, content)

	cfg, err := config.Load(path)
	require.NoError(t, err)
	err = config.ValidateDispatch(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "itervox_schema_version")
	assert.NotContains(t, err.Error(), "agent.agent_mode")
}

func TestAgentProfileAllowedActionsField(t *testing.T) {
	content := minimal(`agent:
  profiles:
    responder:
      command: claude --model claude-sonnet-4-6
      allowed_actions:
        - comment
        - provide_input
`)
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.NotNil(t, cfg.Agent.Profiles)
	assert.Equal(t, []string{"comment", "provide_input"}, cfg.Agent.Profiles["responder"].AllowedActions)
}

func TestAgentProfileCreateIssueStateField(t *testing.T) {
	content := minimal(`agent:
  profiles:
    triage:
      command: claude --model claude-sonnet-4-6
      allowed_actions:
        - create_issue
      create_issue_state: Todo
`)
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.NotNil(t, cfg.Agent.Profiles)
	assert.Equal(t, []string{"create_issue"}, cfg.Agent.Profiles["triage"].AllowedActions)
	assert.Equal(t, "Todo", cfg.Agent.Profiles["triage"].CreateIssueState)
}

func TestAgentProfileEnabledField(t *testing.T) {
	content := minimal(`agent:
  profiles:
    active:
      command: claude --model claude-sonnet-4-6
    paused:
      command: codex --model gpt-5.3-codex
      enabled: false
`)
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.NotNil(t, cfg.Agent.Profiles)
	assert.True(t, config.ProfileEnabled(cfg.Agent.Profiles["active"]))
	assert.False(t, config.ProfileEnabled(cfg.Agent.Profiles["paused"]))
}

func TestAgentBackendField(t *testing.T) {
	content := minimal(`agent:
  command: run-codex-wrapper
  backend: codex
`)
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, "run-codex-wrapper", cfg.Agent.Command)
	assert.Equal(t, "codex", cfg.Agent.Backend)
}

func TestSSHHostDescriptionsField(t *testing.T) {
	content := minimal(`agent:
  ssh_hosts: ["worker-1","worker-2:2222"]
  ssh_host_descriptions:
    "worker-1": "fast box"
    "worker-2:2222": "gpu box"
`)
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, []string{"worker-1", "worker-2:2222"}, cfg.Agent.SSHHosts)
	assert.Equal(
		t,
		map[string]string{
			"worker-1":      "fast box",
			"worker-2:2222": "gpu box",
		},
		cfg.Agent.SSHHostDescriptions,
	)
}

func TestSSHHostDescriptionsDefaultEmpty(t *testing.T) {
	path := workflowWithContent(t, minimal(""))
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Empty(t, cfg.Agent.SSHHostDescriptions)
}

// TestSSHStrictHostCheckingFields verifies the T-32 follow-up config fields
// parse correctly: both the global default and per-host overrides round-trip
// from WORKFLOW.md to cfg.Agent.SSH*.
func TestSSHStrictHostCheckingFields(t *testing.T) {
	content := minimal(`agent:
  ssh_strict_host_checking: "yes"
  ssh_strict_host_by_host:
    "sandbox.example.com": "no"
    "prod.example.com": "yes"
`)
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, "yes", cfg.Agent.SSHStrictHostChecking)
	assert.Equal(
		t,
		map[string]string{
			"sandbox.example.com": "no",
			"prod.example.com":    "yes",
		},
		cfg.Agent.SSHStrictHostByHost,
	)
}

func TestSSHStrictHostCheckingDefaultsEmpty(t *testing.T) {
	path := workflowWithContent(t, minimal(""))
	cfg, err := config.Load(path)
	require.NoError(t, err)
	// Empty string means "use the agent package default (accept-new TOFU)";
	// startup logic only calls SetSSHStrictHostDefault when this is non-empty.
	assert.Equal(t, "", cfg.Agent.SSHStrictHostChecking)
	assert.Empty(t, cfg.Agent.SSHStrictHostByHost)
}

func TestWorktreeDefaultsFalse(t *testing.T) {
	path := workflowWithContent(t, minimal(""))
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.False(t, cfg.Workspace.Worktree)
}

func TestWorktreeParsedTrue(t *testing.T) {
	content := minimal("workspace:\n  worktree: true\n")
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.True(t, cfg.Workspace.Worktree)
}

func TestWorkspaceCloneURL(t *testing.T) {
	content := "---\ntracker:\n  kind: linear\n  api_key: key\n  project_slug: proj\nworkspace:\n  root: /tmp/ws\n  worktree: true\n  clone_url: git@github.com:org/repo.git\n  base_branch: develop\n---\n\nPrompt.\n"
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, "git@github.com:org/repo.git", cfg.Workspace.CloneURL)
	assert.Equal(t, "develop", cfg.Workspace.BaseBranch)
}

func TestWorkspaceCloneURLDefault(t *testing.T) {
	content := "---\ntracker:\n  kind: linear\n  api_key: key\n  project_slug: proj\nworkspace:\n  root: /tmp/ws\n  worktree: true\n---\n\nPrompt.\n"
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, "", cfg.Workspace.CloneURL)
	assert.Equal(t, "main", cfg.Workspace.BaseBranch)
}

func TestAutomationsParsed(t *testing.T) {
	content := minimal(`automations:
  - id: backlog-review
    enabled: true
    profile: reviewer
    instructions: "Review backlog issues and comment with missing details."
    trigger:
      type: cron
      cron: "0 9 * * 1"
      timezone: "Asia/Jerusalem"
    filter:
      match_mode: any
      states: ["Backlog","Todo"]
      labels_any: ["bug"]
      identifier_regex: "^ENG-"
      limit: 2
  - id: moved-to-backlog
    enabled: true
    profile: pm
    instructions: "Review why the issue returned to backlog."
    trigger:
      type: issue_moved_to_backlog
  - id: qa-state-entry
    enabled: true
    profile: qa
    instructions: "Run QA when the issue enters Ready for QA."
    trigger:
      type: issue_entered_state
      state: "Ready for QA"
  - id: comment-triage
    enabled: true
    profile: reviewer
    instructions: "React to new tracker comments."
    trigger:
      type: tracker_comment_added
  - id: failed-run
    enabled: true
    profile: reviewer
    instructions: "Summarise the failed run and suggest next action."
    trigger:
      type: run_failed
  - id: input-responder
    enabled: true
    profile: input-responder
    instructions: "Answer low-risk blocked-run questions."
    trigger:
      type: input_required
    filter:
      input_context_regex: "continue|branch"
    policy:
      auto_resume: true
`)
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Automations, 6)
	assert.Equal(t, "backlog-review", cfg.Automations[0].ID)
	assert.Equal(t, "cron", cfg.Automations[0].Trigger.Type)
	assert.Equal(t, "0 9 * * 1", cfg.Automations[0].Trigger.Cron)
	assert.Equal(t, "Asia/Jerusalem", cfg.Automations[0].Trigger.Timezone)
	assert.Equal(t, "reviewer", cfg.Automations[0].Profile)
	assert.Equal(t, "any", cfg.Automations[0].Filter.MatchMode)
	assert.Equal(t, []string{"Backlog", "Todo"}, cfg.Automations[0].Filter.States)
	assert.Equal(t, []string{"bug"}, cfg.Automations[0].Filter.LabelsAny)
	assert.Equal(t, "^ENG-", cfg.Automations[0].Filter.IdentifierRegex)
	assert.Equal(t, 2, cfg.Automations[0].Filter.Limit)
	assert.Equal(t, "issue_moved_to_backlog", cfg.Automations[1].Trigger.Type)
	assert.Equal(t, "issue_entered_state", cfg.Automations[2].Trigger.Type)
	assert.Equal(t, "Ready for QA", cfg.Automations[2].Trigger.State)
	assert.Equal(t, "tracker_comment_added", cfg.Automations[3].Trigger.Type)
	assert.Equal(t, "run_failed", cfg.Automations[4].Trigger.Type)
	assert.Equal(t, "input_required", cfg.Automations[5].Trigger.Type)
	assert.Equal(t, "continue|branch", cfg.Automations[5].Filter.InputContextRegex)
	assert.True(t, cfg.Automations[5].Policy.AutoResume)
}

// Gap §5.2 — `auto_switch: true` is the preferred YAML key for rate_limited
// triggers; the parser must accept it and unify into AutoResume=true. Old
// configs using `auto_resume: true` still work (back-compat).
func TestAutomationPolicy_AutoSwitchAlias(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want bool
		err  bool
	}{
		{
			name: "auto_switch true",
			yaml: "policy:\n      auto_switch: true",
			want: true,
		},
		{
			name: "auto_resume true (back-compat)",
			yaml: "policy:\n      auto_resume: true",
			want: true,
		},
		{
			name: "both keys true",
			yaml: "policy:\n      auto_resume: true\n      auto_switch: true",
			want: true,
		},
		{
			name: "neither key set",
			yaml: "policy:\n      auto_resume: false",
			want: false,
			err:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := minimalV2WithProfileFiles(t, `automations:
  - id: rl
    enabled: true
    profile: fallback
    trigger:
      type: rate_limited
    `+tc.yaml+`
      switch_to_profile: codex-coder
agent:
  profiles:
    fallback:
      command: claude
    codex-coder:
      command: codex
`)
			path := workflowWithContent(t, content)
			cfg, err := config.Load(path)
			require.NoError(t, err)
			err = config.ValidateDispatch(cfg)
			if tc.err {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "policy.auto_switch")
				return
			}
			require.NoError(t, err)
			require.Len(t, cfg.Automations, 1)
			assert.Equal(t, tc.want, cfg.Automations[0].Policy.AutoResume)
			assert.Equal(t, "codex-coder", cfg.Automations[0].Policy.SwitchToProfile)
		})
	}
}

func TestAutomationBlockersResolvedValidatesBacklogToTodoPolicy(t *testing.T) {
	content := minimalV2WithProfileFiles(t, `agent:
  profiles:
    pm:
      command: claude
      allowed_actions: [comment, move_state]
automations:
  - id: unblock-backlog-to-todo
    enabled: true
    profile: pm
    trigger:
      type: blockers_resolved
    filter:
      states_any: ["backlog", "Backlog"]
    policy:
      move_to_state: "Todo"
`)
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.NoError(t, config.ValidateDispatch(cfg))
	require.Len(t, cfg.Automations, 1)
	assert.Equal(t, "blockers_resolved", cfg.Automations[0].Trigger.Type)
	assert.Equal(t, []string{"backlog", "Backlog"}, cfg.Automations[0].Filter.States)
	assert.Equal(t, "Todo", cfg.Automations[0].Policy.MoveToState)
}

func TestAutomationBlockersResolvedMoveToStateRequiresMoveStatePermission(t *testing.T) {
	content := minimalV2WithProfileFiles(t, `agent:
  profiles:
    pm:
      command: claude
      allowed_actions: [comment]
automations:
  - id: unblock-backlog-to-todo
    enabled: true
    profile: pm
    trigger:
      type: blockers_resolved
    filter:
      states_any: ["Backlog"]
    policy:
      move_to_state: "Todo"
`)
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	err = config.ValidateDispatch(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "move_state")
}

func TestAutomationMoveToStateOnlyAllowedOnBlockersResolved(t *testing.T) {
	content := minimalV2WithProfileFiles(t, `agent:
  profiles:
    pm:
      command: claude
      allowed_actions: [comment, move_state]
automations:
  - id: wrong-trigger
    enabled: true
    profile: pm
    trigger:
      type: cron
      cron: "0 9 * * 1"
    policy:
      move_to_state: "Todo"
`)
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	err = config.ValidateDispatch(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "policy.move_to_state")
}

func TestV020AutomationBoundaryExamplesValidate(t *testing.T) {
	content := minimalV2WithProfileFiles(t, `agent:
  max_concurrent_agents: 3
  profiles:
    implementer-codex:
      command: codex
      backend: codex
    input-responder:
      command: claude
      allowed_actions: [comment, provide_input]
    unblock-manager:
      command: claude
      allowed_actions: [comment]
    readiness-manager:
      command: claude
      allowed_actions: [comment]
    planner-claude:
      command: claude
      allowed_actions: [comment]
    planner-codex:
      command: codex
      backend: codex
      allowed_actions: [comment]
    debate-moderator:
      command: claude
      allowed_actions: [comment]
    release-captain:
      command: claude
      allowed_actions: [comment]
    failure-analyst:
      command: claude
      allowed_actions: [comment]
    capability-curator:
      command: codex
      backend: codex
      allowed_actions: [comment]
    qa-browser:
      command: claude
      allowed_actions: [comment]
automations:
  - id: rate-limit-fallback
    enabled: true
    profile: implementer-codex
    trigger:
      type: rate_limited
    policy:
      auto_switch: true
      switch_to_profile: implementer-codex
      switch_to_backend: codex
      cooldown_minutes: 30
  - id: input-responder
    enabled: true
    profile: input-responder
    trigger:
      type: input_required
    filter:
      input_context_regex: "continue|branch|which file|test command"
      max_age_minutes: 5
      match_mode: all
    policy:
      auto_resume: true
  - id: unblock-manager
    enabled: false
    profile: unblock-manager
    trigger:
      type: cron
      cron: "*/30 * * * *"
      timezone: "UTC"
    filter:
      states: ["Backlog"]
      labels_any: ["blocked"]
      limit: 10
  - id: failure-summary
    enabled: true
    profile: failure-analyst
    trigger:
      type: run_failed
  - id: plan-required-gate
    enabled: true
    profile: readiness-manager
    trigger:
      type: issue_entered_state
      state: "Todo"
    filter:
      labels_any: ["needs-plan"]
  - id: planner-pair-claude
    enabled: true
    profile: planner-claude
    trigger:
      type: tracker_comment_added
    filter:
      labels_any: ["planning"]
  - id: planner-pair-codex
    enabled: true
    profile: planner-codex
    trigger:
      type: tracker_comment_added
    filter:
      labels_any: ["planning"]
  - id: debate-moderator
    enabled: true
    profile: debate-moderator
    trigger:
      type: tracker_comment_added
    filter:
      labels_any: ["planning"]
  - id: evaluator-optimizer
    enabled: true
    profile: qa-browser
    trigger:
      type: issue_entered_state
      state: "Ready for QA"
    filter:
      labels_any: ["ui"]
  - id: release-captain
    enabled: true
    profile: release-captain
    trigger:
      type: cron
      cron: "0 10 * * 1-5"
      timezone: "UTC"
    filter:
      states: ["Ready for Release"]
      labels_any: ["release"]
      limit: 10
  - id: skills-hygiene
    enabled: true
    profile: capability-curator
    trigger:
      type: cron
      cron: "0 11 * * 1"
      timezone: "UTC"
    filter:
      states: ["Backlog"]
      labels_any: ["skills-hygiene"]
      limit: 5
`)
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.NoError(t, config.ValidateDispatch(cfg))
	require.Len(t, cfg.Automations, 11)
	assert.Equal(t, "implementer-codex", cfg.Automations[0].Policy.SwitchToProfile)
	assert.Equal(t, "codex", cfg.Automations[0].Policy.SwitchToBackend)
	assert.Equal(t, 5, cfg.Automations[1].Filter.MaxAgeMinutes)
	assert.Equal(t, "cron", cfg.Automations[2].Trigger.Type)
	assert.Equal(t, "run_failed", cfg.Automations[3].Trigger.Type)
	assert.Equal(t, "issue_entered_state", cfg.Automations[4].Trigger.Type)
	assert.Equal(t, "tracker_comment_added", cfg.Automations[7].Trigger.Type)
	assert.Equal(t, "cron", cfg.Automations[9].Trigger.Type)
	assert.Equal(t, []string{"skills-hygiene"}, cfg.Automations[10].Filter.LabelsAny)
}

func TestRateLimitedAutomationValidatesSwitchToProfileReference(t *testing.T) {
	cases := []struct {
		name     string
		profile  string
		profiles string
		err      string
	}{
		{
			name:    "unknown switch profile",
			profile: "missing",
			profiles: `
    fallback:
      command: claude
`,
			err: `unknown switch_to_profile "missing"`,
		},
		{
			name:    "disabled switch profile",
			profile: "codex-coder",
			profiles: `
    fallback:
      command: claude
    codex-coder:
      command: codex
      enabled: false
`,
			err: `disabled switch_to_profile "codex-coder"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := minimalV2WithProfileFiles(t, `automations:
  - id: rl
    enabled: true
    profile: fallback
    trigger:
      type: rate_limited
    policy:
      auto_switch: true
      switch_to_profile: `+tc.profile+`
agent:
  profiles:`+tc.profiles)
			path := workflowWithContent(t, content)
			cfg, err := config.Load(path)
			require.NoError(t, err)
			err = config.ValidateDispatch(cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.err)
		})
	}
}

func TestLegacySchedulesParsedAsCronAutomations(t *testing.T) {
	content := minimal(`schedules:
  - id: weekday-review
    enabled: true
    cron: "0 9 * * 1-5"
    timezone: "UTC"
    profile: reviewer
    filter:
      states: ["Backlog"]
      labels_any: ["triage"]
`)
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.Len(t, cfg.Automations, 1)
	assert.Equal(t, "weekday-review", cfg.Automations[0].ID)
	assert.Equal(t, "cron", cfg.Automations[0].Trigger.Type)
	assert.Equal(t, "0 9 * * 1-5", cfg.Automations[0].Trigger.Cron)
	assert.Equal(t, "UTC", cfg.Automations[0].Trigger.Timezone)
	assert.Equal(t, "reviewer", cfg.Automations[0].Profile)
	assert.Equal(t, []string{"Backlog"}, cfg.Automations[0].Filter.States)
	assert.Equal(t, []string{"triage"}, cfg.Automations[0].Filter.LabelsAny)
}

func TestDependencyAuditRefreshDefaults(t *testing.T) {
	path := workflowWithContent(t, minimal(""))
	cfg, err := config.Load(path)
	require.NoError(t, err)

	// 10 minutes, deliberately much larger than polling.interval_ms — an
	// interval shorter than the poll never binds, and the dependency audit is
	// one of the largest tracker-request consumers (issue #42).
	assert.Equal(t, 600000, cfg.Agent.DependencyAuditRefreshIntervalMs)
	assert.Equal(t, 30000, cfg.Agent.DependencyAuditRefreshTimeoutMs)
	assert.Equal(t, 100, cfg.Agent.DependencyAuditRefreshBatchSize)
}

func TestDependencyAuditRefreshOverrides(t *testing.T) {
	path := workflowWithContent(t, minimal(
		"agent:\n"+
			"  dependency_audit_refresh_interval_ms: 5000\n"+
			"  dependency_audit_refresh_timeout_ms: 9000\n"+
			"  dependency_audit_refresh_batch_size: 25\n"))
	cfg, err := config.Load(path)
	require.NoError(t, err)

	assert.Equal(t, 5000, cfg.Agent.DependencyAuditRefreshIntervalMs)
	assert.Equal(t, 9000, cfg.Agent.DependencyAuditRefreshTimeoutMs)
	assert.Equal(t, 25, cfg.Agent.DependencyAuditRefreshBatchSize)
}

// positiveIntField replaces <= 0 with the default. None of these three fields
// has a meaningful "disabled" semantic: an interval of 0 re-fetches every tick,
// a timeout of 0 cancels instantly, a batch size of 0 refreshes nothing.
func TestDependencyAuditRefreshRejectsNonPositive(t *testing.T) {
	path := workflowWithContent(t, minimal(
		"agent:\n"+
			"  dependency_audit_refresh_interval_ms: 0\n"+
			"  dependency_audit_refresh_timeout_ms: -1\n"+
			"  dependency_audit_refresh_batch_size: 0\n"))
	cfg, err := config.Load(path)
	require.NoError(t, err)

	// 10 minutes, deliberately much larger than polling.interval_ms — an
	// interval shorter than the poll never binds, and the dependency audit is
	// one of the largest tracker-request consumers (issue #42).
	assert.Equal(t, 600000, cfg.Agent.DependencyAuditRefreshIntervalMs)
	assert.Equal(t, 30000, cfg.Agent.DependencyAuditRefreshTimeoutMs)
	assert.Equal(t, 100, cfg.Agent.DependencyAuditRefreshBatchSize)
}

func TestDepsAnalyzerJobDefaults(t *testing.T) {
	path := workflowWithContent(t, minimal(""))
	cfg, err := config.Load(path)
	require.NoError(t, err)

	assert.Equal(t, 600000, cfg.Agent.DepsAnalyzerTimeoutMs)
	assert.Equal(t, 75, cfg.Agent.DepsAnalyzerChunkSize)
}

func TestDepsAnalyzerJobOverrides(t *testing.T) {
	path := workflowWithContent(t, minimal(
		"agent:\n"+
			"  deps_analyzer_timeout_ms: 120000\n"+
			"  deps_analyzer_chunk_size: 20\n"))
	cfg, err := config.Load(path)
	require.NoError(t, err)

	assert.Equal(t, 120000, cfg.Agent.DepsAnalyzerTimeoutMs)
	assert.Equal(t, 20, cfg.Agent.DepsAnalyzerChunkSize)
}

// positiveIntField replaces <= 0 with the default. Neither field has a
// meaningful "disabled" semantic: a 0 timeout yields an instantly-expired
// context (every job launches, does nothing, and still looks like it ran), and
// a 0 chunk size would produce no chunks at all.
func TestDepsAnalyzerJobRejectsNonPositive(t *testing.T) {
	path := workflowWithContent(t, minimal(
		"agent:\n"+
			"  deps_analyzer_timeout_ms: 0\n"+
			"  deps_analyzer_chunk_size: -5\n"))
	cfg, err := config.Load(path)
	require.NoError(t, err)

	assert.Equal(t, 600000, cfg.Agent.DepsAnalyzerTimeoutMs)
	assert.Equal(t, 75, cfg.Agent.DepsAnalyzerChunkSize)
}

// TestDependenciesConfigDefaults asserts that an absent `dependencies:`
// section fully defaults: gating on, threshold at
// DefaultDependenciesConfidenceThreshold, staleness at
// DefaultDependenciesStalenessHours.
func TestDependenciesConfigDefaults(t *testing.T) {
	path := workflowWithContent(t, minimal(""))
	cfg, err := config.Load(path)
	require.NoError(t, err)

	assert.True(t, cfg.Dependencies.InferredGating)
	assert.Equal(t, config.DefaultDependenciesConfidenceThreshold, cfg.Dependencies.ConfidenceThreshold)
	assert.Equal(t, config.DefaultDependenciesStalenessHours, cfg.Dependencies.StalenessHours)
}

// TestDependenciesConfigIntConfidenceThreshold pins #50's test-coverage gap:
// an int-typed YAML literal (`confidence_threshold: 1`, no decimal point)
// decodes as a Go `int` via yaml.v3, not `float64`. toFloat's `case int`
// branch must coerce it to the in-range float 1.0 — verified empirically at
// the #50 review but never pinned by a test — and it must NOT trigger the
// out-of-range warning, since 1.0 sits exactly on the closed [0,1] bound.
func TestDependenciesConfigIntConfidenceThreshold(t *testing.T) {
	path := workflowWithContent(t, minimal(
		"dependencies:\n"+
			"  confidence_threshold: 1\n"))

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	cfg, err := config.Load(path)
	require.NoError(t, err)

	assert.Equal(t, 1.0, cfg.Dependencies.ConfidenceThreshold, "int-typed 1 must coerce to float64 1.0")
	assert.NotContains(t, buf.String(), "confidence_threshold out of range",
		"an in-range int-typed threshold must not trigger the out-of-range warning")
}

// TestDependenciesConfigParsed asserts explicit `dependencies:` values,
// including a non-default boolean (inferred_gating: false), round-trip
// unchanged.
func TestDependenciesConfigParsed(t *testing.T) {
	path := workflowWithContent(t, minimal(
		"dependencies:\n"+
			"  inferred_gating: false\n"+
			"  confidence_threshold: 0.85\n"+
			"  staleness_hours: 24\n"))
	cfg, err := config.Load(path)
	require.NoError(t, err)

	assert.False(t, cfg.Dependencies.InferredGating)
	assert.Equal(t, 0.85, cfg.Dependencies.ConfidenceThreshold)
	assert.Equal(t, 24, cfg.Dependencies.StalenessHours)
}

// TestDependenciesConfigClamped asserts out-of-range values fall back to the
// DEFAULT (not the nearest bound): confidence_threshold outside [0,1] falls
// back to DefaultDependenciesConfidenceThreshold, and staleness_hours <= 0
// falls back to DefaultDependenciesStalenessHours.
func TestDependenciesConfigClamped(t *testing.T) {
	path := workflowWithContent(t, minimal(
		"dependencies:\n"+
			"  confidence_threshold: 1.5\n"+
			"  staleness_hours: -5\n"))
	cfg, err := config.Load(path)
	require.NoError(t, err)

	assert.Equal(t, config.DefaultDependenciesConfidenceThreshold, cfg.Dependencies.ConfidenceThreshold)
	assert.Equal(t, config.DefaultDependenciesStalenessHours, cfg.Dependencies.StalenessHours)
}

// TestDependenciesOrderingDefaults asserts that when `dependencies:` is
// absent entirely, Ordering defaults to "critical_path" and
// EscalateBlockedAfterHours defaults to 48.
func TestDependenciesOrderingDefaults(t *testing.T) {
	path := workflowWithContent(t, minimal(""))
	cfg, err := config.Load(path)
	require.NoError(t, err)

	assert.Equal(t, config.DependenciesOrderingCriticalPath, cfg.Dependencies.Ordering)
	assert.Equal(t, config.DefaultDependenciesOrdering, cfg.Dependencies.Ordering)
	assert.Equal(t, config.DefaultDependenciesEscalateHours, cfg.Dependencies.EscalateBlockedAfterHours)
}

// TestDependenciesOrderingParsed asserts explicit `dependencies.ordering:
// simple` is respected, and that an explicit
// `escalate_blocked_after_hours: 0` is preserved as the meaningful
// "disabled" value rather than being replaced by the default (the exact
// footgun a positiveIntField-style parse would introduce).
func TestDependenciesOrderingParsed(t *testing.T) {
	path := workflowWithContent(t, minimal(
		"dependencies:\n"+
			"  ordering: simple\n"+
			"  escalate_blocked_after_hours: 0\n"))
	cfg, err := config.Load(path)
	require.NoError(t, err)

	assert.Equal(t, config.DependenciesOrderingSimple, cfg.Dependencies.Ordering)
	assert.Equal(t, 0, cfg.Dependencies.EscalateBlockedAfterHours)
}

// TestReviewerProfilesAndQuorumParsed asserts the multi-reviewer fan-out
// fields round-trip, and that review_quorum defaults to the strictest rule
// when absent (adding reviewers must never weaken the gate).
func TestReviewerProfilesAndQuorumParsed(t *testing.T) {
	path := workflowWithContent(t, minimal(
		"agent:\n"+
			"  reviewer_profiles:\n"+
			"    - security\n"+
			"    - correctness\n"+
			"  review_quorum: majority\n"))
	cfg, err := config.Load(path)
	require.NoError(t, err)

	assert.Equal(t, []string{"security", "correctness"}, cfg.Agent.ReviewerProfiles)
	assert.Equal(t, config.ReviewQuorumMajority, cfg.Agent.ReviewQuorum)
}

func TestReviewQuorumDefaultsToAnyBlock(t *testing.T) {
	cfg, err := config.Load(workflowWithContent(t, minimal("")))
	require.NoError(t, err)
	assert.Equal(t, config.ReviewQuorumAnyBlock, cfg.Agent.ReviewQuorum)
	assert.Equal(t, config.DefaultReviewQuorum, cfg.Agent.ReviewQuorum)
	assert.Empty(t, cfg.Agent.ReviewerProfiles, "absent reviewer_profiles must stay empty so ReviewerProfileChain falls back")
}

func TestReviewQuorumInvalidFallsBack(t *testing.T) {
	path := workflowWithContent(t, minimal(
		"agent:\n"+
			"  review_quorum: sometimes\n"))
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, config.DefaultReviewQuorum, cfg.Agent.ReviewQuorum,
		"an unrecognized quorum must fall back to the strictest rule, not the loosest")
}

// TestDependenciesOrderingStrictParsed asserts the `critical_path_strict`
// value survives the loader as itself rather than being swallowed by the
// unrecognized-value fallback. This is the test that would catch a typo in
// the YAML key or a missed arm in the validation switch — both of which fail
// silently by resetting the field to "critical_path", which is exactly the
// mode the operator was trying to opt out of.
func TestDependenciesOrderingStrictParsed(t *testing.T) {
	path := workflowWithContent(t, minimal(
		"dependencies:\n"+
			"  ordering: critical_path_strict\n"))
	cfg, err := config.Load(path)
	require.NoError(t, err)

	assert.Equal(t, config.DependenciesOrderingCriticalPathStrict, cfg.Dependencies.Ordering)
	assert.NotEqual(t, config.DefaultDependenciesOrdering, cfg.Dependencies.Ordering,
		"critical_path_strict must not silently fall back to the default")
}

// TestDependenciesOrderingInvalidFallsBack asserts an unknown ordering value
// falls back to the default "critical_path", and a negative
// escalate_blocked_after_hours falls back to the default 48 (unlike an
// explicit 0, which is meaningful and preserved).
func TestDependenciesOrderingInvalidFallsBack(t *testing.T) {
	path := workflowWithContent(t, minimal(
		"dependencies:\n"+
			"  ordering: foo\n"+
			"  escalate_blocked_after_hours: -5\n"))
	cfg, err := config.Load(path)
	require.NoError(t, err)

	assert.Equal(t, config.DependenciesOrderingCriticalPath, cfg.Dependencies.Ordering)
	assert.Equal(t, config.DefaultDependenciesEscalateHours, cfg.Dependencies.EscalateBlockedAfterHours)
}

// TestPreferHighOutdegreeAlias asserts the deprecated
// agent.sort.prefer_high_outdegree knob aliases to dependencies.ordering:
// critical_path when dependencies.ordering is absent from the YAML, and
// that an explicit dependencies.ordering (including explicit "simple")
// always wins over the alias.
func TestPreferHighOutdegreeAlias(t *testing.T) {
	t.Run("alias applies when ordering absent", func(t *testing.T) {
		path := workflowWithContent(t, minimal(
			"agent:\n"+
				"  sort:\n"+
				"    prefer_high_outdegree: true\n"))
		cfg, err := config.Load(path)
		require.NoError(t, err)

		assert.True(t, cfg.Agent.PreferHighOutdegreeSort)
		assert.Equal(t, config.DependenciesOrderingCriticalPath, cfg.Dependencies.Ordering)
	})

	t.Run("explicit ordering wins over alias", func(t *testing.T) {
		path := workflowWithContent(t, minimal(
			"agent:\n"+
				"  sort:\n"+
				"    prefer_high_outdegree: true\n"+
				"dependencies:\n"+
				"  ordering: simple\n"))
		cfg, err := config.Load(path)
		require.NoError(t, err)

		assert.True(t, cfg.Agent.PreferHighOutdegreeSort)
		assert.Equal(t, config.DependenciesOrderingSimple, cfg.Dependencies.Ordering)
	})
}

// TestDependenciesAutoAnalyzeDefaults asserts that when `dependencies:` is
// absent entirely, AutoAnalyze defaults to true, AutoAnalyzeMinIntervalMinutes
// defaults to 60, and AutoAnalyzeDebounceMinutes defaults to 5.
func TestDependenciesAutoAnalyzeDefaults(t *testing.T) {
	path := workflowWithContent(t, minimal(""))
	cfg, err := config.Load(path)
	require.NoError(t, err)

	assert.True(t, cfg.Dependencies.AutoAnalyze)
	assert.Equal(t, config.DefaultDependenciesAutoAnalyzeMinIntervalMinutes, cfg.Dependencies.AutoAnalyzeMinIntervalMinutes)
	assert.Equal(t, config.DefaultDependenciesAutoAnalyzeDebounceMinutes, cfg.Dependencies.AutoAnalyzeDebounceMinutes)
}

// TestDependenciesAutoAnalyzeParsed asserts that explicit
// `dependencies.auto_analyze` values are respected, along with explicit
// interval values. It also verifies that <=0 values fall back to defaults
// (positiveIntField behavior: there is no meaningful zero for analyzer timing).
func TestDependenciesAutoAnalyzeParsed(t *testing.T) {
	t.Run("auto_analyze false respected", func(t *testing.T) {
		path := workflowWithContent(t, minimal(
			"dependencies:\n"+
				"  auto_analyze: false\n"))
		cfg, err := config.Load(path)
		require.NoError(t, err)

		assert.False(t, cfg.Dependencies.AutoAnalyze)
	})

	t.Run("explicit intervals respected", func(t *testing.T) {
		path := workflowWithContent(t, minimal(
			"dependencies:\n"+
				"  auto_analyze_min_interval_minutes: 90\n"+
				"  auto_analyze_debounce_minutes: 10\n"))
		cfg, err := config.Load(path)
		require.NoError(t, err)

		assert.Equal(t, 90, cfg.Dependencies.AutoAnalyzeMinIntervalMinutes)
		assert.Equal(t, 10, cfg.Dependencies.AutoAnalyzeDebounceMinutes)
	})

	t.Run("zero and negative intervals fall back to defaults", func(t *testing.T) {
		path := workflowWithContent(t, minimal(
			"dependencies:\n"+
				"  auto_analyze_min_interval_minutes: 0\n"+
				"  auto_analyze_debounce_minutes: -3\n"))
		cfg, err := config.Load(path)
		require.NoError(t, err)

		assert.Equal(t, config.DefaultDependenciesAutoAnalyzeMinIntervalMinutes, cfg.Dependencies.AutoAnalyzeMinIntervalMinutes)
		assert.Equal(t, config.DefaultDependenciesAutoAnalyzeDebounceMinutes, cfg.Dependencies.AutoAnalyzeDebounceMinutes)
	})
}
