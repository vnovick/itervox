package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"

	"golang.org/x/net/idna"
)

// normalizeAllowedHosts validates server.allowed_hosts and rewrites each entry
// into exactly the form the server's Host guard compares against (BH2):
// lower case, no port, no trailing dot, and a Unicode name converted to its
// IDNA ASCII (punycode, xn--) form — browsers always send that form in Host.
//
// An entry the guard could never match is a config-load ERROR, not a warning
// that drops it. The guard fails closed, so an unmatched entry never weakens
// security, but it does silently break exactly the deployment the operator
// listed it for (a reverse proxy or tunnel answered with 403
// host_not_allowed), and a warning scrolled past at startup is easy to miss.
// Refusing to load names the entry and the form that works. server.
// allowed_hosts is new in this release, so no existing config can be broken
// by the stricter rule.
//
// Rejected: a URL ("https://proxy.example" — the name alone is wanted), a
// path, query or fragment, userinfo, a wildcard, and anything that is not a
// valid host name. Wildcards are deliberately unsupported: the guard's value
// is an exact list of names the operator controls, and a suffix pattern such
// as *.ts.net would admit names the operator does not (every tailnet, or
// every customer of a shared domain). List each full name instead.
//
// Accepted as-is after normalization: IP literals (redundant — the guard
// always admits IP literals — but harmless), and a trailing :port, which is
// dropped because the guard ignores ports on both sides.
func normalizeAllowedHosts(entries []string) ([]string, error) {
	if entries == nil {
		return nil, nil
	}
	out := make([]string, 0, len(entries))
	var errs []error
	for _, raw := range entries {
		name, err := normalizeAllowedHost(raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("config: server.allowed_hosts entry %q: %w", raw, err))
			continue
		}
		if name != "" {
			out = append(out, name)
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
}

// idnaProfile converts a Unicode host name to ASCII with the lookup mapping
// (case folding, width mapping) and label validation. StrictDomainName is off
// so a name with an underscore — a Docker Compose service name, say — is not
// rejected: it is a valid Host header even if it is not a valid DNS label.
var idnaProfile = idna.New(idna.MapForLookup(), idna.Transitional(false), idna.StrictDomainName(false))

func normalizeAllowedHost(raw string) (string, error) {
	h := strings.TrimSpace(raw)
	if h == "" {
		return "", nil
	}
	if strings.Contains(h, "://") {
		if u, err := url.Parse(h); err == nil && u.Hostname() != "" {
			return "", fmt.Errorf("is a URL; list the host name only, e.g. %q", strings.ToLower(u.Hostname()))
		}
		return "", errors.New("is a URL; list the host name only")
	}
	if strings.Contains(h, "*") {
		return "", errors.New("wildcards are not supported; list each full host name")
	}
	if i := strings.IndexAny(h, "/?#"); i >= 0 {
		return "", fmt.Errorf("has a path, query or fragment; list the host name only, e.g. %q", strings.ToLower(h[:i]))
	}
	if strings.Contains(h, "@") {
		return "", errors.New("contains userinfo (@); list the host name only")
	}
	host := h
	if hp, _, err := net.SplitHostPort(h); err == nil {
		host = hp
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if addr, err := netip.ParseAddr(host); err == nil {
		return addr.String(), nil
	}
	host = strings.TrimSuffix(host, ".")
	ascii, err := idnaProfile.ToASCII(host)
	if err != nil {
		return "", fmt.Errorf("is not a valid host name: %w", err)
	}
	ascii = strings.ToLower(ascii)
	if !validHostName(ascii) {
		return "", errors.New("is not a valid host name")
	}
	return ascii, nil
}

// validHostName reports whether s is a dot-separated sequence of non-empty
// labels of letters, digits, '-' and '_' (the last for container service
// names), none starting or ending with '-'.
func validHostName(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			switch {
			case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
			default:
				return false
			}
		}
	}
	return true
}
