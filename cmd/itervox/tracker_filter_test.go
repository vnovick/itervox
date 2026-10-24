package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/issuefilter"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
)

// fakeLinearFilterDoctor answers the doctor's filter check: the viewer
// ("me") and the filtered poll, which returns two issues.
func fakeLinearFilterDoctor(t *testing.T, polled *[]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		data := map[string]any{}
		switch {
		case strings.Contains(req.Query, "ItervoxAssigneeViewer"):
			data = map[string]any{"viewer": map[string]any{"id": "u-bot", "name": "Itervox Bot"}}
		case strings.Contains(req.Query, "ItervoxAssigneeByEmail"):
			data = map[string]any{"users": map[string]any{"nodes": []any{}}}
		case strings.Contains(req.Query, "ItervoxLinearPollScoped"):
			*polled = append(*polled, fmt.Sprint(req.Variables["filter"]))
			node := func(id, ident string) map[string]any {
				return map[string]any{"id": id, "identifier": ident, "title": "T", "state": map[string]any{"name": "Todo"}}
			}
			data = map[string]any{"issues": map[string]any{
				"nodes":    []any{node("00000000-0000-4000-8000-000000000001", "ENG-1"), node("00000000-0000-4000-8000-000000000002", "ENG-2")},
				"pageInfo": map[string]any{"hasNextPage": false},
			}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestDoctorValidatesFilter (#198): `itervox doctor` validates
// tracker.filter with the workflow (a conflict with the legacy keys fails
// the schema check), resolves its assignees (an unknown one fails doctor)
// and reports how many issues in the active states it matches.
func TestDoctorValidatesFilter(t *testing.T) {
	var polled []string
	srv := fakeLinearFilterDoctor(t, &polled)
	const filter = "  filter:\n    - labels_any: [agent]\n    - assignee: me\n"

	report, _ := collectDoctorReport(doctorWorkflow(t, srv.URL, filter))
	require.True(t, report.SchemaPassed, report.WorkflowError)
	require.True(t, report.Filter.Ran)
	assert.Equal(t, 2, report.Filter.Matches)
	assert.False(t, filterCheckFailed(report.Filter))
	out := renderDoctorReport(report)
	assert.Contains(t, out, "tracker filter: OK — agent  OR  me — 2 issue(s) in the active states match (e.g. ENG-1, ENG-2)")
	require.Len(t, polled, 1)
	assert.Contains(t, polled[0], "u-bot", "the count reads through the compiled filter, me resolved")

	report, _ = collectDoctorReport(doctorWorkflow(t, srv.URL, "  project_slug: proj\n"+filter))
	assert.False(t, report.SchemaPassed)
	assert.Contains(t, report.WorkflowError, "tracker.filter cannot be combined with tracker.project_slug")
	assert.Equal(t, 1, doctorExitCode(report))
	assert.False(t, report.Filter.Ran, "an invalid filter is not counted")

	report, _ = collectDoctorReport(doctorWorkflow(t, srv.URL, "  filter:\n    - assignee: nobody@example.com\n"))
	assert.True(t, report.Filter.Unresolvable)
	assert.Equal(t, 1, doctorExitCode(report))
	assert.Contains(t, renderDoctorReport(report), "ERROR: tracker.filter names an assignee this API key cannot see")

	report, _ = collectDoctorReport(doctorWorkflow(t, srv.URL, "  project_slug: proj\n"))
	assert.Empty(t, report.Filter.Label, "no filter, no check")
}

// memFilterManager is a tracker.FilterManager over a fixed issue list,
// evaluated with the shared matcher.
type memFilterManager struct {
	tracker.Tracker
	filter issuefilter.Filter
	issues []domain.Issue
}

func (m *memFilterManager) Filter() issuefilter.Filter                              { return m.filter }
func (m *memFilterManager) FilterMode() bool                                        { return len(m.filter) > 0 }
func (m *memFilterManager) SetFilter(f issuefilter.Filter)                          { m.filter = f }
func (m *memFilterManager) FilterConditions() []string                              { return issuefilter.SupportedBy("github") }
func (m *memFilterManager) ResolveFilter(context.Context, issuefilter.Filter) error { return nil }
func (m *memFilterManager) MatchesFilter(is domain.Issue) (bool, bool) {
	return m.filter.Match(is, issuefilter.Env{})
}
func (m *memFilterManager) PreviewFilter(_ context.Context, f issuefilter.Filter, limit int) (tracker.FilterPreview, error) {
	p := tracker.FilterPreview{Identifiers: []string{}}
	for _, is := range m.issues {
		if in, _ := f.Match(is, issuefilter.Env{}); in {
			p.Count++
			if len(p.Identifiers) < limit {
				p.Identifiers = append(p.Identifiers, is.Identifier)
			}
		}
	}
	return p, nil
}
func (m *memFilterManager) FetchTeams(context.Context) ([]tracker.Team, error) { return nil, nil }
func (m *memFilterManager) FetchLabels(context.Context) ([]string, error)      { return nil, nil }
func (m *memFilterManager) FetchUsers(context.Context) ([]tracker.AssigneeUser, error) {
	return nil, nil
}
func (m *memFilterManager) ScopeKey() string   { return m.filter.Key() }
func (m *memFilterManager) ScopeLabel() string { return m.filter.Label() }

// TestFilterSaveWritesTheSettingsFile (#198, #239; was
// TestFilterSaveWritesWorkflow): the dashboard's group builder save writes
// tracker.filter to the settings file — removing it and the legacy keys from
// WORKFLOW.md — so the config loads (and validates) as exactly the filter
// memory holds; the preview counts without saving; an unsupported condition
// is refused with files and memory unchanged; an empty filter removes
// tracker.filter; a legacy scope save removes it too.
func TestFilterSaveWritesTheSettingsFile(t *testing.T) {
	path := writeFixture(t, "---\nitervox_schema_version: 2\ntracker:\n  kind: linear\n  api_key: k\n  # project_slug: old\n  labels: [agent]\n  filter:\n    - labels_any: [old]\n      priority: [3]\n  active_states: [Todo]\n---\n\nPrompt.\n")
	_, gen, err := loadSettingsGeneration(path)
	require.NoError(t, err)
	fm := &memFilterManager{issues: []domain.Issue{
		{Identifier: "ENG-1", Labels: []string{"agent"}}, {Identifier: "ENG-2", Labels: []string{"agent", "blocked"}},
	}}
	m := &trackerScopeManager{fm: fm, workflowPath: path, settingsGen: gen}
	want := issuefilter.Filter{{LabelsAny: []string{"agent", "ai"}, LabelsNone: []string{"blocked"}}, {Assignee: "me", LabelsAll: []string{"urgent"}}}

	p, err := m.PreviewFilter(t.Context(), want)
	require.NoError(t, err)
	assert.Equal(t, server.FilterPreview{Count: 1, Identifiers: []string{"ENG-1"}}, p)
	assert.Nil(t, fm.filter, "the preview changes nothing")

	require.NoError(t, m.SetFilter(t.Context(), want))
	assert.Equal(t, want, fm.filter)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	text := string(raw)
	assert.NotContains(t, text, "filter:", "the old block leaves WORKFLOW.md")
	assert.NotContains(t, text, "labels: [agent]", "the legacy keys leave WORKFLOW.md")
	assert.Contains(t, text, "  active_states: [Todo]")
	settings, err := config.ReadSettingsFile(config.ProjectSettingsPath(path))
	require.NoError(t, err)
	tr := settings["tracker"].(map[string]any)
	assert.Equal(t, []any{
		map[string]any{"labels_any": []any{"agent", "ai"}, "labels_none": []any{"blocked"}},
		map[string]any{"assignee": "me", "labels_all": []any{"urgent"}},
	}, tr["filter"])
	settingsText := readString(t, config.ProjectSettingsPath(path))
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, want, cfg.Tracker.Filter, "the file loads as what memory holds")
	require.NoError(t, config.ValidateDispatch(cfg))

	err = m.SetFilter(t.Context(), issuefilter.Filter{{Teams: []string{"ENG"}}})
	require.ErrorIs(t, err, server.ErrFilterInvalid)
	assert.Equal(t, want, fm.filter, "refused: memory unchanged")
	after, _ := os.ReadFile(path)
	assert.Equal(t, text, string(after), "refused: WORKFLOW.md unchanged")
	assert.Equal(t, settingsText, readString(t, config.ProjectSettingsPath(path)), "refused: settings file unchanged")

	require.NoError(t, m.SetFilter(t.Context(), nil))
	cfg, err = config.Load(path)
	require.NoError(t, err)
	assert.Nil(t, cfg.Tracker.Filter, "an empty filter removes tracker.filter")
	assert.Nil(t, cfg.Tracker.EffectiveFilter(), "and reads every issue")

	// A filter again, then a legacy scope save.
	require.NoError(t, m.SetFilter(t.Context(), want))
	fsm := &fakeScopeManager{}
	legacy := &trackerScopeManager{sm: fsm, fm: fm, workflowPath: path, settingsGen: gen}
	require.NoError(t, legacy.SetScope(t.Context(), server.TrackerScope{Projects: []string{"alpha"}, Labels: []string{"agent"}}))
	cfg, err = config.Load(path)
	require.NoError(t, err)
	assert.Nil(t, cfg.Tracker.Filter, "a legacy save removes tracker.filter")
	assert.Equal(t, "alpha", cfg.Tracker.ProjectSlug)
	require.NoError(t, config.ValidateDispatch(cfg))
}
