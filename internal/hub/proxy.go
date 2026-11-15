package hub

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/vnovick/itervox/internal/apitokens"
)

// handleProxy serves /hub/p/{project}/api/... : the member's API, behind
// the hub token's scope (checked against the member's own route table),
// with the hub's member token injected in place of the caller's.
//
// Fail-closed rules, in order:
//   - unknown project: 404; a path that is not a clean /api/v1/... path
//     (dot segments, encoded slashes, empty segments): 400;
//   - a member without a route table, or that runs unauthenticated: 503 /
//     403 (it is never proxied);
//   - a path the member's table does not declare: 404; an agent-grant
//     route, a public non-GET route (tracker webhooks) or an unknown access
//     class: 403 not_proxied;
//   - a public GET route (probes) needs at least viewer; any other route the scope it
//     declares; a member admin route is refused (the hub holds an operator
//     token, never an admin one);
//   - the caller's scope picks the member token: viewer for a viewer hub
//     token, operator otherwise, so the member's own redaction applies.
func (h *Hub) handleProxy(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	m, ok := h.members.get(chi.URLParam(r, "project"))
	if !ok {
		writeError(w, http.StatusNotFound, "project_not_found", "no hub member has that project name")
		return
	}
	rest := "/api/" + chi.URLParam(r, "*")
	if !cleanAPIPath(r, rest) {
		writeError(w, http.StatusBadRequest, "bad_path", "the path must be a plain /api/v1/... path")
		return
	}
	if m.routes == nil {
		writeError(w, http.StatusServiceUnavailable, "member_unavailable", "the hub has no route table for this project yet: "+m.routesErr)
		return
	}
	if !m.rec.TokenRequired {
		writeError(w, http.StatusForbidden, "member_unauthenticated",
			"this project runs with server.allow_unauthenticated, so the hub cannot cap a caller's scope on it; open its own dashboard")
		return
	}
	access, ok := m.routes.match(r.Method, rest)
	if !ok {
		writeError(w, http.StatusNotFound, "route_not_found", "the project declares no such route")
		return
	}
	required := apitokens.Scope(access)
	switch {
	case access == accessPublic && r.Method == http.MethodGet:
		required = apitokens.ScopeViewer // health and readiness probes
	case !required.Valid():
		writeError(w, http.StatusForbidden, "not_proxied", "this route is not reachable through the hub")
		return
	}
	if !p.Scope.Allows(required) {
		writeInsufficientScope(w, required, p.Scope)
		return
	}
	if required == apitokens.ScopeAdmin {
		writeError(w, http.StatusForbidden, "admin_route_not_proxied",
			"project administration (settings, tokens, tmux control) is not reachable through the hub; open the project's own dashboard")
		return
	}
	memberScope := apitokens.ScopeOperator
	if p.Scope == apitokens.ScopeViewer {
		memberScope = apitokens.ScopeViewer
	}
	secret, err := h.client.token(r.Context(), m.rec, memberScope)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "member_token_unavailable", "the hub has no token for this project")
		return
	}
	target, err := url.Parse(m.rec.URL)
	if err != nil || target.Host == "" {
		writeError(w, http.StatusServiceUnavailable, "member_unavailable", "the project's registry record has no usable URL")
		return
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme, pr.Out.URL.Host = target.Scheme, target.Host
			pr.Out.URL.Path, pr.Out.URL.RawPath = rest, ""
			pr.Out.URL.RawQuery = r.URL.RawQuery
			pr.Out.Host = target.Host
			// Never forward the caller's credentials or browser context.
			for _, k := range []string{"Authorization", "Cookie", "Origin", "Referer"} {
				pr.Out.Header.Del(k)
			}
			if secret != "" {
				pr.Out.Header.Set("Authorization", "Bearer "+secret)
			}
		},
		// SSE: flush every write straight through.
		FlushInterval: -1,
		ModifyResponse: func(resp *http.Response) error {
			if resp.StatusCode == http.StatusUnauthorized {
				// The member refused the HUB's token (revoked or rotated): it
				// is not the caller's token, so a 401 would wrongly log the
				// dashboard out. Drop the cached token and answer 502.
				h.client.invalidate(m.rec, memberScope)
				resp.StatusCode = http.StatusBadGateway
				resp.Status = "502 Bad Gateway"
				resp.Header.Set("X-Itervox-Hub-Error", "member_auth_failed")
			}
			resp.Header.Del("Set-Cookie")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			writeError(w, http.StatusBadGateway, "member_unreachable", "the project's daemon did not answer")
		},
	}
	proxy.ServeHTTP(w, r)
}

// cleanAPIPath accepts only a plain /api/v1/... path: no escaped bytes
// that decode differently (an encoded "/" would change which route
// matches), no empty, "." or ".." segments.
func cleanAPIPath(r *http.Request, rest string) bool {
	if r.URL.RawPath != "" || !strings.HasPrefix(rest, "/api/v1/") {
		return false
	}
	for _, seg := range strings.Split(rest[1:], "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}
