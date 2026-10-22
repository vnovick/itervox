package hub

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/vnovick/itervox/internal/apitokens"
)

// Hub authentication. The hub has its own token store (StateDir/tokens.json,
// managed with `itervox token --hub`), separate from every project's: a hub
// token works on the hub only, and its scope caps what it may do on every
// member. There is no bootstrap token and no unauthenticated mode: every
// route except the health probe and the dashboard's static files needs a
// stored hub token (fail closed).

// TokenStoreFileName is the hub token store inside StateDir.
const TokenStoreFileName = apitokens.FileName

// RequiredScopeHeader names the scope a 403 insufficient_scope needed (the
// same header the daemons send, so the dashboard explains the refusal).
const RequiredScopeHeader = "X-Itervox-Required-Scope"

// Route access classes of the hub's own table.
const (
	hubPublic = "public"
	// hubMemberRoute: the proxy; the hub checks the member's own table.
	hubMemberRoute = "member"
)

// hubRouteScopes is the scope declaration of every hub route, keyed by
// "METHOD pattern". TestEveryRouteDeclaresAScope (this package) walks the
// real router and fails for a route missing here or a stale line; the
// middleware refuses an undeclared route at runtime.
var hubRouteScopes = map[string]string{
	"GET /":                    hubPublic, // redirect to /hub/
	"* /*":                     hubPublic, // dashboard static files and index.html
	"GET /hub/api/v1/health":   hubPublic,
	"* /hub/api/*":             hubPublic, // JSON 404 for unknown hub API paths
	"GET /hub/api/v1/whoami":   string(apitokens.ScopeViewer),
	"GET /hub/api/v1/projects": string(apitokens.ScopeViewer),
	"GET /hub/api/v1/feed":     string(apitokens.ScopeViewer),
	"GET /hub/api/v1/events":   string(apitokens.ScopeViewer),
	// #229 notification settings: reading and changing them is admin (the
	// routes say where a project's events go); a test is operator.
	"GET /hub/api/v1/notifications":       string(apitokens.ScopeAdmin),
	"PUT /hub/api/v1/notifications":       string(apitokens.ScopeAdmin),
	"POST /hub/api/v1/notifications/test": string(apitokens.ScopeOperator),
	"* /hub/p/{project}/api/*":            hubMemberRoute,
}

// Principal is an authenticated hub caller.
type Principal struct {
	Scope   apitokens.Scope `json:"scope"`
	TokenID string          `json:"tokenId"`
	Name    string          `json:"name"`
}

type principalKey struct{}

func principalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// authMiddleware resolves the bearer token against the hub token store.
func (h *Hub) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		auth := r.Header.Get("Authorization")
		secret := strings.TrimPrefix(auth, prefix)
		if !strings.HasPrefix(auth, prefix) || secret == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid hub token")
			return
		}
		t, ok := h.tokens.Authenticate(secret)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid hub token")
			return
		}
		p := Principal{Scope: t.Scope, TokenID: t.ID, Name: t.Name}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	})
}

// scopeMiddleware enforces hubRouteScopes after authMiddleware (fail
// closed: no principal is 401, an undeclared route is refused).
func scopeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := principalFrom(r.Context())
		if !ok || !p.Scope.Valid() {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid hub token")
			return
		}
		pattern := ""
		if rctx := chi.RouteContext(r.Context()); rctx != nil {
			pattern = rctx.RoutePattern()
		}
		access, declared := hubRouteScopes[r.Method+" "+pattern]
		if !declared {
			access, declared = hubRouteScopes["* "+pattern]
		}
		switch {
		case !declared || access == hubPublic:
			slog.Error("hub: route has no scope declaration; refusing it", "method", r.Method, "pattern", pattern)
			writeError(w, http.StatusForbidden, "route_scope_undeclared", "this route declares no scope and is refused")
			return
		case access == hubMemberRoute:
			// The proxy handler checks the member's own table.
		case !p.Scope.Allows(apitokens.Scope(access)):
			writeInsufficientScope(w, apitokens.Scope(access), p.Scope)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// writeInsufficientScope answers 403 insufficient_scope like the daemons.
func writeInsufficientScope(w http.ResponseWriter, required, have apitokens.Scope) {
	w.Header().Set(RequiredScopeHeader, string(required))
	writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]string{
		"code":           "insufficient_scope",
		"message":        "this action needs the " + string(required) + " scope; your hub token has " + string(have),
		"required_scope": string(required),
		"scope":          string(have),
	}})
}

// writeError writes the daemons' error envelope {"error":{"code","message"}}.
func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
