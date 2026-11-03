package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/gitexec"
	"github.com/vnovick/itervox/internal/memory"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
)

// memoryAPIFixture is a git repository with a WORKFLOW.md, a private memory
// file redirected into the temp dir, and a server whose Memory client is
// the real adapter.
type memoryAPIFixture struct {
	dir, wf, user string
	srv           *server.Server
}

func newMemoryAPIFixture(t *testing.T) *memoryAPIFixture {
	t.Helper()
	dir := t.TempDir()
	initGitRepo(t, dir)
	wf := filepath.Join(dir, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(wf, []byte("---\n---\n"), 0o644))
	user := filepath.Join(t.TempDir(), ".itervox", "memory.json")
	t.Setenv(memory.UserMemoryEnv, user)
	cfg := &config.Config{WorkflowPath: wf, Memory: config.MemoryConfig{Enabled: true, TokenBudget: 2000, PromptShareTokens: 8000}}
	srv := server.New(server.Config{
		Snapshot:     func() server.StateSnapshot { return server.StateSnapshot{} },
		RefreshChan:  make(chan struct{}, 1),
		Memory:       &memoryAdapter{cfg: cfg},
		AllowedHosts: []string{"example.com"},
	})
	return &memoryAPIFixture{dir: dir, wf: wf, user: user, srv: srv}
}

func (f *memoryAPIFixture) do(t *testing.T, method, path string, body any, wantStatus int, out any) {
	t.Helper()
	rec := rulesRequest(t, f.srv, method, path, body)
	require.Equal(t, wantStatus, rec.Code, rec.Body.String())
	if out != nil {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), out))
	}
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-c", "user.email=t@example.com", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)
	out, err := gitexec.Command(context.Background(), dir, full...).CombinedOutput()
	require.NoError(t, err, string(out))
}

// TestMemoryPromoteMovesEntryToProjectFile (#202): POST
// /api/v1/memory/{id}/promote moves a private entry into
// .itervox/memory.json (same id, provenance kept, scope project), removes it
// from ~/.itervox/memory.json, and returns the project file's git diff —
// the change to the committed file, or the whole file while it is new.
// Promoting a shared entry is a conflict.
func TestMemoryPromoteMovesEntryToProjectFile(t *testing.T) {
	f := newMemoryAPIFixture(t)

	// A committed project file with one shared entry.
	var shared server.MemoryEntryView
	f.do(t, http.MethodPost, "/api/v1/memory", map[string]any{"text": "CI runs on Node 22.", "scope": "project"}, http.StatusCreated, &shared)
	gitIn(t, f.dir, "add", "-f", ".itervox/memory.json")
	gitIn(t, f.dir, "commit", "-q", "-m", "memory")

	var priv server.MemoryEntryView
	f.do(t, http.MethodPost, "/api/v1/memory", map[string]any{
		"text": "This repo uses pnpm, not npm.", "type": "preference", "tags": []string{"tooling"},
		"source": map[string]any{"issue": "ENG-7", "links": []string{"https://github.com/acme/app/pull/3"}},
	}, http.StatusCreated, &priv)
	assert.Equal(t, memory.ScopePrivate, priv.Scope)
	assert.Equal(t, f.user, priv.Path)

	var res server.MemoryPromoteResult
	f.do(t, http.MethodPost, "/api/v1/memory/"+priv.ID+"/promote", nil, http.StatusOK, &res)
	assert.Equal(t, priv.ID, res.Entry.ID)
	assert.Equal(t, memory.ScopeProject, res.Entry.Scope)
	assert.Equal(t, "ENG-7", res.Entry.Source.Issue, "provenance moves with the entry")
	assert.Equal(t, memory.ProjectPath(f.wf), res.Path)
	assert.Contains(t, res.Diff, "diff --git")
	assert.Contains(t, res.Diff, `+      "text": "This repo uses pnpm, not npm."`)
	assert.Empty(t, res.DiffNote)

	project := memory.ReadFile(memory.ScopeProject, memory.ProjectPath(f.wf))
	require.NoError(t, project.Err)
	assert.Equal(t, []string{shared.ID, priv.ID}, idsOf(project.Entries))
	private := memory.ReadFile(memory.ScopePrivate, f.user)
	require.NoError(t, private.Err)
	assert.Empty(t, private.Entries, "the entry left the private file")

	f.do(t, http.MethodPost, "/api/v1/memory/"+priv.ID+"/promote", nil, http.StatusConflict, nil)
	f.do(t, http.MethodPost, "/api/v1/memory/nope/promote", nil, http.StatusNotFound, nil)

	// An untracked project file: the diff shows the whole file as new.
	g := newMemoryAPIFixture(t)
	var e server.MemoryEntryView
	g.do(t, http.MethodPost, "/api/v1/memory", map[string]any{"text": "Staging is read-only on Fridays."}, http.StatusCreated, &e)
	g.do(t, http.MethodPost, "/api/v1/memory/"+e.ID+"/promote", nil, http.StatusOK, &res)
	assert.Contains(t, res.Diff, "Staging is read-only on Fridays.")
	assert.Contains(t, res.DiffNote, "not committed yet")
}

func idsOf(entries []memory.Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.ID
	}
	return out
}

// TestMemoryEndpointsEditPinDeleteAndRestore (#202): the Memory page's
// endpoints list both files, edit, pin, archive, delete (returning the
// entry) and restore it unchanged by posting it back, reject oversize text
// with 413, and never write a secret.
func TestMemoryEndpointsEditPinDeleteAndRestore(t *testing.T) {
	f := newMemoryAPIFixture(t)
	var e server.MemoryEntryView
	f.do(t, http.MethodPost, "/api/v1/memory", map[string]any{"text": "Use the staging DB for e2e."}, http.StatusCreated, &e)
	assert.Equal(t, "manual", e.Source.Kind)
	assert.Equal(t, []server.MemoryRunRef{}, e.Runs)

	f.do(t, http.MethodPut, "/api/v1/memory/"+e.ID, map[string]any{"text": "Use staging for e2e; token ghp_abcdefghijklmnopqrstuvwxyz0123456789AB"}, http.StatusOK, &e)
	assert.NotContains(t, e.Text, "ghp_")
	f.do(t, http.MethodPost, "/api/v1/memory/"+e.ID+"/pin", nil, http.StatusOK, &e)
	assert.True(t, e.Pinned)
	var unpinned server.MemoryEntryView // pinned is omitted when false
	f.do(t, http.MethodPost, "/api/v1/memory/"+e.ID+"/pin", map[string]any{"pinned": false}, http.StatusOK, &unpinned)
	assert.False(t, unpinned.Pinned)
	f.do(t, http.MethodPut, "/api/v1/memory/"+e.ID, map[string]any{"status": "archived"}, http.StatusOK, &e)
	assert.Equal(t, memory.StatusArchived, e.Status)
	f.do(t, http.MethodPut, "/api/v1/memory/"+e.ID, map[string]any{"status": "forgotten"}, http.StatusBadRequest, nil)
	f.do(t, http.MethodPost, "/api/v1/memory", map[string]any{"text": strings.Repeat("x", memory.MaxTextBytes+1)}, http.StatusRequestEntityTooLarge, nil)

	var view server.MemoryView
	f.do(t, http.MethodGet, "/api/v1/memory", nil, http.StatusOK, &view)
	assert.True(t, view.Enabled)
	assert.Equal(t, 2000, view.TokenBudget)
	require.Len(t, view.Files, 2)
	assert.Equal(t, memory.ScopePrivate, view.Files[0].Scope)
	require.Len(t, view.Entries, 1)

	var deleted server.MemoryEntryView
	f.do(t, http.MethodDelete, "/api/v1/memory/"+e.ID, nil, http.StatusOK, &deleted)
	f.do(t, http.MethodGet, "/api/v1/memory", nil, http.StatusOK, &view)
	assert.Empty(t, view.Entries)
	var restored server.MemoryEntryView
	f.do(t, http.MethodPost, "/api/v1/memory", deleted.Entry, http.StatusCreated, &restored)
	assert.Equal(t, deleted.Entry, restored.Entry, "undo puts the entry back as it was")
	f.do(t, http.MethodDelete, "/api/v1/memory/"+e.ID+"x", nil, http.StatusNotFound, nil)

	raw, err := os.ReadFile(f.user)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "ghp_abcdefghijklmnopqrstuvwxyz0123456789AB")
}

// TestMemoryListShowsTheRunsThatUsedAnEntry (#202): each entry lists the
// newest history runs whose prompt carried it.
func TestMemoryListShowsTheRunsThatUsedAnEntry(t *testing.T) {
	f := newMemoryAPIFixture(t)
	var e server.MemoryEntryView
	f.do(t, http.MethodPost, "/api/v1/memory", map[string]any{"text": "pnpm"}, http.StatusCreated, &e)
	hist := []orchestrator.CompletedRun{
		{Identifier: "ENG-1", RunID: "r1", Status: "succeeded", FinishedAt: time.Now(), MemoryIDs: []string{e.ID}},
		{Identifier: "ENG-2", RunID: "r2", Status: "failed", FinishedAt: time.Now()},
	}
	a := &memoryAdapter{cfg: &config.Config{WorkflowPath: f.wf}, history: func() []orchestrator.CompletedRun { return hist }}
	view := a.ListMemory()
	require.Len(t, view.Entries, 1)
	require.Len(t, view.Entries[0].Runs, 1)
	assert.Equal(t, "r1", view.Entries[0].Runs[0].RunID)
	assert.Equal(t, "ENG-1", view.Entries[0].Runs[0].Identifier)
}

// TestMemoryFileGitignorePolicy (#202): `itervox init` keeps
// .itervox/memory.json committable under the runtime /*.json ignore and
// under a root .gitignore that hides .itervox/.
func TestMemoryFileGitignorePolicy(t *testing.T) {
	for name, root := range map[string]string{"root hides .itervox": ".itervox/\n", "plain root": "node_modules/\n"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			initGitRepo(t, dir)
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(root), 0o644))
			require.NoError(t, finalizeItervoxGitignore(filepath.Join(dir, ".itervox")))
			for _, name := range []string{"memory.json", "tracker_store.json"} {
				require.NoError(t, os.WriteFile(filepath.Join(dir, ".itervox", name), []byte("{}\n"), 0o644))
			}
			assert.False(t, gitPathIgnored(t, dir, ".itervox/memory.json"), "the project memory is committable")
			assert.True(t, gitPathIgnored(t, dir, ".itervox/tracker_store.json"), "runtime JSON stays ignored")
		})
	}
}
