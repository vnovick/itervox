// Package egress is the network side of process isolation (agent.egress):
// an HTTP CONNECT proxy run by the daemon for one sandboxed run, which lets
// the agent reach only allowlisted hosts, and the tiny forwarder that
// bridges that proxy (and the daemon's own API) into a sandbox with no
// network of its own.
//
// It imports only the standard library, internal/logging and
// internal/metrics, so internal/agent and internal/config can use it.
package egress

import (
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
)

// DefaultPorts are the ports an allowlist entry without a port allows:
// HTTPS, and plain HTTP (some registries redirect through it).
var DefaultPorts = []int{443, 80}

// Rule is one allowlist entry: a host (or every subdomain of a domain,
// "*.example.com") and a port, or the DefaultPorts when Port is 0.
type Rule struct {
	// Host is lower-case, without a trailing dot; for a wildcard it is the
	// domain the subdomains are of ("example.com").
	Host string
	// Wildcard matches subdomains of Host, not Host itself.
	Wildcard bool
	// Port is the one port allowed; 0 means DefaultPorts.
	Port int
}

// String is the rule as it is written in config.
func (r Rule) String() string {
	h := r.Host
	if r.Wildcard {
		h = "*." + h
	}
	if r.Port != 0 {
		return net.JoinHostPort(h, strconv.Itoa(r.Port))
	}
	return h
}

// ParseRule parses one allowlist entry: "host", "host:port", "*.domain" or
// "*.domain:port", where host is a DNS name or an IP address (IPv6 in
// brackets when it has a port). A scheme, path, user info, a bare "*" or a
// wildcard on an IP address is refused: an entry names hosts, never "all".
func ParseRule(entry string) (Rule, error) {
	s := strings.ToLower(strings.TrimSpace(entry))
	if s == "" {
		return Rule{}, errors.New("egress: empty allowlist entry")
	}
	if strings.ContainsAny(s, "/@?#") || strings.Contains(s, "://") {
		return Rule{}, fmt.Errorf("egress: allowlist entry %q must be a host or host:port, not a URL", entry)
	}
	host, port := s, 0
	if h, p, err := net.SplitHostPort(s); err == nil {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return Rule{}, fmt.Errorf("egress: allowlist entry %q has an invalid port", entry)
		}
		host, port = h, n
	} else if strings.Count(s, ":") > 1 && !strings.HasPrefix(s, "[") {
		host = s // a bare IPv6 address
	} else if strings.Contains(s, ":") {
		return Rule{}, fmt.Errorf("egress: allowlist entry %q has an invalid port", entry)
	}
	host = strings.TrimSuffix(strings.Trim(host, "[]"), ".")
	r := Rule{Host: host, Port: port}
	if rest, ok := strings.CutPrefix(host, "*."); ok {
		r.Host, r.Wildcard = rest, true
	}
	if net.ParseIP(r.Host) != nil {
		if r.Wildcard {
			return Rule{}, fmt.Errorf("egress: allowlist entry %q: a wildcard needs a domain name", entry)
		}
		return r, nil
	}
	if !validDNSName(r.Host) || (r.Wildcard && !strings.Contains(r.Host, ".")) {
		return Rule{}, fmt.Errorf("egress: allowlist entry %q is not a host name", entry)
	}
	return r, nil
}

// validDNSName reports whether h is a plausible DNS name: labels of
// letters, digits and hyphens, not starting or ending with a hyphen.
func validDNSName(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	for label := range strings.SplitSeq(h, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' {
				return false
			}
		}
	}
	return true
}

// Allowlist decides which destinations a run's proxy connects to.
type Allowlist struct {
	rules []Rule
	deny  []string
}

// NewAllowlist parses allow (ParseRule entries) and deny (host names that
// are refused on every port even when an allow entry matches them: the
// run's push remotes, so git traffic stays with the daemon).
func NewAllowlist(allow, deny []string) (*Allowlist, error) {
	a := &Allowlist{}
	for _, e := range allow {
		r, err := ParseRule(e)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(a.rules, r) {
			a.rules = append(a.rules, r)
		}
	}
	for _, d := range deny {
		if h := normalizeHost(d); h != "" && !slices.Contains(a.deny, h) {
			a.deny = append(a.deny, h)
		}
	}
	return a, nil
}

// Rules returns the parsed allow entries, for the run log.
func (a *Allowlist) Rules() []Rule { return slices.Clone(a.rules) }

// Refusal reasons a Decision carries.
const (
	// ReasonNotAllowed: no allowlist entry names the host and port.
	ReasonNotAllowed = "not_allowed"
	// ReasonPushRemote: the host is one of the run's git push remotes.
	ReasonPushRemote = "push_remote"
	// ReasonLocalAddress: the host resolves to a loopback, link-local,
	// unspecified or multicast address (the daemon's machine and cloud
	// metadata services are not reachable through the proxy).
	ReasonLocalAddress = "local_address"
	// ReasonDialFailed: the host is allowed but could not be reached.
	ReasonDialFailed = "dial_failed"
	// ReasonBadRequest: the request named no usable host.
	ReasonBadRequest = "bad_request"
)

// Allows reports whether host:port may be reached, and the refusal reason
// when it may not.
func (a *Allowlist) Allows(host string, port int) (bool, string) {
	h := normalizeHost(host)
	if h == "" || port < 1 || port > 65535 {
		return false, ReasonBadRequest
	}
	if slices.Contains(a.deny, h) {
		return false, ReasonPushRemote
	}
	for _, r := range a.rules {
		if !r.matchesHost(h) {
			continue
		}
		if (r.Port == 0 && slices.Contains(DefaultPorts, port)) || r.Port == port {
			return true, ""
		}
	}
	return false, ReasonNotAllowed
}

func (r Rule) matchesHost(h string) bool {
	if r.Wildcard {
		return strings.HasSuffix(h, "."+r.Host)
	}
	return h == r.Host
}

// normalizeHost lower-cases h and drops brackets and a trailing dot.
func normalizeHost(h string) string {
	return strings.TrimSuffix(strings.Trim(strings.ToLower(strings.TrimSpace(h)), "[]"), ".")
}

// HostOfRemote returns the host of a git remote URL ("https://host/x",
// "ssh://git@host:22/x", "git@host:org/x"), or "" for a local path or a
// file:// URL, which reach no network.
func HostOfRemote(remote string) string {
	r := strings.TrimSpace(remote)
	if r == "" || strings.HasPrefix(r, "/") || strings.HasPrefix(r, ".") || strings.HasPrefix(r, "file:") {
		return ""
	}
	if _, rest, ok := strings.Cut(r, "://"); ok {
		hostPart, _, _ := strings.Cut(rest, "/")
		if i := strings.LastIndex(hostPart, "@"); i >= 0 {
			hostPart = hostPart[i+1:]
		}
		if h, _, err := net.SplitHostPort(hostPart); err == nil {
			return normalizeHost(h)
		}
		return normalizeHost(hostPart)
	}
	// scp-like: [user@]host:path
	hostPart, _, ok := strings.Cut(r, ":")
	if !ok {
		return ""
	}
	if i := strings.LastIndex(hostPart, "@"); i >= 0 {
		hostPart = hostPart[i+1:]
	}
	return normalizeHost(hostPart)
}
