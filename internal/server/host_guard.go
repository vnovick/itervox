package server

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// healthPath is one of the two routes the Host guard exempts (readyPath is
// the other); see hostGuard.
const healthPath = "/api/v1/health"

// hostGuard is the DNS-rebinding guard for server.allow_unauthenticated mode
// (CORE-162). A page on evil.test whose DNS record is re-pointed at 127.0.0.1
// talks to the daemon SAME-ORIGIN: the browser attaches Origin evil.test and
// Sec-Fetch-Site same-origin, so crossOriginGuardMiddleware (correctly)
// passes it, and with no token nothing else stands between that page and
// PUT /api/v1/settings/profiles/{name} — an arbitrary agent `command` the
// orchestrator runs on the next dispatch — or GET /state and the logs. The
// only thing the rebound request cannot fake is its Host header: it carries
// the attacker's NAME.
//
// Rule, on every route (API, SSE, SPA and static files) except
// GET /api/v1/health:
//
//   - an IP literal is always allowed (IPv4, or IPv6 with or without
//     brackets). Rebinding needs a DNS name the attacker controls; a browser
//     sends an IP literal as Host only when the page's own origin IS that
//     address, so it is never a rebinding vector. This keeps LAN access by
//     address (server.host 0.0.0.0, browsing to http://192.168.x.y:8090),
//     container probes (Host: <pod-ip>) and SSH tunnels working unchanged.
//   - "localhost" is allowed.
//   - the configured server.host is allowed when it is a name.
//   - any name in server.allowed_hosts is allowed (reverse proxies, tunnels,
//     Tailscale MagicDNS, container service names).
//   - every other name is refused with 403 host_not_allowed.
//
// Names compare case-insensitively with the port and one trailing dot
// removed. A request with no Host at all (HTTP/1.0) is not a browser and
// passes.
//
// /api/v1/health is exempt because it answers a constant {"status":"ok"} —
// no state, no side effect — and load balancers commonly probe it by a
// hostname nobody thought to allowlist. GET /api/v1/ready (CORE-043) is
// exempt for the same reason: probes (Kubernetes httpGet with a host
// override, a Compose healthcheck against a service name, an ALB target
// check) address it by a name nobody allowlists, and its body is five
// booleans plus the tracker's rate-limit reset — no error text, identifiers,
// configuration or side effect. A rebound page learns only whether the daemon
// is up and whether the tracker is rate limiting it, which is not worth
// breaking probes over. (A pod-IP probe would pass anyway: IP literals are
// always allowed.)
//
// Token mode does not install this guard: a rebound page never holds the
// bearer token (the token lives in the real origin's storage, and the
// browser never attaches an Authorization header on its own), so everything
// it could reach without one is already public — health, the SPA shell, and
// the per-run-token agent-action routes. Adding the check there would only
// break existing reverse-proxy deployments (deploy/ keeps the public Host)
// on upgrade without closing any hole.
//
// 403 rather than 421 Misdirected Request: the refusal is a policy decision
// with the same {error:{code,message}} shape as cross_origin_forbidden, and
// 421 carries retry-on-another-connection semantics for HTTP/2 clients that
// do not apply here.
func hostGuard(bindHost string, allowed []string) func(http.Handler) http.Handler {
	names := map[string]struct{}{"localhost": {}}
	for _, h := range append([]string{bindHost}, allowed...) {
		if n := normalizeHostName(h); n != "" {
			names[n] = struct{}{}
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if hostAllowed(r.Host, names) ||
				(r.Method == http.MethodGet && (r.URL.Path == healthPath || r.URL.Path == readyPath)) {
				next.ServeHTTP(w, r)
				return
			}
			writeError(w, http.StatusForbidden, "host_not_allowed",
				"request Host is not allowed: this daemon runs without an API token "+
					"(server.allow_unauthenticated), so it only answers requests addressed to "+
					"localhost, an IP address, server.host, or a name listed in server.allowed_hosts "+
					"(DNS-rebinding protection)")
		})
	}
}

// hostAllowed applies hostGuard's rule to a raw Host header value.
func hostAllowed(rawHost string, names map[string]struct{}) bool {
	if rawHost == "" {
		return true
	}
	host := hostWithoutPort(rawHost)
	if _, err := netip.ParseAddr(host); err == nil {
		return true
	}
	_, ok := names[normalizeHostName(host)]
	return ok
}

// hostWithoutPort strips an optional :port and IPv6 brackets.
func hostWithoutPort(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
}

// normalizeHostName lower-cases a configured or presented host name, strips
// an optional port and one trailing dot ("localhost." resolves like
// "localhost").
func normalizeHostName(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if h == "" {
		return ""
	}
	return strings.TrimSuffix(hostWithoutPort(h), ".")
}
