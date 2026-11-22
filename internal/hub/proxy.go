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
//   - the live terminal (#298): through the hub it is READ-ONLY and
//     brokered, never proxied as is (terminal.go). A POST to the terminal
//     ticket route passes every check below (the member declares it
//     operator, so a viewer hub token is refused) and is then brokered by
//     brokerTerminalTicket: "write" is refused, the member is asked for a
//     read ticket with the operator member token and the caller's name.
//     The socket itself is its own hub route outside the bearer group
//     (handleTerminalSocket), opened only for a ticket the hub brokered.
//     Here, any other WebSocket upgrade, a "ticket" route reached with a
//     bearer, and the socket's path: 403 terminal_not_proxied;
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
	brokered := isTerminalTicketRequest(r, rest)
	if !brokered && isTerminalRequest(r, rest) {
		writeError(w, http.StatusForbidden, "terminal_not_proxied",
			"only the read-only live terminal is reachable through the hub, through a ticket the hub brokers; open the project's own dashboard for anything else")
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
	case access == accessTicket:
		writeError(w, http.StatusForbidden, "terminal_not_proxied",
			"the live terminal is not reachable through the hub; open the project's own dashboard")
		return
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
	if brokered {
		h.brokerTerminalTicket(w, r, m, chi.URLParam(r, "project"), rest, p)
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
			// Never forward the caller's credentials or browser context,
			// nor a hub caller name: only brokerTerminalTicket sets one.
			for _, k := range []string{"Authorization", "Cookie", "Origin", "Referer", hubCallerHeader} {
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
			// The hub, not the member, says whether this caller gets the
			// read-only live terminal through it (terminal.go).
			resp.Header.Del(TerminalHeader)
			if p.Scope.Allows(apitokens.ScopeOperator) {
				resp.Header.Set(TerminalHeader, TerminalHeaderRead)
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			writeError(w, http.StatusBadGateway, "member_unreachable", "the project's daemon did not answer")
		},
	}
	proxy.ServeHTTP(w, r)
}

// isTerminalRequest reports a request the hub never proxies as is because
// it is (or leads to) a member's live terminal (#298): a WebSocket upgrade
// of any path, or the terminal ticket and socket routes by their shape
// (/api/v1/sessions/{id}/terminal[/ticket]), checked before the member's
// table so a table that mis-declares them changes nothing. handleProxy
// brokers the one exception, the ticket POST (isTerminalTicketRequest).
func isTerminalRequest(r *http.Request, rest string) bool {
	if isUpgrade(r) {
		return true
	}
	_, _, ok := terminalPath(rest)
	return ok
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
