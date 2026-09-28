package server

import (
	"net/http"
)

// crossOriginGuardMiddleware is the CSRF guard for server.allow_unauthenticated
// mode (CORE-041). With no API token there is no bearer check, so without it
// any web page the operator visits could fire POSTs at the loopback daemon —
// refresh, terminate, a tracker comment posted as the operator, a WORKFLOW.md
// rewrite. It is installed ONLY when no token is configured: in token mode a
// browser never attaches the Authorization header cross-site on its own, so
// the bearer check already defeats forgery and this guard would add nothing.
//
// The decision is net/http.CrossOriginProtection (Go 1.25), with no trusted
// origins and no bypass patterns:
//
//   - GET, HEAD and OPTIONS always pass (reads and the SSE streams).
//   - Sec-Fetch-Site, when present, is authoritative: "same-origin" and
//     "none" (user-initiated) pass; "same-site" and "cross-site" are refused.
//     Browsers set it and page script cannot forge it.
//   - Otherwise an Origin header must match the request's Host by
//     host[:port] only — never the scheme, because behind the TLS-terminating
//     proxy in deploy/ the daemon sees plain HTTP while the browser sends an
//     https Origin. "Origin: null" never matches.
//   - A request with neither header passes: every current browser sends
//     Origin (and Sec-Fetch-Site) on a cross-origin POST/PUT/PATCH/DELETE, so
//     such a request is curl, a script, or the TUI, whatever its Content-Type.
//
// The Vite dev server (page on :5173, proxy to the daemon with
// changeOrigin: true) sends Origin http://localhost:5173 against Host
// localhost:8090, but also Sec-Fetch-Site: same-origin, so it passes on any
// browser from 2023 on.
func crossOriginGuardMiddleware(next http.Handler) http.Handler {
	protection := http.NewCrossOriginProtection()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := protection.Check(r); err != nil {
			writeError(w, http.StatusForbidden, "cross_origin_forbidden",
				"cross-origin state-changing request refused: this daemon runs without an API token "+
					"(server.allow_unauthenticated), so only same-origin browser requests or "+
					"non-browser clients may change state ("+err.Error()+")")
			return
		}
		next.ServeHTTP(w, r)
	})
}
