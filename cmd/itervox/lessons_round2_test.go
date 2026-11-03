package main

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/memory"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/rules"
	"github.com/vnovick/itervox/internal/server"
)

// TestPromoteLessonToRuleArchivesAndLinks (#217): POST
// /api/v1/memory/{id}/promote-to-rule adds an approved lesson as a rule in
// the chosen layer (id lesson-<entry id>, the lesson's text, the chosen
// severity, scoped to the lesson's profiles, a description linking back to
// the lesson) and archives the lesson with a pointer to the rule, so runs
// get the rule instead of the memory. A proposed lesson cannot be promoted
// (409), nor can the archived one again; only the user and project layers
// are accepted (404 otherwise).
func TestPromoteLessonToRuleArchivesAndLinks(t *testing.T) {
	f := newMemoryAPIFixture(t)
	t.Setenv(rules.UserRulesEnv, filepath.Join(t.TempDir(), "rules.json"))
	lesson := proposeTestLesson(t, f.wf, "Don't add a CSV dependency; use the csv helper instead.")
	proposed := proposeTestLesson(t, f.wf, "Never log the API token; redact it.")
	store := memory.Store{WorkflowPath: f.wf}
	approved, err := store.ApproveLesson(lesson.ID, memory.ApproveOptions{Scope: memory.ScopeProject})
	require.NoError(t, err)
	_, err = store.Update(lesson.ID, memory.Patch{Profiles: &[]string{"coder"}})
	require.NoError(t, err)

	path := "/api/v1/memory/" + lesson.ID + "/promote-to-rule"
	f.do(t, http.MethodPost, "/api/v1/memory/"+proposed.ID+"/promote-to-rule", map[string]any{"layer": "project"}, http.StatusConflict, nil)
	f.do(t, http.MethodPost, path, map[string]any{"layer": "local"}, http.StatusNotFound, nil)
	f.do(t, http.MethodPost, path, map[string]any{"layer": "project", "severity": "maybe"}, http.StatusBadRequest, nil)

	var res server.LessonRuleResult
	f.do(t, http.MethodPost, path, map[string]any{"layer": "project", "severity": "must"}, http.StatusOK, &res)
	assert.Equal(t, "project", res.Layer)
	assert.Equal(t, rules.ProjectPath(f.wf), res.Path)
	wantID := "lesson-" + lesson.ID
	assert.Equal(t, wantID, res.Rule.ID)

	layer := rules.ReadLayer(rules.LayerProject, rules.ProjectPath(f.wf))
	require.NoError(t, layer.Err)
	require.Len(t, layer.Rules, 1, "the rule is in the project layer's file")
	r := layer.Rules[0]
	assert.Equal(t, wantID, r.ID)
	assert.Equal(t, rules.SeverityMust, r.Severity)
	assert.Equal(t, approved.Text, r.Text)
	assert.Contains(t, r.Description, lesson.ID, "the rule links back to the lesson")
	assert.Contains(t, r.Description, "ENG-1")
	assert.Equal(t, []string{"coder"}, r.Scope.Profiles, "scoped like the lesson")

	got, err := store.Get(lesson.ID)
	require.NoError(t, err)
	assert.Equal(t, memory.StatusArchived, got.Status, "the lesson is archived")
	require.NotNil(t, got.Lesson.Rule)
	assert.Equal(t, memory.RuleLink{Layer: "project", ID: wantID, At: got.Lesson.Rule.At}, *got.Lesson.Rule, "with a pointer to the rule")
	assert.False(t, got.Lesson.Rule.At.IsZero())
	assert.Equal(t, memory.StatusArchived, res.Entry.Status)

	set := rules.Resolve(f.wf, rules.Target{Profile: "coder", Backend: "claude"})
	assert.Contains(t, set.IDs(), wantID, "runs of the profile get the rule")
	sel, _ := memory.Resolve(f.wf, memory.Target{Profile: "coder", Title: "CSV export"}, 2000)
	assert.NotContains(t, sel.IDs(), lesson.ID, "and no longer the archived lesson")

	f.do(t, http.MethodPost, path, map[string]any{"layer": "project"}, http.StatusConflict, nil)
}

// TestPromoteLessonToRuleRetryAfterPartialWrite (#217): a promotion whose
// rule was written but whose lesson was not archived (a crash between the
// two writes) completes on retry without a duplicate rule; a different rule
// under the same id is a conflict.
func TestPromoteLessonToRuleRetryAfterPartialWrite(t *testing.T) {
	f := newMemoryAPIFixture(t)
	lesson := proposeTestLesson(t, f.wf, "Always regenerate the API client after editing the schema.")
	store := memory.Store{WorkflowPath: f.wf}
	approved, err := store.ApproveLesson(lesson.ID, memory.ApproveOptions{})
	require.NoError(t, err)
	path := rules.ProjectPath(f.wf)
	require.NoError(t, rules.SaveRules(path, []rules.Rule{{ID: lessonRuleID(lesson.ID), Severity: rules.SeverityShould, Text: approved.Text}}, ""))

	a := &memoryAdapter{cfg: &config.Config{WorkflowPath: f.wf}}
	res, err := a.PromoteLessonToRule(lesson.ID, server.LessonRuleRequest{Layer: "project"})
	require.NoError(t, err)
	assert.Equal(t, memory.StatusArchived, res.Entry.Status)
	assert.Len(t, rules.ReadLayer(rules.LayerProject, path).Rules, 1, "no duplicate rule")

	other := proposeTestLesson(t, f.wf, "Never commit generated files without regenerating them first.")
	_, err = store.ApproveLesson(other.ID, memory.ApproveOptions{})
	require.NoError(t, err)
	layer := rules.ReadLayer(rules.LayerProject, path)
	list := append(layer.Rules, rules.Rule{ID: lessonRuleID(other.ID), Severity: rules.SeverityShould, Text: "something else"})
	require.NoError(t, rules.SaveRules(path, list, layer.Version))
	_, err = a.PromoteLessonToRule(other.ID, server.LessonRuleRequest{Layer: "project"})
	require.ErrorIs(t, err, memory.ErrConflict)
}

// fakeEnforcer stands in for the orchestrator's enforcement accept/dismiss.
type fakeEnforcer struct {
	wf  string
	err error
}

func (f *fakeEnforcer) AcceptLessonEnforcement(_ context.Context, id string) (memory.Entry, *domain.Issue, error) {
	if f.err != nil {
		return memory.Entry{}, nil, f.err
	}
	e, err := memory.Store{WorkflowPath: f.wf}.DecideEnforcement(id, true, "ENG-9", "https://tracker/ENG-9")
	if err != nil {
		return memory.Entry{}, nil, err
	}
	url := "https://tracker/ENG-9"
	return e, &domain.Issue{Identifier: "ENG-9", Title: memory.EnforcementIssueTitle(e), URL: &url}, nil
}

func (f *fakeEnforcer) DismissLessonEnforcement(id string) (memory.Entry, error) {
	return memory.Store{WorkflowPath: f.wf}.DecideEnforcement(id, false, "", "")
}

// TestLessonEnforcementAPI (#217): POST
// /api/v1/memory/{id}/enforcement/accept returns the lesson with its
// accepted suggestion and the issue created; a lesson with no pending
// suggestion is 409, a tracker failure 502; GET /api/v1/memory carries the
// threshold, each lesson's analytics and the "enforced" state.
func TestLessonEnforcementAPI(t *testing.T) {
	f := newMemoryAPIFixture(t)
	lesson := proposeTestLesson(t, f.wf, "Don't add a CSV dependency; use the csv helper instead.")
	store := memory.Store{WorkflowPath: f.wf}
	_, err := store.ApproveLesson(lesson.ID, memory.ApproveOptions{})
	require.NoError(t, err)
	enf := &fakeEnforcer{wf: f.wf}
	cfg := &config.Config{WorkflowPath: f.wf, Memory: config.MemoryConfig{Enabled: true, Lessons: config.LessonsConfig{Enabled: true, StaleAfterRuns: 20, EnforceThreshold: 3}}}
	adapter := &memoryAdapter{cfg: cfg, enforcement: enf, history: func() []orchestrator.CompletedRun {
		return []orchestrator.CompletedRun{
			{Identifier: "ENG-2", Status: "succeeded", MemoryIDs: []string{lesson.ID}},
			{Identifier: "ENG-3", Status: "failed", MemoryIDs: []string{lesson.ID}},
			{Identifier: "ENG-4", Status: "succeeded"},
		}
	}}
	srv := server.New(server.Config{
		Snapshot: func() server.StateSnapshot { return server.StateSnapshot{} }, RefreshChan: make(chan struct{}, 1),
		Memory: adapter, AllowedHosts: []string{"example.com"},
	})
	do := func(path string, want int, out any) {
		t.Helper()
		f.srv = srv
		f.do(t, http.MethodPost, path, nil, want, out)
	}

	do("/api/v1/memory/"+lesson.ID+"/enforcement/accept", http.StatusConflict, nil)
	_, ok, err := store.SuggestEnforcement(lesson.ID, memory.TemplateEnforcement(lesson))
	require.NoError(t, err)
	require.True(t, ok)

	enf.err = errors.New("tracker down")
	do("/api/v1/memory/"+lesson.ID+"/enforcement/accept", http.StatusBadGateway, nil)
	enf.err = nil
	var res server.LessonEnforceResult
	do("/api/v1/memory/"+lesson.ID+"/enforcement/accept", http.StatusOK, &res)
	assert.Equal(t, "ENG-9", res.Issue.Identifier)
	assert.Equal(t, "https://tracker/ENG-9", res.Issue.URL)
	assert.Equal(t, memory.EnforcementAccepted, res.Entry.Lesson.Enforcement.Status)
	do("/api/v1/memory/"+lesson.ID+"/enforcement/dismiss", http.StatusConflict, nil)

	var view server.MemoryView
	f.do(t, http.MethodGet, "/api/v1/memory", nil, http.StatusOK, &view)
	assert.Equal(t, 3, view.Lessons.EnforceThreshold)
	require.Len(t, view.Entries, 1)
	e := view.Entries[0]
	assert.Equal(t, memory.LessonEnforced, e.NotHelping)
	require.NotNil(t, e.Analytics)
	assert.Equal(t, memory.LessonAnalytics{Runs: 2, Succeeded: 1, Failed: 1}, *e.Analytics)
}

// TestLessonRevertScanReadsRevertedPRs (#217): the orchestrator's
// RevertScan hook reuses #201's revert detection on the base branch, read
// through gitexec: the hotspot fixture's reverted PR #7 comes back with
// the revert's subject. A repository without the base branch scans nothing.
func TestLessonRevertScanReadsRevertedPRs(t *testing.T) {
	repo := hotspotFixture(t)
	cfg := &config.Config{}
	cfg.Workspace.BaseBranch = "main"
	got, err := lessonRevertScan(cfg, filepath.Join(repo, "WORKFLOW.md"))(t.Context())
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, 7, got[0].Number)
	assert.Equal(t, `Revert "Add the guide (#7)"`, got[0].Subject)

	cfg.Workspace.BaseBranch = "trunk"
	got, err = lessonRevertScan(cfg, filepath.Join(repo, "WORKFLOW.md"))(t.Context())
	require.NoError(t, err)
	assert.Empty(t, got)
}
