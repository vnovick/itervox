package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// fakeAssigneeScoper is a project manager with an assignee scope (#182).
type fakeAssigneeScoper struct {
	fakeProjectManager
	resolveErr error
	resolved   int
}

func (f *fakeAssigneeScoper) ResolveAssignee(context.Context) (tracker.AssigneeUser, error) {
	f.resolved++
	if f.resolveErr != nil {
		return tracker.AssigneeUser{}, f.resolveErr
	}
	return tracker.AssigneeUser{ID: "u-1", Name: "Itervox Bot"}, nil
}
func (f *fakeAssigneeScoper) AssigneeSpec() string { return "me" }
func (f *fakeAssigneeScoper) ResolvedAssignee() (tracker.AssigneeUser, bool) {
	return tracker.AssigneeUser{ID: "u-1", Name: "Itervox Bot"}, f.resolved > 0 && f.resolveErr == nil
}
func (f *fakeAssigneeScoper) ScopeKey() string   { return strings.Join(f.GetProjectFilter(), ",") }
func (f *fakeAssigneeScoper) ScopeLabel() string { return "scope " + f.ScopeKey() }

func validWorkflow(t *testing.T, path string) {
	t.Helper()
	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.NoError(t, config.ValidateDispatch(cfg), "the saved WORKFLOW.md names exactly one scope")
}

// TestProjectPickerAssigneeScopeSwitch (#182): choosing the picker's
// "assigned to" entry resolves the user first, writes tracker.assignee and
// comments project_slug out; choosing a project does the reverse. The file
// always names exactly one scope, and an assignee that does not resolve is
// refused with file and memory unchanged.
func TestProjectPickerAssigneeScopeSwitch(t *testing.T) {
	path := writeFixture(t, reloadFenceFixture)
	_, gen, err := loadSettingsGeneration(path)
	require.NoError(t, err)
	fpm := &fakeAssigneeScoper{}
	m := &linearProjectManager{pm: fpm, workflowPath: path, settingsGen: gen}

	require.NoError(t, m.SetProjectFilter([]string{"alpha", tracker.AssigneeScopeFilter}))
	assert.Equal(t, 1, fpm.resolved, "resolved before saving")
	assert.Equal(t, []string{tracker.AssigneeScopeFilter}, fpm.GetProjectFilter(), "the assignee scope stands alone")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "\n  assignee: me\n")
	assert.Contains(t, string(data), "\n  # project_slug: proj\n")
	validWorkflow(t, path)

	require.NoError(t, m.SetProjectFilter([]string{"alpha"}))
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "\n  project_slug: alpha\n")
	assert.Contains(t, string(data), "\n  # assignee: me\n")
	validWorkflow(t, path)

	require.NoError(t, m.SetProjectFilter([]string{tracker.AssigneeScopeFilter}))
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(data), "assignee: me"), "the commented line is reused")
	validWorkflow(t, path)

	before := string(data)
	fpm.resolveErr = tracker.ErrAssigneeNotFound
	fpm.SetProjectFilter([]string{"alpha"})
	err = m.SetProjectFilter([]string{tracker.AssigneeScopeFilter})
	require.ErrorIs(t, err, tracker.ErrAssigneeNotFound)
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, string(data), "an unresolvable assignee writes nothing")
	assert.Equal(t, []string{"alpha"}, fpm.GetProjectFilter(), "and changes no filter")
}

// assigneePollFake is a poll tracker with an assignee scope: only the issues
// assigned to the current assignee are visible.
type assigneePollFake struct {
	*pollFake
	fakeAssigneeScoper
	amu      sync.Mutex
	assignee string
	owner    map[string]string // issue ID → assignee
}

func (f *assigneePollFake) setAssignee(a string) {
	f.amu.Lock()
	defer f.amu.Unlock()
	f.assignee = a
}

func (f *assigneePollFake) ScopeKey() string {
	f.amu.Lock()
	defer f.amu.Unlock()
	return "assignee:" + f.assignee
}

func (f *assigneePollFake) FetchIssuesByStates(ctx context.Context, states []string) ([]domain.Issue, error) {
	issues, err := f.pollFake.FetchIssuesByStates(ctx, states)
	f.amu.Lock()
	defer f.amu.Unlock()
	return slices.DeleteFunc(issues, func(i domain.Issue) bool { return f.owner[i.ID] != f.assignee }), err
}

// TestAutomationPollAssigneeScopeChangeDoesNotFire (#182): the automation
// poll's scope key includes the assignee, so switching scope rebaselines:
// issues that only became visible do not fire a "moved into state" rule; a
// real move afterwards still does.
func TestAutomationPollAssigneeScopeChangeDoesNotFire(t *testing.T) {
	cfg := pollTestConfig(enteredRule("qa-ready", "Ready for QA"))
	base := newPollFake(cfg, pollIssue("1", "In Progress"), pollIssue("2", "Ready for QA"), pollIssue("3", "Todo"))
	tr := &assigneePollFake{pollFake: base, assignee: "ada",
		owner: map[string]string{"id-1": "ada", "id-2": "bot", "id-3": "bot"}}
	orch, runner, ctx := runPollOrchestrator(t, cfg, tr)

	entries := compileAutomations(cfg).polledEvents
	start := time.Now()
	state := pollAutomationEvents(ctx, cfg, tr, orch, entries, automationPollState{issues: map[string]observedAutomationIssue{}}, cfg.Tracker.ActiveStates, start)
	assert.Equal(t, "scope:assignee:ada", state.scope)
	tr.setAssignee("bot") // ENG-2 (already Ready for QA) and ENG-3 become visible
	state = pollAutomationEvents(ctx, cfg, tr, orch, entries, state, cfg.Tracker.ActiveStates, start.Add(15*time.Second))
	time.Sleep(150 * time.Millisecond)
	assert.Empty(t, runner.fired(), "ENG-2 only came into scope; it did not move")

	base.move("id-3", "Ready for QA")
	pollAutomationEvents(ctx, cfg, tr, orch, entries, state, cfg.Tracker.ActiveStates, start.Add(30*time.Second))
	require.Eventually(t, func() bool { return len(runner.fired()) == 1 }, 5*time.Second, 25*time.Millisecond)
	assert.Equal(t, []string{"fired:ENG-3 prev=Todo now=Ready for QA"}, runner.fired())
}

// TestTrackerStoreScopeIncludesTheAssignee (#182): another assignee or team
// is another store file; a project scope's key is unchanged.
func TestTrackerStoreScopeIncludesTheAssignee(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tracker.Kind = "linear"
	cfg.Tracker.ProjectSlug = "proj"
	cfg.Tracker.ActiveStates = []string{"Todo"}
	assert.Equal(t, "linear||proj|todo", trackerStoreScope(cfg), "project scope key unchanged")
	cfg.Tracker.ProjectSlug = ""
	cfg.Tracker.Assignee = "me"
	me := trackerStoreScope(cfg)
	cfg.Tracker.Team = "ENG"
	assert.NotEqual(t, me, trackerStoreScope(cfg))
	cfg.Tracker.Assignee = "ada@example.com"
	assert.NotEqual(t, me, trackerStoreScope(cfg))
}

// fakeLinearDoctor answers the doctor's assignee queries: the viewer, users
// by email (none match "nobody@"), and the read check.
func fakeLinearDoctor(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		var data map[string]any
		switch {
		case strings.Contains(req.Query, "ItervoxAssigneeViewer"):
			data = map[string]any{"viewer": map[string]any{"id": "u-bot", "name": "Itervox Bot"}}
		case strings.Contains(req.Query, "ItervoxAssigneeByEmail"):
			data = map[string]any{"users": map[string]any{"nodes": []any{}}}
		case strings.Contains(req.Query, "ItervoxDoctorAssigneeReads"):
			assert.Equal(t, "u-bot", req.Variables["assigneeId"])
			data = map[string]any{
				"teams": map[string]any{"nodes": []any{
					map[string]any{"id": "t-1", "key": "ENG"}, map[string]any{"id": "t-2", "key": "OPS"},
				}},
				"issues": map[string]any{"nodes": []any{
					map[string]any{"team": map[string]any{"key": "ENG"}},
					map[string]any{"team": map[string]any{"key": "OPS"}},
				}},
			}
		default:
			t.Errorf("unexpected query %s", req.Query)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func doctorWorkflow(t *testing.T, endpoint, scope string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	body := "---\nitervox_schema_version: 2\ntracker:\n  kind: linear\n  api_key: lin_test\n  endpoint: " + endpoint + "\n" + scope +
		"server:\n  port: 0\n---\n\nPrompt.\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

// TestDoctorReportsUnresolvableAssignee (#182): `itervox doctor` resolves
// tracker.assignee and fails (exit 1) when it matches no user the key can
// see; a resolvable one gets the read check across the key's teams.
func TestDoctorReportsUnresolvableAssignee(t *testing.T) {
	srv := fakeLinearDoctor(t)

	report, _ := collectDoctorReport(doctorWorkflow(t, srv.URL, "  assignee: nobody@example.com\n"))
	assert.True(t, report.Assignee.Unresolvable)
	assert.Equal(t, 1, doctorExitCode(report))
	assert.Contains(t, renderDoctorReport(report), `ERROR: tracker.assignee "nobody@example.com" does not resolve to a user this API key can see`)

	report, _ = collectDoctorReport(doctorWorkflow(t, srv.URL, "  assignee: me\n"))
	require.True(t, report.Assignee.Ran)
	assert.False(t, assigneeCheckFailed(report.Assignee))
	out := renderDoctorReport(report)
	assert.Contains(t, out, `assignee scope: "me" resolves to Itervox Bot`)
	assert.Contains(t, out, "assignee reads: OK — 2 issue(s) assigned to Itervox Bot readable across the teams the key reads (ENG, OPS); from ENG, OPS")

	report, _ = collectDoctorReport(doctorWorkflow(t, srv.URL, "  assignee: me\n  team: QA\n"))
	assert.True(t, report.Assignee.TeamMissing)
	assert.Equal(t, 1, doctorExitCode(report))
	assert.Contains(t, renderDoctorReport(report), `ERROR: tracker.team "QA" is not a team this API key can read`)

	report, _ = collectDoctorReport(doctorWorkflow(t, srv.URL, "  project_slug: proj\n"))
	assert.False(t, report.Assignee.Ran, "no assignee scope, no check")
}
