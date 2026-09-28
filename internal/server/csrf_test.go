package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/server"
)

// csrfRequest builds a request against host with optional Origin and
// Sec-Fetch-Site headers. An empty header value means "header absent".
func csrfRequest(method, path, host, origin, fetchSite, body string) *http.Request {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	req.Host = host
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if fetchSite != "" {
		req.Header.Set("Sec-Fetch-Site", fetchSite)
	}
	return req
}

// unauthServer is a server in server.allow_unauthenticated mode (no API
// token), with a counting refresh channel and comment client.
func unauthServer(t *testing.T, token string) (*server.Server, chan struct{}, *int) {
	t.Helper()
	comments := 0
	refresh := make(chan struct{}, 8)
	cfg := makeTestConfig(baseSnap())
	cfg.RefreshChan = refresh
	cfg.APIToken = token
	cfg.Client = &server.FuncClient{
		PostOperatorCommentFn: func(context.Context, string, string) (bool, error) {
			comments++
			return false, nil
		},
	}
	return server.New(cfg), refresh, &comments
}

func assertForbiddenShape(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	require.Equal(t, http.StatusForbidden, w.Code, "body: %s", w.Body.String())
	var resp struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), "403 must use the standard error envelope")
	assert.Equal(t, "cross_origin_forbidden", resp.Error.Code)
	assert.NotEmpty(t, resp.Error.Message)
}

// TestUnauthenticatedMode_RejectsCrossSitePost is CORE-041's acceptance. With
// no API token there is no bearer check, so without this guard any web page
// the operator visits could POST to the loopback daemon: refresh, terminate,
// post a tracker comment as the operator, rewrite WORKFLOW.md.
func TestUnauthenticatedMode_RejectsCrossSitePost(t *testing.T) {
	rows := []struct {
		name      string
		method    string
		host      string
		origin    string
		fetchSite string
	}{
		{"(a) foreign Origin, no Sec-Fetch-Site", http.MethodPost, "localhost:8090", "https://evil.example", ""},
		{"(b) Sec-Fetch-Site cross-site, no Origin", http.MethodPost, "localhost:8090", "", "cross-site"},
		{"(c) opaque Origin null", http.MethodPost, "localhost:8090", "null", ""},
		{"(d) same-site but cross-origin Origin", http.MethodPost, "example.com", "https://api.example.com", ""},
		{"Sec-Fetch-Site same-site", http.MethodPost, "example.com", "", "same-site"},
		{"both cross-site headers", http.MethodPost, "localhost:8090", "https://evil.example", "cross-site"},
		{"foreign port on the same host", http.MethodPost, "localhost:8090", "http://localhost:9999", ""},
		{"PUT", http.MethodPut, "localhost:8090", "https://evil.example", "cross-site"},
		{"PATCH", http.MethodPatch, "localhost:8090", "https://evil.example", "cross-site"},
		{"DELETE", http.MethodDelete, "localhost:8090", "https://evil.example", "cross-site"},
	}
	for _, row := range rows {
		t.Run(row.name+" /refresh", func(t *testing.T) {
			srv, refresh, _ := unauthServer(t, "")
			// /refresh is POST-only; other methods still hit the guard before
			// routing's 405, which is what matters: the guard runs first.
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, csrfRequest(row.method, "/api/v1/refresh", row.host, row.origin, row.fetchSite, ""))
			assertForbiddenShape(t, w)
			assert.Empty(t, refresh, "a rejected request must not queue a refresh")
		})
		if row.method != http.MethodPost {
			continue
		}
		t.Run(row.name+" /comment", func(t *testing.T) {
			srv, _, comments := unauthServer(t, "")
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, csrfRequest(row.method, "/api/v1/issues/X/comment", row.host, row.origin, row.fetchSite,
				`{"body":"posted by a web page the operator happened to visit"}`))
			assertForbiddenShape(t, w)
			assert.Zero(t, *comments, "PostOperatorComment must never run for a cross-site request")
		})
	}
}

// TestUnauthenticatedMode_AllowsRequestsWithoutOrigin pins that non-browser
// callers (curl, scripts, the TUI) keep working. Every current browser sends
// Origin — and Sec-Fetch-Site — on a cross-origin POST/PUT/PATCH/DELETE, so a
// request with neither header is not a browser cross-site request, whatever
// its Content-Type.
func TestUnauthenticatedMode_AllowsRequestsWithoutOrigin(t *testing.T) {
	t.Run("bodyless POST /refresh", func(t *testing.T) {
		srv, refresh, _ := unauthServer(t, "")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, csrfRequest(http.MethodPost, "/api/v1/refresh", "localhost:8090", "", "", ""))
		require.Equal(t, http.StatusAccepted, w.Code, "body: %s", w.Body.String())
		assert.Len(t, refresh, 1)
	})
	t.Run("JSON POST /comment", func(t *testing.T) {
		srv, _, comments := unauthServer(t, "")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, csrfRequest(http.MethodPost, "/api/v1/issues/X/comment", "localhost:8090", "", "",
			`{"body":"from curl"}`))
		require.Less(t, w.Code, 300, "body: %s", w.Body.String())
		assert.Equal(t, 1, *comments)
	})
}

// TestUnauthenticatedMode_AllowsSameHostDifferentScheme pins that only
// host[:port] is compared. Behind the TLS-terminating proxy in deploy/ the
// daemon sees plain HTTP (r.TLS == nil) while the browser sends an https
// Origin; comparing schemes would 403 every legitimate dashboard POST.
func TestUnauthenticatedMode_AllowsSameHostDifferentScheme(t *testing.T) {
	srv, refresh, _ := unauthServer(t, "")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, csrfRequest(http.MethodPost, "/api/v1/refresh", "localhost:8090", "https://localhost:8090", "", ""))
	require.Equal(t, http.StatusAccepted, w.Code, "body: %s", w.Body.String())
	assert.Len(t, refresh, 1)
}

// TestUnauthenticatedMode_AllowsSameOriginDashboard pins the embedded
// dashboard's own requests: authedFetch issues relative /api/v1/... URLs, so
// the browser sends Origin == Host and Sec-Fetch-Site: same-origin.
func TestUnauthenticatedMode_AllowsSameOriginDashboard(t *testing.T) {
	for _, fetchSite := range []string{"same-origin", ""} {
		t.Run("Sec-Fetch-Site="+fetchSite, func(t *testing.T) {
			srv, _, comments := unauthServer(t, "")
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, csrfRequest(http.MethodPost, "/api/v1/issues/X/comment", "localhost:8090",
				"http://localhost:8090", fetchSite, `{"body":"from the dashboard"}`))
			require.Less(t, w.Code, 300, "body: %s", w.Body.String())
			assert.Equal(t, 1, *comments)
		})
	}
	t.Run("Sec-Fetch-Site=none (user-initiated)", func(t *testing.T) {
		srv, refresh, _ := unauthServer(t, "")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, csrfRequest(http.MethodPost, "/api/v1/refresh", "localhost:8090", "", "none", ""))
		require.Equal(t, http.StatusAccepted, w.Code)
		assert.Len(t, refresh, 1)
	})
}

// TestUnauthenticatedMode_ViteDevProxy pins the documented dev-server
// behaviour. The page is served by Vite on :5173 and its proxy forwards /api
// to the daemon with changeOrigin: true, so the daemon sees Host
// localhost:8090 while the browser's Origin is http://localhost:5173. The
// browser's Sec-Fetch-Site is same-origin (page and request are both :5173),
// which is authoritative, so the request passes. Only a browser that sends no
// Sec-Fetch-Site (pre-2023) falls back to the Origin/Host comparison and is
// refused — use token mode for development on such a browser.
func TestUnauthenticatedMode_ViteDevProxy(t *testing.T) {
	t.Run("current browser", func(t *testing.T) {
		srv, refresh, _ := unauthServer(t, "")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, csrfRequest(http.MethodPost, "/api/v1/refresh", "localhost:8090",
			"http://localhost:5173", "same-origin", ""))
		require.Equal(t, http.StatusAccepted, w.Code, "body: %s", w.Body.String())
		assert.Len(t, refresh, 1)
	})
	t.Run("browser without Sec-Fetch-Site", func(t *testing.T) {
		srv, refresh, _ := unauthServer(t, "")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, csrfRequest(http.MethodPost, "/api/v1/refresh", "localhost:8090",
			"http://localhost:5173", "", ""))
		assertForbiddenShape(t, w)
		assert.Empty(t, refresh)
	})
}

// TestUnauthenticatedMode_SafeMethodsUnaffected pins that reads — including
// the snapshot the SSE stream is built from — are never gated: they change no
// state, and a cross-origin page cannot read the response without CORS.
func TestUnauthenticatedMode_SafeMethodsUnaffected(t *testing.T) {
	srv, _, _ := unauthServer(t, "")
	for _, path := range []string{"/api/v1/state", "/api/v1/health", "/api/v1/logs/identifiers"} {
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, csrfRequest(http.MethodGet, path, "localhost:8090", "https://evil.example", "cross-site", ""))
		assert.Equal(t, http.StatusOK, w.Code, path)
	}
}

// TestTokenMode_CrossSiteGuardNotApplied pins that the bearer path is
// unchanged: a browser never attaches the Authorization header cross-site on
// its own, so a request carrying the valid token is not a forgery, and a
// request without it is still a 401 (not a 403).
func TestTokenMode_CrossSiteGuardNotApplied(t *testing.T) {
	const token = "tok-123"
	t.Run("valid token", func(t *testing.T) {
		srv, refresh, _ := unauthServer(t, token)
		req := csrfRequest(http.MethodPost, "/api/v1/refresh", "localhost:8090", "https://evil.example", "cross-site", "")
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		require.Equal(t, http.StatusAccepted, w.Code, "body: %s", w.Body.String())
		assert.Len(t, refresh, 1)
	})
	t.Run("missing token", func(t *testing.T) {
		srv, refresh, _ := unauthServer(t, token)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, csrfRequest(http.MethodPost, "/api/v1/refresh", "localhost:8090", "https://evil.example", "cross-site", ""))
		assert.Equal(t, http.StatusUnauthorized, w.Code)
		assert.Empty(t, refresh)
	})
}
