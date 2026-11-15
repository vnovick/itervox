package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/osteele/liquid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/templates/scaffold"
)

// initTrackerKinds are the tracker kinds `itervox init --tracker` accepts.
var initTrackerKinds = []string{"github", "linear", "local"}

// loadTemplateWorkflow writes a starter template's workflow and profile
// files the way `itervox init` does (without network model discovery) and
// loads it. It returns the config and the generated WORKFLOW.md text.
func loadTemplateWorkflow(t *testing.T, trackerKind, runner string, opts initOptions) (*config.Config, string) {
	t.Helper()
	t.Setenv("GITHUB_TOKEN", "test-token")
	t.Setenv("LINEAR_API_KEY", "lin_api_test")
	path := filepath.Join(t.TempDir(), "WORKFLOW.md")
	info := repoInfo{ProjectName: "demo", Owner: "acme", Repo: "demo", DefaultBranch: "main"}
	content := generateWorkflowWith(trackerKind, runner, info, path, opts)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	require.NoError(t, writeTemplateAgentFiles(path, runner, opts))
	cfg, err := config.Load(path)
	require.NoError(t, err)
	return cfg, content
}

func profileNames(cfg *config.Config) []string {
	names := make([]string, 0, len(cfg.Agent.Profiles))
	for name := range cfg.Agent.Profiles {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func automationByID(t *testing.T, cfg *config.Config, id string) config.AutomationConfig {
	t.Helper()
	for _, a := range cfg.Automations {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("automation %q not found", id)
	return config.AutomationConfig{}
}

// TestStarterTemplatesLoadAndValidate (#121): every starter template, and the
// standard scaffold, loads and passes startup validation (agent profiles,
// automations, backend fallback, reviewers) for every tracker kind and both
// runners, with and without the cross-review preset. Every profile has its
// SOUL.md and INSTRUCTIONS.md on disk, every automation in the file survives
// parsing, and every automation's instructions are valid Liquid.
func TestStarterTemplatesLoadAndValidate(t *testing.T) {
	templates := append([]string{""}, scaffold.Names()...)
	for _, tmpl := range templates {
		for _, kind := range initTrackerKinds {
			for _, runner := range []string{"claude", "codex"} {
				for _, preset := range []string{"", presetCrossReview} {
					name := strings.Join([]string{cmpOr(tmpl, "standard"), kind, runner, cmpOr(preset, "no-preset")}, "/")
					t.Run(name, func(t *testing.T) {
						cfg, content := loadTemplateWorkflow(t, kind, runner, initOptions{Template: tmpl, Preset: preset})
						require.NoError(t, config.ValidateDispatch(cfg), "the scaffold must pass startup validation")
						require.NoError(t, config.ValidateAutomationsWithDefaults(cfg.Automations, cfg.Agent.Profiles, cfg.Agent.Command))
						assert.Equal(t, kind, cfg.Tracker.Kind)

						assert.Equal(t, strings.Count(content, "\n  - id: "), len(cfg.Automations), "no automation is dropped while parsing")
						eng := liquid.NewEngine()
						for _, a := range cfg.Automations {
							_, err := eng.ParseTemplate([]byte(a.Instructions))
							assert.NoError(t, err, "automation %s instructions", a.ID)
						}
						for pname, p := range cfg.Agent.Profiles {
							assert.NotEmpty(t, p.Soul, "profile %s SOUL.md", pname)
							assert.NotEmpty(t, p.Instructions, "profile %s INSTRUCTIONS.md", pname)
						}
						assert.Equal(t, runner, agent.BackendFromCommand(cfg.Agent.Profiles["implementer"].Command), "the implementer runs on the chosen runner")

						if preset == presetCrossReview {
							cross := crossReviewerProfile(runner)
							assert.Equal(t, []string{cross, "reviewer"}, cfg.Agent.ReviewerProfiles)
							for _, r := range cfg.Agent.ReviewerProfiles {
								assert.Contains(t, cfg.Agent.Profiles, r, "reviewer %s is a configured profile", r)
							}
							assert.True(t, cfg.Agent.AutoReview)
							assert.Equal(t, otherRunner(runner), agent.BackendFromCommand(cfg.Agent.Profiles[cross].Command))
						}
					})
				}
			}
		}
	}
}

func cmpOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// TestStarterTemplateMinimal (#121): one implementer profile, no reviewer,
// no deps analyzer and no automations.
func TestStarterTemplateMinimal(t *testing.T) {
	cfg, content := loadTemplateWorkflow(t, "github", "claude", initOptions{Template: scaffold.Minimal})
	assert.Equal(t, []string{"implementer"}, profileNames(cfg))
	assert.Empty(t, cfg.Automations)
	assert.Empty(t, cfg.Agent.ReviewerProfile)
	assert.Empty(t, cfg.Agent.DepsAnalyzerProfile)
	assert.False(t, cfg.Agent.AutoReview)
	assert.False(t, cfg.Agent.BackendFallback.Enabled)
	assert.NotContains(t, content, "reviewer_prompt:")
}

// TestStarterTemplateFull (#121): implementer, reviewer, input-responder and
// deps analyzer; an auto-review chain; input-required and failed-run
// automations.
func TestStarterTemplateFull(t *testing.T) {
	cfg, _ := loadTemplateWorkflow(t, "linear", "codex", initOptions{Template: scaffold.Full})
	assert.Equal(t, []string{"deps-analyzer", "implementer", "input-responder", "reviewer"}, profileNames(cfg))
	assert.Equal(t, "deps-analyzer", cfg.Agent.DepsAnalyzerProfile)
	assert.Equal(t, "reviewer", cfg.Agent.ReviewerProfile)
	assert.Equal(t, []string{"reviewer"}, cfg.Agent.ReviewerProfiles)
	assert.Equal(t, config.ReviewQuorumAnyBlock, cfg.Agent.ReviewQuorum)
	assert.True(t, cfg.Agent.AutoReview, "the review chain runs after every successful run")

	responder := automationByID(t, cfg, "input-responder")
	assert.Equal(t, config.AutomationTriggerInputRequired, responder.Trigger.Type)
	assert.Equal(t, "input-responder", responder.Profile)
	assert.True(t, responder.Policy.AutoResume)
	triage := automationByID(t, cfg, "failure-triage")
	assert.Equal(t, config.AutomationTriggerRunFailed, triage.Trigger.Type)
	assert.Contains(t, triage.Instructions, "{{ trigger.error_message }}")
	assert.Len(t, cfg.Automations, 2)
}

// TestStarterTemplateRateLimitFallback (#121): an implementer on each
// backend, agent.backend_fallback mapping the implementer (and runs without
// a profile) to the other backend, and a rate_limited switch automation.
func TestStarterTemplateRateLimitFallback(t *testing.T) {
	for _, runner := range []string{"claude", "codex"} {
		t.Run(runner, func(t *testing.T) {
			other := otherRunner(runner)
			fallback := "implementer-" + other
			cfg, _ := loadTemplateWorkflow(t, "local", runner, initOptions{Template: scaffold.RateLimitFallback})
			assert.Equal(t, []string{"implementer", fallback}, profileNames(cfg))
			assert.Equal(t, other, agent.BackendFromCommand(cfg.Agent.Profiles[fallback].Command))

			fb := cfg.Agent.BackendFallback
			assert.True(t, fb.Enabled)
			assert.Equal(t, []string{runner, other}, fb.Chain)
			assert.Equal(t, fallback, fb.ProfileMap["implementer"][other])
			assert.Equal(t, fallback, fb.ProfileMap[config.BackendFallbackDefaultProfileKey][other])
			assert.Equal(t, config.BackendFallbackSwitchBackAtReset, fb.SwitchBack)

			sw := automationByID(t, cfg, "rate-limit-switch")
			assert.Equal(t, config.AutomationTriggerRateLimited, sw.Trigger.Type)
			assert.Equal(t, fallback, sw.Profile)
			assert.Equal(t, fallback, sw.Policy.SwitchToProfile)
			assert.Equal(t, other, sw.Policy.SwitchToBackend)
			assert.True(t, sw.Policy.AutoResume, "auto_switch")
			assert.Equal(t, 30, sw.Policy.CooldownMinutes)
			assert.Len(t, cfg.Automations, 1)
		})
	}
}

// TestStarterTemplatePRReview (#121): a run reviewer on auto-review and a
// read-only pull-request reviewer fired by pr_opened and by a "/review"
// comment.
func TestStarterTemplatePRReview(t *testing.T) {
	cfg, _ := loadTemplateWorkflow(t, "github", "claude", initOptions{Template: scaffold.PRReview})
	assert.Equal(t, []string{"implementer", "pr-reviewer", "reviewer"}, profileNames(cfg))
	assert.True(t, cfg.Agent.AutoReview)
	assert.Equal(t, []string{"reviewer"}, cfg.Agent.ReviewerProfiles)
	assert.Contains(t, cfg.Agent.Profiles["pr-reviewer"].AllowedActions, "comment_pr")
	assert.Contains(t, cfg.Agent.Profiles["pr-reviewer"].Instructions, "Do not edit files, commit, push, merge or move the issue")

	opened := automationByID(t, cfg, "review-opened-pr")
	assert.Equal(t, config.AutomationTriggerPROpened, opened.Trigger.Type)
	assert.Equal(t, "pr-reviewer", opened.Profile)
	assert.Contains(t, opened.Instructions, "{{ trigger.pr_url }}")
	onRequest := automationByID(t, cfg, "review-on-request")
	assert.Equal(t, config.AutomationTriggerTrackerComment, onRequest.Trigger.Type)
	assert.Equal(t, "pr-reviewer", onRequest.Profile)
	assert.Equal(t, `^\s*/review\b`, onRequest.Filter.BodyRegex)
	assert.Len(t, cfg.Automations, 2)
}

// TestStarterTemplateDailyQA (#121): a qa profile run by a daily cron
// automation over issues in the tracker's completion state.
func TestStarterTemplateDailyQA(t *testing.T) {
	for kind, state := range map[string]string{"github": "in-review", "linear": "In Review", "local": "In Review"} {
		t.Run(kind, func(t *testing.T) {
			cfg, _ := loadTemplateWorkflow(t, kind, "claude", initOptions{Template: scaffold.DailyQA})
			assert.Equal(t, []string{"implementer", "qa"}, profileNames(cfg))
			assert.Equal(t, state, cfg.Tracker.CompletionState)
			qa := automationByID(t, cfg, "daily-qa")
			assert.Equal(t, config.AutomationTriggerCron, qa.Trigger.Type)
			assert.Equal(t, "0 9 * * *", qa.Trigger.Cron)
			assert.Equal(t, "qa", qa.Profile)
			assert.Equal(t, []string{cfg.Tracker.CompletionState}, qa.Filter.States)
			assert.Contains(t, cfg.Agent.Profiles["qa"].Instructions, "acceptance criterion")
			assert.Len(t, cfg.Automations, 1)
		})
	}
}

// TestStarterTemplatesDiffer (#121): no two templates (or a template and
// the standard scaffold) write the same workflow or the same profile set
// and automations.
func TestStarterTemplatesDiffer(t *testing.T) {
	seen := map[string]string{}
	for _, tmpl := range append([]string{""}, scaffold.Names()...) {
		cfg, _ := loadTemplateWorkflow(t, "github", "claude", initOptions{Template: tmpl})
		var ids []string
		for _, a := range cfg.Automations {
			ids = append(ids, a.ID)
		}
		key := strings.Join(profileNames(cfg), ",") + "|" + strings.Join(ids, ",")
		if prev, dup := seen[key]; dup {
			t.Errorf("template %q scaffolds the same profiles and automations as %q", cmpOr(tmpl, "standard"), prev)
		}
		seen[key] = cmpOr(tmpl, "standard")
	}
}

// TestInitTemplateHelpListsEachTemplate (#121): `itervox init --help` and the
// CLI reference list every template with its one-line summary.
func TestInitTemplateHelpListsEachTemplate(t *testing.T) {
	usage := templateFlagUsage()
	lines := strings.Split(usage, "\n")
	assert.Len(t, lines, 1+len(scaffold.KnownTemplates), "an intro line plus one line per template")
	cli, err := os.ReadFile(filepath.Join("..", "..", "site", "src", "content", "docs", "cli.mdx"))
	require.NoError(t, err)
	for i, name := range scaffold.KnownTemplates {
		assert.Equal(t, strings.Join(strings.Fields(name+" "+scaffold.Summary(name)), " "), strings.Join(strings.Fields(lines[i+1]), " "), "help line for %s", name)
		assert.Contains(t, string(cli), "| `"+name+"` | "+scaffold.Summary(name)+" |", "CLI reference row for %s", name)
	}
}

// TestScaffoldWorkflowUsesTemplate (#121): the init path (scaffoldWorkflow,
// which `itervox init --template` calls) writes the chosen template's
// workflow and profile files, not the standard scaffold.
func TestScaffoldWorkflowUsesTemplate(t *testing.T) {
	origClaude, origCodex := listClaudeModels, listCodexModels
	listClaudeModels = func() []agent.ModelOption { return nil }
	listCodexModels = func() []agent.ModelOption { return nil }
	t.Cleanup(func() { listClaudeModels, listCodexModels = origClaude, origCodex })

	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	var out strings.Builder
	require.NoError(t, scaffoldWorkflow(path, dir, "local", "claude", initOptions{Template: scaffold.DailyQA}, &out))
	assert.Contains(t, out.String(), "starter template daily-qa: "+scaffold.Summary(scaffold.DailyQA))
	assert.FileExists(t, filepath.Join(dir, ".itervox", "agents", "qa", "SOUL.md"))
	assert.FileExists(t, filepath.Join(dir, ".itervox", "agents", "qa", "INSTRUCTIONS.md"))
	assert.NoDirExists(t, filepath.Join(dir, ".itervox", "agents", "reviewer"), "daily-qa has no reviewer")

	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.NoError(t, config.ValidateDispatch(cfg))
	assert.Equal(t, "daily-qa", automationByID(t, cfg, "daily-qa").ID)
	assert.NotContains(t, cfg.Agent.Profiles, "reviewer")
}

// generateWorkflow builds the standard scaffold's WORKFLOW.md (no template,
// no preset).
func generateWorkflow(trackerKind, runner string, info repoInfo, workflowPath string) string {
	return generateWorkflowWith(trackerKind, runner, info, workflowPath, initOptions{})
}

// writeInitAgentFiles writes the standard scaffold's profile files.
func writeInitAgentFiles(workflowPath, runner string) error {
	return writeTemplateAgentFiles(workflowPath, runner, initOptions{})
}
