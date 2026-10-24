package main

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
)

// fakeScopeManager is an in-memory tracker.ScopeManager (#182).
type fakeScopeManager struct {
	smu        sync.Mutex
	scope      tracker.Scope
	resolveErr error
	resolved   []string
}

func (f *fakeScopeManager) Scope() tracker.Scope {
	f.smu.Lock()
	defer f.smu.Unlock()
	return f.scope
}
func (f *fakeScopeManager) SetScope(sc tracker.Scope) {
	f.smu.Lock()
	defer f.smu.Unlock()
	f.scope = sc
}
func (f *fakeScopeManager) ResolveUser(_ context.Context, spec string) (tracker.AssigneeUser, error) {
	f.smu.Lock()
	defer f.smu.Unlock()
	f.resolved = append(f.resolved, spec)
	if f.resolveErr != nil {
		return tracker.AssigneeUser{}, f.resolveErr
	}
	return tracker.AssigneeUser{ID: "u-" + spec, Name: "User " + spec}, nil
}
func (f *fakeScopeManager) ResolveAssignee(ctx context.Context) (tracker.AssigneeUser, error) {
	return f.ResolveUser(ctx, f.Scope().Assignee)
}
func (f *fakeScopeManager) ResolvedAssignee() (tracker.AssigneeUser, bool) {
	return tracker.AssigneeUser{}, false
}
func (f *fakeScopeManager) FetchLabels(context.Context) ([]string, error) {
	return []string{"agent"}, nil
}
func (f *fakeScopeManager) FetchUsers(context.Context) ([]tracker.AssigneeUser, error) {
	return nil, nil
}
func (f *fakeScopeManager) ScopeKey() string   { return f.Scope().Key() }
func (f *fakeScopeManager) ScopeLabel() string { return f.ScopeKey() }

func validWorkflow(t *testing.T, path string) *config.Config {
	t.Helper()
	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.NoError(t, config.ValidateDispatch(cfg))
	return cfg
}

// TestTrackerScopeSaveCombinations (#182): the dashboard's combined scope
// filter saves any combination of projects, assignee and labels (#239: to
// the settings file) — what the config then loads is what memory holds — resolving
// the assignee first; an unknown user is refused (422) with file and memory
// unchanged.
func TestTrackerScopeSaveCombinations(t *testing.T) {
	path := writeFixture(t, reloadFenceFixture)
	_, gen, err := loadSettingsGeneration(path)
	require.NoError(t, err)
	fsm := &fakeScopeManager{}
	m := &trackerScopeManager{sm: fsm, workflowPath: path, settingsGen: gen}

	for _, tc := range []struct {
		name string
		in   server.TrackerScope
		want tracker.Scope
	}{
		{"project, assignee and labels", server.TrackerScope{Projects: []string{"alpha"}, Assignee: "me", Labels: []string{"agent", "needs triage"}},
			tracker.Scope{Projects: []string{"alpha"}, Assignee: "me", Labels: []string{"agent", "needs triage"}}},
		{"assignee only", server.TrackerScope{Assignee: "ada@example.com"},
			tracker.Scope{Projects: []string{}, Assignee: "ada@example.com", Labels: []string{}}},
		{"labels only", server.TrackerScope{Labels: []string{"agent", " Agent "}},
			tracker.Scope{Projects: []string{}, Labels: []string{"agent"}}},
		{"project only", server.TrackerScope{Projects: []string{"beta"}},
			tracker.Scope{Projects: []string{"beta"}, Labels: []string{}}},
		{"neither (every project)", server.TrackerScope{},
			tracker.Scope{Projects: []string{}, Labels: []string{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, m.SetScope(t.Context(), tc.in))
			assert.Equal(t, tc.want, fsm.Scope(), "applied in memory")
			cfg := validWorkflow(t, path)
			assert.Equal(t, strings.Join(tc.want.Projects, ", "), cfg.Tracker.ProjectSlug, "the file holds the projects")
			assert.Equal(t, tc.want.Assignee, cfg.Tracker.Assignee, "the file holds the assignee")
			assert.Equal(t, len(tc.want.Labels), len(cfg.Tracker.Labels), "the file holds the labels")
			for i, l := range tc.want.Labels {
				assert.Equal(t, l, cfg.Tracker.Labels[i])
			}
		})
	}
	// #239: the scope keys live in the settings file; WORKFLOW.md no longer
	// holds them, and a cleared key is removed (it used to be commented out).
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "project_slug:")
	assert.NotContains(t, string(data), "assignee:")
	settingsPath := config.ProjectSettingsPath(path)
	settings, err := config.ReadSettingsFile(settingsPath)
	require.NoError(t, err)
	tr, _ := settings["tracker"].(map[string]any)
	assert.NotContains(t, tr, "assignee", "a cleared key is removed")
	assert.NotContains(t, tr, "labels")

	before, settingsBefore := string(data), readString(t, settingsPath)
	fsm.resolveErr = tracker.ErrAssigneeNotFound
	err = m.SetScope(t.Context(), server.TrackerScope{Assignee: "nobody@example.com"})
	require.ErrorIs(t, err, server.ErrScopeAssigneeNotFound)
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, string(data), "an unresolvable assignee writes nothing")
	assert.Equal(t, settingsBefore, readString(t, settingsPath))
	assert.Empty(t, fsm.Scope().Assignee, "and changes nothing in memory")
}

// TestTUIPickerScope (#182): the TUI picker's "assigned to" row ANDs with
// the selected projects and keeps the labels.
func TestTUIPickerScope(t *testing.T) {
	cfg := &config.Config{}
	fsm := &fakeScopeManager{scope: tracker.Scope{Labels: []string{"agent"}}}
	assert.Equal(t, tracker.Scope{Projects: []string{"alpha"}, Assignee: "me", Labels: []string{"agent"}},
		tuiPickerScope(fsm, cfg, []string{"alpha", "__assignee__"}))
	fsm.scope.Assignee = "ada@example.com"
	assert.Equal(t, tracker.Scope{Projects: []string{}, Assignee: "ada@example.com", Labels: []string{"agent"}},
		tuiPickerScope(fsm, cfg, []string{"__assignee__"}), "the current assignee is kept")
	assert.Equal(t, tracker.Scope{Projects: []string{"alpha"}, Labels: []string{"agent"}},
		tuiPickerScope(fsm, cfg, []string{"alpha"}), "unchecked: anyone")
}

// scopePollFake is a poll tracker with a combined scope: only the issues
// assigned to the current assignee are visible.
type scopePollFake struct {
	*pollFake
	fakeScopeManager
	owner map[string]string // issue ID → assignee
}

func (f *scopePollFake) FetchIssuesByStates(ctx context.Context, states []string) ([]domain.Issue, error) {
	issues, err := f.pollFake.FetchIssuesByStates(ctx, states)
	who := f.Scope().Assignee
	return slices.DeleteFunc(issues, func(i domain.Issue) bool { return f.owner[i.ID] != who }), err
}

// TestAutomationPollAssigneeScopeChangeDoesNotFire (#182): the automation
// poll's scope key includes the assignee (and labels), so changing scope
// rebaselines: issues that only came into scope do not fire a "moved into
// state" rule; a real move afterwards still does.
func TestAutomationPollAssigneeScopeChangeDoesNotFire(t *testing.T) {
	cfg := pollTestConfig(enteredRule("qa-ready", "Ready for QA"))
	base := newPollFake(cfg, pollIssue("1", "In Progress"), pollIssue("2", "Ready for QA"), pollIssue("3", "Todo"))
	tr := &scopePollFake{pollFake: base, owner: map[string]string{"id-1": "ada", "id-2": "bot", "id-3": "bot"}}
	tr.SetScope(tracker.Scope{Assignee: "ada"})
	orch, runner, ctx := runPollOrchestrator(t, cfg, tr)

	entries := compileAutomations(cfg).polledEvents
	start := time.Now()
	state := pollAutomationEvents(ctx, cfg, tr, orch, entries, automationPollState{issues: map[string]observedAutomationIssue{}}, cfg.Tracker.ActiveStates, start)
	assert.Equal(t, "scope:"+tracker.Scope{Assignee: "ada"}.Key(), state.scope)
	tr.SetScope(tracker.Scope{Assignee: "bot"}) // ENG-2 (already Ready for QA) and ENG-3 come into scope
	state = pollAutomationEvents(ctx, cfg, tr, orch, entries, state, cfg.Tracker.ActiveStates, start.Add(15*time.Second))
	time.Sleep(150 * time.Millisecond)
	assert.Empty(t, runner.fired(), "ENG-2 only came into scope; it did not move")

	base.move("id-3", "Ready for QA")
	pollAutomationEvents(ctx, cfg, tr, orch, entries, state, cfg.Tracker.ActiveStates, start.Add(30*time.Second))
	require.Eventually(t, func() bool { return len(runner.fired()) == 1 }, 5*time.Second, 25*time.Millisecond)
	assert.Equal(t, []string{"fired:ENG-3 prev=Todo now=Ready for QA"}, runner.fired())
}

// TestTrackerStoreScopeIncludesTheAssignee (#182): another assignee, team or
// label filter is another store file; a project-only key is unchanged.
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
	withTeam := trackerStoreScope(cfg)
	cfg.Tracker.Labels = []string{"Agent"}
	assert.NotEqual(t, withTeam, trackerStoreScope(cfg), "labels are part of the scope")
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
			assert.Contains(t, fmt.Sprint(req.Variables["filter"]), "u-bot", "the read check filters by the resolved user")
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
