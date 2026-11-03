package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// "Continue in itervox" was dropped (#178): there is no route that takes
// over a session started by hand.
func TestNoContinueRouteForHandStartedSessions(t *testing.T) {
	w := httptest.NewRecorder()
	sessionsServer(&fakeSessions{}).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/sessions/hand-1/continue", nil))
	assert.Contains(t, []int{http.StatusNotFound, http.StatusMethodNotAllowed}, w.Code, w.Body.String())
}
