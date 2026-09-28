package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/outbox"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
)

// TestFailureAckThroughRouter (CORE-175): POST
// /api/v1/issues/{identifier}/failures/ack goes through the real router
// (bearer auth + host guard), the adapter, an event into the real event loop
// and back out in the snapshot as failureAcks, with the failure_ack
// capability advertised. A later failure is not covered by an earlier ack.
func TestFailureAckThroughRouter(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tracker.ActiveStates = []string{"Todo"}
	cfg.Tracker.TerminalStates = []string{"Done"}
	cfg.Polling.IntervalMs = 50
	cfg.Agent.MaxConcurrentAgents = 1
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := orchestrator.New(cfg, mt, &agenttest.FakeRunner{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = orch.Run(ctx); close(done) }() // test driver
	t.Cleanup(func() { cancel(); <-done })

	failedAt := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	require.True(t, orch.RecordFailure(orchestrator.FailureRecord{
		Kind: orchestrator.FailureKindWorkerFailed, Identifier: "ENG-1", Message: "agent error", OccurredAt: failedAt,
	}))
	require.Eventually(t, func() bool { return len(orch.Snapshot().RecentFailures) == 1 }, 5*time.Second, 5*time.Millisecond)

	adapter := &orchestratorAdapter{orch: orch, cfg: cfg, tr: mt, notify: func() {}}
	ob, err := outbox.New("")
	require.NoError(t, err)
	snap := buildSnapFunc(orch, mt, cfg, "app", nil, "", nil, ob)
	srv := server.New(server.Config{
		Snapshot:     snap,
		RefreshChan:  make(chan struct{}, 1),
		Client:       adapter,
		APIToken:     "tok",
		AllowedHosts: []string{"example.com"},
	})
	post := func(ident, body, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/issues/"+ident+"/failures/ack", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		return w
	}
	upTo := failedAt.Format(time.RFC3339Nano)

	assert.Equal(t, http.StatusUnauthorized, post("ENG-1", `{"upTo":"`+upTo+`"}`, "").Code, "the M1 auth guard applies")
	assert.Equal(t, http.StatusBadRequest, post("ENG-1", `{"upTo":"yesterday"}`, "tok").Code)
	assert.Equal(t, http.StatusBadRequest, post("ENG-1", `{}`, "tok").Code)
	nf := post("ENG-9", `{"upTo":"`+upTo+`"}`, "tok")
	assert.Equal(t, http.StatusNotFound, nf.Code)
	assert.Contains(t, nf.Body.String(), "issue_not_found")

	ok := post("ENG-1", `{"upTo":"`+upTo+`"}`, "tok")
	require.Equal(t, http.StatusAccepted, ok.Code, ok.Body.String())
	assert.JSONEq(t, `{"queued":true}`, ok.Body.String())

	var wire struct {
		Capabilities []string `json:"capabilities"`
		FailureAcks  []struct {
			Identifier string `json:"identifier"`
			UpTo       string `json:"upTo"`
		} `json:"failureAcks"`
	}
	require.Eventually(t, func() bool {
		b, _ := json.Marshal(snap())
		_ = json.Unmarshal(b, &wire)
		return len(wire.FailureAcks) == 1
	}, 5*time.Second, 5*time.Millisecond, "the ack reaches the snapshot through the event loop")
	assert.Equal(t, "ENG-1", wire.FailureAcks[0].Identifier)
	gotUpTo, err2 := time.Parse(time.RFC3339Nano, wire.FailureAcks[0].UpTo)
	require.NoError(t, err2)
	assert.True(t, gotUpTo.Equal(failedAt))
	assert.Contains(t, wire.Capabilities, "failure_ack")

	// An older upTo never lowers the recorded ack.
	require.Equal(t, http.StatusAccepted, post("ENG-1", `{"upTo":"`+failedAt.Add(-time.Hour).Format(time.RFC3339)+`"}`, "tok").Code)
	time.Sleep(100 * time.Millisecond)
	st := orch.Snapshot()
	assert.True(t, st.FailureAcks["ENG-1"].Equal(failedAt), "acks keep the max upTo")
}
