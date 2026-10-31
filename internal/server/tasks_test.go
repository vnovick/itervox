package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/apitokens"
)

// fakeTasks is a TasksClient over memory.
type fakeTasks struct {
	created   []TaskCreateRequest
	createErr error
	instr     map[string]string
	started   map[string]bool
	templates map[string]TaskTemplateRow
}

func (f *fakeTasks) CreateTask(_ context.Context, req TaskCreateRequest) (TaskCreateResult, error) {
	if f.createErr != nil {
		return TaskCreateResult{}, f.createErr
	}
	if req.Title == "" && req.Template == "" {
		return TaskCreateResult{}, &TaskInputError{Code: "title_required", Field: "title", Message: "a task needs a title"}
	}
	f.created = append(f.created, req)
	return TaskCreateResult{Identifier: "TASK-1", Title: req.Title, State: "Todo", Local: req.Where != TaskWhereTracker, Started: req.Start}, nil
}

func (f *fakeTasks) SetTaskInstructions(identifier, text string) error {
	if _, ok := f.instr[identifier]; !ok {
		return ErrTaskNotFound
	}
	if f.started[identifier] {
		return ErrTaskStarted
	}
	f.instr[identifier] = text
	return nil
}

func (f *fakeTasks) TaskTemplates() TaskTemplatesResponse {
	resp := TaskTemplatesResponse{LocalTasks: true}
	for _, t := range f.templates {
		resp.Templates = append(resp.Templates, t)
	}
	return resp
}

func (f *fakeTasks) SaveTaskTemplate(name string, t TaskTemplateRow, replace bool) error {
	if _, ok := f.templates[name]; ok && !replace {
		return ErrTaskTemplateExists
	}
	t.Name = name
	f.templates[name] = t
	return nil
}

func (f *fakeTasks) DeleteTaskTemplate(name string) error {
	if _, ok := f.templates[name]; !ok {
		return ErrTaskNotFound
	}
	delete(f.templates, name)
	return nil
}

// TestTaskRoutesScopes (#251): the task routes are declared in the route
// scope table — creating a task and editing its instructions need operator,
// listing templates is a viewer read, managing templates needs admin — and
// each refuses the scope below it before the client is called. Errors map
// to their statuses, and an unwired daemon answers 503.
func TestTaskRoutesScopes(t *testing.T) {
	want := map[string]routeAccess{
		"POST /api/v1/tasks":                          accessOperator,
		"PUT /api/v1/tasks/{identifier}/instructions": accessOperator,
		"GET /api/v1/task-templates":                  accessViewer,
		"POST /api/v1/task-templates/{name}":          accessAdmin,
		"PUT /api/v1/task-templates/{name}":           accessAdmin,
		"DELETE /api/v1/task-templates/{name}":        accessAdmin,
	}
	for route, access := range want {
		assert.Equal(t, access, routeScopes[route], route)
		assert.Equal(t, string(access), RouteScopes()[route], "the hub's copy of %s", route)
	}

	store := apitokens.Open(filepath.Join(t.TempDir(), ".itervox", apitokens.FileName))
	secrets := map[apitokens.Scope]string{}
	for _, sc := range apitokens.Scopes {
		_, secret, err := store.Create(string(sc)+"-token", sc, nil, "test")
		require.NoError(t, err)
		secrets[sc] = secret
	}
	ft := &fakeTasks{instr: map[string]string{"TASK-1": "old"}, started: map[string]bool{"TASK-2": true},
		templates: map[string]TaskTemplateRow{}}
	ft.instr["TASK-2"] = "frozen"
	srv := New(Config{
		Snapshot: func() StateSnapshot { return StateSnapshot{} }, RefreshChan: make(chan struct{}, 1),
		APIToken: scopesTestBootstrap, Tokens: store, Tasks: ft,
	})
	viewer, operator, admin := secrets[apitokens.ScopeViewer], secrets[apitokens.ScopeOperator], secrets[apitokens.ScopeAdmin]

	// Create: operator.
	body := `{"title":"Upgrade lodash","instructions":"Run the tests.","where":"local","start":true,"profile":"implementer"}`
	assert.Equal(t, http.StatusForbidden, doBody(t, srv, http.MethodPost, "/api/v1/tasks", viewer, body).Code)
	assert.Empty(t, ft.created, "refused before the client is called")
	rec := doBody(t, srv, http.MethodPost, "/api/v1/tasks", operator, body)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var res TaskCreateResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
	assert.Equal(t, TaskCreateResult{Identifier: "TASK-1", Title: "Upgrade lodash", State: "Todo", Local: true, Started: true}, res)
	require.Len(t, ft.created, 1)
	assert.Equal(t, "implementer", ft.created[0].Profile)
	rec = doBody(t, srv, http.MethodPost, "/api/v1/tasks", operator, `{"title":"x","where":"elsewhere"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	rec = doBody(t, srv, http.MethodPost, "/api/v1/tasks", operator, `{"title":""}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), `"field":"title"`)
	ft.createErr = ErrLocalTasksUnavailable
	rec = doBody(t, srv, http.MethodPost, "/api/v1/tasks", operator, `{"title":"x"}`)
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "local_tasks_unavailable")
	secret := "lin_api_" + strings.Repeat("Z", 40)
	ft.createErr = errors.New("tracker said no to " + secret)
	rec = doBody(t, srv, http.MethodPost, "/api/v1/tasks", operator, `{"title":"x","where":"tracker"}`)
	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.NotContains(t, rec.Body.String(), secret, "a tracker error is redacted")
	ft.createErr = nil

	// Instructions: operator; frozen once started; 404 for a non-task.
	assert.Equal(t, http.StatusForbidden, doBody(t, srv, http.MethodPut, "/api/v1/tasks/TASK-1/instructions", viewer, `{"instructions":"new"}`).Code)
	assert.Equal(t, "old", ft.instr["TASK-1"])
	assert.Equal(t, http.StatusOK, doBody(t, srv, http.MethodPut, "/api/v1/tasks/TASK-1/instructions", operator, `{"instructions":"new"}`).Code)
	assert.Equal(t, "new", ft.instr["TASK-1"])
	assert.Equal(t, http.StatusConflict, doBody(t, srv, http.MethodPut, "/api/v1/tasks/TASK-2/instructions", operator, `{"instructions":"x"}`).Code)
	assert.Equal(t, http.StatusNotFound, doBody(t, srv, http.MethodPut, "/api/v1/tasks/ENG-1/instructions", operator, `{"instructions":"x"}`).Code)

	// Templates: viewer reads; admin writes.
	tpl := `{"title":"Upgrade {{package}}","instructions":"Bump {{package}}."}`
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		assert.Equal(t, http.StatusForbidden, doBody(t, srv, method, "/api/v1/task-templates/upgrade", operator, tpl).Code, method)
	}
	assert.Empty(t, ft.templates)
	assert.Equal(t, http.StatusCreated, doBody(t, srv, http.MethodPost, "/api/v1/task-templates/upgrade", admin, tpl).Code)
	assert.Equal(t, http.StatusConflict, doBody(t, srv, http.MethodPost, "/api/v1/task-templates/upgrade", admin, tpl).Code)
	assert.Equal(t, http.StatusOK, doBody(t, srv, http.MethodPut, "/api/v1/task-templates/upgrade", admin, tpl).Code)
	rec = doBody(t, srv, http.MethodGet, "/api/v1/task-templates", viewer, "")
	require.Equal(t, http.StatusOK, rec.Code)
	var list TaskTemplatesResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list.Templates, 1)
	assert.Equal(t, "upgrade", list.Templates[0].Name)
	assert.True(t, list.LocalTasks)
	assert.Equal(t, http.StatusNoContent, doBody(t, srv, http.MethodDelete, "/api/v1/task-templates/upgrade", admin, "").Code)
	assert.Equal(t, http.StatusNotFound, doBody(t, srv, http.MethodDelete, "/api/v1/task-templates/upgrade", admin, "").Code)

	// Not wired: 503.
	bare := New(Config{Snapshot: func() StateSnapshot { return StateSnapshot{} }, RefreshChan: make(chan struct{}, 1)})
	assert.Equal(t, http.StatusServiceUnavailable, doReq(t, bare, http.MethodGet, "/api/v1/task-templates", "").Code)
	assert.Equal(t, http.StatusServiceUnavailable, doReq(t, bare, http.MethodPost, "/api/v1/tasks", "").Code)
}
