package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/vnovick/itervox/internal/egress"
)

// Egress modes (agent.egress): the network side of isolation: "process".
// With "allowlist" the sandboxed agent has no network of its own; it
// reaches only the allowlisted hosts through a proxy the daemon runs for
// the run, and git traffic stays with the daemon. See internal/egress.
const (
	// EgressOff is the default: the sandbox leaves the network open.
	EgressOff = "off"
	// EgressAllowlist closes the network but for the allowlist.
	EgressAllowlist = "allowlist"
)

// ErrEgressNeedsIsolation is the load error for egress: allowlist on a
// profile that does not run with isolation: process: without the sandbox
// nothing stops the agent from opening a connection itself, so the
// allowlist would be a promise Itervox cannot keep.
var ErrEgressNeedsIsolation = errors.New(`config: egress "allowlist" needs isolation: process`)

// EgressModelHosts are the model API hosts each backend's CLI talks to,
// allowed by default (agent.egress_defaults). A backend not listed has no
// default model host: name it in egress_allow.
var EgressModelHosts = map[string][]string{
	BackendClaude: {"api.anthropic.com", "console.anthropic.com"},
	BackendCodex:  {"api.openai.com", "auth.openai.com", "chatgpt.com"},
}

// EgressRegistryHosts are the package registries allowed by default
// (agent.egress_defaults): npm, PyPI, the Go module proxy, crates.io and
// RubyGems. A module fetched straight from a git host (Go's "direct") is
// git traffic and is refused.
var EgressRegistryHosts = []string{
	"registry.npmjs.org", "registry.yarnpkg.com",
	"pypi.org", "files.pythonhosted.org",
	"proxy.golang.org", "sum.golang.org",
	"crates.io", "index.crates.io", "static.crates.io",
	"rubygems.org",
}

// ValidateEgressMode rejects an unknown egress value. allowEmpty is true
// for a profile, where empty means "inherit agent.egress". An unknown value
// is a hard error: falling back to "off" would open the network the
// operator asked to close.
func ValidateEgressMode(field, mode string, allowEmpty bool) error {
	switch mode {
	case EgressOff, EgressAllowlist:
		return nil
	case "":
		if allowEmpty {
			return nil
		}
	}
	return fmt.Errorf("config: %s must be %q or %q, got %q", field, EgressOff, EgressAllowlist, mode)
}

// ResolveEgressMode returns the egress a profile runs with: its own value,
// or agent.egress when it sets none, or "off".
func ResolveEgressMode(profileMode, defaultMode string) string {
	if m := strings.TrimSpace(profileMode); m != "" {
		return m
	}
	if m := strings.TrimSpace(defaultMode); m != "" {
		return m
	}
	return EgressOff
}

// parseEgressAllow reads an egress_allow list, each entry checked by
// egress.ParseRule.
func parseEgressAllow(m map[string]any, field string) ([]string, error) {
	var out []string
	for _, e := range strSliceField(m, "egress_allow", nil) {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		r, err := egress.ParseRule(e)
		if err != nil {
			return nil, fmt.Errorf("config: %s: %w", field, err)
		}
		out = append(out, r.String())
	}
	return out, nil
}

// optionalBoolField reads key as a *bool: nil when absent.
func optionalBoolField(m map[string]any, key string) *bool {
	if _, ok := m[key]; !ok {
		return nil
	}
	return boolPtr(boolField(m, key, true))
}

// profileEgress is the egress part of one parsed profile.
type profileEgress struct {
	Egress         string
	EgressAllow    []string
	EgressDefaults *bool
}

// profileEgressFields parses a profile's egress keys.
func profileEgressFields(m map[string]any, name string) (profileEgress, error) {
	out := profileEgress{Egress: strings.TrimSpace(strField(m, "egress", ""))}
	if err := ValidateEgressMode("agent.profiles."+name+".egress", out.Egress, true); err != nil {
		return profileEgress{}, err
	}
	var err error
	if out.EgressAllow, err = parseEgressAllow(m, "agent.profiles."+name+".egress_allow"); err != nil {
		return profileEgress{}, err
	}
	out.EgressDefaults = optionalBoolField(m, "egress_defaults")
	return out, nil
}

// EgressProfiles returns the enabled profiles that run with egress:
// allowlist, sorted; "(default)" stands for agent.command when
// agent.egress is allowlist.
func EgressProfiles(a AgentConfig) []string {
	var out []string
	for name, p := range a.Profiles {
		if ProfileEnabled(p) && ResolveEgressMode(p.Egress, a.Egress) == EgressAllowlist {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	if a.Egress == EgressAllowlist {
		out = append([]string{"(default)"}, out...)
	}
	return out
}

// ValidateEgress refuses egress: allowlist where it cannot hold: on a
// profile (or agent.command) that does not run with isolation: process.
// SSH workers are already refused for isolation (ValidateIsolationHosts).
func ValidateEgress(a AgentConfig) error {
	var bad []string
	if a.Egress == EgressAllowlist && ResolveIsolation("", a.Isolation) != IsolationProcess {
		bad = append(bad, "(default)")
	}
	names := make([]string, 0, len(a.Profiles))
	for name := range a.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p := a.Profiles[name]
		if !ProfileEnabled(p) || ResolveEgressMode(p.Egress, a.Egress) != EgressAllowlist {
			continue
		}
		if ResolveIsolation(p.Isolation, a.Isolation) != IsolationProcess {
			bad = append(bad, name)
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("%w: %s run with egress: allowlist but not isolation: process; "+
			"set isolation: process for them, or egress: off", ErrEgressNeedsIsolation, strings.Join(bad, ", "))
	}
	return nil
}

// ResolvedEgress is what one run's egress is built from.
type ResolvedEgress struct {
	// On is egress: allowlist.
	On bool
	// Allow are the allowlist entries: the defaults for the backend (when
	// egress_defaults), agent.egress_allow, the profile's egress_allow and
	// the hosts of the remote model servers the command points at.
	Allow []string
	// LocalPorts are the loopback ports of the local model servers the
	// command points at (ANTHROPIC_BASE_URL=http://127.0.0.1:8000 …), which
	// the sandbox reaches outside the proxy.
	LocalPorts []int
}

// ResolveEgress returns the egress a run of profile p (backend, command)
// runs with. modelServerURLs are the model-server base URLs the agent will
// use beyond those in command (the daemon's own ANTHROPIC_BASE_URL & co.,
// which the agent inherits).
func ResolveEgress(p AgentProfile, a AgentConfig, backend, command string, modelServerURLs []string) ResolvedEgress {
	if ResolveEgressMode(p.Egress, a.Egress) != EgressAllowlist {
		return ResolvedEgress{}
	}
	out := ResolvedEgress{On: true}
	defaults := a.EgressDefaults
	if p.EgressDefaults != nil {
		defaults = *p.EgressDefaults
	}
	if backend == "" {
		backend = BackendFromCommand(command)
	}
	if defaults {
		out.Allow = append(out.Allow, EgressModelHosts[backend]...)
		out.Allow = append(out.Allow, EgressRegistryHosts...)
	}
	out.Allow = append(out.Allow, a.EgressAllow...)
	out.Allow = append(out.Allow, p.EgressAllow...)
	urls := slices.Clone(modelServerURLs)
	for _, u := range ModelServerURLs(command) {
		urls = append(urls, u)
	}
	sort.Strings(urls)
	for _, raw := range urls {
		host, port, ok := modelServerAddr(raw)
		if !ok {
			continue
		}
		if isLoopbackHost(host) {
			if !slices.Contains(out.LocalPorts, port) {
				out.LocalPorts = append(out.LocalPorts, port)
			}
			continue
		}
		out.Allow = append(out.Allow, net.JoinHostPort(host, strconv.Itoa(port)))
	}
	sort.Ints(out.LocalPorts)
	return out
}

// EnvModelServerURLs are the model-server base URLs in this process's
// environment (ModelServerEnvVars), which an agent it starts inherits.
func EnvModelServerURLs() []string {
	var out []string
	for _, k := range ModelServerEnvVars {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// modelServerAddr returns the host and port of a model-server base URL; a
// $VAR the shell expands at run time is skipped.
func modelServerAddr(raw string) (string, int, bool) {
	if strings.Contains(raw, "$") {
		return "", 0, false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "", 0, false
	}
	port := 443
	if u.Scheme == "http" {
		port = 80
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil {
			return "", 0, false
		}
		port = n
	}
	return strings.ToLower(u.Hostname()), port, true
}

// isLoopbackHost reports whether host names this machine's loopback.
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
