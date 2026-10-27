package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/server"
)

// runTaskCLI runs `itervox task` against d.
func runTaskCLI(t *testing.T, d *fakeDaemon, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut strings.Builder
	code := taskCommand(t.Context(), append([]string{"--url", d.srv.URL}, args...), &out, &errOut)
	return code, out.String(), errOut.String()
}

// TestTaskCLIFlags (#251): `itervox task "<title>"` posts the task to the
// daemon with every flag mapped onto the request (instructions inline or
// from @file, profile, backend, playbook, base, --tracker as where, --start,
// --template with repeated --var), prints the created identifier, refuses
// bad usage with exit 2 before any request, reports a daemon refusal with
// exit 1, and lists templates with --templates.
func TestTaskCLIFlags(t *testing.T) {
	var d *fakeDaemon
	d = newFakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/task-templates":
			writeJSONBody(w, server.TaskTemplatesResponse{LocalTasks: true, Templates: []server.TaskTemplateRow{
				{Name: "upgrade", Title: "Upgrade {{package}}", Vars: []string{"package"}}}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/tasks":
			var req server.TaskCreateRequest // the recorder already read the body
			calls := d.recorded()
			_ = json.Unmarshal([]byte(calls[len(calls)-1].body), &req)
			if req.Title == "refuse me" {
				w.WriteHeader(http.StatusConflict)
				writeJSONBody(w, map[string]any{"error": map[string]string{"code": "local_tasks_unavailable", "message": "this project's tracker cannot hold local tasks"}})
				return
			}
			w.WriteHeader(http.StatusCreated)
			writeJSONBody(w, server.TaskCreateResult{Identifier: "TASK-7", Title: req.Title, State: "Todo",
				Local: req.Where != server.TaskWhereTracker, Started: req.Start})
		default:
			http.NotFound(w, r)
		}
	})
	instrFile := filepath.Join(t.TempDir(), "instructions.md")
	require.NoError(t, os.WriteFile(instrFile, []byte("Run `make test`.\n"), 0o644))

	code, out, errOut := runTaskCLI(t, d, "Upgrade lodash", "--instructions", "@"+instrFile, "--profile", "implementer",
		"--backend", "codex", "--playbook", "dependency-upgrade", "--base", "release/2.x", "--start")
	require.Equal(t, 0, code, errOut)
	assert.Contains(t, out, "Created TASK-7 (a local task) in Todo: Upgrade lodash")
	assert.Contains(t, out, "Dispatch requested")
	calls := d.recorded()
	require.Len(t, calls, 1)
	assert.Equal(t, "Bearer "+clientTestToken, calls[0].auth)
	var got server.TaskCreateRequest
	require.NoError(t, json.Unmarshal([]byte(calls[0].body), &got))
	assert.Equal(t, server.TaskCreateRequest{Title: "Upgrade lodash", Instructions: "Run `make test`.\n", Profile: "implementer",
		Backend: "codex", Playbook: "dependency-upgrade", BaseBranch: "release/2.x", Where: server.TaskWhereLocal, Start: true}, got)

	// --tracker, inline instructions, flags after the title.
	code, out, _ = runTaskCLI(t, d, "--tracker", "Add parser tests", "--instructions", "Cover the error paths.")
	require.Equal(t, 0, code)
	assert.Contains(t, out, "(a tracker issue)")
	assert.Contains(t, out, "next poll")
	calls = d.recorded()
	got = server.TaskCreateRequest{}
	require.NoError(t, json.Unmarshal([]byte(calls[len(calls)-1].body), &got))
	assert.Equal(t, server.TaskCreateRequest{Title: "Add parser tests", Instructions: "Cover the error paths.", Where: server.TaskWhereTracker}, got)

	// --template with vars, no title.
	code, _, _ = runTaskCLI(t, d, "--template", "upgrade", "--var", "package=lodash", "--var", "version=4.17.21")
	require.Equal(t, 0, code)
	calls = d.recorded()
	got = server.TaskCreateRequest{}
	require.NoError(t, json.Unmarshal([]byte(calls[len(calls)-1].body), &got))
	assert.Equal(t, "upgrade", got.Template)
	assert.Equal(t, map[string]string{"package": "lodash", "version": "4.17.21"}, got.Vars)
	assert.Empty(t, got.Title)

	// Usage errors: exit 2 and no request.
	n := len(d.recorded())
	for _, args := range [][]string{
		{},                                 // no title, no template
		{"a", "b"},                         // unquoted title
		{"x", "--var", "k=v"},              // --var without --template
		{"--template", "t", "--var", "kv"}, // not key=value
		{"x", "--instructions", "@" + filepath.Join(t.TempDir(), "missing.md")},
		{"x", "--no-such-flag"},
	} {
		code, _, errOut := runTaskCLI(t, d, args...)
		assert.Equal(t, 2, code, "%v: %s", args, errOut)
	}
	assert.Len(t, d.recorded(), n, "bad usage sends nothing")

	// A refusal is exit 1 with the daemon's message.
	code, _, errOut = runTaskCLI(t, d, "refuse me")
	assert.Equal(t, 1, code)
	assert.Contains(t, errOut, "cannot hold local tasks")

	// --templates lists them.
	code, out, _ = runTaskCLI(t, d, "--templates")
	require.Equal(t, 0, code)
	assert.Contains(t, out, "upgrade — Upgrade {{package}} (vars: package)")
}
