package server

import "net/http"

// securityHeadersMiddleware sets a fixed, default-deny set of hardening
// headers on every response — including 401s, the SPA fallback, and SSE
// streams. It is registered on the root router in New(), before routes(),
// so it runs ahead of the bearer-auth group and applies unconditionally.
//
// There is no config field for this in this slice: the server.embed
// allowlist variant (letting an operator opt into framing for an embedded
// dashboard) is tracked separately in topic 04. Here the policy is a static
// deny:
//   - Content-Security-Policy: frame-ancestors 'none' and
//     X-Frame-Options: DENY together deny framing in both CSP-aware and
//     legacy browsers.
//   - X-Content-Type-Options: nosniff stops MIME-sniffing of served content.
//   - Referrer-Policy: no-referrer avoids leaking the (possibly
//     token-bearing, see AuthGate's ?token= capture) URL via the Referer
//     header on any outbound request the page makes.
func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "frame-ancestors 'none'")
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}
