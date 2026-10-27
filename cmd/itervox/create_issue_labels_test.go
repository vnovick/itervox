package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/agentactions"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/issuefilter"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
)

// TestCreateIssueActionBodyFlags (#253): the create-issue shim sends
// --label (repeatable) as labels and --blocked-by-source as
// blocked_by_source, and leaves both out when not given.
func TestCreateIssueActionBodyFlags(t *testing.T) {
	body, err := createIssueActionBody([]string{"--title", "Web follow-up", "--body", "B",
		"--label", "repo:web", "--label", "needs-design", "--blocked-by-source"}, io.Discard)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"title": "Web follow-up", "body": "B",
		"labels": []string{"repo:web", "needs-design"}, "blocked_by_source": true}, body)

	body, err = createIssueActionBody([]string{"--title", "Plain"}, io.Discard)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"title": "Plain", "body": ""}, body)

	_, err = createIssueActionBody([]string{"--body", "no title"}, io.Discard)
	require.EqualError(t, err, "--title is required")
	_, err = createIssueActionBody([]string{"--title", "T", "--label", " "}, io.Discard)
	require.Error(t, err, "an empty label is a usage error")

	var help bytes.Buffer
	_, _ = createIssueActionBody([]string{"-h"}, &help)
	assert.Contains(t, help.String(), "-label")
	assert.Contains(t, help.String(), "-blocked-by-source")
}

// TestUpsertProfileKeepsCreateIssueLabels (#253): create_issue_labels round-
// trips through WORKFLOW.md and survives a dashboard profile save, which does
// not edit it.
func TestUpsertProfileKeepsCreateIssueLabels(t *testing.T) {
	dir := t.TempDir()
	workflowPath := filepath.Join(dir, "WORKFLOW.md")
	content := `---
itervox_schema_version: 2
tracker:
  kind: linear
  api_key: key
  project_slug: proj
agent:
  command: claude
  profiles:
    splitter:
      command: claude
      allowed_actions: [create_issue]
      create_issue_state: Todo
      create_issue_labels: [repo:web, "needs design"]
` + testProfileFileFields(t, dir, "splitter") + `---

Prompt.
`
	require.NoError(t, os.WriteFile(workflowPath, []byte(content), 0o644))
	cfg, err := config.Load(workflowPath)
	require.NoError(t, err)
	require.Equal(t, []string{"repo:web", "needs design"}, cfg.Agent.Profiles["splitter"].CreateIssueLabels)

	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := orchestrator.New(cfg, mt, &agenttest.FakeRunner{}, nil)
	adapter := &orchestratorAdapter{orch: orch, cfg: cfg, tr: mt, workflowPath: workflowPath, notify: func() {}}
	require.NoError(t, adapter.UpsertProfile("splitter", server.ProfileDef{
		Command:          "claude --model claude-sonnet-4-6",
		Enabled:          true,
		AllowedActions:   []string{"create_issue"},
		CreateIssueState: "Todo",
	}, "splitter"))

	reloaded, err := config.Load(workflowPath)
	require.NoError(t, err)
	assert.Equal(t, "claude --model claude-sonnet-4-6", reloaded.Agent.Profiles["splitter"].Command, "the save applied")
	assert.Equal(t, []string{"repo:web", "needs design"}, reloaded.Agent.Profiles["splitter"].CreateIssueLabels,
		"create_issue_labels survives a dashboard save")
	assert.Equal(t, []string{"repo:web", "needs design"}, orch.ProfilesCfg()["splitter"].CreateIssueLabels)
}

// recipeWorkflow returns the YAML block of the automations guide's "Work
// across two repositories" recipe whose first line is header.
func recipeWorkflow(t *testing.T, guide, header string) string {
	t.Helper()
	start := strings.Index(guide, "### 9. Work across two repositories")
	require.GreaterOrEqual(t, start, 0, "the recipe section exists")
	section := guide[start:]
	if end := strings.Index(section[1:], "\n## "); end >= 0 {
		section = section[:end+1]
	}
	for _, block := range regexp.MustCompile("(?s)```yaml\n(.*?)```").FindAllStringSubmatch(section, -1) {
		if strings.HasPrefix(block[1], header+"\n") {
			return block[1]
		}
	}
	t.Fatalf("no yaml block starting %q in the recipe", header)
	return ""
}

// writeRecipeWorkflow writes front matter as dir/WORKFLOW.md with the
// SOUL.md / INSTRUCTIONS.md files it names, and loads and validates it the
// way the daemon does at startup.
func writeRecipeWorkflow(t *testing.T, dir, frontMatter string) *config.Config {
	t.Helper()
	for _, m := range regexp.MustCompile(`(?m)^\s+(?:soul|instructions)_file: (\S+)$`).FindAllStringSubmatch(frontMatter, -1) {
		path := filepath.Join(dir, m[1])
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte("# "+filepath.Base(path)+"\n"), 0o644))
	}
	path := filepath.Join(dir, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(path, []byte("---\n"+frontMatter+"---\n\nWork on {{ issue.identifier }}.\n"), 0o644))
	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.NoError(t, config.ValidateDispatch(cfg))
	return cfg
}

// TestCrossRepoRecipeConfigRoutesFollowUp (#253): the two WORKFLOW.md front
// matters of the automations guide's "Work across two repositories" recipe
// load and validate; A's automation runs a profile allowed create_issue with
// the routing label; the follow-up its instructions file — through the shim's
// flags, the agent-action route, the daemon adapter and a tracker — carries
// the label and is blocked by the source; B's tracker filter selects it and
// A's does not (and the reverse for the source issue); and the blocker holds
// it until the source reaches one of B's terminal states.
func TestCrossRepoRecipeConfigRoutesFollowUp(t *testing.T) {
	t.Setenv("LINEAR_API_KEY", "lin-test-"+"placeholder")
	guide := repoFile(t, "site/src/content/docs/guides/automations.mdx")
	cfgA := writeRecipeWorkflow(t, t.TempDir(), recipeWorkflow(t, guide, "# api/WORKFLOW.md (front matter)"))
	cfgB := writeRecipeWorkflow(t, t.TempDir(), recipeWorkflow(t, guide, "# web/WORKFLOW.md (front matter)"))

	// A's automation and the profile it runs.
	require.Len(t, cfgA.Automations, 1)
	rule := cfgA.Automations[0]
	assert.True(t, rule.Enabled)
	assert.Equal(t, config.AutomationTriggerPROpened, rule.Trigger.Type)
	profile, ok := cfgA.Agent.Profiles[rule.Profile]
	require.True(t, ok, "the automation's profile exists")
	assert.Equal(t, []string{config.AgentActionCreateIssue}, profile.AllowedActions)
	require.Equal(t, []string{"repo:web"}, profile.CreateIssueLabels)
	require.NotEmpty(t, profile.CreateIssueState)

	// The create-issue command the instructions give the agent.
	cmdLine := regexp.MustCompile(`itervox action create-issue (.*)`).FindStringSubmatch(rule.Instructions)
	require.NotNil(t, cmdLine, "the instructions name the create-issue command")
	var args []string
	for _, m := range regexp.MustCompile(`--label (\S+)`).FindAllStringSubmatch(cmdLine[1], -1) {
		args = append(args, "--label", m[1])
	}
	require.Contains(t, cmdLine[1], "--blocked-by-source")
	args = append(args, "--blocked-by-source", "--title", "Web: call the new endpoint", "--body", "Depends on the API change.")
	reqBody, err := createIssueActionBody(args, io.Discard)
	require.NoError(t, err)

	// The shared tracker, with A's issue in review after its PR opened.
	source := domain.Issue{ID: "id-api", Identifier: "PLAT-1", Title: "API: add endpoint",
		State: cfgA.Tracker.CompletionState, ProjectSlug: "platform"}
	mt := tracker.NewMemoryTracker([]domain.Issue{source}, cfgA.Tracker.ActiveStates, cfgA.Tracker.TerminalStates)

	// The grant A's worker issues for the splitter run, and the route.
	store := agentactions.NewStore()
	token, err := store.IssueWithScope(source.Identifier, "run-1", profile.AllowedActions, agentactions.Scope{
		CreateIssueState: profile.CreateIssueState, CreateIssueLabels: profile.CreateIssueLabels,
	}, time.Minute)
	require.NoError(t, err)
	srv := server.New(server.Config{
		Snapshot:         func() server.StateSnapshot { return server.StateSnapshot{} },
		RefreshChan:      make(chan struct{}, 1),
		AllowedHosts:     []string{"example.com"},
		Client:           &orchestratorAdapter{cfg: cfgA, tr: mt},
		ActionTokenStore: store,
	})
	payload, err := json.Marshal(reqBody)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent-actions/"+source.Identifier+"/create-issue", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp struct {
		Issue domain.Issue `json:"issue"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	followUp, err := mt.FetchIssueByIdentifier(t.Context(), resp.Issue.Identifier)
	require.NoError(t, err)
	require.NotNil(t, followUp)
	assert.Equal(t, []string{"repo:web"}, followUp.Labels)
	assert.Equal(t, profile.CreateIssueState, followUp.State)

	matches := func(cfg *config.Config, is domain.Issue) bool {
		m, known := cfg.Tracker.EffectiveFilter().Match(is, issuefilter.Env{
			ActiveStates: cfg.Tracker.ActiveStates, WorkingState: cfg.Tracker.WorkingState,
		})
		require.True(t, known)
		return m
	}
	assert.True(t, matches(cfgB, *followUp), "B's filter selects the labelled follow-up")
	assert.False(t, matches(cfgA, *followUp), "A's filter does not")
	assert.True(t, matches(cfgA, source), "A's filter selects its own issue")
	assert.False(t, matches(cfgB, source), "B's filter does not")

	// Blocked by the source until the source is in one of B's terminal states.
	require.Len(t, followUp.BlockedBy, 1)
	require.NotNil(t, followUp.BlockedBy[0].Identifier)
	assert.Equal(t, source.Identifier, *followUp.BlockedBy[0].Identifier)
	require.NotNil(t, followUp.BlockedBy[0].State)
	terminalForB := func(state string) bool {
		return slices.ContainsFunc(cfgB.Tracker.TerminalStates, func(s string) bool { return strings.EqualFold(s, state) })
	}
	assert.False(t, terminalForB(*followUp.BlockedBy[0].State), "the source in review still blocks the follow-up")
	assert.True(t, terminalForB("Done"), "the source reaching Done releases it")
	assert.False(t, cfgB.Dependencies.StackedPRs, "B does not stack on a blocker in review")

	// A label outside the profile's allow-list is refused.
	bad, err := json.Marshal(map[string]any{"title": "T", "labels": []string{"repo:mobile"}})
	require.NoError(t, err)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/agent-actions/"+source.Identifier+"/create-issue", bytes.NewReader(bad))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "label_not_allowed:repo:mobile")
}
