package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/apitokens"
)

const scopesTestBootstrap = "bootstrap-admin-token-0123456789abcdef"

// scopedServer is a token-mode server with a stored token of every scope.
type scopedServer struct {
	srv     *Server
	store   *apitokens.Store
	secrets map[apitokens.Scope]string
}

func newScopedServer(t *testing.T) scopedServer {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".itervox", apitokens.FileName)
	store := apitokens.Open(path)
	secrets := make(map[apitokens.Scope]string)
	for _, sc := range apitokens.Scopes {
		_, secret, err := store.Create(string(sc)+"-token", sc, nil, "test")
		require.NoError(t, err)
		secrets[sc] = secret
	}
	srv := New(Config{
		Snapshot: func() StateSnapshot {
			return StateSnapshot{ProfileDefs: map[string]ProfileDef{
				"coder": {Command: "ANTHROPIC_API_KEY=sk-ant-abcdefghijklmnopqrstuvwxyz0123456789 claude --verbose"},
			}}
		},
		RefreshChan: make(chan struct{}, 1),
		APIToken:    scopesTestBootstrap,
		Tokens:      store,
		Metrics:     http.NotFoundHandler(),
	})
	return scopedServer{srv: srv, store: store, secrets: secrets}
}

// paramRe matches a chi URL parameter.
var paramRe = regexp.MustCompile(`\{[^}]+\}`)

// concretePath turns a route pattern into a request path.
func concretePath(pattern string) string {
	return paramRe.ReplaceAllString(pattern, "x")
}

func doReq(t *testing.T, h http.Handler, method, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader("{}"))
	req.Host = "127.0.0.1:8090" // the unauthenticated-mode Host guard accepts IP literals
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// scopedRoutes returns the routeScopes entries that require a token scope,
// split into method and pattern, sorted.
func scopedRoutes(t *testing.T) [][2]string {
	t.Helper()
	var out [][2]string
	for key, access := range routeScopes {
		if !apitokens.Scope(access).Valid() {
			continue
		}
		method, pattern, ok := strings.Cut(key, " ")
		require.True(t, ok, key)
		out = append(out, [2]string{method, pattern})
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0]+out[i][1] < out[j][0]+out[j][1] })
	return out
}

// streamingRoute reports whether a GET route streams (and would block).
func streamingRoute(pattern string) bool {
	return strings.HasSuffix(pattern, "/events") || strings.HasSuffix(pattern, "-stream")
}

// TestEveryRouteDeclaresAScope (#214) walks the real router: every
// registered method+pattern has exactly one routeScopes line, and every
// line names a registered route. A route added without a declaration fails
// here, and at runtime scopeMiddleware refuses it.
func TestEveryRouteDeclaresAScope(t *testing.T) {
	ss := newScopedServer(t)
	registered := make(map[string]bool)
	err := chi.Walk(ss.srv.router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if route == "/*" {
			method = anyMethod // the SPA is mounted with Handle: every method
		}
		registered[method+" "+route] = true
		return nil
	})
	require.NoError(t, err)
	require.Greater(t, len(registered), 100, "the walk must see the whole router")

	var undeclared, stale []string
	for key := range registered {
		if _, ok := routeScopes[key]; !ok {
			undeclared = append(undeclared, key)
		}
	}
	for key := range routeScopes {
		if !registered[key] {
			stale = append(stale, key)
		}
	}
	sort.Strings(undeclared)
	sort.Strings(stale)
	assert.Empty(t, undeclared, "routes registered without a scope declaration in routeScopes")
	assert.Empty(t, stale, "routeScopes lines with no registered route")

	// Every scoped route sits behind the bearer check: no token is 401.
	for _, mp := range scopedRoutes(t) {
		rec := doReq(t, ss.srv, mp[0], concretePath(mp[1]), "")
		assert.Equal(t, http.StatusUnauthorized, rec.Code, "%s %s without a token", mp[0], mp[1])
	}
	// Viewer is GET only.
	for key, access := range routeScopes {
		if access == accessViewer {
			assert.True(t, strings.HasPrefix(key, "GET "), "viewer route %q is not a GET", key)
		}
	}
}

// TestUndeclaredRouteIsRefused: a route missing from routeScopes is refused
// even for admin (fail closed), and a wrong method still answers 405.
func TestUndeclaredRouteIsRefused(t *testing.T) {
	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(unauthenticatedAdminMiddleware)
			r.Use(scopeMiddleware)
			r.Get("/undeclared-route", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
			r.Get("/state", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
			r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusMethodNotAllowed) })
		})
	})
	rec := doReq(t, r, http.MethodGet, "/api/v1/undeclared-route", "")
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "route_scope_undeclared")
	assert.Equal(t, http.StatusOK, doReq(t, r, http.MethodGet, "/api/v1/state", "").Code)
	assert.Equal(t, http.StatusMethodNotAllowed, doReq(t, r, http.MethodPost, "/api/v1/state", "").Code)

	ss := newScopedServer(t)
	assert.Equal(t, http.StatusMethodNotAllowed, doReq(t, ss.srv, http.MethodPatch, "/api/v1/state", scopesTestBootstrap).Code)
}

// TestViewerCannotMutate (#214): a viewer token is refused every non-GET
// route with 403 insufficient_scope naming the scope needed, reads state
// with callerScope viewer and redacted secrets, and may open the SSE stream.
func TestViewerCannotMutate(t *testing.T) {
	ss := newScopedServer(t)
	viewer := ss.secrets[apitokens.ScopeViewer]
	mutations := 0
	for _, mp := range scopedRoutes(t) {
		method, pattern := mp[0], mp[1]
		required := apitokens.Scope(routeScopes[method+" "+pattern])
		if streamingRoute(pattern) {
			continue // streams block; /events is checked below
		}
		rec := doReq(t, ss.srv, method, concretePath(pattern), viewer)
		if method != http.MethodGet {
			// Independent of the table: a viewer never reaches a mutation.
			require.Equal(t, http.StatusForbidden, rec.Code, "viewer reached %s %s", method, pattern)
		}
		if required == apitokens.ScopeViewer {
			assert.NotEqual(t, http.StatusForbidden, rec.Code, "viewer refused a read: %s %s", method, pattern)
			assert.NotEqual(t, http.StatusUnauthorized, rec.Code, "viewer refused a read: %s %s", method, pattern)
			continue
		}
		if method != http.MethodGet {
			mutations++
		}
		require.Equal(t, http.StatusForbidden, rec.Code, "%s %s as viewer", method, pattern)
		var body struct {
			Error struct {
				Code          string `json:"code"`
				RequiredScope string `json:"required_scope"`
			} `json:"error"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		assert.Equal(t, "insufficient_scope", body.Error.Code)
		assert.Equal(t, string(required), body.Error.RequiredScope)
		assert.Equal(t, string(required), rec.Header().Get(RequiredScopeHeader))
	}
	assert.Greater(t, mutations, 60, "every mutating route was tried")

	rec := doReq(t, ss.srv, http.MethodGet, "/api/v1/state", viewer)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"callerScope":"viewer"`)
	assert.NotContains(t, rec.Body.String(), "sk-ant-abcdefghijklmnopqrstuvwxyz", "a viewer must not see secrets")
	assert.Contains(t, rec.Body.String(), "ANTHROPIC_API_KEY=")
	assert.Contains(t, rec.Body.String(), "claude --verbose", "the command stays readable")

	// The SSE stream is open to viewers, with the redacted frame.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+viewer)
	sse := httptest.NewRecorder()
	ss.srv.ServeHTTP(sse, req)
	assert.Equal(t, http.StatusOK, sse.Code)
	assert.Equal(t, "text/event-stream", sse.Header().Get("Content-Type"))
	assert.Contains(t, sse.Body.String(), `"callerScope":"viewer"`)
	assert.NotContains(t, sse.Body.String(), "sk-ant-abcdefghijklmnopqrstuvwxyz")
}

// TestOperatorCannotChangeSettingsOrTokens (#214): an operator token is
// refused every admin route — settings, tokens, tmux control, workspace
// deletion, automations — and reaches every operator route.
func TestOperatorCannotChangeSettingsOrTokens(t *testing.T) {
	ss := newScopedServer(t)
	operator := ss.secrets[apitokens.ScopeOperator]
	admins := 0
	for _, mp := range scopedRoutes(t) {
		method, pattern := mp[0], mp[1]
		required := apitokens.Scope(routeScopes[method+" "+pattern])
		if streamingRoute(pattern) {
			continue
		}
		rec := doReq(t, ss.srv, method, concretePath(pattern), operator)
		if required == apitokens.ScopeAdmin {
			admins++
			assert.Equal(t, http.StatusForbidden, rec.Code, "%s %s as operator", method, pattern)
			assert.Equal(t, "admin", rec.Header().Get(RequiredScopeHeader), "%s %s", method, pattern)
			continue
		}
		assert.NotEqual(t, http.StatusForbidden, rec.Code, "operator refused %s %s", method, pattern)
		assert.NotEqual(t, http.StatusUnauthorized, rec.Code, "operator refused %s %s", method, pattern)
	}
	assert.Greater(t, admins, 25)
	for _, route := range []string{"POST /api/v1/tokens", "PUT /api/v1/settings/layers/{layer}", "PUT /api/v1/settings/automations", "POST /api/v1/sessions/{id}/keys"} {
		assert.Equal(t, accessAdmin, routeScopes[route], route)
	}

	// The operator's view of profiles is redacted too.
	rec := doReq(t, ss.srv, http.MethodGet, "/api/v1/state", operator)
	assert.Contains(t, rec.Body.String(), `"callerScope":"operator"`)
	assert.NotContains(t, rec.Body.String(), "sk-ant-abcdefghijklmnopqrstuvwxyz")

	// Tokens listed via the API never show a hash or a secret.
	admin := ss.secrets[apitokens.ScopeAdmin]
	list := doReq(t, ss.srv, http.MethodGet, "/api/v1/tokens", admin)
	require.Equal(t, http.StatusOK, list.Code)
	assert.NotContains(t, list.Body.String(), "hash")
	assert.NotContains(t, list.Body.String(), operator)
}

// TestEnvTokenStillAdmin (#214): the bootstrap token (ITERVOX_API_TOKEN, the
// auto-generated or supervisor token) is admin whether or not stored tokens
// exist, and server.allow_unauthenticated mode stays all-admin.
func TestEnvTokenStillAdmin(t *testing.T) {
	ss := newScopedServer(t)
	rec := doReq(t, ss.srv, http.MethodGet, "/api/v1/whoami", scopesTestBootstrap)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"scope":"admin","source":"bootstrap"}`, rec.Body.String())
	for _, mp := range scopedRoutes(t) {
		if streamingRoute(mp[1]) {
			continue
		}
		rec := doReq(t, ss.srv, mp[0], concretePath(mp[1]), scopesTestBootstrap)
		assert.NotEqual(t, http.StatusForbidden, rec.Code, "%s %s with the bootstrap token", mp[0], mp[1])
		assert.NotEqual(t, http.StatusUnauthorized, rec.Code, "%s %s with the bootstrap token", mp[0], mp[1])
	}
	state := doReq(t, ss.srv, http.MethodGet, "/api/v1/state", scopesTestBootstrap)
	assert.Contains(t, state.Body.String(), `"callerScope":"admin"`)
	assert.Contains(t, state.Body.String(), "sk-ant-abcdefghijklmnopqrstuvwxyz", "admins edit commands as written")

	// A wrong token is still 401, and a stored token is not mistaken for
	// the bootstrap one.
	assert.Equal(t, http.StatusUnauthorized, doReq(t, ss.srv, http.MethodGet, "/api/v1/state", "nope").Code)
	who := doReq(t, ss.srv, http.MethodGet, "/api/v1/whoami", ss.secrets[apitokens.ScopeAdmin])
	assert.Contains(t, who.Body.String(), `"source":"token"`)

	// server.allow_unauthenticated: no token, every caller admin; stored
	// tokens are not consulted and token management is unavailable.
	open := New(Config{Snapshot: func() StateSnapshot { return StateSnapshot{} }, RefreshChan: make(chan struct{}, 1), Tokens: ss.store})
	rec = doReq(t, open, http.MethodGet, "/api/v1/whoami", "")
	assert.JSONEq(t, `{"scope":"admin","source":"unauthenticated"}`, rec.Body.String())
	assert.Equal(t, http.StatusServiceUnavailable, doReq(t, open, http.MethodGet, "/api/v1/tokens", "").Code)
}

// TestRotateWithoutRestartRevokesOldTokenWithinOneSecond (#214): a rotation
// written by another process (`itervox token rotate`, simulated by a second
// Store on the same file) stops the old secret within 1 s on the running
// server, which never restarts; the new secret works. A rotation through the
// dashboard API takes effect at once.
func TestRotateWithoutRestartRevokesOldTokenWithinOneSecond(t *testing.T) {
	ss := newScopedServer(t)
	cli := apitokens.Open(ss.store.FilePath()) // another process's view of the file

	created, oldSecret, err := cli.Create("ci", apitokens.ScopeOperator, nil, "cli")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return doReq(t, ss.srv, http.MethodGet, "/api/v1/state", oldSecret).Code == http.StatusOK
	}, time.Second, 10*time.Millisecond, "a token created by the CLI is picked up without a restart")

	_, newSecret, err := cli.Rotate(created.ID, "cli")
	require.NoError(t, err)
	start := time.Now()
	for doReq(t, ss.srv, http.MethodGet, "/api/v1/state", oldSecret).Code != http.StatusUnauthorized {
		require.Less(t, time.Since(start), time.Second, "the old token still works a second after rotation")
		time.Sleep(10 * time.Millisecond)
	}
	assert.Equal(t, http.StatusOK, doReq(t, ss.srv, http.MethodGet, "/api/v1/state", newSecret).Code)

	// Through the dashboard: immediate.
	admin := ss.secrets[apitokens.ScopeAdmin]
	rec := doReq(t, ss.srv, http.MethodPost, "/api/v1/tokens/"+created.ID+"/rotate", admin)
	require.Equal(t, http.StatusOK, rec.Code)
	var rotated TokenSecretResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rotated))
	assert.Equal(t, http.StatusUnauthorized, doReq(t, ss.srv, http.MethodGet, "/api/v1/state", newSecret).Code)
	assert.Equal(t, http.StatusOK, doReq(t, ss.srv, http.MethodGet, "/api/v1/state", rotated.Secret).Code)

	// Revoke through the CLI: gone within 1 s too.
	_, err = cli.Revoke(created.ID, "cli")
	require.NoError(t, err)
	assert.Eventually(t, func() bool {
		return doReq(t, ss.srv, http.MethodGet, "/api/v1/state", rotated.Secret).Code == http.StatusUnauthorized
	}, time.Second, 10*time.Millisecond)
}

// TestTokenAPICreatesHashedTokenOnce: POST /api/v1/tokens answers the
// secret once; the file holds only its hash.
func TestTokenAPICreatesHashedTokenOnce(t *testing.T) {
	ss := newScopedServer(t)
	admin := ss.secrets[apitokens.ScopeAdmin]
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tokens", strings.NewReader(`{"name":"teammate","scope":"viewer","expires":"30d"}`))
	req.Header.Set("Authorization", "Bearer "+admin)
	rec := httptest.NewRecorder()
	ss.srv.ServeHTTP(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	var created TokenSecretResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	assert.Equal(t, "viewer", created.Token.Scope)
	require.NotNil(t, created.Token.Expires)
	raw, err := os.ReadFile(ss.store.FilePath())
	require.NoError(t, err)
	assert.NotContains(t, string(raw), created.Secret)
	assert.Contains(t, string(raw), apitokens.HashSecret(created.Secret))

	bad := httptest.NewRequest(http.MethodPost, "/api/v1/tokens", strings.NewReader(`{"name":"x","scope":"root"}`))
	bad.Header.Set("Authorization", "Bearer "+admin)
	badRec := httptest.NewRecorder()
	ss.srv.ServeHTTP(badRec, bad)
	assert.Equal(t, http.StatusBadRequest, badRec.Code)

	del := doReq(t, ss.srv, http.MethodDelete, "/api/v1/tokens/"+created.Token.ID, admin)
	assert.Equal(t, http.StatusOK, del.Code)
	assert.Equal(t, http.StatusUnauthorized, doReq(t, ss.srv, http.MethodGet, "/api/v1/state", created.Secret).Code)
	assert.Equal(t, http.StatusNotFound, doReq(t, ss.srv, http.MethodDelete, "/api/v1/tokens/"+created.Token.ID, admin).Code)
}

// TestSettingsLayersRedactedBelowAdmin: viewers and operators get no raw
// settings file text and no secret-shaped values; admins get both.
func TestSettingsLayersRedactedBelowAdmin(t *testing.T) {
	view := SettingsLayersView{
		Layers: []SettingsLayerView{{Name: "project", Raw: `{"workspace.clone_url":"https://bot:ghp_abcdefghijklmnopqrstuvwxyz0123456789AB@github.com/a/b"}`,
			Values: map[string]any{"workspace.clone_url": "https://bot:ghp_abcdefghijklmnopqrstuvwxyz0123456789AB@github.com/a/b", "agent.max_turns": float64(3)}}},
		Effective: []SettingsEffective{{Key: "agent.command", Value: "OPENAI_API_KEY=abc123secret codex"}},
	}
	got := settingsLayersForScope(view, apitokens.ScopeViewer)
	encoded, err := json.Marshal(got)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "ghp_abcdefghijklmnopqrstuvwxyz")
	assert.NotContains(t, string(encoded), "abc123secret")
	assert.Equal(t, "", got.Layers[0].Raw)
	assert.True(t, got.Layers[0].Redacted)
	assert.Equal(t, float64(3), got.Layers[0].Values["agent.max_turns"])
	assert.Equal(t, view, settingsLayersForScope(view, apitokens.ScopeAdmin))
	assert.Contains(t, view.Layers[0].Raw, "ghp_", "the shared view is not modified")
}

// TestRouteScopesRouteServesTheTable (#228): GET /api/v1/route-scopes is a
// viewer route that serves this daemon's whole route scope table, which the
// hub's proxy checks a hub token against.
func TestRouteScopesRouteServesTheTable(t *testing.T) {
	ss := newScopedServer(t)
	assert.Equal(t, http.StatusUnauthorized, doReq(t, ss.srv, http.MethodGet, "/api/v1/route-scopes", "").Code)
	rec := doReq(t, ss.srv, http.MethodGet, "/api/v1/route-scopes", ss.secrets[apitokens.ScopeViewer])
	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Routes map[string]string `json:"routes"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Len(t, body.Routes, len(routeScopes))
	assert.Equal(t, "viewer", body.Routes["GET /api/v1/route-scopes"])
	assert.Equal(t, "operator", body.Routes["POST /api/v1/dispatch/pause"])
	assert.Equal(t, "admin", body.Routes["GET /api/v1/tokens"])
	assert.Equal(t, "agent_grant", body.Routes["POST /api/v1/agent-actions/{identifier}/comment"])
}
