package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/vnovick/itervox/internal/logging"
)

// tasks.go — #251: New task and saved task templates.
//
//	POST   /api/v1/tasks                            operator create a task (local issue or tracker issue)
//	PUT    /api/v1/tasks/{identifier}/instructions  operator edit a task's instructions until it starts
//	GET    /api/v1/task-templates                   viewer   the templates in .itervox/tasks/
//	POST   /api/v1/task-templates/{name}            admin    save a new template
//	PUT    /api/v1/task-templates/{name}            admin    create or replace a template
//	DELETE /api/v1/task-templates/{name}            admin    delete a template

// TaskCreateRequest is the body of POST /api/v1/tasks. With Template, the
// template's fields fill whatever the request leaves empty, and Vars fill
// its `{{var}}` placeholders.
type TaskCreateRequest struct {
	Title        string            `json:"title"`
	Instructions string            `json:"instructions"`
	Profile      string            `json:"profile,omitempty"`
	Backend      string            `json:"backend,omitempty"`
	Playbook     string            `json:"playbook,omitempty"`
	BaseBranch   string            `json:"baseBranch,omitempty"`
	Template     string            `json:"template,omitempty"`
	Vars         map[string]string `json:"vars,omitempty"`
	// Where is "local" (default: an Itervox-only issue file) or "tracker".
	Where string `json:"where,omitempty"`
	// Start asks for dispatch at once ("Create and start").
	Start bool `json:"start,omitempty"`
}

// Task locations.
const (
	TaskWhereLocal   = "local"
	TaskWhereTracker = "tracker"
)

// TaskCreateResult is the 201 body of POST /api/v1/tasks.
type TaskCreateResult struct {
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	State      string `json:"state"`
	URL        string `json:"url,omitempty"`
	Local      bool   `json:"local"`
	Started    bool   `json:"started"`
}

// TaskTemplateRow is one saved task template.
type TaskTemplateRow struct {
	Name         string   `json:"name"`
	Title        string   `json:"title"`
	Description  string   `json:"description,omitempty"`
	Instructions string   `json:"instructions,omitempty"`
	Profile      string   `json:"profile,omitempty"`
	Playbook     string   `json:"playbook,omitempty"`
	Backend      string   `json:"backend,omitempty"`
	BaseBranch   string   `json:"baseBranch,omitempty"`
	Vars         []string `json:"vars"`
}

// TaskTemplateProblem is a template file that could not be read.
type TaskTemplateProblem struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// TaskTemplatesResponse is GET /api/v1/task-templates.
type TaskTemplatesResponse struct {
	// LocalTasks reports whether the project can hold local tasks (the
	// local tracker, or Linear/GitHub with Itervox-only task files).
	LocalTasks bool                  `json:"localTasks"`
	Templates  []TaskTemplateRow     `json:"templates"`
	Problems   []TaskTemplateProblem `json:"problems,omitempty"`
}

// TaskInputError is a request the client must fix (400): Code names it and
// Field the form input it belongs to.
type TaskInputError struct {
	Code    string
	Field   string
	Message string
}

func (e *TaskInputError) Error() string { return "server: " + e.Message }

// Task errors with their own statuses.
var (
	// ErrLocalTasksUnavailable: the tracker cannot hold local tasks (409).
	ErrLocalTasksUnavailable = errors.New("server: this project's tracker cannot hold local tasks")
	// ErrTaskNotFound: no task (or template) of that name (404).
	ErrTaskNotFound = errors.New("server: not found")
	// ErrTaskStarted: the task has started; its instructions are frozen (409).
	ErrTaskStarted = errors.New("server: the task has started")
	// ErrTaskTemplateExists: a create found a template of that name (409).
	ErrTaskTemplateExists = errors.New("server: a template with that name exists")
)

// TasksClient backs the task endpoints (#251). cmd/itervox adapts the
// orchestrator and the templates directory; nothing blocks on the event
// loop.
type TasksClient interface {
	CreateTask(ctx context.Context, req TaskCreateRequest) (TaskCreateResult, error)
	SetTaskInstructions(identifier, instructions string) error
	TaskTemplates() TaskTemplatesResponse
	// SaveTaskTemplate writes t under name; replace false refuses an
	// existing one (ErrTaskTemplateExists).
	SaveTaskTemplate(name string, t TaskTemplateRow, replace bool) error
	DeleteTaskTemplate(name string) error
}

// writeTaskError maps a TasksClient error to its status.
func writeTaskError(w http.ResponseWriter, err error, fallbackCode, fallbackMsg string) {
	var in *TaskInputError
	switch {
	case errors.As(err, &in):
		writeErrorWithField(w, http.StatusBadRequest, in.Code, in.Message, in.Field)
	case errors.Is(err, ErrLocalTasksUnavailable):
		writeErrorWithField(w, http.StatusConflict, "local_tasks_unavailable", "this project's tracker cannot hold local tasks; create the task in the tracker", "where")
	case errors.Is(err, ErrTaskNotFound):
		writeError(w, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, ErrTaskStarted):
		writeError(w, http.StatusConflict, "task_started", "the task has started; its instructions can no longer change")
	case errors.Is(err, ErrTaskTemplateExists):
		writeErrorWithField(w, http.StatusConflict, "template_exists", "a template with that name exists", "name")
	default:
		writeError(w, http.StatusBadGateway, fallbackCode, fallbackMsg+": "+logging.RedactString(err.Error()))
	}
}

func (s *Server) tasksAvailable(w http.ResponseWriter) bool {
	if s.tasks == nil {
		writeError(w, http.StatusServiceUnavailable, "not_supported", "tasks are not available")
		return false
	}
	return true
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	if !s.tasksAvailable(w) {
		return
	}
	var body TaskCreateRequest
	if err := decodeJSONBody(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	if body.Where != "" && body.Where != TaskWhereLocal && body.Where != TaskWhereTracker {
		writeErrorWithField(w, http.StatusBadRequest, "bad_request", `where is "local" or "tracker"`, "where")
		return
	}
	out, err := s.tasks.CreateTask(r.Context(), body)
	if err != nil {
		writeTaskError(w, err, "task_create_failed", "the task could not be created")
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleSetTaskInstructions(w http.ResponseWriter, r *http.Request) {
	if !s.tasksAvailable(w) {
		return
	}
	var body struct {
		Instructions string `json:"instructions"`
	}
	if err := decodeJSONBody(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	id := chi.URLParam(r, "identifier")
	if err := s.tasks.SetTaskInstructions(id, body.Instructions); err != nil {
		writeTaskError(w, err, "task_update_failed", "the instructions could not be saved")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"identifier": id, "instructions": body.Instructions})
}

func (s *Server) handleListTaskTemplates(w http.ResponseWriter, _ *http.Request) {
	if !s.tasksAvailable(w) {
		return
	}
	resp := s.tasks.TaskTemplates()
	if resp.Templates == nil {
		resp.Templates = []TaskTemplateRow{}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleSaveTaskTemplate(replace bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.tasksAvailable(w) {
			return
		}
		var body TaskTemplateRow
		if err := decodeJSONBody(w, r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid body")
			return
		}
		name := chi.URLParam(r, "name")
		if err := s.tasks.SaveTaskTemplate(name, body, replace); err != nil {
			writeTaskError(w, err, "template_save_failed", "the template could not be saved")
			return
		}
		status := http.StatusOK
		if !replace {
			status = http.StatusCreated
		}
		writeJSON(w, status, map[string]string{"name": name})
	}
}

func (s *Server) handleDeleteTaskTemplate(w http.ResponseWriter, r *http.Request) {
	if !s.tasksAvailable(w) {
		return
	}
	if err := s.tasks.DeleteTaskTemplate(chi.URLParam(r, "name")); err != nil {
		writeTaskError(w, err, "template_delete_failed", "the template could not be deleted")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
