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

// M3-close V1 — POST /api/v1/backend-health/clear: the operator's
// clear-breaker action. Event-loop mediated (202 = queued), validated, and
// behind the same auth/CSRF guards as every state-changing route.

func clearBreakerServer(t *testing.T, fn func(string, string) bool) *server.Server {
	t.Helper()
	cfg := makeTestConfig(baseSnap())
	cfg.Client = &server.FuncClient{ClearBackendBreakerFn: fn}
	return server.New(cfg)
}

func TestHandleClearBackendBreaker(t *testing.T) {
	var gotBackend, gotHost string
	calls := 0
	srv := clearBreakerServer(t, func(b, h string) bool { calls++; gotBackend, gotHost = b, h; return true })

	w := postJSON(t, srv, "/api/v1/backend-health/clear", `{"backend":"claude","host":"build-1"}`)
	require.Equal(t, http.StatusAccepted, w.Code, "body: %s", w.Body.String())
	assert.Equal(t, "claude", gotBackend)
	assert.Equal(t, "build-1", gotHost)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["queued"])

	for _, body := range []string{
		`{"backend":"gemini"}`, `{"backend":" claude"}`, `{"backend":""}`,
		`{"backend":"claude","host":"-oProxyCommand=x"}`, `{"backend":"claude","host":"a b"}`, `{bad`,
	} {
		w = postJSON(t, srv, "/api/v1/backend-health/clear", body)
		assert.Equal(t, http.StatusBadRequest, w.Code, body)
	}
	assert.Equal(t, 1, calls, "an invalid request never reaches the orchestrator")

	full := clearBreakerServer(t, func(string, string) bool { return false })
	w = postJSON(t, full, "/api/v1/backend-health/clear", `{"backend":"codex"}`)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code, "a full event queue is retryable, not success")
}

func TestHandleClearBackendBreaker_Guards(t *testing.T) {
	calls := 0
	cfg := makeTestConfig(baseSnap())
	cfg.APIToken = "my-secret"
	cfg.Client = &server.FuncClient{ClearBackendBreakerFn: func(string, string) bool { calls++; return true }}
	srv := server.New(cfg)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/backend-health/clear", strings.NewReader(`{"backend":"claude"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code, "bearer token required")

	unauth := makeTestConfig(baseSnap())
	unauth.Client = &server.FuncClient{ClearBackendBreakerFn: func(string, string) bool { calls++; return true }}
	us := server.New(unauth)
	w = httptest.NewRecorder()
	us.ServeHTTP(w, csrfRequest(http.MethodPost, "/api/v1/backend-health/clear", "localhost:8090", "https://evil.example", "cross-site", `{"backend":"claude"}`))
	assertForbiddenShape(t, w)
	assert.Zero(t, calls)
}
