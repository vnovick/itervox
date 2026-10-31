package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/playbook"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tasktemplate"
)

// tasksAdapter backs the #251 endpoints: New task through the orchestrator
// (CreateTask runs the tracker write on the HTTP goroutine and leaves
// dispatch to the event loop) and the templates in .itervox/tasks/.
type tasksAdapter struct {
	orch *orchestrator.Orchestrator
	dir  string // the templates directory
}

func newTasksAdapter(orch *orchestrator.Orchestrator, workflowPath string) tasksAdapter {
	return tasksAdapter{orch: orch, dir: tasktemplate.Dir(workflowPath)}
}

// inputError is a 400 the form shows next to field.
func inputError(code, field, format string, args ...any) error {
	return &server.TaskInputError{Code: code, Field: field, Message: fmt.Sprintf(format, args...)}
}

// resolveTaskRequest fills req from its template, when it names one, and
// checks every field against the daemon's profiles, backends and playbooks.
func (a tasksAdapter) resolveTaskRequest(req server.TaskCreateRequest) (orchestrator.TaskRequest, error) {
	out := orchestrator.TaskRequest{
		Title: strings.TrimSpace(req.Title), Instructions: strings.TrimSpace(req.Instructions),
		Profile: strings.TrimSpace(req.Profile), Backend: strings.TrimSpace(req.Backend),
		Playbook: strings.TrimSpace(req.Playbook), BaseBranch: strings.TrimSpace(req.BaseBranch),
		OnTracker: req.Where == server.TaskWhereTracker, Start: req.Start,
	}
	if name := strings.TrimSpace(req.Template); name != "" {
		t, err := tasktemplate.Read(a.dir, name)
		switch {
		case errors.Is(err, tasktemplate.ErrNotFound), errors.Is(err, tasktemplate.ErrInvalidName):
			return out, inputError("unknown_template", "template", "no task template named %q", name)
		case err != nil:
			return out, inputError("unknown_template", "template", "the task template %q cannot be read: %v", name, err)
		}
		title, instructions, missing := t.Render(req.Vars)
		if len(missing) > 0 && (out.Title == "" || out.Instructions == "") {
			return out, inputError("missing_vars", "vars", "the template needs a value for: %s", strings.Join(missing, ", "))
		}
		out.Title = cmp.Or(out.Title, title)
		out.Instructions = cmp.Or(out.Instructions, instructions)
		out.Profile = cmp.Or(out.Profile, t.Profile)
		out.Backend = cmp.Or(out.Backend, t.Backend)
		out.Playbook = cmp.Or(out.Playbook, t.Playbook)
		out.BaseBranch = cmp.Or(out.BaseBranch, t.BaseBranch)
	}
	if out.Title == "" {
		return out, inputError("title_required", "title", "a task needs a title")
	}
	if len(out.Instructions) > orchestrator.MaxTaskInstructionsBytes {
		return out, inputError("instructions_too_long", "instructions", "the instructions are longer than %d bytes", orchestrator.MaxTaskInstructionsBytes)
	}
	if out.Profile != "" {
		if _, ok := a.orch.ProfilesCfg()[out.Profile]; !ok {
			return out, inputError("unknown_profile", "profile", "no agent profile named %q", out.Profile)
		}
	}
	if out.Backend != "" && out.Backend != "claude" && out.Backend != "codex" {
		return out, inputError("unknown_backend", "backend", `backend must be "claude" or "codex", got %q`, out.Backend)
	}
	if out.Playbook != "" && out.Playbook != playbook.None &&
		!slices.ContainsFunc(a.orch.Playbooks(), func(p playbook.Playbook) bool { return p.Name == out.Playbook }) {
		return out, inputError("unknown_playbook", "playbook", "no playbook named %q", out.Playbook)
	}
	if strings.ContainsAny(out.BaseBranch, " \t\n~^:?*[\\") || strings.HasPrefix(out.BaseBranch, "-") {
		return out, inputError("invalid_base", "baseBranch", "%q is not a branch name", out.BaseBranch)
	}
	return out, nil
}

func (a tasksAdapter) CreateTask(ctx context.Context, req server.TaskCreateRequest) (server.TaskCreateResult, error) {
	tr, err := a.resolveTaskRequest(req)
	if err != nil {
		return server.TaskCreateResult{}, err
	}
	res, err := a.orch.CreateTask(ctx, tr)
	switch {
	case errors.Is(err, orchestrator.ErrLocalTasksUnavailable):
		return server.TaskCreateResult{}, server.ErrLocalTasksUnavailable
	case errors.Is(err, orchestrator.ErrTaskTitleRequired):
		return server.TaskCreateResult{}, inputError("title_required", "title", "a task needs a title")
	case errors.Is(err, orchestrator.ErrTaskNoActiveState):
		return server.TaskCreateResult{}, inputError("no_active_state", "", "tracker.active_states is empty")
	case err != nil:
		return server.TaskCreateResult{}, err
	}
	out := server.TaskCreateResult{Identifier: res.Issue.Identifier, Title: res.Issue.Title, State: res.Issue.State,
		Local: res.Local, Started: res.StartRequested}
	if res.Issue.URL != nil {
		out.URL = *res.Issue.URL
	}
	return out, nil
}

func (a tasksAdapter) SetTaskInstructions(identifier, instructions string) error {
	switch err := a.orch.SetTaskInstructions(identifier, instructions); {
	case errors.Is(err, orchestrator.ErrTaskNotFound):
		return server.ErrTaskNotFound
	case errors.Is(err, orchestrator.ErrTaskStarted):
		return server.ErrTaskStarted
	case errors.Is(err, orchestrator.ErrTaskInstructionsTooBig):
		return inputError("instructions_too_long", "instructions", "%s", strings.TrimPrefix(err.Error(), "orchestrator: "))
	default:
		return err
	}
}

// templateRow converts a template for the API.
func templateRow(t tasktemplate.Template) server.TaskTemplateRow {
	vars := t.Vars()
	if vars == nil {
		vars = []string{}
	}
	return server.TaskTemplateRow{Name: t.Name, Title: t.Title, Description: t.Description, Instructions: t.Instructions,
		Profile: t.Profile, Playbook: t.Playbook, Backend: t.Backend, BaseBranch: t.BaseBranch, Vars: vars}
}

func (a tasksAdapter) TaskTemplates() server.TaskTemplatesResponse {
	list, problems := tasktemplate.Load(a.dir)
	resp := server.TaskTemplatesResponse{LocalTasks: a.orch.LocalTasksAvailable(), Templates: make([]server.TaskTemplateRow, 0, len(list))}
	for _, t := range list {
		resp.Templates = append(resp.Templates, templateRow(t))
	}
	for _, p := range problems {
		resp.Problems = append(resp.Problems, server.TaskTemplateProblem{Path: p.Path, Message: p.Message})
	}
	return resp
}

func (a tasksAdapter) SaveTaskTemplate(name string, row server.TaskTemplateRow, replace bool) error {
	var invalid *tasktemplate.InvalidError
	t := tasktemplate.Template{Name: name, Title: row.Title, Description: row.Description, Instructions: row.Instructions,
		Profile: row.Profile, Playbook: row.Playbook, Backend: row.Backend, BaseBranch: row.BaseBranch}
	switch err := tasktemplate.Write(a.dir, t, replace); {
	case errors.Is(err, tasktemplate.ErrExists):
		return server.ErrTaskTemplateExists
	case errors.Is(err, tasktemplate.ErrInvalidName):
		return inputError("invalid_name", "name", "%s", strings.TrimPrefix(err.Error(), "tasktemplate: "))
	case errors.As(err, &invalid):
		return inputError("invalid_template", "", "%s", invalid.Message)
	default:
		return err
	}
}

func (a tasksAdapter) DeleteTaskTemplate(name string) error {
	switch err := tasktemplate.Delete(a.dir, name); {
	case errors.Is(err, tasktemplate.ErrNotFound), errors.Is(err, tasktemplate.ErrInvalidName):
		return server.ErrTaskNotFound
	default:
		return err
	}
}

// issueTaskRow is the issue detail's task record (#251), nil for an issue
// that is not a task.
func issueTaskRow(orch *orchestrator.Orchestrator, identifier string) *server.IssueTaskRow {
	spec, ok := orch.TaskSpec(identifier)
	if !ok {
		return nil
	}
	return &server.IssueTaskRow{Instructions: spec.Instructions, BaseBranch: spec.BaseBranch, Local: spec.Local, Started: spec.StartedAt != nil}
}
