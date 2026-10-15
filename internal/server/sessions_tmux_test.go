package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/server"
)

func sessionsReq(t *testing.T, f server.SessionsClient, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	sessionsServer(f).ServeHTTP(w, req)
	return w
}

// #178 tmux: GET /api/v1/sessions carries the top-level tmux status and the
// New session options; rows carry tmuxPane (null when none) and
// controllable.
func TestSessionsEndpointCarriesTmuxStatus(t *testing.T) {
	f := &fakeSessions{
		rows: []server.SessionRow{
			{ID: "itervox-abc123", Owner: "itervox", Kind: "tmux", Status: "running", Cwd: "/w",
				TmuxPane: &server.TmuxPaneRow{ID: "%3", Label: "itervox-abc123:0.0"}, Controllable: true,
				AttachCommand: "tmux attach -t itervox-abc123"},
			{ID: "hand", Owner: "external", Status: "exited"},
		},
		tmux: server.TmuxStatusRow{Available: false, InstallHint: "brew install tmux", Control: true},
		opts: server.NewSessionOptions{ProjectRoot: "/w", Folders: []string{"/w"}, Max: 8},
	}
	w := sessionsReq(t, f, http.MethodGet, "/api/v1/sessions", "")
	require.Equal(t, http.StatusOK, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, map[string]any{"available": false, "installHint": "brew install tmux", "control": true}, body["tmux"])
	opts := body["newSession"].(map[string]any)
	assert.Equal(t, []any{"/w"}, opts["folders"])
	assert.Equal(t, []any{}, opts["profiles"])
	rows := body["sessions"].([]any)
	first, second := rows[0].(map[string]any), rows[1].(map[string]any)
	assert.Equal(t, map[string]any{"id": "%3", "label": "itervox-abc123:0.0"}, first["tmuxPane"])
	assert.Equal(t, true, first["controllable"])
	assert.Equal(t, "tmux attach -t itervox-abc123", first["attachCommand"])
	assert.Contains(t, second, "tmuxPane")
	assert.Nil(t, second["tmuxPane"])
	assert.Equal(t, false, second["controllable"])
}

// The tmux endpoints map the client's refusals to their documented codes.
func TestTmuxEndpointsMapErrors(t *testing.T) {
	for _, c := range []struct {
		err  error
		code int
		want string
	}{
		{&server.TmuxUnavailableError{Hint: "brew install tmux"}, http.StatusConflict, "tmux_unavailable"},
		{server.ErrNotControllable, http.StatusForbidden, "not_controllable"},
		{server.ErrPaneGone, http.StatusConflict, "pane_gone"},
		{server.ErrSessionNotFound, http.StatusNotFound, "not_found"},
	} {
		f := &fakeSessions{tmuxErr: c.err}
		w := sessionsReq(t, f, http.MethodGet, "/api/v1/sessions/s1/screen?lines=10", "")
		assert.Equal(t, c.code, w.Code, c.want)
		assert.Contains(t, w.Body.String(), `"`+c.want+`"`)
		w = sessionsReq(t, f, http.MethodPost, "/api/v1/sessions/s1/keys", `{"key":"Enter"}`)
		assert.Equal(t, c.code, w.Code, c.want)
	}
	f := &fakeSessions{tmuxErr: server.ErrKeyNotAllowed}
	w := sessionsReq(t, f, http.MethodPost, "/api/v1/sessions/s1/keys", `{"key":"C-d"}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "key_not_allowed")
	assert.Equal(t, []string{"s1", "C-d"}, f.keyArgs)

	w = sessionsReq(t, &fakeSessions{}, http.MethodGet, "/api/v1/sessions/s1/screen?lines=0", "")
	assert.Equal(t, http.StatusBadRequest, w.Code)

	// Messages take {text, submit}: submit defaults to true.
	f = &fakeSessions{}
	w = sessionsReq(t, f, http.MethodPost, "/api/v1/sessions/s1/message", `{"text":"hi","submit":false}`)
	require.Equal(t, http.StatusAccepted, w.Code)
	assert.Equal(t, "hi", f.sentMsg)
	assert.False(t, f.sentSubmit)
	w = sessionsReq(t, f, http.MethodPost, "/api/v1/sessions/s1/message", `{"message":"yo"}`)
	require.Equal(t, http.StatusAccepted, w.Code)
	assert.True(t, f.sentSubmit)
	f.sendErr = server.ErrPaneGone
	w = sessionsReq(t, f, http.MethodPost, "/api/v1/sessions/s1/message", `{"text":"hi"}`)
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Contains(t, w.Body.String(), "pane_gone")
}

// POST /api/v1/sessions validates the backend and maps the refusals;
// DELETE refuses sessions itervox did not start.
func TestNewAndKillSessionEndpoints(t *testing.T) {
	f := &fakeSessions{}
	w := sessionsReq(t, f, http.MethodPost, "/api/v1/sessions", `{"backend":"claude","cwd":"/w","prompt":"go"}`)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	assert.Equal(t, server.NewSessionRequest{Backend: "claude", Cwd: "/w", Prompt: "go"}, *f.startReq)
	assert.Contains(t, w.Body.String(), `"attachCommand":"tmux attach -t itervox-abc123"`)

	w = sessionsReq(t, &fakeSessions{}, http.MethodPost, "/api/v1/sessions", `{"backend":"vim"}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	for _, c := range []struct {
		err  error
		code int
		want string
	}{
		{&server.BadSessionRequestError{Field: "cwd", Message: "outside"}, http.StatusBadRequest, `"field":"cwd"`},
		{server.ErrUnknownProfile, http.StatusBadRequest, `"field":"profile"`},
		{server.ErrTooManySessions, http.StatusTooManyRequests, "too_many_sessions"},
		{&server.TmuxUnavailableError{Hint: "brew install tmux"}, http.StatusConflict, "brew install tmux"},
	} {
		w = sessionsReq(t, &fakeSessions{tmuxErr: c.err}, http.MethodPost, "/api/v1/sessions", `{"backend":"codex"}`)
		assert.Equal(t, c.code, w.Code, c.want)
		assert.Contains(t, w.Body.String(), c.want)
	}

	f = &fakeSessions{}
	w = sessionsReq(t, f, http.MethodDelete, "/api/v1/sessions/itervox-abc123", "")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "itervox-abc123", f.killed)
	w = sessionsReq(t, &fakeSessions{tmuxErr: server.ErrSessionNotOwned}, http.MethodDelete, "/api/v1/sessions/hand", "")
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "not_owned")
}
