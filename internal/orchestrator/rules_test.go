package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/logbuffer"
	"github.com/vnovick/itervox/internal/rules"
	"github.com/vnovick/itervox/internal/tracker"
)

// rulesProject lays out a project named "acme-web" with the three rules
// layers (the user layer through ITERVOX_USER_RULES) and returns its
// WORKFLOW.md path.
func rulesProject(t *testing.T, user, project, local string) string {
	t.Helper()
	root := t.TempDir()
	wf := filepath.Join(root, "acme-web", "WORKFLOW.md")
	userPath := filepath.Join(root, "home", "rules.json")
	t.Setenv(rules.UserRulesEnv, userPath)
	for path, body := range map[string]string{
		wf:                    "---\n---\n",
		userPath:              user,
		rules.ProjectPath(wf): project,
		rules.LocalPath(wf):   local,
	} {
		if body == "" {
			continue
		}
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	}
	return wf
}

const (
	userRules = `// user layer
{"rules": [
  {"id": "small-prs", "severity": "should", "text": "U-SHOULD keep pull requests small."},
  {"id": "reviewers-only", "severity": "must", "text": "U-REVIEWER-ONLY.", "scope": {"profiles": ["reviewer"]}},
]}`
	projectRules = `{"rules": [
  {"id": "pnpm", "severity": "must", "text": "P-MUST use pnpm."},
  {"id": "codex-only", "severity": "must", "text": "P-CODEX-ONLY.", "scope": {"backends": ["codex"]}},
  {"id": "billing-only", "severity": "must", "text": "P-BILLING-ONLY.", "scope": {"projects": ["billing"]}},
]}`
	localRules = `{"rules": [
  {"id": "port", "severity": "must", "text": "L-MUST dev server on 5174.", "scope": {"profiles": ["implementer"], "projects": ["acme-*"]}},
]}`
)

// renderRulesPrompt runs the real runWorker once for profile on backend and
// returns the first turn's prompt.
func renderRulesPrompt(t *testing.T, wf, template, profile, backend string, p config.AgentProfile) string {
	t.Helper()
	cfg := automationBaseCfg()
	cfg.WorkflowPath = wf
	cfg.PromptTemplate = template
	cfg.Agent.MaxTurns = 1
	cfg.Agent.Profiles[profile] = p
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "T", State: "In Progress"}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	runner := &promptCapturingRunner{}
	o := New(cfg, mt, runner, &stubWorkspaceProvider{path: t.TempDir()})
	o.events = make(chan OrchestratorEvent, 64)
	o.workersWg.Add(issue.Identifier)
	o.runWorker(t.Context(), issue, 0, "", backend, backend, profile, true, nil, nil, nil)
	runner.mu.Lock()
	defer runner.mu.Unlock()
	require.NotEmpty(t, runner.prompts)
	return runner.prompts[0]
}

var implementerProfile = config.AgentProfile{Command: "claude", Soul: "SOUL-MARK", Instructions: "INSTRUCTIONS-MARK"}

// TestWorkerPromptCarriesLayeredRulesInScopeAheadOfSoul (#157): rules from
// the user, project and project-local files appear in the prompt as one
// `## Rules` block, must rules first, ahead of SOUL.md and INSTRUCTIONS.md,
// with rules out of scope for the project, profile and backend dropped.
func TestWorkerPromptCarriesLayeredRulesInScopeAheadOfSoul(t *testing.T) {
	wf := rulesProject(t, userRules, projectRules, localRules)
	p := renderRulesPrompt(t, wf, "MAIN {{ issue.identifier }}", "implementer", "claude", implementerProfile)

	require.Equal(t, 1, strings.Count(p, "## Rules"), p)
	at := func(s string) int {
		i := strings.Index(p, s)
		require.GreaterOrEqual(t, i, 0, "%q missing from prompt:\n%s", s, p)
		return i
	}
	rulesAt, pnpm, port, small := at("## Rules"), at("P-MUST use pnpm."), at("L-MUST dev server"), at("U-SHOULD")
	soul, instructions := at("SOUL-MARK"), at("INSTRUCTIONS-MARK")
	assert.Less(t, rulesAt, pnpm)
	assert.Less(t, pnpm, port, "project before project-local within the must rules")
	assert.Less(t, port, small, "must rules before should rules")
	assert.Less(t, small, soul, "the rules block comes ahead of SOUL.md")
	assert.Less(t, soul, instructions)
	assert.Less(t, at("MAIN ENG-1"), rulesAt)
	for _, out := range []string{"P-CODEX-ONLY", "P-BILLING-ONLY", "U-REVIEWER-ONLY"} {
		assert.NotContains(t, p, out, "out-of-scope rule %s", out)
	}

	codex := renderRulesPrompt(t, wf, "MAIN", "implementer", "codex", implementerProfile)
	assert.Contains(t, codex, "P-CODEX-ONLY", "a backend-scoped rule reaches that backend")

	other := renderRulesPrompt(t, wf, "MAIN", "writer", "claude", config.AgentProfile{Command: "claude", Soul: "SOUL-MARK"})
	assert.NotContains(t, other, "L-MUST", "a profile-scoped rule stays with its profile")
	assert.Contains(t, other, "P-MUST use pnpm.")
}

// TestReviewerPromptCarriesRules (#157): reviewers get the rules too, scoped
// to the reviewer profile.
func TestReviewerPromptCarriesRules(t *testing.T) {
	wf := rulesProject(t, userRules, projectRules, "")
	cfg := automationBaseCfg()
	cfg.WorkflowPath = wf
	cfg.Agent.MaxTurns = 1
	cfg.Agent.ReviewerProfile = "reviewer"
	cfg.Agent.ReviewerPrompt = "REVIEW {{ issue.identifier }}"
	cfg.Agent.Profiles["reviewer"] = config.AgentProfile{Command: "claude", Soul: "REVIEWER-SOUL"}
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "T", State: "In Review"}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	runner := &promptCapturingRunner{}
	o := New(cfg, mt, runner, &stubWorkspaceProvider{path: t.TempDir()})
	o.events = make(chan OrchestratorEvent, 64)
	o.workersWg.Add(issue.Identifier)
	o.runWorker(t.Context(), issue, 0, "", "claude", "claude", "reviewer", true, nil, nil, nil)
	runner.mu.Lock()
	p := runner.prompts[0]
	runner.mu.Unlock()
	assert.Contains(t, p, "REVIEW ENG-1")
	assert.Contains(t, p, "U-REVIEWER-ONLY.")
	assert.Less(t, strings.Index(p, "## Rules"), strings.Index(p, "REVIEWER-SOUL"))
}

// TestTemplatePlacingRulesGetsNoAutomaticBlock (#157): a template that
// prints `{{ rules }}` (or `{{ rules.must }}`) places the rules itself, and
// the daemon adds no second block.
func TestTemplatePlacingRulesGetsNoAutomaticBlock(t *testing.T) {
	wf := rulesProject(t, userRules, projectRules, localRules)

	p := renderRulesPrompt(t, wf, "HEAD\n{{ rules }}\nTAIL", "implementer", "claude", implementerProfile)
	assert.Equal(t, 1, strings.Count(p, "## Rules"), "exactly one rules block:\n%s", p)
	assert.Equal(t, 1, strings.Count(p, "P-MUST use pnpm."))
	assert.Less(t, strings.Index(p, "HEAD"), strings.Index(p, "## Rules"))
	assert.Less(t, strings.Index(p, "## Rules"), strings.Index(p, "TAIL"), "the template decides where the rules go")

	must := renderRulesPrompt(t, wf, "HEAD\n{{ rules.must }}\nTAIL", "implementer", "claude", implementerProfile)
	assert.NotContains(t, must, "## Rules", "rules.must alone suppresses the automatic block")
	assert.Contains(t, must, "P-MUST use pnpm.")
	assert.NotContains(t, must, "U-SHOULD")

	inSoul := renderRulesPrompt(t, wf, "MAIN", "implementer", "claude",
		config.AgentProfile{Command: "claude", Soul: "SOUL {{ rules.should }}"})
	assert.NotContains(t, inSoul, "## Rules", "a profile file can place the rules too")
	assert.Contains(t, inSoul, "SOUL - U-SHOULD")
}

// TestRunRecordNamesRuleSetAndRereadsRulesPerDispatch (#157): each run's
// record names the rule set it used (hash and ids), and the rules files are
// read again at every dispatch, so an edit reaches the next run without a
// restart.
func TestRunRecordNamesRuleSetAndRereadsRulesPerDispatch(t *testing.T) {
	wf := rulesProject(t, "", projectRules, "")
	cfg := automationBaseCfg()
	cfg.WorkflowPath = wf
	o := New(cfg, nil, nil, nil)
	state := NewState(cfg)
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1"}

	first := &RunEntry{Issue: issue, ProfileName: "implementer", Backend: "claude", StartedAt: time.Now()}
	_ = o.startRun(t.Context(), &state, first)
	assert.Equal(t, []string{"pnpm"}, first.RuleIDs)
	assert.True(t, strings.HasPrefix(first.RulesHash, "sha256:"))
	assert.Equal(t, first.RulesHash, state.LatestRun["id1"].RulesHash)
	assert.Equal(t, first.RuleIDs, state.LatestRun["id1"].RuleIDs)

	o.recordHistory(first, issue, time.Now(), "succeeded")
	hist := o.RunHistory()
	require.NotEmpty(t, hist)
	assert.Equal(t, first.RulesHash, hist[len(hist)-1].RulesHash, "the completed run names its rule set")
	assert.Equal(t, []string{"pnpm"}, hist[len(hist)-1].RuleIDs)

	require.NoError(t, os.WriteFile(rules.LocalPath(wf),
		[]byte(`{"rules":[{"id":"fresh","severity":"should","text":"Added while running."}]}`), 0o644))
	second := &RunEntry{Issue: issue, ProfileName: "implementer", Backend: "claude", StartedAt: time.Now()}
	_ = o.startRun(t.Context(), &state, second)
	assert.Equal(t, []string{"pnpm", "fresh"}, second.RuleIDs, "the edit applies to the next dispatch")
	assert.NotEqual(t, first.RulesHash, second.RulesHash)
}

// TestRuleConflictsAreReportedInTheIssueLog (#157): a project rule that tries
// to replace a user must rule is dropped, and the run's log says so.
func TestRuleConflictsAreReportedInTheIssueLog(t *testing.T) {
	wf := rulesProject(t,
		`{"rules":[{"id":"pnpm","severity":"must","text":"USER-PNPM."}]}`,
		`{"rules":[{"id":"pnpm","severity":"should","text":"PROJECT-NPM."}]}`, "")
	p := renderRulesPrompt(t, wf, "MAIN", "implementer", "claude", implementerProfile)
	assert.Contains(t, p, "USER-PNPM.")
	assert.NotContains(t, p, "PROJECT-NPM.")

	o := &Orchestrator{logBuf: logbuffer.New()}
	set := rules.Resolve(wf, rules.Target{})
	require.Len(t, set.Conflicts, 1)
	o.reportRuleProblems(set, "ENG-1", "run-1")
	logged := strings.Join(o.logBuf.Get("ENG-1"), "\n")
	assert.Contains(t, logged, `the project rule \"pnpm\" was ignored`, logged)
}
