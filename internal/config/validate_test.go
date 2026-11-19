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
	content := "---\nitervox_schema_version: 2\ntracker:\n  kind: trello\n  api_key: key\n  project_slug: proj\n---\n\nPrompt.\n"
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

// TestAutomationWithoutProfileIsReported: an automation with no profile
// used to be skipped by parseAutomations before validation could see it, so
// the daemon started with the rule silently missing. It now reaches
// ValidateDispatch, which refuses the config and names the automation; one
// with no trigger.type is refused the same way.
func TestAutomationWithoutProfileIsReported(t *testing.T) {
	for _, tc := range []struct {
		name, rule, want string
	}{
		{
			name: "no profile",
			rule: `  - id: unblock-backlog
    trigger:
      type: blockers_resolved
`,
			want: `automation "unblock-backlog" requires profile`,
		},
		{
			name: "no trigger type",
			rule: `  - id: nightly
    profile: qa
    trigger:
      cron: "0 3 * * *"
`,
			want: `automation "nightly" requires trigger.type`,
		},
		{
			name: "no id",
			rule: `  - profile: qa
    trigger:
      type: run_failed
`,
			want: "automations[1]: each automation requires id",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content := minimalV2WithProfileFiles(t, `agent:
  profiles:
    qa:
      command: claude
automations:
  - id: comment-watch
    profile: qa
    trigger:
      type: tracker_comment_added
`+tc.rule)
			cfg, err := config.Load(workflowWithContent(t, content))
			require.NoError(t, err)
			require.Len(t, cfg.Automations, 2, "the incomplete rule must reach validation, not be dropped by the parser")

			err = config.ValidateDispatch(cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
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

// jiraWorkflow builds a WORKFLOW.md with tracker.kind: jira. Each argument is
// the raw YAML value for that key; an empty string omits the key.
func jiraWorkflow(endpoint, slug, apiKey string) string {
	s := "---\nitervox_schema_version: 2\ntracker:\n  kind: jira\n"
	if endpoint != "" {
		s += "  endpoint: " + endpoint + "\n"
	}
	if slug != "" {
		s += "  project_slug: " + slug + "\n"
	}
	if apiKey != "" {
		s += "  api_key: " + apiKey + "\n"
	}
	return s + "  active_states: [\"To Do\", \"In Progress\"]\n  terminal_states: [Done]\n---\n\nPrompt.\n"
}

func TestValidateAcceptsJiraKind(t *testing.T) {
	path := workflowWithContent(t, jiraWorkflow("https://your-site.atlassian.net", "PROJ", "me@example.com:token123"))
	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.NoError(t, config.ValidateDispatch(cfg))
	assert.True(t, config.TrackerNeedsAPIKey("jira"))
}

func TestJiraRequiresEndpointProjectAndEmailToken(t *testing.T) {
	const okEndpoint, okSlug, okKey = "https://your-site.atlassian.net", "PROJ", "me@example.com:token123"
	tests := []struct {
		name, endpoint, slug, apiKey, wantSetting string
	}{
		{"missing endpoint", "", okSlug, okKey, "tracker.endpoint"},
		{"http endpoint", "http://your-site.atlassian.net", okSlug, okKey, "tracker.endpoint"},
		{"endpoint without host", "https://", okSlug, okKey, "tracker.endpoint"},
		{"endpoint with userinfo", `"https://me%40example.com:SECRETTOKEN@your-site.atlassian.net"`, okSlug, okKey, "tracker.endpoint"},
		{"endpoint with query", `"https://your-site.atlassian.net/?x=1"`, okSlug, okKey, "tracker.endpoint"},
		{"endpoint with fragment", `"https://your-site.atlassian.net/#x"`, okSlug, okKey, "tracker.endpoint"},
		{"endpoint with API path", `"https://your-site.atlassian.net/rest/api/3"`, okSlug, okKey, "tracker.endpoint"},
		{"endpoint with leading space", `" https://your-site.atlassian.net"`, okSlug, okKey, "tracker.endpoint"},
		{"endpoint with trailing space", `"https://your-site.atlassian.net "`, okSlug, okKey, "tracker.endpoint"},
		{"endpoint with inner space", `"https://your site.atlassian.net"`, okSlug, okKey, "tracker.endpoint"},
		{"missing project_slug", okEndpoint, "", okKey, "tracker.project_slug"},
		{"lowercase project_slug", okEndpoint, "proj", okKey, "tracker.project_slug"},
		{"project_slug with quote", okEndpoint, `'PROJ" OR 1=1'`, okKey, "tracker.project_slug"},
		{"api_key without colon", okEndpoint, okSlug, "me@example.comtoken123", "tracker.api_key"},
		{"api_key empty email", okEndpoint, okSlug, `":token123"`, "tracker.api_key"},
		{"api_key email without at", okEndpoint, okSlug, "meexample.com:token123", "tracker.api_key"},
		{"api_key empty token", okEndpoint, okSlug, `"me@example.com:"`, "tracker.api_key"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Load(workflowWithContent(t, jiraWorkflow(tc.endpoint, tc.slug, tc.apiKey)))
			require.NoError(t, err)
			err = config.ValidateDispatch(cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantSetting)
			if tc.wantSetting == "tracker.endpoint" {
				assert.NotContains(t, err.Error(), "SECRETTOKEN")
			}
			if tc.wantSetting == "tracker.api_key" {
				assert.NotContains(t, err.Error(), "token123")
				assert.NotContains(t, err.Error(), "me@example.com")
				assert.NotContains(t, err.Error(), "meexample.com")
			}
		})
	}
}
