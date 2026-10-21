package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/server"
)

type fakeSessions struct {
	rows    []server.SessionRow
	sendErr error
	sentID  string
	sentMsg string
	// #178 step 3
	page     server.TranscriptResponse
	pageErr  error
	pageArgs []any
}

func (f *fakeSessions) SessionTranscript(id string, before int64, limit int) (server.TranscriptResponse, error) {
	f.pageArgs = []any{id, before, limit}
	return f.page, f.pageErr
}

func (f *fakeSessions) ListSessions() []server.SessionRow { return f.rows }

func (f *fakeSessions) SendSessionMessage(id, text string) (server.SessionMessageRow, error) {
	f.sentID, f.sentMsg = id, text
	if f.sendErr != nil {
		return server.SessionMessageRow{}, f.sendErr
	}
	return server.SessionMessageRow{ID: "msg-1", Text: text, Status: "queued", QueuedAt: time.Unix(0, 0).UTC()}, nil
}

func sessionsServer(f server.SessionsClient) *server.Server {
	cfg := makeTestConfig(baseSnap())
	cfg.Sessions = f
	return server.New(cfg)
}

// TestSessionsEndpointListsSessionsWithOwnerAndStatus: GET /api/v1/sessions
// returns the client's rows in order with the documented wire fields.
func TestSessionsEndpointListsSessionsWithOwnerAndStatus(t *testing.T) {
	started := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	f := &fakeSessions{rows: []server.SessionRow{
		{ID: "sess-w", Owner: server.SessionOwnerItervox, Identifier: "ENG-2", Status: "input_required", InputContext: "Which DB?", QueuedAt: &started},
		{ID: "sess-r", RunID: "run-1", Owner: server.SessionOwnerItervox, Identifier: "ENG-1", Profile: "coder", Backend: "claude", WorkerHost: "vm-1", Status: "running", StartedAt: &started, Steering: "live",
			Messages: []server.SessionMessageRow{{ID: "m1", Text: "hi", Status: "delivered", Delivery: "live", QueuedAt: started}}},
	}}
	w := httptest.NewRecorder()
	sessionsServer(f).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var body struct {
		Sessions []map[string]any `json:"sessions"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Len(t, body.Sessions, 2)
	assert.Equal(t, "sess-w", body.Sessions[0]["id"])
	assert.Equal(t, "input_required", body.Sessions[0]["status"])
	assert.Equal(t, "Which DB?", body.Sessions[0]["inputContext"])
	r := body.Sessions[1]
	for k, want := range map[string]any{
		"id": "sess-r", "runId": "run-1", "owner": "itervox", "identifier": "ENG-1", "profile": "coder",
		"backend": "claude", "workerHost": "vm-1", "status": "running", "startedAt": "2026-10-15T09:00:00Z", "steering": "live",
	} {
		assert.Equal(t, want, r[k], k)
	}
	msgs := r["messages"].([]any)
	assert.Equal(t, "delivered", msgs[0].(map[string]any)["status"])
}

func TestSessionsEndpointWithoutClientIs503(t *testing.T) {
	w := httptest.NewRecorder()
	server.New(makeTestConfig(baseSnap())).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil))
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func postSessionMessage(t *testing.T, f server.SessionsClient, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	sessionsServer(f).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+id+"/message", bytes.NewReader([]byte(body))))
	return w
}

// TestSessionMessageEndpointQueuesAndMapsErrors: POST
// /api/v1/sessions/{id}/message queues a message (202) and maps each refusal
// to its documented status.
func TestSessionMessageEndpointQueuesAndMapsErrors(t *testing.T) {
	f := &fakeSessions{}
	w := postSessionMessage(t, f, "sess-r", `{"message":"  please add tests  "}`)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	assert.Equal(t, "sess-r", f.sentID)
	assert.Equal(t, "please add tests", f.sentMsg)
	assert.Contains(t, w.Body.String(), `"status":"queued"`)

	for _, tc := range []struct {
		err  error
		code int
		key  string
	}{
		{server.ErrSessionNotFound, http.StatusNotFound, "not_found"},
		{server.ErrSessionInputRequired, http.StatusConflict, "input_required"},
		{server.ErrSessionNotOwned, http.StatusForbidden, "not_owned"},
		{server.ErrInvalidSessionMessage, http.StatusBadRequest, "bad_request"},
		{server.ErrBusy, http.StatusServiceUnavailable, "orchestrator_busy"},
	} {
		w := postSessionMessage(t, &fakeSessions{sendErr: tc.err}, "x", `{"message":"hi"}`)
		assert.Equal(t, tc.code, w.Code, tc.err.Error())
		assert.Contains(t, w.Body.String(), tc.key)
	}
	for _, body := range []string{`{"message":"   "}`, `nope`, `{"message":"` + strings.Repeat("x", 10*1024+1) + `"}`} {
		w := postSessionMessage(t, &fakeSessions{}, "x", body)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	}
}
