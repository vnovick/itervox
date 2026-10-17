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

	"github.com/vnovick/itervox/internal/server"
)

type fakeScopeManager struct {
	scope  server.TrackerScope
	setErr error
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
