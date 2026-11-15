package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/issuefilter"
	"github.com/vnovick/itervox/internal/server"
)

type fakeScopeManager struct {
	scope    server.TrackerScope
	setErr   error
	filter   issuefilter.Filter
	previews []issuefilter.Filter
	issues   []domain.Issue
}

func (f *fakeScopeManager) SetFilter(_ context.Context, filter issuefilter.Filter) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.filter = filter
	return nil
}

// PreviewFilter counts the fake's issues with the shared matcher.
func (f *fakeScopeManager) PreviewFilter(_ context.Context, filter issuefilter.Filter) (server.FilterPreview, error) {
	f.previews = append(f.previews, filter)
	if c := filter.Unsupported(issuefilter.SupportedBy("github")); c != "" {
		return server.FilterPreview{}, fmt.Errorf("%w: %s", server.ErrFilterInvalid, c)
	}
	p := server.FilterPreview{}
	for _, is := range f.issues {
		if m, _ := filter.Match(is, issuefilter.Env{}); m {
			p.Count++
			p.Identifiers = append(p.Identifiers, is.Identifier)
		}
	}
	return p, nil
}
func (f *fakeScopeManager) FetchTeams(context.Context) ([]server.TrackerTeam, error) {
	return []server.TrackerTeam{{ID: "t-1", Key: "ENG", Name: "Engineering"}}, nil
}

func (f *fakeScopeManager) GetScope() server.TrackerScope { return f.scope }
func (f *fakeScopeManager) SetScope(_ context.Context, sc server.TrackerScope) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.scope = sc
	return nil
}
func (f *fakeScopeManager) FetchLabels(context.Context) ([]string, error) {
	return []string{"agent", "bug"}, nil
}
func (f *fakeScopeManager) FetchUsers(context.Context) ([]server.TrackerUser, error) {
	return []server.TrackerUser{{ID: "u-1", Name: "Ada"}}, nil
}

// TestTrackerScopeEndpoints (#182): GET/PUT /api/v1/tracker/scope and the
// label and user lists; an unknown assignee is a 422, no scope support a 501.
func TestTrackerScopeEndpoints(t *testing.T) {
	plain := testServer(t)
	w := httptest.NewRecorder()
	plain.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/tracker/scope", nil))
	assert.Equal(t, http.StatusNotImplemented, w.Code)

	fsm := &fakeScopeManager{}
	cfg := makeTestConfig(baseSnap())
	cfg.ScopeManager = fsm
	srv := server.New(cfg)

	body := `{"projects":["alpha"],"assignee":"me","labels":["agent"]}`
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/v1/tracker/scope", strings.NewReader(body)))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, server.TrackerScope{Projects: []string{"alpha"}, Assignee: "me", Labels: []string{"agent"}}, fsm.scope)

	w = httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/tracker/scope", nil))
	var got struct{ Scope server.TrackerScope }
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, fsm.scope, got.Scope)

	for path, want := range map[string]string{"/api/v1/tracker/labels": `"agent"`, "/api/v1/tracker/users": `"Ada"`} {
		w = httptest.NewRecorder()
		srv.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), want)
	}

	fsm.setErr = fmt.Errorf("%w: nobody", server.ErrScopeAssigneeNotFound)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/v1/tracker/scope", strings.NewReader(`{"assignee":"nobody@example.com"}`)))
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Contains(t, w.Body.String(), "assignee_not_found")
}

// TestFilterPreviewEndpoint (#198): POST /api/v1/tracker/filter/preview
// parses the filter with the WORKFLOW.md parser and returns the count and
// first identifiers, read-only (nothing is saved); an invalid filter or an
// unsupported condition is a 422. PUT /tracker/scope with "filter" saves it,
// and GET /tracker/teams lists the teams.
func TestFilterPreviewEndpoint(t *testing.T) {
	fsm := &fakeScopeManager{issues: []domain.Issue{
		{Identifier: "ENG-1", Labels: []string{"agent"}},
		{Identifier: "ENG-2", Labels: []string{"agent", "blocked"}},
		{Identifier: "ENG-3", Labels: []string{"urgent"}},
	}}
	cfg := makeTestConfig(baseSnap())
	cfg.ScopeManager = fsm
	srv := server.New(cfg)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
	}

	w := do(http.MethodPost, "/api/v1/tracker/filter/preview",
		`{"filter":[{"labels_any":["agent"],"labels_none":["blocked"]},{"labels_all":"urgent"}]}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.JSONEq(t, `{"count":2,"identifiers":["ENG-1","ENG-3"]}`, w.Body.String())
	assert.Nil(t, fsm.filter, "a preview saves nothing")
	require.Len(t, fsm.previews, 1)
	assert.Equal(t, issuefilter.Filter{{LabelsAny: []string{"agent"}, LabelsNone: []string{"blocked"}}, {LabelsAll: []string{"urgent"}}}, fsm.previews[0])

	w = do(http.MethodPost, "/api/v1/tracker/filter/preview", `{"filter":[]}`)
	require.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"count":3,"identifiers":["ENG-1","ENG-2","ENG-3"]}`, w.Body.String(), "no filter: every issue")

	for _, bad := range []string{`{"filter":[{"labels":["x"]}]}`, `{"filter":[{}]}`, `{"filter":[{"priority":[9]}]}`} {
		w = do(http.MethodPost, "/api/v1/tracker/filter/preview", bad)
		assert.Equal(t, http.StatusUnprocessableEntity, w.Code, bad)
		assert.Contains(t, w.Body.String(), "invalid_filter")
	}
	w = do(http.MethodPost, "/api/v1/tracker/filter/preview", `{"filter":[{"teams":["ENG"]}]}`)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code, "an unsupported condition")
	w = do(http.MethodPost, "/api/v1/tracker/filter/preview", `{}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	w = do(http.MethodPut, "/api/v1/tracker/scope", `{"filter":[{"assignee":"me","priority":[1,2]}]}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, issuefilter.Filter{{Assignee: "me", Priority: []int{1, 2}}}, fsm.filter)
	assert.Equal(t, server.TrackerScope{}, fsm.scope, "a filter save is not a legacy scope save")
	w = do(http.MethodPut, "/api/v1/tracker/scope", `{"filter":[{"projects":"x","bogus":1}]}`)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)

	w = do(http.MethodGet, "/api/v1/tracker/teams", "")
	require.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"teams":[{"id":"t-1","key":"ENG","name":"Engineering"}]}`, w.Body.String())
}
