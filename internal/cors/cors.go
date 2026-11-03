// Package cors is the server.allowed_origins CORS allow-list (#231), shared
// by the daemon's API (internal/server) and the hub (internal/hub): a
// dashboard hosted on another origin (a static host, a phone's installed
// web app) may call them with its bearer token.
//
// The default, an empty list, is same-origin only: no CORS header is ever
// sent and every preflight is refused (403 origin_not_allowed). Because the
// API needs an Authorization header, which a browser sends cross-origin
// only after a successful preflight, a page on an unlisted origin can
// neither call it with a token nor read any answer. A listed origin gets
// Access-Control-Allow-Origin for exactly itself, never "*" and never
// credentials: the API authenticates with a bearer header, not cookies.
// Requests themselves are not refused here: the daemon's own guards
// (bearer auth, the unauthenticated-mode cross-origin and Host guards)
// decide them as before.
//
// The package imports the standard library only.
package cors

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// Headers a cross-origin caller may send and read.
const (
	allowMethods  = "GET, POST, PUT, PATCH, DELETE"
	allowHeaders  = "Authorization, Content-Type, Last-Event-ID, If-None-Match"
	exposeHeaders = "X-Itervox-Required-Scope, Retry-After, ETag"
	maxAge        = "600"
)

// NormalizeOrigin returns raw as a browser serializes an Origin header:
// lower-case scheme://host[:port], the default port dropped. Only http and
// https origins without a path, query, fragment, userinfo or wildcard are
// accepted.
func NormalizeOrigin(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("is empty")
	}
	if strings.Contains(s, "*") {
		return "", errors.New("wildcards are not supported; list each origin")
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("is not a URL: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	switch {
	case scheme != "http" && scheme != "https":
		return "", errors.New("must start with https:// (or http://)")
	case u.User != nil:
		return "", errors.New("must not contain userinfo")
	case u.Host == "" || u.Hostname() == "":
		return "", errors.New("has no host")
	case (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || strings.HasSuffix(s, "?") || strings.HasSuffix(s, "#"):
		return "", errors.New("must be an origin only (scheme://host[:port]), without a path")
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		return scheme + "://" + net.JoinHostPort(host, port), nil
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return scheme + "://" + host, nil
}

// Middleware applies the allow-list (already normalized) in front of next.
func Middleware(allowed []string) func(http.Handler) http.Handler {
	allowed = slices.Clone(allowed)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" || sameOrigin(origin, r.Host) {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Add("Vary", "Origin")
			ok := slices.Contains(allowed, origin)
			preflight := r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
			switch {
			case ok && preflight:
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", origin)
				h.Set("Access-Control-Allow-Methods", allowMethods)
				h.Set("Access-Control-Allow-Headers", allowHeaders)
				h.Set("Access-Control-Max-Age", maxAge)
				w.WriteHeader(http.StatusNoContent)
			case ok:
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Expose-Headers", exposeHeaders)
				next.ServeHTTP(w, r)
			case preflight:
				refuse(w)
			default:
				// No CORS headers: a browser does not let the page read the
				// answer. Auth and the daemon's guards decide the request.
				next.ServeHTTP(w, r)
			}
		})
	}
}

// sameOrigin compares the Origin's host[:port] with the request's Host,
// not the scheme: behind a TLS-terminating proxy the server sees plain
// HTTP while the browser sends an https Origin.
func sameOrigin(origin, host string) bool {
	u, err := url.Parse(origin)
	return err == nil && u.Host != "" && strings.EqualFold(u.Host, host)
}

func refuse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{
		"code":    "origin_not_allowed",
		"message": "this origin is not in server.allowed_origins",
	}})
}
