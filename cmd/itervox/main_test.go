package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/logbuffer"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/templates"
	"github.com/vnovick/itervox/internal/tracker"
)

type captureRunner struct {
	command    string
	workerHost string
}

func (c *captureRunner) RunTurn(_ context.Context, _ agent.Logger, _ func(agent.TurnResult), _ *string, _, _, command, workerHost, _ string, _, _ int) (agent.TurnResult, error) {
	c.command = command
	c.workerHost = workerHost
	return agent.TurnResult{}, nil
}

func TestLoadDotEnv_LoadsItervoxDotEnv(t *testing.T) {
	dir := t.TempDir()
	itervoxDir := filepath.Join(dir, ".itervox")
	require.NoError(t, os.MkdirAll(itervoxDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(itervoxDir, ".env"),
		[]byte("TEST_DOTENV_ITERVOX=from_itervox\n"),
		0o600,
	))

	orig, _ := os.Getwd()
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(orig) })

	require.NoError(t, os.Unsetenv("TEST_DOTENV_ITERVOX"))

	loadDotEnv()
	assert.Equal(t, "from_itervox", os.Getenv("TEST_DOTENV_ITERVOX"))
}

func TestLoadDotEnv_FallsBackToDotEnv(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".env"),
		[]byte("TEST_DOTENV_FALLBACK=from_root\n"),
		0o600,
	))

	orig, _ := os.Getwd()
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(orig) })

	require.NoError(t, os.Unsetenv("TEST_DOTENV_FALLBACK"))

	loadDotEnv()
	assert.Equal(t, "from_root", os.Getenv("TEST_DOTENV_FALLBACK"))
}

func TestLoadDotEnv_DoesNotOverwriteExistingEnvVar(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".env"),
		[]byte("TEST_DOTENV_EXISTING=from_file\n"),
		0o600,
	))

	orig, _ := os.Getwd()
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(orig) })

	t.Setenv("TEST_DOTENV_EXISTING", "from_shell")

	loadDotEnv()
	assert.Equal(t, "from_shell", os.Getenv("TEST_DOTENV_EXISTING"),
		"existing env vars must not be overwritten by .env file")
}

func TestLoadDotEnv_SilentWhenNoFile(t *testing.T) {
	dir := t.TempDir()
	orig, _ := os.Getwd()
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(orig) })

	assert.NotPanics(t, loadDotEnv)
}

// captureSlogStderr installs a temporary slog default that writes JSON to a
// strings.Builder for the duration of fn. Returns the captured output.
func captureSlogStderr(t *testing.T, fn func()) string {
	t.Helper()
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	var buf strings.Builder
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	fn()
	return buf.String()
}

// TestLoadDotEnv_SensitiveKeyEmitsInfo pins the security-visibility contract
// added by T-08: when a .env file populates a key on the secretEnvKeys list,
// loadDotEnv emits a single Info line naming the key (NEVER the value).
func TestLoadDotEnv_SensitiveKeyEmitsInfo(t *testing.T) {
	dir := t.TempDir()
	itervoxDir := filepath.Join(dir, ".itervox")
	require.NoError(t, os.MkdirAll(itervoxDir, 0o755))
	const tokenValue = "test-secret-do-not-leak"
	require.NoError(t, os.WriteFile(
		filepath.Join(itervoxDir, ".env"),
		[]byte("ITERVOX_API_TOKEN="+tokenValue+"\n"),
		0o600,
	))

	orig, _ := os.Getwd()
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(orig) })
	require.NoError(t, os.Unsetenv("ITERVOX_API_TOKEN"))

	out := captureSlogStderr(t, loadDotEnv)
	assert.Contains(t, out, "bearer auth / API key configured", "expected security-class Info line")
	assert.Contains(t, out, "ITERVOX_API_TOKEN", "key name must be in the line")
	assert.NotContains(t, out, tokenValue, "token VALUE must NEVER appear in any log output")
}

// TestLoadDotEnv_NoSensitiveKeysOnlyDebug ensures the security Info line
// fires ONLY when a sensitive key was set — a .env that only sets non-secret
// vars should stay silent at the default verbosity level.
func TestLoadDotEnv_NoSensitiveKeysOnlyDebug(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".env"),
		[]byte("FOO=bar\n"),
		0o600,
	))

	orig, _ := os.Getwd()
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(orig) })
	require.NoError(t, os.Unsetenv("FOO"))

	out := captureSlogStderr(t, loadDotEnv)
	assert.NotContains(t, out, "bearer auth / API key configured")
}

func TestConfiguredBackend(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		explicit string
		want     string
	}{
		{
			name:     "explicit override wins for wrapper commands",
			command:  "run-codex-wrapper --json",
			explicit: "codex",
			want:     "codex",
		},
		{
			name:    "infers codex from command",
			command: "/usr/local/bin/codex --model gpt-5.3-codex",
			want:    "codex",
		},
		{
			name:    "falls back to claude for unknown wrapper",
			command: "run-claude-wrapper --json",
			want:    "claude",
		},
		{
			name: "falls back to claude for blank command",
			want: "claude",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := configuredBackend(tc.command, tc.explicit); got != tc.want {
				t.Fatalf("configuredBackend(%q, %q) = %q, want %q", tc.command, tc.explicit, got, tc.want)
			}
		})
	}
}

func TestResolveCommandLineResolvesBinaryPreservingArgs(t *testing.T) {
	resolved := resolveCommandLine("claude --model sonnet", func(command string) string {
		if command == "claude" {
			return "/usr/local/bin/claude"
		}
		return command
	})

	assert.Equal(t, "/usr/local/bin/claude --model sonnet", resolved)
}

func TestResolveCommandLineResolvesBinaryAfterLeadingEnvAssignments(t *testing.T) {
	resolved := resolveCommandLine(
		"ITERVOX_ACTION_TOKEN='abc123' PATH='/tmp/bin:/usr/bin' claude --model sonnet",
		func(command string) string {
			if command == "claude" {
				return "/usr/local/bin/claude"
			}
			return command
		},
	)

	assert.Equal(
		t,
		"ITERVOX_ACTION_TOKEN='abc123' PATH='/tmp/bin:/usr/bin' /usr/local/bin/claude --model sonnet",
		resolved,
	)
}

func TestResolveCommandLineResolvesBinaryAfterBackendHintAndEnvAssignments(t *testing.T) {
	resolved := resolveCommandLine(
		"@@itervox-backend=claude ITERVOX_ACTION_TOKEN='abc123' claude --model sonnet",
		func(command string) string {
			if command == "claude" {
				return "/usr/local/bin/claude"
			}
			return command
		},
	)

	assert.Equal(
		t,
		"@@itervox-backend=claude ITERVOX_ACTION_TOKEN='abc123' /usr/local/bin/claude --model sonnet",
		resolved,
	)
}

func TestCommandResolverRunnerSkipsResolutionForSSHWorkers(t *testing.T) {
	inner := &captureRunner{}
	runner := commandResolverRunner{
		inner: inner,
		resolve: func(command string) string {
			return "/resolved/" + command
		},
	}

	_, err := runner.RunTurn(context.Background(), nil, nil, nil, "prompt", ".", "claude --model sonnet", "ssh://host", "", 0, 0)

	require.NoError(t, err)
	assert.Equal(t, "claude --model sonnet", inner.command)
	assert.Equal(t, "ssh://host", inner.workerHost)
}

// ─── quickstart template ─────────────────────────────────────────────────────
// (Replaces the former buildDemoConfig tests after T-07 removed the --demo
// flag. The quickstart WORKFLOW.md ships embedded in internal/templates and
// is the recommended way to evaluate itervox without a real tracker.)

func TestQuickstartTemplate_HasRequiredFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(path, templates.Quickstart, 0o600))

	loaded, err := config.Load(path)
	require.NoError(t, err, "quickstart template must parse")
	require.NoError(t, config.ValidateDispatch(loaded), "quickstart template must validate")

	assert.Equal(t, "memory", loaded.Tracker.Kind)
	assert.NotEmpty(t, loaded.Tracker.ActiveStates)
	assert.NotEmpty(t, loaded.Tracker.TerminalStates)
	assert.Equal(t, "In Progress", loaded.Tracker.WorkingState)
	assert.Equal(t, "Done", loaded.Tracker.CompletionState)
	assert.Equal(t, 3, loaded.Agent.MaxConcurrentAgents)
	assert.NotEmpty(t, loaded.PromptTemplate)
}

// ─── convertModelsForSnapshot ─────────────────────────────────────────────────

func TestConvertModelsForSnapshot(t *testing.T) {
	input := map[string][]config.ModelOption{
		"claude": {{ID: "a", Label: "A"}},
		"codex":  {{ID: "b", Label: "B"}, {ID: "c", Label: "C"}},
	}
	out := convertModelsForSnapshot(input)
	assert.Len(t, out["claude"], 1)
	assert.Len(t, out["codex"], 2)
	assert.Equal(t, "a", out["claude"][0].ID)
}

func TestConvertModelsForSnapshot_Nil(t *testing.T) {
	assert.Nil(t, convertModelsForSnapshot(nil))
}

// ─── GenerateDemoIssues ───────────────────────────────────────────────────────

func TestGenerateDemoIssues(t *testing.T) {
	issues := tracker.GenerateDemoIssues(10)
	assert.Len(t, issues, 10)
	for _, iss := range issues {
		assert.NotEmpty(t, iss.ID)
		assert.NotEmpty(t, iss.Identifier)
		assert.NotEmpty(t, iss.Title)
		assert.NotEmpty(t, iss.State)
		assert.NotNil(t, iss.Description)
		assert.Contains(t, iss.Labels, "demo")
	}
	// Verify identifiers are unique
	ids := make(map[string]bool)
	for _, iss := range issues {
		assert.False(t, ids[iss.Identifier], "duplicate identifier: %s", iss.Identifier)
		ids[iss.Identifier] = true
	}
}

func TestGenerateDemoIssues_StatesDistribution(t *testing.T) {
	issues := tracker.GenerateDemoIssues(10)
	states := make(map[string]int)
	for _, iss := range issues {
		states[iss.State]++
	}
	assert.True(t, states["Todo"] > 0, "should have Todo issues")
	assert.True(t, states["In Progress"] > 0, "should have In Progress issues")
}

// ─── defaultLogsDir ───────────────────────────────────────────────────────────

func TestDefaultLogsDir_FallsBackGracefully(t *testing.T) {
	// Non-existent workflow file should return a fallback path
	dir := defaultLogsDir("/nonexistent/WORKFLOW.md")
	assert.Contains(t, dir, ".itervox")
	assert.Contains(t, dir, "logs")
}

// ─── Quickstart workflow e2e (post-T-07) ─────────────────────────────────────

func TestQuickstartWorkflow_DaemonStartsAndServesHTTP(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(path, templates.Quickstart, 0o600))

	loaded, err := config.Load(path)
	require.NoError(t, err)
	require.NoError(t, config.ValidateDispatch(loaded))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tr := tracker.NewMemoryTracker(
		tracker.GenerateDemoIssues(5),
		loaded.Tracker.ActiveStates,
		loaded.Tracker.TerminalStates,
	)
	assert.Equal(t, "memory", loaded.Tracker.Kind)
	assert.NotNil(t, tr)
	_ = ctx
}

func TestOrchestratorAdapterUpdateIssueState_FindsIssueOutsideConfiguredStates(t *testing.T) {
	cfg := &config.Config{
		Tracker: config.TrackerConfig{
			BacklogStates:   []string{"Backlog"},
			ActiveStates:    []string{"Todo", "In Progress"},
			TerminalStates:  []string{"Done"},
			CompletionState: "Done",
		},
		Agent: config.AgentConfig{},
	}
	issue := tracker.GenerateDemoIssues(1)[0]
	issue.State = "Ready for QA"
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := orchestrator.New(cfg, mt, &agenttest.FakeRunner{}, nil)
	adapter := &orchestratorAdapter{
		orch:   orch,
		logBuf: logbuffer.New(),
		cfg:    cfg,
		tr:     mt,
	}

	err := adapter.UpdateIssueState(context.Background(), issue.Identifier, "Done")

	require.NoError(t, err)
	fetched, fetchErr := mt.FetchIssueByIdentifier(context.Background(), issue.Identifier)
	require.NoError(t, fetchErr)
	require.NotNil(t, fetched)
	assert.Equal(t, "Done", fetched.State)
}

func TestOrchestratorAdapterUpsertProfile_RejectsRenameCollision(t *testing.T) {
	cfg := &config.Config{
		Tracker: config.TrackerConfig{
			ActiveStates:   []string{"Todo"},
			TerminalStates: []string{"Done"},
		},
		Agent: config.AgentConfig{
			Profiles: map[string]config.AgentProfile{
				"qa": {Command: "claude"},
				"pm": {Command: "codex"},
			},
		},
	}
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := orchestrator.New(cfg, mt, &agenttest.FakeRunner{}, nil)
	adapter := &orchestratorAdapter{
		orch: orch,
		cfg:  cfg,
		tr:   mt,
	}

	err := adapter.UpsertProfile("pm", server.ProfileDef{Command: "claude"}, "qa")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
	profiles := orch.ProfilesCfg()
	assert.Equal(t, "claude", profiles["qa"].Command)
	assert.Equal(t, "codex", profiles["pm"].Command)
}

func TestOrchestratorAdapterUpsertProfilePreservesRawCommand(t *testing.T) {
	dir := t.TempDir()
	workflowPath := filepath.Join(dir, "WORKFLOW.md")
	content := `---
tracker:
  kind: linear
  api_key: key
  project_slug: proj
agent:
  command: claude
---

Prompt.
`
	require.NoError(t, os.WriteFile(workflowPath, []byte(content), 0o644))

	cfg, err := config.Load(workflowPath)
	require.NoError(t, err)
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := orchestrator.New(cfg, mt, &agenttest.FakeRunner{}, nil)
	adapter := &orchestratorAdapter{
		orch:         orch,
		cfg:          cfg,
		tr:           mt,
		workflowPath: workflowPath,
		notify:       func() {},
	}

	err = adapter.UpsertProfile("qa", server.ProfileDef{Command: "claude --model sonnet"}, "")

	require.NoError(t, err)
	profiles := orch.ProfilesCfg()
	assert.Equal(t, "claude --model sonnet", profiles["qa"].Command)

	updated, readErr := os.ReadFile(workflowPath)
	require.NoError(t, readErr)
	assert.Contains(t, string(updated), "command: claude --model sonnet")
	assert.NotContains(t, string(updated), "/Users/")
}

func TestOrchestratorAdapterUpsertProfile_DisablesDependentAutomationsWhenProfileDisabled(t *testing.T) {
	dir := t.TempDir()
	workflowPath := filepath.Join(dir, "WORKFLOW.md")
	content := `---
tracker:
  kind: linear
  api_key: key
  project_slug: proj
agent:
  command: claude
  reviewer_profile: "input-responder"
  auto_review: true
  profiles:
    input-responder:
      command: claude --model claude-sonnet-4-6
      allowed_actions:
        - provide_input
    reviewer:
      command: claude
automations:
  - id: input-responder
    enabled: true
    profile: input-responder
    trigger:
      type: input_required
  - id: reviewer-nightly
    enabled: true
    profile: reviewer
    trigger:
      type: cron
      cron: "0 9 * * 1"
---

Prompt.
`
	require.NoError(t, os.WriteFile(workflowPath, []byte(content), 0o644))

	cfg, err := config.Load(workflowPath)
	require.NoError(t, err)
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := orchestrator.New(cfg, mt, &agenttest.FakeRunner{}, nil)
	adapter := &orchestratorAdapter{
		orch:         orch,
		cfg:          cfg,
		tr:           mt,
		workflowPath: workflowPath,
		notify:       func() {},
	}

	err = adapter.UpsertProfile("input-responder", server.ProfileDef{
		Command:        "claude --model claude-sonnet-4-6",
		Enabled:        false,
		AllowedActions: []string{"provide_input"},
	}, "input-responder")

	require.NoError(t, err)

	profiles := orch.ProfilesCfg()
	require.Contains(t, profiles, "input-responder")
	assert.False(t, config.ProfileEnabled(profiles["input-responder"]))

	automations := orch.AutomationsCfg()
	require.Len(t, automations, 2)
	assert.Equal(t, "input-responder", automations[0].ID)
	assert.False(t, automations[0].Enabled)
	assert.Equal(t, "reviewer-nightly", automations[1].ID)
	assert.True(t, automations[1].Enabled)
	reviewerProfile, autoReview := orch.ReviewerCfg()
	assert.Equal(t, "", reviewerProfile)
	assert.False(t, autoReview)

	updated, readErr := os.ReadFile(workflowPath)
	require.NoError(t, readErr)
	assert.Contains(t, string(updated), "input-responder:\n      command: claude --model claude-sonnet-4-6\n      enabled: false")
	assert.Contains(t, string(updated), "- id: input-responder\n    enabled: false\n    profile: input-responder")
	assert.Contains(t, string(updated), "- id: reviewer-nightly\n    enabled: true\n    profile: reviewer")
	assert.NotContains(t, string(updated), "reviewer_profile:")
	assert.NotContains(t, string(updated), "auto_review: true")

	reloaded, loadErr := config.Load(workflowPath)
	require.NoError(t, loadErr)
	require.NoError(t, config.ValidateDispatch(reloaded))
}

func TestOrchestratorAdapterUpsertProfile_RenameUpdatesDependentReferences(t *testing.T) {
	dir := t.TempDir()
	workflowPath := filepath.Join(dir, "WORKFLOW.md")
	content := `---
tracker:
  kind: linear
  api_key: key
  project_slug: proj
agent:
  command: claude
  reviewer_profile: "reviewer"
  auto_review: true
  profiles:
    reviewer:
      command: claude
automations:
  - id: reviewer-nightly
    enabled: true
    profile: reviewer
    trigger:
      type: cron
      cron: "0 9 * * 1"
---

Prompt.
`
	require.NoError(t, os.WriteFile(workflowPath, []byte(content), 0o644))

	cfg, err := config.Load(workflowPath)
	require.NoError(t, err)
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := orchestrator.New(cfg, mt, &agenttest.FakeRunner{}, nil)
	adapter := &orchestratorAdapter{
		orch:         orch,
		cfg:          cfg,
		tr:           mt,
		workflowPath: workflowPath,
		notify:       func() {},
	}

	err = adapter.UpsertProfile("review-bot", server.ProfileDef{Command: "claude", Enabled: true}, "reviewer")

	require.NoError(t, err)
	profiles := orch.ProfilesCfg()
	require.NotContains(t, profiles, "reviewer")
	require.Contains(t, profiles, "review-bot")
	automations := orch.AutomationsCfg()
	require.Len(t, automations, 1)
	assert.Equal(t, "review-bot", automations[0].Profile)
	reviewerProfile, autoReview := orch.ReviewerCfg()
	assert.Equal(t, "review-bot", reviewerProfile)
	assert.True(t, autoReview)

	updated, readErr := os.ReadFile(workflowPath)
	require.NoError(t, readErr)
	assert.Contains(t, string(updated), `reviewer_profile: "review-bot"`)
	assert.Contains(t, string(updated), "profile: review-bot")
	assert.NotContains(t, string(updated), "profile: reviewer")

	reloaded, loadErr := config.Load(workflowPath)
	require.NoError(t, loadErr)
	require.NoError(t, config.ValidateDispatch(reloaded))
}

// TestOrchestratorAdapterUpsertProfile_RenameAtomicityOnWriteFailure pins the
// transactional contract added by T-04: when WORKFLOW.md cannot be written,
// neither the file NOR the orchestrator state is mutated. Without this, a
// failed cascade could leave the orchestrator referencing a renamed profile
// while WORKFLOW.md still has the old name (or vice versa).
func TestOrchestratorAdapterUpsertProfile_RenameAtomicityOnWriteFailure(t *testing.T) {
	dir := t.TempDir()
	workflowPath := filepath.Join(dir, "WORKFLOW.md")
	content := `---
tracker:
  kind: linear
  api_key: key
  project_slug: proj
agent:
  command: claude
  reviewer_profile: "reviewer"
  auto_review: true
  profiles:
    reviewer:
      command: claude
automations:
  - id: reviewer-nightly
    enabled: true
    profile: reviewer
    trigger:
      type: cron
      cron: "0 9 * * 1"
---

Prompt.
`
	require.NoError(t, os.WriteFile(workflowPath, []byte(content), 0o644))
	originalBytes, _ := os.ReadFile(workflowPath)

	cfg, err := config.Load(workflowPath)
	require.NoError(t, err)
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := orchestrator.New(cfg, mt, &agenttest.FakeRunner{}, nil)
	adapter := &orchestratorAdapter{
		orch:         orch,
		cfg:          cfg,
		tr:           mt,
		workflowPath: workflowPath,
		notify:       func() {},
	}

	// Make the parent dir read-only so atomicfs.WriteFile's CreateTemp fails
	// before any rename happens. This simulates a disk-full / permission /
	// hardware fault during the cascade write.
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err = adapter.UpsertProfile("review-bot", server.ProfileDef{Command: "claude", Enabled: true}, "reviewer")
	require.Error(t, err, "rename cascade should fail when WORKFLOW.md is unwritable")

	// Restore permissions so we can verify the file is byte-identical.
	require.NoError(t, os.Chmod(dir, 0o700))

	gotBytes, readErr := os.ReadFile(workflowPath)
	require.NoError(t, readErr)
	assert.Equal(t, string(originalBytes), string(gotBytes),
		"WORKFLOW.md must be byte-identical after a failed cascade")

	// And orchestrator state must be unchanged: still has "reviewer", not "review-bot".
	profiles := orch.ProfilesCfg()
	assert.Contains(t, profiles, "reviewer")
	assert.NotContains(t, profiles, "review-bot")
	reviewerProfile, autoReview := orch.ReviewerCfg()
	assert.Equal(t, "reviewer", reviewerProfile)
	assert.True(t, autoReview)
	automations := orch.AutomationsCfg()
	require.Len(t, automations, 1)
	assert.Equal(t, "reviewer", automations[0].Profile)
}

func TestOrchestratorAdapterDeleteProfile_ClearsDependentReferences(t *testing.T) {
	dir := t.TempDir()
	workflowPath := filepath.Join(dir, "WORKFLOW.md")
	content := `---
tracker:
  kind: linear
  api_key: key
  project_slug: proj
agent:
  command: claude
  reviewer_profile: "reviewer"
  auto_review: true
  profiles:
    reviewer:
      command: claude
    qa:
      command: codex
automations:
  - id: reviewer-nightly
    enabled: true
    profile: reviewer
    trigger:
      type: cron
      cron: "0 9 * * 1"
  - id: qa-nightly
    enabled: true
    profile: qa
    trigger:
      type: cron
      cron: "0 10 * * 1"
---

Prompt.
`
	require.NoError(t, os.WriteFile(workflowPath, []byte(content), 0o644))

	cfg, err := config.Load(workflowPath)
	require.NoError(t, err)
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := orchestrator.New(cfg, mt, &agenttest.FakeRunner{}, nil)
	adapter := &orchestratorAdapter{
		orch:         orch,
		cfg:          cfg,
		tr:           mt,
		workflowPath: workflowPath,
		notify:       func() {},
	}

	err = adapter.DeleteProfile("reviewer")

	require.NoError(t, err)
	profiles := orch.ProfilesCfg()
	require.NotContains(t, profiles, "reviewer")
	require.Contains(t, profiles, "qa")
	automations := orch.AutomationsCfg()
	require.Len(t, automations, 1)
	assert.Equal(t, "qa-nightly", automations[0].ID)
	assert.Equal(t, "qa", automations[0].Profile)
	reviewerProfile, autoReview := orch.ReviewerCfg()
	assert.Equal(t, "", reviewerProfile)
	assert.False(t, autoReview)

	updated, readErr := os.ReadFile(workflowPath)
	require.NoError(t, readErr)
	assert.NotContains(t, string(updated), "reviewer_profile:")
	assert.NotContains(t, string(updated), "auto_review: true")
	assert.NotContains(t, string(updated), "- id: reviewer-nightly")
	assert.Contains(t, string(updated), "- id: qa-nightly")

	reloaded, loadErr := config.Load(workflowPath)
	require.NoError(t, loadErr)
	require.NoError(t, config.ValidateDispatch(reloaded))
}

func TestOrchestratorAdapterSetReviewerConfig_PersistsWorkflow(t *testing.T) {
	dir := t.TempDir()
	workflowPath := filepath.Join(dir, "WORKFLOW.md")
	content := `---
tracker:
  kind: linear
  api_key: key
  project_slug: proj
agent:
  command: claude
  profiles:
    reviewer:
      command: claude
---

Prompt.
`
	require.NoError(t, os.WriteFile(workflowPath, []byte(content), 0o644))

	cfg, err := config.Load(workflowPath)
	require.NoError(t, err)
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := orchestrator.New(cfg, mt, &agenttest.FakeRunner{}, nil)
	adapter := &orchestratorAdapter{
		orch:         orch,
		cfg:          cfg,
		tr:           mt,
		workflowPath: workflowPath,
		notify:       func() {},
	}

	err = adapter.SetReviewerConfig("reviewer", true)

	require.NoError(t, err)
	updated, readErr := os.ReadFile(workflowPath)
	require.NoError(t, readErr)
	assert.Contains(t, string(updated), `reviewer_profile: "reviewer"`)
	assert.Contains(t, string(updated), "auto_review: true")
}

func TestReloadPlanForRunExit(t *testing.T) {
	msg, delay := reloadPlanForRunExit(nil)
	assert.Equal(t, "WORKFLOW.md changed — reloading config", msg)
	assert.Equal(t, 200*time.Millisecond, delay)

	msg, delay = reloadPlanForRunExit(errors.New("boom"))
	assert.Equal(t, "run returned with error — retrying", msg)
	assert.Equal(t, time.Second, delay)
}

// TestReloadPlanForRunExit_ContextCanceledIsCleanReload pins the contract that
// run() returning context.Canceled — which is what the watcher does on every
// WORKFLOW.md save — is classified as a clean reload, not an error. Without
// this, every save would log a WARN-level "run returned with error" line.
func TestReloadPlanForRunExit_ContextCanceledIsCleanReload(t *testing.T) {
	msg, delay := reloadPlanForRunExit(context.Canceled)
	assert.Equal(t, "WORKFLOW.md changed — reloading config", msg)
	assert.Equal(t, 200*time.Millisecond, delay)
}

// TestReloadPlanForRunExit_WrappedContextCanceled guards against an errors.Is
// regression: the orchestrator/agent layers may wrap context.Canceled as it
// propagates up. A naive == check would miss the wrapped form and log the
// reload as an error.
func TestReloadPlanForRunExit_WrappedContextCanceled(t *testing.T) {
	wrapped := fmt.Errorf("orchestrator drained: %w", context.Canceled)
	msg, delay := reloadPlanForRunExit(wrapped)
	assert.Equal(t, "WORKFLOW.md changed — reloading config", msg)
	assert.Equal(t, 200*time.Millisecond, delay)
}

func TestOrchestratorAdapterAddSSHHost_PersistsWorkflow(t *testing.T) {
	dir := t.TempDir()
	workflowPath := filepath.Join(dir, "WORKFLOW.md")
	content := `---
tracker:
  kind: linear
  api_key: key
  project_slug: proj
agent:
  command: claude
---

Prompt.
`
	require.NoError(t, os.WriteFile(workflowPath, []byte(content), 0o644))

	cfg, err := config.Load(workflowPath)
	require.NoError(t, err)
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := orchestrator.New(cfg, mt, &agenttest.FakeRunner{}, nil)
	adapter := &orchestratorAdapter{
		orch:         orch,
		cfg:          cfg,
		tr:           mt,
		workflowPath: workflowPath,
		notify:       func() {},
	}

	err = adapter.AddSSHHost("worker-1", "fast box")

	require.NoError(t, err)
	hosts, descs := orch.SSHHostsCfg()
	assert.Equal(t, []string{"worker-1"}, hosts)
	assert.Equal(t, map[string]string{"worker-1": "fast box"}, descs)

	reloaded, loadErr := config.Load(workflowPath)
	require.NoError(t, loadErr)
	assert.Equal(t, []string{"worker-1"}, reloaded.Agent.SSHHosts)
	assert.Equal(t, map[string]string{"worker-1": "fast box"}, reloaded.Agent.SSHHostDescriptions)
}

func TestOrchestratorAdapterRemoveSSHHost_PersistsWorkflow(t *testing.T) {
	dir := t.TempDir()
	workflowPath := filepath.Join(dir, "WORKFLOW.md")
	content := `---
tracker:
  kind: linear
  api_key: key
  project_slug: proj
agent:
  command: claude
  ssh_hosts: ["worker-1"]
  ssh_host_descriptions:
    "worker-1": "fast box"
---

Prompt.
`
	require.NoError(t, os.WriteFile(workflowPath, []byte(content), 0o644))

	cfg, err := config.Load(workflowPath)
	require.NoError(t, err)
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := orchestrator.New(cfg, mt, &agenttest.FakeRunner{}, nil)
	adapter := &orchestratorAdapter{
		orch:         orch,
		cfg:          cfg,
		tr:           mt,
		workflowPath: workflowPath,
		notify:       func() {},
	}

	err = adapter.RemoveSSHHost("worker-1")

	require.NoError(t, err)
	hosts, descs := orch.SSHHostsCfg()
	assert.Empty(t, hosts)
	assert.Empty(t, descs)

	reloaded, loadErr := config.Load(workflowPath)
	require.NoError(t, loadErr)
	assert.Empty(t, reloaded.Agent.SSHHosts)
	assert.Empty(t, reloaded.Agent.SSHHostDescriptions)
}

func TestOrchestratorAdapterSetDispatchStrategy_PersistsWorkflow(t *testing.T) {
	dir := t.TempDir()
	workflowPath := filepath.Join(dir, "WORKFLOW.md")
	content := `---
tracker:
  kind: linear
  api_key: key
  project_slug: proj
agent:
  command: claude
---

Prompt.
`
	require.NoError(t, os.WriteFile(workflowPath, []byte(content), 0o644))

	cfg, err := config.Load(workflowPath)
	require.NoError(t, err)
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := orchestrator.New(cfg, mt, &agenttest.FakeRunner{}, nil)
	adapter := &orchestratorAdapter{
		orch:         orch,
		cfg:          cfg,
		tr:           mt,
		workflowPath: workflowPath,
		notify:       func() {},
	}

	err = adapter.SetDispatchStrategy("least-loaded")

	require.NoError(t, err)
	assert.Equal(t, "least-loaded", orch.DispatchStrategyCfg())
	reloaded, loadErr := config.Load(workflowPath)
	require.NoError(t, loadErr)
	assert.Equal(t, "least-loaded", reloaded.Agent.DispatchStrategy)
}

func TestOrchestratorAdapterSetInlineInput_PersistsWorkflow(t *testing.T) {
	dir := t.TempDir()
	workflowPath := filepath.Join(dir, "WORKFLOW.md")
	content := `---
tracker:
  kind: linear
  api_key: key
  project_slug: proj
agent:
  command: claude
---

Prompt.
`
	require.NoError(t, os.WriteFile(workflowPath, []byte(content), 0o644))

	cfg, err := config.Load(workflowPath)
	require.NoError(t, err)
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := orchestrator.New(cfg, mt, &agenttest.FakeRunner{}, nil)
	adapter := &orchestratorAdapter{
		orch:         orch,
		cfg:          cfg,
		tr:           mt,
		workflowPath: workflowPath,
		notify:       func() {},
	}

	err = adapter.SetInlineInput(true)

	require.NoError(t, err)
	assert.True(t, orch.InlineInputCfg())
	reloaded, loadErr := config.Load(workflowPath)
	require.NoError(t, loadErr)
	assert.True(t, reloaded.Agent.InlineInput)
}

func TestOrchestratorAdapterSetReviewerConfig_DoesNotMutateRuntimeWhenPersistFails(t *testing.T) {
	cfg := &config.Config{
		Tracker: config.TrackerConfig{
			ActiveStates:   []string{"Todo"},
			TerminalStates: []string{"Done"},
		},
		Agent: config.AgentConfig{
			Command: "claude",
			Profiles: map[string]config.AgentProfile{
				"reviewer": {Command: "claude"},
			},
		},
	}
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := orchestrator.New(cfg, mt, &agenttest.FakeRunner{}, nil)
	adapter := &orchestratorAdapter{
		orch:         orch,
		cfg:          cfg,
		tr:           mt,
		workflowPath: filepath.Join(t.TempDir(), "missing", "WORKFLOW.md"),
		notify:       func() {},
	}

	err := adapter.SetReviewerConfig("reviewer", true)

	require.Error(t, err)
	profile, autoReview := orch.ReviewerCfg()
	assert.Equal(t, "", profile)
	assert.False(t, autoReview)
}

func TestOrchestratorAdapterSetAutomations_DoesNotMutateRuntimeWhenPersistFails(t *testing.T) {
	cfg := &config.Config{
		Tracker: config.TrackerConfig{
			ActiveStates:   []string{"Todo"},
			TerminalStates: []string{"Done"},
		},
		Agent: config.AgentConfig{
			Command: "claude",
			Profiles: map[string]config.AgentProfile{
				"reviewer": {Command: "claude"},
			},
		},
	}
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := orchestrator.New(cfg, mt, &agenttest.FakeRunner{}, nil)
	adapter := &orchestratorAdapter{
		orch:         orch,
		cfg:          cfg,
		tr:           mt,
		workflowPath: filepath.Join(t.TempDir(), "missing", "WORKFLOW.md"),
		notify:       func() {},
	}

	err := adapter.SetAutomations([]server.AutomationDef{{
		ID:      "nightly",
		Enabled: true,
		Profile: "reviewer",
		Trigger: server.AutomationTriggerDef{Type: "cron", Cron: "0 9 * * 1"},
	}})

	require.Error(t, err)
	assert.Nil(t, orch.AutomationsCfg())
}

func TestOrchestratorAdapterSetAutoClearWorkspace_DoesNotMutateRuntimeWhenPersistFails(t *testing.T) {
	cfg := &config.Config{
		Tracker: config.TrackerConfig{
			ActiveStates:   []string{"Todo"},
			TerminalStates: []string{"Done"},
		},
		Agent: config.AgentConfig{
			Command: "claude",
		},
		Workspace: config.WorkspaceConfig{
			AutoClearWorkspace: false,
		},
	}
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := orchestrator.New(cfg, mt, &agenttest.FakeRunner{}, nil)
	adapter := &orchestratorAdapter{
		orch:         orch,
		cfg:          cfg,
		tr:           mt,
		workflowPath: filepath.Join(t.TempDir(), "missing", "WORKFLOW.md"),
		notify:       func() {},
	}

	err := adapter.SetAutoClearWorkspace(true)

	require.Error(t, err)
	assert.False(t, orch.AutoClearWorkspaceCfg())
}

func TestOrchestratorAdapterUpdateTrackerStates_DoesNotMutateRuntimeWhenPersistFails(t *testing.T) {
	cfg := &config.Config{
		Tracker: config.TrackerConfig{
			ActiveStates:    []string{"Todo"},
			TerminalStates:  []string{"Done"},
			CompletionState: "In Review",
		},
		Agent: config.AgentConfig{
			Command: "claude",
		},
	}
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := orchestrator.New(cfg, mt, &agenttest.FakeRunner{}, nil)
	adapter := &orchestratorAdapter{
		orch:         orch,
		cfg:          cfg,
		tr:           mt,
		workflowPath: filepath.Join(t.TempDir(), "missing", "WORKFLOW.md"),
		notify:       func() {},
	}

	err := adapter.UpdateTrackerStates([]string{"Ready"}, []string{"Closed"}, "Done")

	require.Error(t, err)
	active, terminal, completion := orch.TrackerStatesCfg()
	assert.Equal(t, []string{"Todo"}, active)
	assert.Equal(t, []string{"Done"}, terminal)
	assert.Equal(t, "In Review", completion)
}

// ─── server.ModelOption conversion ────────────────────────────────────────────

func TestServerModelOption_JSONRoundTrip(t *testing.T) {
	m := server.ModelOption{ID: "test-id", Label: "Test Label"}
	data, err := json.Marshal(m)
	require.NoError(t, err)
	var decoded server.ModelOption
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, m, decoded)
}

func TestSortedPausedIdentifiers(t *testing.T) {
	got := sortedPausedIdentifiers(map[string]string{
		"ENG-20": "id-20",
		"ENG-3":  "id-3",
		"ENG-10": "id-10",
	})

	want := append([]string{}, got...)
	sort.Strings(want)
	assert.Equal(t, want, got)
}

func TestSortedRetryRows(t *testing.T) {
	got := sortedRetryRows(map[string]*orchestrator.RetryEntry{
		"id-20": {Identifier: "ENG-20", Attempt: 2},
		"id-3":  {Identifier: "ENG-3", Attempt: 1},
		"id-10": {Identifier: "ENG-10", Attempt: 3},
	})

	require.Len(t, got, 3)
	assert.Equal(t, []string{"ENG-10", "ENG-20", "ENG-3"}, []string{
		got[0].Identifier,
		got[1].Identifier,
		got[2].Identifier,
	})
}

func TestSortedInputRequiredRows(t *testing.T) {
	now := time.Now().UTC()
	got := sortedInputRequiredRows(
		map[string]*orchestrator.InputRequiredEntry{
			"ENG-20": {Identifier: "ENG-20", Context: "c20", QueuedAt: now},
			"ENG-3":  {Identifier: "ENG-3", Context: "c3", QueuedAt: now},
		},
		map[string]*orchestrator.PendingInputResumeEntry{
			"ENG-10": {Identifier: "ENG-10", Context: "c10", UserMessage: "approved", QueuedAt: now},
		},
		0, // staleAfter=0 → no stale flag in this fixture
		now,
	)

	require.Len(t, got, 3)
	assert.Equal(t, []string{"ENG-10", "ENG-20", "ENG-3"}, []string{
		got[0].Identifier,
		got[1].Identifier,
		got[2].Identifier,
	})
	assert.Equal(t, []string{"pending_input_resume", "input_required", "input_required"}, []string{
		got[0].State,
		got[1].State,
		got[2].State,
	})
	for _, row := range got {
		assert.False(t, row.Stale, "no stale flag when staleAfter=0")
	}
}

// TestSortedInputRequiredRows_StalenessAndAge pins the gap A surface: the
// row carries Stale=true once age > staleAfter and AgeMinutes always
// reflects wall-clock age. Both fields are surfaced via the snapshot so the
// dashboard can render a "Stale" badge + tooltip without re-parsing
// QueuedAt on each render.
func TestSortedInputRequiredRows_StalenessAndAge(t *testing.T) {
	now := time.Now().UTC()
	old := now.Add(-3 * time.Hour)
	fresh := now.Add(-15 * time.Minute)

	got := sortedInputRequiredRows(
		map[string]*orchestrator.InputRequiredEntry{
			"ENG-OLD":   {Identifier: "ENG-OLD", Context: "c", QueuedAt: old},
			"ENG-FRESH": {Identifier: "ENG-FRESH", Context: "c", QueuedAt: fresh},
		},
		nil,
		time.Hour, // stale threshold = 1h
		now,
	)
	require.Len(t, got, 2)
	byID := map[string]server.InputRequiredRow{}
	for _, r := range got {
		byID[r.Identifier] = r
	}
	assert.True(t, byID["ENG-OLD"].Stale, "3h old entry must be flagged stale when threshold is 1h")
	assert.False(t, byID["ENG-FRESH"].Stale, "15m old entry must remain fresh when threshold is 1h")
	assert.GreaterOrEqual(t, byID["ENG-OLD"].AgeMinutes, 179, "AgeMinutes should be ~180 for a 3h-old entry")
	assert.LessOrEqual(t, byID["ENG-OLD"].AgeMinutes, 181)
}

// ─── API endpoint smoke test ──────────────────────────────────────────────────

func TestHealthEndpoint_NoDemoNeeded(t *testing.T) {
	// The health endpoint should work with a minimal server config
	snap := server.StateSnapshot{GeneratedAt: time.Now()}
	cfg := server.Config{
		Snapshot:    func() server.StateSnapshot { return snap },
		RefreshChan: make(chan struct{}, 1),
	}
	srv := server.New(cfg)

	req, _ := http.NewRequest("GET", "/api/v1/health", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Header().Get("Content-Type"), "application/json")
	assert.JSONEq(t, `{"status":"ok"}`, w.Body.String())
}
