package egress

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultSandboxProxyPort is the loopback port the agent reaches the proxy
// on inside a sandbox with its own network namespace (bridge mode). It only
// has to differ from the bridged local ports; the next free one is used
// when it does not.
const DefaultSandboxProxyPort = 3128

// Config is what one run's egress runtime is started from.
type Config struct {
	// Allow are the allowlist entries (ParseRule); Deny the hosts refused
	// even when allowed (the run's push remotes).
	Allow []string
	Deny  []string
	// LocalPorts are loopback ports on the daemon's machine the sandboxed
	// agent may reach directly, outside the proxy: the daemon's own API
	// (guardrail shims, agent actions) and a local model server.
	LocalPorts []int
	// Bridge selects how the sandbox reaches the proxy. true (Linux,
	// bubblewrap with its own network namespace): the proxy and one relay
	// per local port listen on Unix sockets in a private run directory the
	// sandbox binds, and the in-sandbox forwarder (ForwarderArgv) listens on
	// the sandbox's loopback. false (macOS sandbox-exec): the proxy listens
	// on the daemon's loopback and the sandbox profile allows connecting to
	// it and to LocalPorts only.
	Bridge bool
	// ParentDir holds the run directories (bridge mode); "" uses
	// DefaultParentDir. It is denied inside the sandbox as a whole; only
	// the run's own directory is re-exposed.
	ParentDir string
	// Label names the run in log lines.
	Label string
	// OnDecision receives every proxy decision (Proxy.OnDecision).
	OnDecision func(Decision)
	// Dial replaces the proxy's upstream dialer (tests).
	Dial DialFunc
}

// Bridge is one in-sandbox loopback port and the Unix socket it relays to.
type Bridge struct {
	Port   int
	Socket string
}

// Runtime is one run's running proxy and relays.
type Runtime struct {
	// Dir is the run's private directory (bridge mode), else "".
	Dir string
	// ParentDir is Config.ParentDir resolved (bridge mode), else "".
	ParentDir string
	// ProxyPort is the loopback port the agent's proxy variables name.
	ProxyPort int
	// Bridges are the in-sandbox listeners (bridge mode): the proxy first,
	// then one per local port.
	Bridges []Bridge
	// LocalPorts are Config.LocalPorts.
	LocalPorts []int
	// Allowlist is the parsed allowlist.
	Allowlist *Allowlist

	proxy     *Proxy
	listeners []net.Listener
	wg        sync.WaitGroup
	closeOnce sync.Once
}

// DefaultParentDir is where bridge-mode run directories are created: a
// per-user directory under the system temp directory.
func DefaultParentDir() string {
	return filepath.Join(os.TempDir(), "itervox-egress-"+strconv.Itoa(os.Getuid()))
}

// Start starts a run's proxy (and, in bridge mode, its relays). The caller
// must Close it when the run ends.
func Start(cfg Config) (*Runtime, error) {
	allow, err := NewAllowlist(cfg.Allow, cfg.Deny)
	if err != nil {
		return nil, err
	}
	rt := &Runtime{Allowlist: allow, LocalPorts: slices.Clone(cfg.LocalPorts)}
	rt.proxy = &Proxy{Allow: allow, Dial: cfg.Dial, OnDecision: cfg.OnDecision, Label: cfg.Label}
	if err := rt.start(cfg); err != nil {
		rt.Close()
		return nil, err
	}
	return rt, nil
}

func (rt *Runtime) start(cfg Config) error {
	if !cfg.Bridge {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return fmt.Errorf("egress: listen: %w", err)
		}
		rt.ProxyPort = l.Addr().(*net.TCPAddr).Port
		rt.serveProxy(l)
		return nil
	}
	parent := cfg.ParentDir
	if parent == "" {
		parent = DefaultParentDir()
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("egress: create %s: %w", parent, err)
	}
	if fi, err := os.Lstat(parent); err != nil || !fi.IsDir() || fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("egress: %s must be a private directory (0700)", parent)
	}
	if resolved, err := filepath.EvalSymlinks(parent); err == nil {
		parent = resolved
	}
	dir, err := os.MkdirTemp(parent, "run-")
	if err != nil {
		return fmt.Errorf("egress: create run dir: %w", err)
	}
	rt.Dir, rt.ParentDir = dir, parent
	rt.ProxyPort = sandboxProxyPort(cfg.LocalPorts)

	sock := filepath.Join(dir, "proxy.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		return fmt.Errorf("egress: listen: %w", err)
	}
	rt.serveProxy(l)
	rt.Bridges = append(rt.Bridges, Bridge{Port: rt.ProxyPort, Socket: sock})
	for _, port := range cfg.LocalPorts {
		sock := filepath.Join(dir, "port-"+strconv.Itoa(port)+".sock")
		rl, err := net.Listen("unix", sock)
		if err != nil {
			return fmt.Errorf("egress: listen: %w", err)
		}
		rt.listeners = append(rt.listeners, rl)
		target := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
		rt.wg.Add(1)
		go func() {
			defer recoverEgressGoroutine("local port relay")
			defer rt.wg.Done()
			rt.relay(rl, func() (net.Conn, error) {
				return net.DialTimeout("tcp", target, dialTimeout)
			})
		}()
		rt.Bridges = append(rt.Bridges, Bridge{Port: port, Socket: sock})
	}
	return nil
}

// sandboxProxyPort is DefaultSandboxProxyPort, or the next port that is
// not one of the bridged local ports.
func sandboxProxyPort(local []int) int {
	p := DefaultSandboxProxyPort
	for slices.Contains(local, p) {
		p++
	}
	return p
}

func (rt *Runtime) serveProxy(l net.Listener) {
	rt.listeners = append(rt.listeners, l)
	rt.wg.Add(1)
	go func() {
		defer recoverEgressGoroutine("proxy server")
		defer rt.wg.Done()
		if err := rt.proxy.Serve(l); err != nil && !errors.Is(err, net.ErrClosed) {
			slog.Warn("egress: proxy stopped", "run", rt.proxy.Label, "error", err)
		}
	}()
}

// relay accepts connections on l and pipes each to a connection from dial,
// until l is closed. Open connections are tracked by the proxy, whose Close
// ends them.
func (rt *Runtime) relay(l net.Listener, dial func() (net.Conn, error)) {
	var conns sync.WaitGroup
	defer conns.Wait()
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		conns.Add(1)
		go func() {
			defer recoverEgressGoroutine("relay connection")
			defer conns.Done()
			up, err := dial()
			if err != nil {
				_ = c.Close()
				return
			}
			if !rt.proxy.track(c, up) {
				_ = c.Close()
				_ = up.Close()
				return
			}
			pipe(c, up, nil)
			rt.proxy.untrack(c, up)
		}()
	}
}

// Close stops the proxy, the relays and every open connection, and removes
// the run directory. Safe on nil and more than once.
func (rt *Runtime) Close() {
	if rt == nil {
		return
	}
	rt.closeOnce.Do(func() {
		if rt.proxy != nil {
			_ = rt.proxy.Close()
		}
		for _, l := range rt.listeners {
			_ = l.Close()
		}
		done := make(chan struct{})
		go func() {
			defer recoverEgressGoroutine("runtime close wait")
			rt.wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			slog.Warn("egress: relays still draining at close", "run", rt.proxy.Label)
		}
		if rt.Dir != "" {
			_ = os.RemoveAll(rt.Dir)
		}
	})
}

// ProxyURL is the proxy as the agent's environment names it.
func (rt *Runtime) ProxyURL() string {
	return "http://127.0.0.1:" + strconv.Itoa(rt.ProxyPort)
}

// noProxy is what the agent reaches without the proxy: its own loopback
// (inside a network namespace, only the bridged ports answer there).
const noProxy = "localhost,127.0.0.1,::1"

// Env is the environment the sandboxed agent gets: the proxy variables in
// both spellings (curl reads only the lower-case http_proxy), NO_PROXY for
// loopback, Node's opt-in to honour them, and Claude Code's switch for its
// non-essential traffic (telemetry, error reports, update checks), which
// the allowlist would refuse anyway.
func (rt *Runtime) Env() []string {
	u := rt.ProxyURL()
	var out []string
	for _, k := range []string{"HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY"} {
		out = append(out, k+"="+u, strings.ToLower(k)+"="+u)
	}
	return append(out, "NO_PROXY="+noProxy, "no_proxy="+noProxy,
		"NODE_USE_ENV_PROXY=1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1")
}

// UnsetEnv are the variables removed from a sandboxed agent's environment
// when egress is on: the SSH agent socket and the askpass helpers, which
// would hand git credentials to the agent.
var UnsetEnv = []string{"SSH_AUTH_SOCK", "GIT_ASKPASS", "SSH_ASKPASS"}
