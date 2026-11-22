package egress

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/metrics"
)

func TestParseRule(t *testing.T) {
	ok := map[string]Rule{
		"registry.npmjs.org":     {Host: "registry.npmjs.org"},
		" API.Anthropic.com. ":   {Host: "api.anthropic.com"},
		"example.com:8443":       {Host: "example.com", Port: 8443},
		"*.pythonhosted.org":     {Host: "pythonhosted.org", Wildcard: true},
		"*.pythonhosted.org:443": {Host: "pythonhosted.org", Wildcard: true, Port: 443},
		"10.0.0.5":               {Host: "10.0.0.5"},
		"10.0.0.5:5000":          {Host: "10.0.0.5", Port: 5000},
		"[::1]:8080":             {Host: "::1", Port: 8080},
		"fd00::1":                {Host: "fd00::1"},
	}
	for in, want := range ok {
		got, err := ParseRule(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	for _, bad := range []string{"", "*", "*.com", "https://example.com", "example.com/path",
		"user@example.com", "example.com:0", "example.com:99999", "example.com:x", "*.10.0.0.1", "ex ample.com", "-bad.com"} {
		_, err := ParseRule(bad)
		assert.Error(t, err, "%q must be refused", bad)
	}
}

func TestAllowlistAllows(t *testing.T) {
	a, err := NewAllowlist([]string{"registry.npmjs.org", "*.example.com", "git.example.org:8443", "github.com"},
		[]string{"GitHub.com"})
	require.NoError(t, err)
	cases := []struct {
		host   string
		port   int
		ok     bool
		reason string
	}{
		{"registry.npmjs.org", 443, true, ""},
		{"REGISTRY.npmjs.org.", 80, true, ""},
		{"registry.npmjs.org", 22, false, ReasonNotAllowed},
		{"a.example.com", 443, true, ""},
		{"a.b.example.com", 443, true, ""},
		{"example.com", 443, false, ReasonNotAllowed},
		{"evil-example.com", 443, false, ReasonNotAllowed},
		{"git.example.org", 8443, true, ""},
		{"git.example.org", 443, false, ReasonNotAllowed},
		{"github.com", 443, false, ReasonPushRemote}, // deny wins over allow
		{"github.com", 22, false, ReasonPushRemote},
		{"api.openai.com", 443, false, ReasonNotAllowed},
		{"", 443, false, ReasonBadRequest},
	}
	for _, c := range cases {
		ok, reason := a.Allows(c.host, c.port)
		assert.Equal(t, c.ok, ok, "%s:%d", c.host, c.port)
		assert.Equal(t, c.reason, reason, "%s:%d", c.host, c.port)
	}
}

func TestHostOfRemote(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/org/repo.git":    "github.com",
		"https://x:tok@GitHub.com/org/r.git": "github.com",
		"ssh://git@gitlab.example.com:22/r":  "gitlab.example.com",
		"git@github.com:org/repo.git":        "github.com",
		"/srv/git/repo.git":                  "",
		"file:///srv/git/repo.git":           "",
		"../repo.git":                        "",
	} {
		assert.Equal(t, want, HostOfRemote(in), in)
	}
}

// testUpstream maps every host the test dials to srv.
func testUpstream(srv *httptest.Server) DialFunc {
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, srv.Listener.Addr().String())
	}
}

type decisions struct {
	mu  sync.Mutex
	all []Decision
}

func (d *decisions) add(x Decision) {
	d.mu.Lock()
	d.all = append(d.all, x)
	d.mu.Unlock()
}

func (d *decisions) list() []Decision {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]Decision(nil), d.all...)
}

func proxyClient(t *testing.T, rt *Runtime, tlsCfg *tls.Config) *http.Client {
	t.Helper()
	pu, err := url.Parse(rt.ProxyURL())
	require.NoError(t, err)
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu), TLSClientConfig: tlsCfg}}
}

// TestProxyAllowsOnlyAllowlistedHosts: the proxy forwards plain HTTP and
// tunnels HTTPS to an allowlisted host, refuses (403) every other host,
// the push remote and git's ssh port, records each decision without the
// URL's path or query, and counts them.
func TestProxyAllowsOnlyAllowlistedHosts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "upstream ok")
	}))
	defer srv.Close()
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "tls ok")
	}))
	defer tlsSrv.Close()

	var got decisions
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(prev)

	allowedBefore := metrics.EgressDecisionCount(metrics.EgressAllowed)
	refusedBefore := metrics.EgressDecisionCount(metrics.EgressRefused)
	plain := testUpstream(srv)
	tlsDial := testUpstream(tlsSrv)
	rt, err := Start(Config{
		Allow:      []string{"allowed.test", "example.com"},
		Deny:       []string{"git.push.test"},
		Label:      "ENG-1",
		OnDecision: got.add,
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if strings.HasPrefix(addr, "example.com:") {
				return tlsDial(ctx, network, addr)
			}
			return plain(ctx, network, addr)
		},
	})
	require.NoError(t, err)
	defer rt.Close()

	c := proxyClient(t, rt, &tls.Config{RootCAs: tlsSrv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs})
	resp, err := c.Get("http://allowed.test/secret-path?token=sekret-value")
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "upstream ok", string(body))

	resp, err = c.Get("https://example.com/")
	require.NoError(t, err, "CONNECT to an allowlisted host tunnels")
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Equal(t, "tls ok", string(body))

	resp, err = c.Get("http://denied.test/")
	require.NoError(t, err)
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Equal(t, "refused", resp.Header.Get("X-Itervox-Egress"))
	assert.Contains(t, string(body), "denied.test:80 refused")

	_, err = c.Get("https://denied.test/")
	assert.Error(t, err, "CONNECT to a host off the allowlist is refused")

	assert.Equal(t, "403", rawConnect(t, rt, "github.com:22"), "git over ssh is refused")
	assert.Equal(t, "403", rawConnect(t, rt, "git.push.test:443"), "the push remote is refused")
	assert.Equal(t, "403", rawConnect(t, rt, "allowed.test:22"), "an allowed host on another port is refused")

	ds := got.list()
	require.Len(t, ds, 7)
	assert.Equal(t, Decision{Host: "allowed.test", Port: 80, Allowed: true, Method: "GET"}, ds[0])
	assert.Equal(t, Decision{Host: "example.com", Port: 443, Allowed: true, Method: "CONNECT"}, ds[1])
	assert.Equal(t, Decision{Host: "denied.test", Port: 80, Reason: ReasonNotAllowed, Method: "GET"}, ds[2])
	assert.Equal(t, ReasonNotAllowed, ds[4].Reason)
	assert.Equal(t, Decision{Host: "git.push.test", Port: 443, Reason: ReasonPushRemote, Method: "CONNECT"}, ds[5])
	assert.Equal(t, uint64(2), metrics.EgressDecisionCount(metrics.EgressAllowed)-allowedBefore)
	assert.Equal(t, uint64(5), metrics.EgressDecisionCount(metrics.EgressRefused)-refusedBefore)

	out := logs.String()
	assert.Contains(t, out, "egress: refused")
	assert.Contains(t, out, "host=denied.test")
	assert.NotContains(t, out, "secret-path", "a decision never logs the URL path")
	assert.NotContains(t, out, "sekret-value", "a decision never logs the query")
}

// rawConnect sends one CONNECT to the proxy and returns the status code.
func rawConnect(t *testing.T, rt *Runtime, target string) string {
	t.Helper()
	conn, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(rt.ProxyPort))
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	_, err = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	require.NoError(t, err)
	line, err := bufio.NewReader(conn).ReadString('\n')
	require.NoError(t, err)
	f := strings.Fields(line)
	require.GreaterOrEqual(t, len(f), 2)
	return f[1]
}

// TestProxyRefusesLocalDestinations: without an injected dialer the proxy
// never connects to a loopback address, even when the allowlist names it.
func TestProxyRefusesLocalDestinations(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "local service")
	}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	var got decisions
	rt, err := Start(Config{Allow: []string{"127.0.0.1:" + strconv.Itoa(port), "localhost:" + strconv.Itoa(port)}, OnDecision: got.add})
	require.NoError(t, err)
	defer rt.Close()
	assert.Equal(t, "403", rawConnect(t, rt, "127.0.0.1:"+strconv.Itoa(port)))
	assert.Equal(t, "403", rawConnect(t, rt, "localhost:"+strconv.Itoa(port)))
	ds := got.list()
	require.NotEmpty(t, ds)
	assert.Equal(t, ReasonLocalAddress, ds[len(ds)-1].Reason)
}

// TestBridgeModeServesUnixSockets: in bridge mode the proxy and the local
// port relays listen on Unix sockets in a private run directory, which
// Close removes.
func TestBridgeModeServesUnixSockets(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "daemon api")
	}))
	defer local.Close()
	localPort := local.Listener.Addr().(*net.TCPAddr).Port
	parent := filepath.Join(shortTempDir(t), "eg")
	rt, err := Start(Config{Allow: []string{"allowed.test"}, LocalPorts: []int{localPort}, Bridge: true,
		ParentDir: parent, Dial: testUpstream(local)})
	require.NoError(t, err)
	require.Len(t, rt.Bridges, 2)
	assert.Equal(t, DefaultSandboxProxyPort, rt.Bridges[0].Port)
	assert.Equal(t, localPort, rt.Bridges[1].Port)
	fi, err := os.Stat(rt.ParentDir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), fi.Mode().Perm())

	unixClient := func(sock string) *http.Client {
		return &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		}}}
	}
	resp, err := unixClient(rt.Bridges[1].Socket).Get("http://127.0.0.1/")
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	assert.Equal(t, "daemon api", string(body), "the local port relay reaches the daemon's loopback")

	pc := &http.Client{Transport: &http.Transport{
		Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: "proxy"}),
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", rt.Bridges[0].Socket)
		}}}
	resp, err = pc.Get("http://allowed.test/")
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp, err = pc.Get("http://other.test/")
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)

	rt.Close()
	assert.NoDirExists(t, rt.Dir)
	rt.Close() // idempotent
}

func TestSandboxProxyPortAvoidsLocalPorts(t *testing.T) {
	assert.Equal(t, 3128, sandboxProxyPort(nil))
	assert.Equal(t, 3130, sandboxProxyPort([]int{3128, 3129, 8090}))
}

func TestForwarderArgsRoundTrip(t *testing.T) {
	bridges := []Bridge{{Port: 3128, Socket: "/r/proxy.sock"}, {Port: 8090, Socket: "/r/port-8090.sock"}}
	argv := ForwarderArgv("/bin/itervox", bridges, []string{"/bin/sh", "-c", "echo --"})
	assert.Equal(t, []string{"/bin/itervox", ForwardEntrypoint, "--listen", "3128=/r/proxy.sock",
		"--listen", "8090=/r/port-8090.sock", "--", "/bin/sh", "-c", "echo --"}, argv)
	got, serve, rest, err := parseForwardArgs(argv[2:])
	require.NoError(t, err)
	assert.False(t, serve)
	assert.Equal(t, bridges, got)
	assert.Equal(t, []string{"/bin/sh", "-c", "echo --"}, rest)
	_, _, _, err = parseForwardArgs([]string{"--listen", "x=/s"})
	assert.Error(t, err)
}

func TestEnvNamesTheProxy(t *testing.T) {
	rt := &Runtime{ProxyPort: 3128}
	env := rt.Env()
	for _, want := range []string{"HTTPS_PROXY=http://127.0.0.1:3128", "https_proxy=http://127.0.0.1:3128",
		"http_proxy=http://127.0.0.1:3128", "NO_PROXY=localhost,127.0.0.1,::1"} {
		assert.Contains(t, env, want)
	}
}

// shortTempDir is a temp dir with a short path: a Unix socket path is
// limited to ~104 bytes, which t.TempDir's long names can exceed.
func shortTempDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "eg")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}
