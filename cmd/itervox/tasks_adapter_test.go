package main

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tasktemplate"
	"github.com/vnovick/itervox/internal/tracker"
)

// TestTasksAdapterResolvesTemplates (#251): a create naming a template
// takes the template's fields where the request leaves them empty and fills
// its placeholders from vars; missing vars, unknown templates, profiles,
// backends and playbooks are 400s on their form field; a template is saved
// and listed with its placeholders.
func TestTasksAdapterResolvesTemplates(t *testing.T) {
	cfg := &config.Config{}
	cfg.WorkflowPath = filepath.Join(t.TempDir(), "WORKFLOW.md")
	cfg.Tracker.ActiveStates = []string{"Todo"}
	cfg.Agent.Profiles = map[string]config.AgentProfile{"implementer": {Command: "claude"}}
	orch := orchestrator.New(cfg, tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, nil), nil, nil)
	a := newTasksAdapter(orch, cfg.WorkflowPath)
	require.NoError(t, a.SaveTaskTemplate("upgrade", server.TaskTemplateRow{
		Title: "Upgrade {{package}}", Instructions: "Bump {{package}} to {{version}}.", Profile: "implementer", BaseBranch: "main",
	}, false))
	assert.ErrorIs(t, a.SaveTaskTemplate("upgrade", server.TaskTemplateRow{Title: "x"}, false), server.ErrTaskTemplateExists)
	var in *server.TaskInputError
	assert.True(t, errors.As(a.SaveTaskTemplate("Bad Name", server.TaskTemplateRow{Title: "x"}, true), &in))
	list := a.TaskTemplates()
	require.Len(t, list.Templates, 1)
	assert.Equal(t, []string{"package", "version"}, list.Templates[0].Vars)
	assert.False(t, list.LocalTasks, "the memory tracker holds no local tasks")

	req, err := a.resolveTaskRequest(server.TaskCreateRequest{Template: "upgrade", Vars: map[string]string{"package": "lodash", "version": "4"}, Backend: "codex"})
	require.NoError(t, err)
	assert.Equal(t, orchestrator.TaskRequest{Title: "Upgrade lodash", Instructions: "Bump lodash to 4.", Profile: "implementer",
		Backend: "codex", BaseBranch: "main"}, req)

	for name, bad := range map[string]server.TaskCreateRequest{
		"vars":         {Template: "upgrade", Vars: map[string]string{"package": "x"}},
		"template":     {Template: "nope", Title: "x"},
		"profile":      {Title: "x", Profile: "ghost"},
		"backend":      {Title: "x", Backend: "gpt"},
		"playbook":     {Title: "x", Playbook: "ghost"},
		"baseBranch":   {Title: "x", BaseBranch: "-rf"},
		"title":        {Title: "  "},
		"instructions": {Title: "x", Instructions: string(make([]byte, orchestrator.MaxTaskInstructionsBytes+1))},
	} {
		_, err := a.resolveTaskRequest(bad)
		require.True(t, errors.As(err, &in), name)
		assert.Equal(t, name, in.Field, name)
	}
	require.NoError(t, a.DeleteTaskTemplate("upgrade"))
	assert.ErrorIs(t, a.DeleteTaskTemplate("upgrade"), server.ErrTaskNotFound)
	_, err = tasktemplate.Read(tasktemplate.Dir(cfg.WorkflowPath), "upgrade")
	assert.ErrorIs(t, err, tasktemplate.ErrNotFound)
}
