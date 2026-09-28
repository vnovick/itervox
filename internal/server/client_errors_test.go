package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/metrics"
	"github.com/vnovick/itervox/internal/server"
)

// CORE-048 — POST /api/v1/client-errors: authenticated, 8 KiB-capped (trailing
// bytes included), rate-limited, redacted, counted, and handed to the
// daemon's RecentFailures ring through a non-blocking reporter.

type clientErrorSink struct {
	mu      sync.Mutex
	reports []server.ClientErrorReport
	full    bool
}

func (s *clientErrorSink) report(r server.ClientErrorReport) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.full {
		return false
	}
	s.reports = append(s.reports, r)
	return true
}

func clientErrorsCfg(token string, sink *clientErrorSink) server.Config {
	cfg := makeTestConfig(baseSnap())
	cfg.APIToken = token
	cfg.ReportClientError = sink.report
	// A fresh limiter per test: the default one is process-wide.
	cfg.ClientErrorLimiter = server.NewClientErrorLimiter()
	return cfg
}

func postClientError(t *testing.T, srv *server.Server, body, bearer string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/client-errors", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if mutate != nil {
		mutate(req)
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

func TestClientErrors_Accepted(t *testing.T) {
	sink := &clientErrorSink{}
	srv := server.New(clientErrorsCfg("tok", sink))
	before := metrics.ClientErrorCount("render", metrics.ClientErrorOutcomeAccepted)
	secret := "ghp_" + strings.Repeat("Zz9", 12)
	w := postClientError(t, srv, `{"kind":"render","message":"boom `+secret+`","route":"/","stack":"at X"}`, "tok", nil)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, map[string]any{"accepted": true}, body)
	require.Len(t, sink.reports, 1)
	assert.Equal(t, "render", sink.reports[0].Kind)
	assert.Equal(t, "/", sink.reports[0].Route)
	assert.NotContains(t, sink.reports[0].Message, secret, "redacted before it leaves the handler")
	assert.Equal(t, before+1, metrics.ClientErrorCount("render", metrics.ClientErrorOutcomeAccepted))
}

func TestClientErrors_TrailingBytesStill413(t *testing.T) {
	sink := &clientErrorSink{}
	srv := server.New(clientErrorsCfg("tok", sink))
	body := `{"kind":"render","message":"x","route":"/"}` + strings.Repeat(" ", 9<<10)
	w := postClientError(t, srv, body, "tok", nil)
	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code, w.Body.String())
	assert.Empty(t, sink.reports)

	big := `{"kind":"render","message":"` + strings.Repeat("a", 9<<10) + `","route":"/"}`
	assert.Equal(t, http.StatusRequestEntityTooLarge, postClientError(t, srv, big, "tok", nil).Code)
}

func TestClientErrors_RejectsMalformed(t *testing.T) {
	srv := server.New(clientErrorsCfg("tok", &clientErrorSink{}))
	assert.Equal(t, http.StatusBadRequest, postClientError(t, srv, `{"kind":"render"`, "tok", nil).Code)
	assert.Equal(t, http.StatusBadRequest, postClientError(t, srv, `{"kind":"render","message":""}`, "tok", nil).Code)
	assert.Equal(t, http.StatusBadRequest, postClientError(t, srv, `{"kind":"render","message":"x"} {"again":1}`, "tok", nil).Code,
		"exactly one JSON value")
}

func TestClientErrors_UnknownKindIsBucketedAsOther(t *testing.T) {
	sink := &clientErrorSink{}
	srv := server.New(clientErrorsCfg("tok", sink))
	require.Equal(t, http.StatusAccepted, postClientError(t, srv, `{"kind":"weird-new-kind","message":"x"}`, "tok", nil).Code)
	assert.Equal(t, "other", sink.reports[0].Kind, "the metric label set stays bounded")
}

func TestClientErrors_RequiresBearerToken(t *testing.T) {
	srv := server.New(clientErrorsCfg("tok", &clientErrorSink{}))
	assert.Equal(t, http.StatusUnauthorized, postClientError(t, srv, `{"kind":"render","message":"x"}`, "", nil).Code)
}

func TestClientErrors_UnauthenticatedModeIsCSRFAndHostGuarded(t *testing.T) {
	sink := &clientErrorSink{}
	srv := server.New(clientErrorsCfg("", sink))
	assert.Equal(t, http.StatusAccepted, postClientError(t, srv, `{"kind":"render","message":"x"}`, "", nil).Code,
		"same-origin / non-browser clients may report")
	cross := postClientError(t, srv, `{"kind":"render","message":"x"}`, "", func(r *http.Request) {
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		r.Header.Set("Origin", "https://evil.example")
	})
	assert.Equal(t, http.StatusForbidden, cross.Code, "CSRF guard")
	rebound := postClientError(t, srv, `{"kind":"render","message":"x"}`, "", func(r *http.Request) {
		r.Host = "evil.example"
	})
	assert.Equal(t, http.StatusForbidden, rebound.Code, "Host guard")
	assert.Len(t, sink.reports, 1)
}

func TestClientErrors_RateLimited(t *testing.T) {
	sink := &clientErrorSink{}
	srv := server.New(clientErrorsCfg("tok", sink))
	codes := map[int]int{}
	for range server.ClientErrorBurst + 5 {
		codes[postClientError(t, srv, `{"kind":"error","message":"x"}`, "tok", nil).Code]++
	}
	assert.Equal(t, server.ClientErrorBurst, codes[http.StatusAccepted])
	assert.Equal(t, 5, codes[http.StatusTooManyRequests])
	assert.Len(t, sink.reports, server.ClientErrorBurst)
}

func TestClientErrors_503WhenEventQueueFull(t *testing.T) {
	sink := &clientErrorSink{full: true}
	srv := server.New(clientErrorsCfg("tok", sink))
	w := postClientError(t, srv, `{"kind":"schema","message":"x"}`, "tok", nil)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Equal(t, "1", w.Header().Get("Retry-After"))
}
