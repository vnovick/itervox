package server_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/vnovick/itervox/internal/server"
)

// CORE-045 — GET /metrics is opt-in and behind the bearer token.

func metricsCfg(handler http.Handler, token string) server.Config {
	cfg := makeTestConfig(baseSnap())
	cfg.APIToken = token
	cfg.Metrics = handler
	return cfg
}

var fakeMetrics = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte("# HELP itervox_up x\n# TYPE itervox_up gauge\nitervox_up 1\n"))
})

func getMetrics(t *testing.T, cfg server.Config, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	w := httptest.NewRecorder()
	server.New(cfg).ServeHTTP(w, req)
	return w
}

func TestMetricsRequiresBearerToken(t *testing.T) {
	cfg := metricsCfg(fakeMetrics, "tok-123456789")
	assert.Equal(t, http.StatusUnauthorized, getMetrics(t, cfg, "").Code)
	assert.Equal(t, http.StatusUnauthorized, getMetrics(t, cfg, "wrong-token").Code)
	w := getMetrics(t, cfg, "tok-123456789")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "itervox_up 1")
}

func TestMetricsDisabledIs404(t *testing.T) {
	w := getMetrics(t, metricsCfg(nil, "tok-123456789"), "tok-123456789")
	assert.Equal(t, http.StatusNotFound, w.Code, "disabled: 404, not the SPA shell")
	assert.NotContains(t, w.Body.String(), "<html")
}

func TestMetricsUnauthenticatedModeIsHostGuarded(t *testing.T) {
	cfg := metricsCfg(fakeMetrics, "")
	assert.Equal(t, http.StatusOK, getMetrics(t, cfg, "").Code, "no token configured: open, like the rest of the API")
	cfg.AllowedHosts = nil
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Host = "evil.example"
	w := httptest.NewRecorder()
	server.New(cfg).ServeHTTP(w, req)
	assert.Equal(t, http.StatusForbidden, w.Code, "not exempt from the DNS-rebinding Host guard")
}
