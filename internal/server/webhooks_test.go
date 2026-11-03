package server_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/vnovick/itervox/internal/server"
)

// recordingWebhooks stands in for the tracker webhook receiver (#131).
type recordingWebhooks struct{ hits int }

func (r *recordingWebhooks) Tracker() string { return "github" }

func (r *recordingWebhooks) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	r.hits++
	w.WriteHeader(http.StatusAccepted)
}

func postWebhook(s http.Handler, path, host string) int {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
	req.Host = host
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec.Code
}

// TestWebhookRouteNeedsNoBearerToken: GitHub and Linear cannot send the API
// token, so the route is outside the bearer group; the receiver checks the
// delivery's signature itself.
func TestWebhookRouteNeedsNoBearerToken(t *testing.T) {
	hooks := &recordingWebhooks{}
	cfg := makeTestConfig(baseSnap())
	cfg.APIToken = "tok"
	cfg.Webhooks = hooks
	s := server.New(cfg)
	assert.Equal(t, http.StatusAccepted, postWebhook(s, "/api/v1/webhooks/github", "tunnel.example.net"))
	assert.Equal(t, 1, hooks.hits)
	assert.Equal(t, http.StatusNotFound, postWebhook(s, "/api/v1/webhooks/linear", "localhost"), "another tracker")
	assert.Equal(t, 1, hooks.hits)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/state", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "the rest of the API still needs the token")
}

// In unauthenticated mode the Host guard lets a delivery through a tunnel's
// name, and only a POST to the webhook route.
func TestWebhookRoutePassesTheHostGuard(t *testing.T) {
	hooks := &recordingWebhooks{}
	s, _ := hostGuardServer(t, func(c *server.Config) { c.Webhooks = hooks })
	assert.Equal(t, http.StatusAccepted, postWebhook(s, "/api/v1/webhooks/github", "abc.trycloudflare.com"))
	assert.Equal(t, 1, hooks.hits)
	assert.Equal(t, http.StatusForbidden, postWebhook(s, "/api/v1/settings/profiles/x", "abc.trycloudflare.com"),
		"other routes keep the guard")
}

func TestWebhookRouteWithoutWebhooksIs404(t *testing.T) {
	cfg := makeTestConfig(baseSnap())
	s := server.New(cfg)
	assert.Equal(t, http.StatusNotFound, postWebhook(s, "/api/v1/webhooks/github", "localhost"))
}
