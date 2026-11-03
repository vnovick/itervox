package main

import (
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/memory"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
)

func proposeTestLesson(t *testing.T, wf, correction string) memory.Entry {
	t.Helper()
	c := memory.Candidate{Trigger: memory.TriggerInputReply, Issue: "ENG-1", Corrections: []string{correction}, RunIDs: []string{"run-1"}}
	res, err := memory.Store{WorkflowPath: wf}.ProposeLesson(memory.NewLessonEntry(c, memory.TemplateDraft(c)), memory.ProposeOptions{})
	require.NoError(t, err)
	require.Equal(t, memory.LessonProposed, res.Outcome)
	return res.Entry
}

// TestMemoryLessonApproveRejectAPI (#203): POST /api/v1/memory/{id}/approve
// makes a proposed lesson active in the chosen scope with the edited text;
// POST /api/v1/memory/{id}/reject removes one and remembers it; acting on a
// non-proposal is 409, an unknown id 404, a bad scope 400. GET
// /api/v1/memory carries the lessons configuration.
func TestMemoryLessonApproveRejectAPI(t *testing.T) {
	f := newMemoryAPIFixture(t)
	a := proposeTestLesson(t, f.wf, "Don't add a CSV dependency; use the csv helper instead.")
	b := proposeTestLesson(t, f.wf, "Never log the API token; redact it.")

	var view server.MemoryView
	f.do(t, http.MethodGet, "/api/v1/memory", nil, http.StatusOK, &view)
	assert.Equal(t, "template", view.Lessons.Drafter)
	assert.Equal(t, 0, view.Lessons.Rejected)

	f.do(t, http.MethodPost, "/api/v1/memory/"+a.ID+"/approve", map[string]any{"scope": "team"}, http.StatusBadRequest, nil)
	text := "When a feature needs CSV, reuse the csv helper instead of adding a dependency."
	var approved server.MemoryEntryView
	f.do(t, http.MethodPost, "/api/v1/memory/"+a.ID+"/approve", map[string]any{"scope": "project", "text": text}, http.StatusOK, &approved)
	assert.Equal(t, memory.StatusActive, approved.Status)
	assert.Equal(t, memory.ScopeProject, approved.Scope)
	assert.Equal(t, text, approved.Text)
	assert.Equal(t, memory.ProjectPath(f.wf), approved.Path)
	f.do(t, http.MethodPost, "/api/v1/memory/"+a.ID+"/approve", nil, http.StatusConflict, nil)
	f.do(t, http.MethodPost, "/api/v1/memory/"+a.ID+"/reject", nil, http.StatusConflict, nil)
	f.do(t, http.MethodPost, "/api/v1/memory/nope/reject", nil, http.StatusNotFound, nil)

	var rejected server.MemoryEntryView
	f.do(t, http.MethodPost, "/api/v1/memory/"+b.ID+"/reject", nil, http.StatusOK, &rejected)
	assert.Equal(t, b.ID, rejected.ID)
	f.do(t, http.MethodGet, "/api/v1/memory", nil, http.StatusOK, &view)
	assert.Equal(t, 1, view.Lessons.Rejected)
	for _, e := range view.Entries {
		assert.NotEqual(t, b.ID, e.ID, "the rejected lesson is gone")
	}
}

// TestMemoryViewFlagsLessonsNotHelping (#203): GET /api/v1/memory flags an
// approved lesson the last memory.lessons.stale_after_runs runs did not use.
func TestMemoryViewFlagsLessonsNotHelping(t *testing.T) {
	f := newMemoryAPIFixture(t)
	e := proposeTestLesson(t, f.wf, "Always run the linter before pushing instead of after.")
	_, err := memory.Store{WorkflowPath: f.wf}.ApproveLesson(e.ID, memory.ApproveOptions{})
	require.NoError(t, err)
	cfg := &config.Config{WorkflowPath: f.wf, Memory: config.MemoryConfig{Enabled: true, Lessons: config.LessonsConfig{StaleAfterRuns: 2}}}
	later := time.Now().Add(time.Hour)
	adapter := &memoryAdapter{cfg: cfg, history: func() []orchestrator.CompletedRun {
		return []orchestrator.CompletedRun{{Identifier: "ENG-2", Status: "succeeded", FinishedAt: later}, {Identifier: "ENG-3", Status: "succeeded", FinishedAt: later}}
	}}
	view := adapter.ListMemory()
	require.Len(t, view.Entries, 1)
	assert.Equal(t, memory.LessonUnused, view.Entries[0].NotHelping)
	assert.Equal(t, "not used by the last 2 runs", view.Entries[0].NotHelpingReason)
}

// TestProposedLessonCounter (#203): the snapshot's proposedLessons counts
// the proposed entries of both files and follows changes to them.
func TestProposedLessonCounter(t *testing.T) {
	f := newMemoryAPIFixture(t)
	c := newProposedLessonCounter(f.wf)
	assert.Equal(t, 0, c.Count())
	a := proposeTestLesson(t, f.wf, "Don't add a CSV dependency; use the csv helper instead.")
	proposeTestLesson(t, f.wf, "Never log the API token; redact it.")
	assert.Equal(t, 2, c.Count())
	_, err := memory.Store{WorkflowPath: f.wf}.ApproveLesson(a.ID, memory.ApproveOptions{Scope: memory.ScopeProject})
	require.NoError(t, err)
	// A rewrite within the file system's timestamp granularity keeps the
	// size check honest: the approve changed the private file's size.
	assert.Equal(t, 1, c.Count())
	require.NoError(t, os.Remove(f.user))
	assert.Equal(t, 0, c.Count())
}
