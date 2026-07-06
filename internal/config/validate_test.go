package config_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/config"
)

func validWorkflowPath(t *testing.T) string {
	t.Helper()
	return workflowWithContent(t, minimalV2(""))
}

func TestValidateDispatchPassesForValidConfig(t *testing.T) {
	path := validWorkflowPath(t)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	err = config.ValidateDispatch(cfg)
	assert.NoError(t, err)
}

func TestValidateDispatchFailsMissingTrackerKind(t *testing.T) {
	content := "---\nitervox_schema_version: 2\ntracker:\n  api_key: key\n  project_slug: proj\n---\n\nPrompt.\n"
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	err = config.ValidateDispatch(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tracker.kind")
}

func TestValidateDispatchSchemaErrorPrecedesOtherValidation(t *testing.T) {
	cfg := &config.Config{}
	err := config.ValidateDispatch(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "itervox_schema_version")
	assert.NotContains(t, err.Error(), "tracker.kind")
}

func TestValidateDispatchFailsUnsupportedTrackerKind(t *testing.T) {
	content := "---\nitervox_schema_version: 2\ntracker:\n  kind: jira\n  api_key: key\n  project_slug: proj\n---\n\nPrompt.\n"
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	err = config.ValidateDispatch(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported_tracker_kind")
}

func TestValidateDispatchFailsMissingAPIKey(t *testing.T) {
	_ = os.Unsetenv("MISSING_KEY_XYZ")
	content := "---\nitervox_schema_version: 2\ntracker:\n  kind: linear\n  api_key: $MISSING_KEY_XYZ\n  project_slug: proj\n---\n\nPrompt.\n"
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	err = config.ValidateDispatch(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tracker.api_key")
}

func TestValidateDispatchLinearOKWithoutProjectSlug(t *testing.T) {
	// Linear project_slug is optional — project is selected via TUI/dashboard.
	content := "---\nitervox_schema_version: 2\ntracker:\n  kind: linear\n  api_key: mykey\n---\n\nPrompt.\n"
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	err = config.ValidateDispatch(cfg)
	assert.NoError(t, err)
}

func TestValidateDispatchGitHubFailsMissingProjectSlug(t *testing.T) {
	// GitHub project_slug (owner/repo) is required — it identifies the target repo.
	content := "---\nitervox_schema_version: 2\ntracker:\n  kind: github\n  api_key: ghtoken\n---\n\nPrompt.\n"
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	err = config.ValidateDispatch(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tracker.project_slug")
}

func TestValidateDispatchFailsMissingAgentCommand(t *testing.T) {
	cfg := &config.Config{}
	cfg.SchemaVersion = config.LatestWorkflowSchemaVersion
	cfg.Tracker.Kind = "linear"
	cfg.Tracker.APIKey = "key"
	cfg.Tracker.ProjectSlug = "proj"
	cfg.Agent.Command = "" // explicitly blank

	err := config.ValidateDispatch(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent.command")
}

func TestValidateDispatchGitHubKindAccepted(t *testing.T) {
	content := "---\nitervox_schema_version: 2\ntracker:\n  kind: github\n  api_key: ghtoken\n  project_slug: owner/repo\n---\n\nPrompt.\n"
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	err = config.ValidateDispatch(cfg)
	assert.NoError(t, err)
}

// New (v0.2.0): auto_clear and auto_review now coexist. The clear is
// deferred from main-worker success to reviewer success under the new
// terminal-state-only semantics, so they no longer race. ValidateDispatch
// must accept this combination.
func TestValidateDispatchAcceptsAutoReviewWithAutoClear(t *testing.T) {
	content := minimalV2WithProfileFiles(t, `agent:
  reviewer_profile: code-reviewer
  auto_review: true
  profiles:
    code-reviewer:
      command: claude
workspace:
  auto_clear: true
`)
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)

	err = config.ValidateDispatch(cfg)
	assert.NoError(t, err, "auto_clear + auto_review must validate cleanly under terminal-state-only clear semantics")
}

func TestValidateDispatchFailsWhenAutoReviewEnabledWithoutReviewerProfile(t *testing.T) {
	content := minimalV2(`agent:
  auto_review: true
`)
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)

	err = config.ValidateDispatch(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reviewer_profile")
	assert.Contains(t, err.Error(), "auto_review")
}

func TestValidateDispatchRejectsUnknownReviewerProfile(t *testing.T) {
	content := minimalV2WithProfileFiles(t, `agent:
  reviewer_profile: reviewer
  profiles:
    qa:
      command: claude
`)
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)

	err = config.ValidateDispatch(cfg)
	require.Error(t, err)
	assert.ErrorIs(t, err, config.ErrReviewerProfileNotFound)
}

func TestValidateDispatchRejectsDisabledReviewerProfile(t *testing.T) {
	content := minimalV2WithProfileFiles(t, `agent:
  reviewer_profile: reviewer
  profiles:
    reviewer:
      command: claude
      enabled: false
`)
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)

	err = config.ValidateDispatch(cfg)
	require.Error(t, err)
	assert.ErrorIs(t, err, config.ErrReviewerProfileDisabled)
}

func TestValidateDispatchAcceptsEmptyDepsAnalyzerProfile(t *testing.T) {
	content := minimalV2WithProfileFiles(t, `agent:
  profiles:
    qa:
      command: claude
`)
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)

	require.NoError(t, config.ValidateDispatch(cfg))
}

func TestValidateDispatchAcceptsValidDepsAnalyzerProfile(t *testing.T) {
	content := minimalV2WithProfileFiles(t, `agent:
  deps_analyzer_profile: deps-analyzer
  profiles:
    deps-analyzer:
      command: claude
`)
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)

	require.NoError(t, config.ValidateDispatch(cfg))
	assert.Equal(t, "deps-analyzer", cfg.Agent.DepsAnalyzerProfile)
}

func TestValidateDispatchRejectsUnknownDepsAnalyzerProfile(t *testing.T) {
	content := minimalV2WithProfileFiles(t, `agent:
  deps_analyzer_profile: deps-analyzer
  profiles:
    qa:
      command: claude
`)
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)

	err = config.ValidateDispatch(cfg)
	require.Error(t, err)
	assert.ErrorIs(t, err, config.ErrDepsAnalyzerProfileNotFound)
}

func TestValidateDispatchRejectsDisabledDepsAnalyzerProfile(t *testing.T) {
	content := minimalV2WithProfileFiles(t, `agent:
  deps_analyzer_profile: deps-analyzer
  profiles:
    deps-analyzer:
      command: claude
      enabled: false
`)
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)

	err = config.ValidateDispatch(cfg)
	require.Error(t, err)
	assert.ErrorIs(t, err, config.ErrDepsAnalyzerProfileDisabled)
}

func TestValidateDispatchRejectsCreateIssueProfileWithoutState(t *testing.T) {
	content := minimalV2WithProfileFiles(t, `agent:
  profiles:
    qa:
      command: claude
      allowed_actions: [create_issue]
`)
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)

	err = config.ValidateDispatch(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create_issue_state")
}

func TestValidateDispatchRejectsDuplicateAutomationIDs(t *testing.T) {
	content := minimalV2WithProfileFiles(t, `agent:
  profiles:
    qa:
      command: claude
automations:
  - id: comment-watch
    enabled: true
    profile: qa
    trigger:
      type: tracker_comment_added
  - id: comment-watch
    enabled: true
    profile: qa
    trigger:
      type: run_failed
`)
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)

	err = config.ValidateDispatch(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate automation id")
}

func TestValidateDispatchRejectsInvalidAutomationRegex(t *testing.T) {
	content := minimalV2WithProfileFiles(t, `agent:
  profiles:
    qa:
      command: claude
automations:
  - id: comment-watch
    enabled: true
    profile: qa
    trigger:
      type: tracker_comment_added
    filter:
      identifier_regex: "["
`)
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)

	err = config.ValidateDispatch(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "identifier_regex")
}

func TestValidateDispatchRejectsUnknownAutomationProfile(t *testing.T) {
	content := minimalV2WithProfileFiles(t, `agent:
  profiles:
    qa:
      command: claude
automations:
  - id: comment-watch
    enabled: true
    profile: pm
    trigger:
      type: tracker_comment_added
`)
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)

	err = config.ValidateDispatch(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown profile")
}

func TestValidateDispatchRejectsDisabledAutomationProfile(t *testing.T) {
	content := minimalV2WithProfileFiles(t, `agent:
  profiles:
    qa:
      command: claude
      enabled: false
automations:
  - id: comment-watch
    enabled: true
    profile: qa
    trigger:
      type: tracker_comment_added
`)
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)

	err = config.ValidateDispatch(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "disabled profile")
}

// T-42 (06.G-02): the unknown-profile check now fires regardless of the
// automation's enabled flag. Previously a disabled-but-misconfigured rule
// passed `ValidateDispatch` at startup and only crashed dispatch the moment
// a user re-enabled it from the dashboard. The disabled-profile check is
// scoped to enabled automations only, because the UpsertProfile cascade
// deliberately leaves a disabled automation pointing at a disabled profile
// in lock-step.
func TestValidateDispatchRejectsDisabledAutomationReferencingUnknownProfile(t *testing.T) {
	content := minimalV2WithProfileFiles(t, `agent:
  profiles:
    qa:
      command: claude
      enabled: true
automations:
  - id: comment-watch
    enabled: false
    profile: ghost-profile
    trigger:
      type: tracker_comment_added
`)
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)

	err = config.ValidateDispatch(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown profile")
}

// T-42: a disabled automation referencing a disabled profile is allowed —
// matches the UpsertProfile cascade behavior that disables both sides
// together. Re-enabling either side without fixing the other still trips
// the existing enabled-side validation.
func TestValidateDispatchAllowsDisabledAutomationReferencingDisabledProfile(t *testing.T) {
	content := minimalV2(`agent:
  profiles:
    qa:
      command: claude
` + schema2ProfileFileFields(t, "      ") + `      enabled: false
automations:
  - id: comment-watch
    enabled: false
    profile: qa
    trigger:
      type: tracker_comment_added
`)
	path := workflowWithContent(t, content)
	cfg, err := config.Load(path)
	require.NoError(t, err)

	require.NoError(t, config.ValidateDispatch(cfg))
}
