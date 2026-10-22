package server

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/vnovick/itervox/internal/apitokens"
)

// Authentication and scopes (#214).
//
// Every request to the authenticated groups is turned into a Principal by
// one of two middlewares, then checked against the route scope table
// (route_scopes.go) by scopeMiddleware:
//
//   - token mode (an API token is configured): bearerAuthMiddleware accepts
//     the bootstrap token (ITERVOX_API_TOKEN, the auto-generated ephemeral
//     token, or the supervisor token) as admin, and any stored token in
//     .itervox/tokens.json with that token's scope. Anything else is 401.
//   - server.allow_unauthenticated mode (no API token): every caller is
//     admin, exactly as before scopes existed; stored tokens are not
//     consulted. cmd/itervox logs a startup warning saying so.
//
// Agent-action and guardrail routes keep their own per-run grants and never
// pass through here.

// Principal is the authenticated caller of a request.
type Principal struct {
	Scope apitokens.Scope `json:"scope"`
	// Source is "bootstrap" (the process's admin token), "token" (a stored
	// token) or "unauthenticated" (server.allow_unauthenticated).
	Source string `json:"source"`
	// TokenID and Name identify a stored token; empty otherwise.
	TokenID string `json:"tokenId,omitempty"`
	Name    string `json:"name,omitempty"`
}

// Principal sources.
const (
	principalBootstrap       = "bootstrap"
	principalToken           = "token"
	principalUnauthenticated = "unauthenticated"
)

// actor names p in audit log lines (never a secret).
func (p Principal) actor() string {
	if p.TokenID != "" {
		return "dashboard:" + p.TokenID
	}
	return "dashboard:" + p.Source
}

type principalKey struct{}

func withPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// principalFrom returns the request's principal. ok is false when no auth
// middleware ran, which scopeMiddleware treats as unauthenticated (401).
func principalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// callerScope is the request's scope, or "" when there is no principal.
func callerScope(r *http.Request) apitokens.Scope {
	p, _ := principalFrom(r.Context())
	return p.Scope
}

// bearerAuthMiddleware rejects requests that do not carry a valid
// "Authorization: Bearer <token>" header: the bootstrap token (admin) or a
// stored token (its scope).
func (s *Server) bearerAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.authenticate(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
			return
		}
		next.ServeHTTP(w, r.WithContext(withPrincipal(r.Context(), p)))
	})
}

// authenticate resolves the request's bearer token.
func (s *Server) authenticate(r *http.Request) (Principal, bool) {
	const prefix = "Bearer "
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, prefix) {
		return Principal{}, false
	}
	presented := strings.TrimPrefix(auth, prefix)
	if presented == "" {
		return Principal{}, false
	}
	// subtle.ConstantTimeCompare returns 0 (not a panic) for a length
	// mismatch, so no length pre-check that would leak via early return.
	if s.apiToken != "" && subtle.ConstantTimeCompare([]byte(presented), []byte(s.apiToken)) == 1 {
		return Principal{Scope: apitokens.ScopeAdmin, Source: principalBootstrap}, true
	}
	if s.tokens != nil {
		if t, ok := s.tokens.Authenticate(presented); ok {
			return Principal{Scope: t.Scope, Source: principalToken, TokenID: t.ID, Name: t.Name}, true
		}
	}
	return Principal{}, false
}

// unauthenticatedAdminMiddleware is server.allow_unauthenticated mode's
// principal: every caller is admin (the pre-scope behaviour).
func unauthenticatedAdminMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := Principal{Scope: apitokens.ScopeAdmin, Source: principalUnauthenticated}
		next.ServeHTTP(w, r.WithContext(withPrincipal(r.Context(), p)))
	})
}

// RequiredScopeHeader names the scope a 403 insufficient_scope response
// needed, so a client can explain the refusal without parsing the body.
const RequiredScopeHeader = "X-Itervox-Required-Scope"

// scopeMiddleware enforces the route scope table. It runs after the
// principal middleware, inside the matched route, so chi's RoutePattern is
// the full pattern of the route about to run. It fails closed: no
// principal is 401, an undeclared route is refused, and a scope below the
// declared one is 403 insufficient_scope naming the scope needed.
func scopeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := principalFrom(r.Context())
		if !ok || !p.Scope.Valid() {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
			return
		}
		pattern := ""
		if rctx := chi.RouteContext(r.Context()); rctx != nil {
			pattern = rctx.RoutePattern()
		}
		required, declared := scopedRouteScope(r.Method, pattern)
		if !declared {
			if strings.HasSuffix(pattern, "/*") {
				// No concrete route matched: chi is answering 405 for a
				// path that exists under another method.
				writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
				return
			}
			slog.Error("server: route has no scope declaration; refusing it", "method", r.Method, "pattern", pattern)
			writeError(w, http.StatusForbidden, "route_scope_undeclared", "this route declares no scope and is refused")
			return
		}
		if !p.Scope.Allows(required) {
			w.Header().Set(RequiredScopeHeader, string(required))
			writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]string{
				"code":           "insufficient_scope",
				"message":        "this action needs the " + string(required) + " scope; your token has " + string(p.Scope),
				"required_scope": string(required),
				"scope":          string(p.Scope),
			}})
			return
		}
		next.ServeHTTP(w, r)
	})
}
