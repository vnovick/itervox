package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/egress"
)

// Network egress for process isolation (agent.egress: allowlist).
//
// The sandbox gets no network of its own: bubblewrap runs the agent in new
// network and pid namespaces (only a loopback interface), sandbox-exec
// denies network* but the proxy port. The daemon runs one HTTP CONNECT
// proxy per run (internal/egress) that reaches only the allowlisted hosts
// and never the run's push remotes; the agent's HTTPS_PROXY names it. On
// Linux the proxy listens on a Unix socket in a private run directory that
// the sandbox binds, and a forwarder started first inside the sandbox
// (this binary with egress.ForwardEntrypoint) bridges the sandbox's
// loopback to it, and to the daemon's own API port for the guardrail shims
// and agent actions. Landlock cannot restrict connections by host, so an
// egress run never falls back to it: no bubblewrap means no run.
//
// With egress on, git credentials are hidden as well (IsolationEgressHomeDeny
// and the SSH agent socket), so a git push from inside the sandbox has
// neither a route nor a credential. The daemon's own push (push: daemon)
// runs outside the sandbox through gitexec and is unaffected.

// EgressInputs is what a run's egress is built from (IsolationInputs.Egress).
type EgressInputs struct {
	// Allow are the allowlist entries (config.ResolveEgress); Deny the
	// hosts refused even when allowed (the workspace's git remotes).
	Allow []string
	Deny  []string
	// LocalPorts are loopback ports on the daemon's machine the agent may
	// reach outside the proxy: the daemon's API port and local model
	// servers.
	LocalPorts []int
	// Label names the run in proxy log lines (issue identifier).
	Label string
	// OnDecision receives every proxy decision; it must not block.
	OnDecision func(egress.Decision)
	// Dial replaces the proxy's upstream dialer (tests).
	Dial egress.DialFunc
	// ParentDir overrides where bridge-mode run directories are created
	// (tests; Unix socket paths must stay short).
	ParentDir string
}

// EgressInputsFor turns a resolved egress into a run's EgressInputs (nil
// when egress is off). label names the run in proxy log lines. The caller
// adds Deny (the workspace's remotes) and OnDecision where it has them.
func EgressInputsFor(r config.ResolvedEgress, label string) *EgressInputs {
	if !r.On {
		return nil
	}
	return &EgressInputs{Allow: slices.Clone(r.Allow), LocalPorts: slices.Clone(r.LocalPorts), Label: label}
}

// IsolationEgressHomeDeny is never readable with egress on, relative to
// $HOME, on top of IsolationHomeDeny and IsolationPatchOutHomeDeny: git's
// credential-cache sockets. With them, every stored git, gh and glab
// credential the deny lists know of is hidden.
var IsolationEgressHomeDeny = []string{".git-credential-cache", ".cache/git/credential"}

// EgressMechanism is a sandbox that can also close the network.
type EgressMechanism interface {
	// EgressAvailable returns nil when the mechanism can run an agent
	// without network on this machine, or why it cannot.
	EgressAvailable() error
	// EgressBridge reports whether the sandbox reaches the proxy through
	// Unix sockets and the in-sandbox forwarder (its own network
	// namespace) rather than over the daemon's loopback.
	EgressBridge() bool
}

// EgressPolicy is the network part of an IsolationPolicy: set only when
// egress is on.
type EgressPolicy struct {
	// ProxyPort is the loopback port the agent's proxy variables name.
	ProxyPort int
	// Bridges are the in-sandbox listeners and their Unix sockets
	// (bubblewrap); Forwarder is the binary that serves them.
	Bridges   []egress.Bridge
	Forwarder string
	// LocalPorts are the daemon-side loopback ports the sandbox may
	// connect to directly (sandbox-exec).
	LocalPorts []int
}

// egressForwarderReady is set by RunIsolationHelper: only a binary that
// handles egress.ForwardEntrypoint at startup can be the forwarder.
var egressForwarderReady atomic.Bool

// RunIsolationHelper must be called first thing in main (and in a test
// binary's TestMain). When args name a sandbox helper — the Landlock
// launcher or the egress forwarder — it runs it, returning only on failure
// with the exit code to use (ok=true); otherwise it marks this binary as
// able to act as either and returns ok=false.
func RunIsolationHelper(args []string) (code int, ok bool) {
	if len(args) > 1 && egress.IsEntrypoint(args[1]) {
		return egress.Main(args[1:], os.Stderr), true
	}
	egressForwarderReady.Store(true)
	return runLandlockHelper(args)
}

// startEgress checks that m can close the network, starts the run's proxy
// and returns it with the policy additions. The caller closes the runtime
// when the run ends (Isolation.Close).
func startEgress(m IsolationMechanism, in *EgressInputs) (*egress.Runtime, *EgressPolicy, error) {
	em, ok := m.(EgressMechanism)
	if !ok {
		return nil, nil, fmt.Errorf("%w: egress: allowlist needs bubblewrap (Linux) or sandbox-exec (macOS); "+
			"%s cannot restrict network connections by host, so the run does not start", ErrIsolationUnavailable, m.Name())
	}
	if err := em.EgressAvailable(); err != nil {
		return nil, nil, fmt.Errorf("%w: egress: allowlist: %s cannot close the network here: %v", ErrIsolationUnavailable, m.Name(), err)
	}
	pol := &EgressPolicy{LocalPorts: in.LocalPorts}
	if em.EgressBridge() {
		if !egressForwarderReady.Load() {
			return nil, nil, fmt.Errorf("%w: egress: this process cannot act as the in-sandbox forwarder", ErrIsolationUnavailable)
		}
		exe, err := os.Executable()
		if err == nil {
			exe, err = filepath.EvalSymlinks(exe)
		}
		if err != nil {
			return nil, nil, fmt.Errorf("%w: egress: cannot locate the itervox binary for the forwarder: %v", ErrIsolationUnavailable, err)
		}
		pol.Forwarder = exe
	}
	rt, err := egress.Start(egress.Config{
		Allow: in.Allow, Deny: in.Deny, LocalPorts: in.LocalPorts, Bridge: em.EgressBridge(),
		ParentDir: in.ParentDir, Label: in.Label, OnDecision: in.OnDecision, Dial: in.Dial,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("%w: egress: %v", ErrIsolationUnavailable, err)
	}
	pol.ProxyPort, pol.Bridges = rt.ProxyPort, rt.Bridges
	return rt, pol, nil
}

// egressCredentialDeny is what egress hides on top of the base deny lists:
// the push: daemon credential files, git's credential-cache sockets and the
// SSH agent socket the daemon's environment names.
func egressCredentialDeny(home string) []string {
	out := joinAll(home, IsolationPatchOutHomeDeny)
	out = append(out, joinAll(home, IsolationEgressHomeDeny)...)
	if sock := os.Getenv("SSH_AUTH_SOCK"); filepath.IsAbs(sock) {
		out = append(out, sock)
	}
	return out
}

// addEgressPolicy adds the run's egress to p: the private run directory is
// writable (its sockets), the directory holding every run's directory is
// hidden, the credentials are denied, and p.Egress is set.
func addEgressPolicy(p *IsolationPolicy, rt *egress.Runtime, pol *EgressPolicy, home string) {
	if rt.Dir != "" {
		p.Write = dedupe(append(p.Write, resolveExisting([]string{rt.Dir})...))
		p.Deny = dedupe(append(p.Deny, resolveExisting([]string{rt.ParentDir})...))
	}
	p.Deny = dedupe(append(p.Deny, resolveExisting(egressCredentialDeny(home))...))
	p.Egress = pol
}

// egressRunEnv is the environment an egress run's agent gets.
func (iso *Isolation) egressRunEnv() RunEnv {
	if iso == nil || iso.Egress == nil {
		return RunEnv{}
	}
	return RunEnv{Vars: iso.Egress.Env(), Unset: egress.UnsetEnv}
}

// describeEgress is the network part of Describe.
func (iso *Isolation) describeEgress() string {
	if iso.Egress == nil {
		return "network: allowed"
	}
	rules := iso.Egress.Allowlist.Rules()
	names := make([]string, 0, len(rules))
	for _, r := range rules {
		names = append(names, r.String())
	}
	ports := make([]string, 0, len(iso.Egress.LocalPorts))
	for _, p := range iso.Egress.LocalPorts {
		ports = append(ports, strconv.Itoa(p))
	}
	return fmt.Sprintf("network: egress allowlist via proxy (%s); local ports: %s", listOrNone(names), listOrNone(ports))
}

// Close stops the run's egress proxy, if any. Safe on nil.
func (iso *Isolation) Close() {
	if iso == nil {
		return
	}
	iso.Egress.Close()
}

// EgressOn reports whether the run's network is closed but for the
// allowlist.
func (iso *Isolation) EgressOn() bool { return iso != nil && iso.Egress != nil }

// turnRunEnv is the run environment a local agent turn applies: the
// RunEnv ctx carries plus, for an egress run, the proxy variables and the
// credential variables removed.
func turnRunEnv(ctxEnv RunEnv, iso *Isolation) RunEnv {
	extra := iso.egressRunEnv()
	if len(extra.Vars) == 0 && len(extra.Unset) == 0 {
		return ctxEnv
	}
	out := ctxEnv
	out.Vars = append(append([]string{}, ctxEnv.Vars...), extra.Vars...)
	out.Unset = append(append([]string{}, ctxEnv.Unset...), extra.Unset...)
	return out
}

// turnEnv is turnRunEnv for the RunEnv and Isolation ctx carries.
func turnEnv(ctx context.Context) RunEnv { return turnRunEnv(RunEnvFrom(ctx), IsolationFrom(ctx)) }

// EgressAvailability is doctor's answer to "can egress be enforced
// here?": the first mechanism that can close the network, or why none can.
func EgressAvailability() (string, error) {
	var reasons []string
	for _, m := range platformIsolationMechanisms() {
		em, ok := m.(EgressMechanism)
		if !ok {
			reasons = append(reasons, m.Name()+": cannot restrict network by host")
			continue
		}
		if err := m.Available(); err != nil {
			reasons = append(reasons, m.Name()+": "+err.Error())
			continue
		}
		if err := em.EgressAvailable(); err != nil {
			reasons = append(reasons, m.Name()+": "+err.Error())
			continue
		}
		return m.Name(), nil
	}
	if len(reasons) == 0 {
		return "", fmt.Errorf("%w: no sandbox mechanism is supported on this operating system", ErrIsolationUnavailable)
	}
	return "", fmt.Errorf("%w: %s", ErrIsolationUnavailable, strings.Join(reasons, "; "))
}
