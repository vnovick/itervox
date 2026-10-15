package server_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/server"
)

// #178 step 3: a session started by hand is listed with owner "external",
// its directory, last write and — while its CLI may still run — why.
func TestSessionsEndpointListsExternalSessions(t *testing.T) {
	updated := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	f := &fakeSessions{rows: []server.SessionRow{
		{ID: "hand-1", Owner: server.SessionOwnerExternal, Title: "Add health check", Backend: "claude", Status: "running",
			Cwd: "/work/app", UpdatedAt: &updated, InUseReason: "claude process 7 has the transcript open"},
		{ID: "hand-2", Owner: server.SessionOwnerExternal, Backend: "codex", Status: "exited", Cwd: "/work/app/svc"},
	}}
	w := httptest.NewRecorder()
	sessionsServer(f).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil))
	require.Equal(t, http.StatusOK, w.Code)
	var body struct {
		Sessions []map[string]any `json:"sessions"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	for k, want := range map[string]any{
		"id": "hand-1", "owner": "external", "identifier": "", "title": "Add health check", "status": "running",
		"cwd": "/work/app", "updatedAt": "2026-10-15T09:00:00Z", "inUseReason": "claude process 7 has the transcript open",
	} {
		assert.Equal(t, want, body.Sessions[0][k], k)
	}
	assert.Equal(t, "exited", body.Sessions[1]["status"])
	assert.NotContains(t, body.Sessions[1], "inUseReason")
}

// The transcript endpoint pages backwards with ?before= and ?limit= and
// returns the documented shape; bad cursors and unknown sessions are
// refused.
func TestSessionTranscriptEndpoint(t *testing.T) {
	at := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	before := int64(4096)
	f := &fakeSessions{page: server.TranscriptResponse{ID: "hand-1", Backend: "claude", HasMore: true, Before: &before, Size: 9000,
		Entries: []server.TranscriptEntryRow{{Role: "user", Kind: "text", Text: "hi", At: &at}, {Role: "assistant", Kind: "tool", Text: "Edit"}}}}
	srv := sessionsServer(f)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/hand-1/transcript?before=8192&limit=50", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, []any{"hand-1", int64(8192), 50}, f.pageArgs)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, true, body["hasMore"])
	assert.Equal(t, float64(4096), body["before"])
	entries := body["entries"].([]any)
	require.Len(t, entries, 2)
	assert.Equal(t, map[string]any{"role": "user", "kind": "text", "text": "hi", "at": "2026-10-15T09:00:00Z"}, entries[0])

	w = httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/hand-1/transcript", nil))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, []any{"hand-1", int64(-1), 0}, f.pageArgs, "no cursor reads from the end")

	for _, q := range []string{"?before=-1", "?before=x", "?limit=0", "?limit=501"} {
		w = httptest.NewRecorder()
		srv.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/hand-1/transcript"+q, nil))
		assert.Equal(t, http.StatusBadRequest, w.Code, q)
	}
	f.pageErr = server.ErrSessionNotFound
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/nope/transcript", nil))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func postContinue(t *testing.T, f server.SessionsClient, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+id+"/continue", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	sessionsServer(f).ServeHTTP(w, req)
	return w
}

// POST /continue answers 202 with the now-owned session, 409
// session_in_use (with the reason) while the original process may still
// run, and maps the other refusals.
func TestContinueSessionEndpoint(t *testing.T) {
	f := &fakeSessions{}
	w := postContinue(t, f, "hand-1", `{"message":"go on","profile":"coder"}`)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	assert.Equal(t, []string{"hand-1", "go on", "coder"}, f.continueArgs)
	var body struct {
		Session map[string]any `json:"session"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "itervox", body.Session["owner"])
	assert.Equal(t, "manual", body.Session["kind"])

	w = postContinue(t, f, "hand-1", "")
	assert.Equal(t, http.StatusAccepted, w.Code, "an empty body continues with the default prompt")

	for _, c := range []struct {
		err  error
		code int
		body string
	}{
		{&server.SessionInUseError{Reason: "claude process 7 has the transcript open"}, http.StatusConflict, `"session_in_use"`},
		{server.ErrSessionAlreadyOwned, http.StatusConflict, `"already_owned"`},
		{server.ErrUnknownProfile, http.StatusBadRequest, `"profile"`},
		{server.ErrTooManySessions, http.StatusTooManyRequests, `"too_many_sessions"`},
		{server.ErrSessionNotFound, http.StatusNotFound, `"not_found"`},
		{server.ErrDraining, http.StatusConflict, `"draining"`},
		{server.ErrBusy, http.StatusServiceUnavailable, `"orchestrator_busy"`},
	} {
		f.continueErr = c.err
		w = postContinue(t, f, "hand-1", `{}`)
		assert.Equal(t, c.code, w.Code, c.err.Error())
		assert.Contains(t, w.Body.String(), c.body, c.err.Error())
	}
	f.continueErr = &server.SessionInUseError{Reason: "claude process 7 has the transcript open"}
	w = postContinue(t, f, "hand-1", `{}`)
	assert.Contains(t, w.Body.String(), "claude process 7 has the transcript open")
	var inUse *server.SessionInUseError
	assert.True(t, errors.As(f.continueErr, &inUse))

	w = postContinue(t, &fakeSessions{}, "hand-1", `{"message":"`+strings.Repeat("x", 10*1024+1)+`"}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}
