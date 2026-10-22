package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
)

// Distinctive strings that must never reach the feed (#213).
const (
	markerPrompt   = "PROMPT-MARKER-b71e"
	markerOutput   = "AGENT-OUTPUT-MARKER-91c2"
	markerResult   = "RESULT-MARKER-3d0f"
	markerQuestion = "QUESTION-MARKER-5aa8"
	markerFailure  = "FAILURE-MARKER-c4e2"
	markerReply    = "REPLY-MARKER-77b1"
	markerSecret   = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
)

// markerRunner answers ENG-1 with a successful turn full of output, ENG-2
// with a question, and fails ENG-3 — each with a marker in the text.
type markerRunner struct {
	ok, ask *agenttest.FakeRunner
	fail    *agenttest.CountingFailRunner
}

func (m markerRunner) RunTurn(ctx context.Context, l agent.Logger, p func(agent.TurnResult), sid *string, prompt, ws, cmd, host, logDir string, rt, tt int, pm agent.PermissionMode) (agent.TurnResult, error) {
	switch {
	case strings.Contains(prompt, "ENG-2"):
		return m.ask.RunTurn(ctx, l, p, sid, prompt, ws, cmd, host, logDir, rt, tt, pm)
	case strings.Contains(prompt, "ENG-3"):
		return m.fail.RunTurn(ctx, l, p, sid, prompt, ws, cmd, host, logDir, rt, tt, pm)
	default:
		return m.ok.RunTurn(ctx, l, p, sid, prompt, ws, cmd, host, logDir, rt, tt, pm)
	}
}

// TestFeedNeverContainsAgentOutput (#213): runs that succeed with output,
// ask a question and fail — plus an operator reply and a pause reason
// carrying a secret — produce feed events, and neither the persisted
// feed.jsonl nor GET /api/v1/feed contains any agent output, prompt text,
// question, failure text, reply or the secret.
func TestFeedNeverContainsAgentOutput(t *testing.T) {
	dir := t.TempDir()
	feedPath := filepath.Join(dir, "feed.jsonl")
	cfg := &config.Config{}
	cfg.Tracker.ActiveStates = []string{"Todo", "In Progress"}
	cfg.Tracker.TerminalStates = []string{"Done"}
	cfg.Agent.Command = "claude"
	cfg.Agent.MaxConcurrentAgents = 3
	cfg.Agent.MaxTurns = 1
	cfg.Agent.MaxRetries = 1
	cfg.Agent.MaxRetryBackoffMs = 10
	cfg.Polling.IntervalMs = 20
	cfg.PromptTemplate = "Work on {{ issue.identifier }}. " + markerPrompt
	issues := []domain.Issue{
		{ID: "id1", Identifier: "ENG-1", Title: "One", State: "In Progress"},
		{ID: "id2", Identifier: "ENG-2", Title: "Two", State: "In Progress"},
		{ID: "id3", Identifier: "ENG-3", Title: "Three", State: "In Progress"},
	}
	mt := tracker.NewMemoryTracker(issues, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	runner := markerRunner{
		ok: agenttest.NewFakeRunner([]agent.StreamEvent{
			{Type: agent.EventSystem, SessionID: "s1"},
			{Type: agent.EventAssistant, SessionID: "s1", Message: markerOutput, TextBlocks: []string{markerOutput}},
			{Type: agent.EventResult, SessionID: "s1", ResultText: markerResult},
		}),
		ask:  agenttest.InputRequiredRunner("s2", markerQuestion),
		fail: agenttest.FailRunner("Error: " + markerFailure),
	}
	orch := orchestrator.New(cfg, mt, runner, nil)
	orch.SetFeedFile(feedPath)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = orch.Run(ctx) }()

	kinds := func() map[string]int {
		out := map[string]int{}
		for _, e := range orch.Feed(0, 0).Events {
			out[e.Kind+"/"+e.Identifier]++
		}
		return out
	}
	require.Eventually(t, func() bool {
		k := kinds()
		return k["run_finished/ENG-1"] > 0 && k["input_required/ENG-2"] > 0 && k["run_failed/ENG-3"] > 0
	}, 10*time.Second, 10*time.Millisecond, "the three runs reach the feed")
	require.NoError(t, orch.ProvideInput("ENG-2", markerReply))
	require.Eventually(t, func() bool { return kinds()["input_answered/ENG-2"] > 0 }, 5*time.Second, 10*time.Millisecond)
	paused, err := orch.PauseDispatch(ctx, "deploy with token "+markerSecret, time.Time{})
	require.NoError(t, err)
	assert.NotContains(t, paused.Reason, markerSecret, "the pause reason is redacted")

	const token = "feed-test-token-0123456789abcdef"
	srv := server.New(server.Config{
		Snapshot:    func() server.StateSnapshot { return server.StateSnapshot{} },
		RefreshChan: make(chan struct{}, 1),
		APIToken:    token,
		Dispatch:    dispatchAdapter{orch: orch},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/feed?since=0&limit=1000", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	api := w.Body.String()
	var page server.FeedPage
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &page))
	require.NotEmpty(t, page.Events)
	assert.Equal(t, page.Events[len(page.Events)-1].Seq, page.NextSeq)
	assert.Equal(t, "dispatch_paused", page.Events[len(page.Events)-1].Kind)

	cancel()
	<-done
	file, err := os.ReadFile(feedPath)
	require.NoError(t, err)
	require.Contains(t, string(file), `"kind":"run_failed"`, "the file holds the same events")
	for _, m := range []string{markerPrompt, markerOutput, markerResult, markerQuestion, markerFailure, markerReply, markerSecret, "Work on"} {
		assert.NotContains(t, api, m, "GET /api/v1/feed leaks %q", m)
		assert.NotContains(t, string(file), m, "feed.jsonl leaks %q", m)
	}
}

// TestDispatchEndpointsRoundTrip (#213): POST /api/v1/dispatch/pause and
// /resume go through the event loop and answer the applied pause; a past
// until is a 400.
func TestDispatchEndpointsRoundTrip(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tracker.ActiveStates = []string{"Todo"}
	cfg.Tracker.TerminalStates = []string{"Done"}
	cfg.Polling.IntervalMs = 50
	orch := orchestrator.New(cfg, tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates), nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = orch.Run(ctx) }()
	defer func() { cancel(); <-done }()

	const token = "feed-test-token-0123456789abcdef"
	srv := server.New(server.Config{
		Snapshot:    func() server.StateSnapshot { return server.StateSnapshot{} },
		RefreshChan: make(chan struct{}, 1),
		APIToken:    token,
		Dispatch:    dispatchAdapter{orch: orch},
	})
	post := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		return w
	}
	var w *httptest.ResponseRecorder
	require.Eventually(t, func() bool {
		w = post("/api/v1/dispatch/pause", `{"reason":"deploy","until":"`+time.Now().Add(time.Hour).UTC().Format(time.RFC3339)+`"}`)
		return w.Code == http.StatusOK
	}, 5*time.Second, 10*time.Millisecond, "the loop answers once it runs")
	var res struct {
		DispatchPaused *server.DispatchPauseRow `json:"dispatchPaused"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &res))
	require.NotNil(t, res.DispatchPaused)
	assert.Equal(t, "deploy", res.DispatchPaused.Reason)
	require.NotNil(t, res.DispatchPaused.Until)
	assert.True(t, orch.Snapshot().DispatchPaused.Active)

	assert.Equal(t, http.StatusBadRequest, post("/api/v1/dispatch/pause", `{"until":"2001-01-01T00:00:00Z"}`).Code)

	w = post("/api/v1/dispatch/resume", "")
	require.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"dispatchPaused":null}`, w.Body.String())
	assert.False(t, orch.Snapshot().DispatchPaused.Active)
	assert.Nil(t, dispatchPauseRow(orch.Snapshot().DispatchPaused), "the snapshot row is null when not paused")
}
