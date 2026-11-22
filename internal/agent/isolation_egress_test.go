package agent

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/egress"
	"github.com/vnovick/itervox/internal/gitexec"
)

// fakeMechanism is an available sandbox that wraps nothing and cannot
// close the network.
type fakeMechanism struct{ name string }

func (f fakeMechanism) Name() string   { return f.name }
func (fakeMechanism) Available() error { return nil }
func (fakeMechanism) Wrap(argv []string, _ IsolationPolicy) ([]string, error) {
	return argv, nil
}

// fakeEgressMechanism is a sandbox that can close the network (bridge
// mode like bubblewrap) without running anything.
type fakeEgressMechanism struct {
	fakeMechanism
	egressErr error
}

func (f fakeEgressMechanism) EgressAvailable() error { return f.egressErr }
func (fakeEgressMechanism) EgressBridge() bool       { return true }

func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "ieg")
	require.NoError(t, err)
	d, err = filepath.EvalSymlinks(d)
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

// TestEgressRefusedWithoutAnEgressMechanism: a mechanism that cannot
// restrict network by host (Landlock) never runs an egress run, and a
// mechanism whose namespaces fail here does not either: fail closed.
func TestEgressRefusedWithoutAnEgressMechanism(t *testing.T) {
	in := IsolationInputs{Egress: &EgressInputs{Allow: []string{"example.com"}, ParentDir: shortDir(t)}}
	_, err := PrepareIsolation("process", in, func() (IsolationMechanism, error) {
		return fakeMechanism{name: "landlock"}, nil
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrIsolationUnavailable))
	assert.Contains(t, err.Error(), "landlock cannot restrict network connections by host")

	_, err = PrepareIsolation("process", in, func() (IsolationMechanism, error) {
		return fakeEgressMechanism{fakeMechanism: fakeMechanism{name: "bubblewrap"}, egressErr: errors.New("no userns")}, nil
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrIsolationUnavailable))
	assert.Contains(t, err.Error(), "no userns")
}

// TestEgressPolicyHidesCredentialsAndBridges: with egress on, the policy
// denies the git credential stores, the credential-cache socket and the
// SSH agent socket, hides every run's egress directory but this run's,
// and the run environment names the proxy and drops SSH_AUTH_SOCK.
func TestEgressPolicyHidesCredentialsAndBridges(t *testing.T) {
	home := shortDir(t)
	for _, rel := range []string{".git-credentials", ".config/glab-cli/config.yml", ".cache/git/credential/socket"} {
		p := filepath.Join(home, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o600))
	}
	agentSock := filepath.Join(home, "agent.sock")
	require.NoError(t, os.WriteFile(agentSock, nil, 0o600))
	t.Setenv("SSH_AUTH_SOCK", agentSock)
	parent := filepath.Join(shortDir(t), "eg")

	iso, err := PrepareIsolation("process", IsolationInputs{Home: home, Egress: &EgressInputs{
		Allow: []string{"example.com"}, LocalPorts: []int{8090}, ParentDir: parent,
	}}, func() (IsolationMechanism, error) {
		return fakeEgressMechanism{fakeMechanism: fakeMechanism{name: "bubblewrap"}}, nil
	})
	require.NoError(t, err)
	defer iso.Close()
	require.True(t, iso.EgressOn())
	for _, want := range []string{filepath.Join(home, ".git-credentials"), filepath.Join(home, ".config/glab-cli"),
		filepath.Join(home, ".cache/git/credential"), agentSock, iso.Egress.ParentDir} {
		assert.Contains(t, iso.Policy.Deny, want)
	}
	assert.Contains(t, iso.Policy.Write, iso.Egress.Dir, "the run's own sockets stay reachable")
	require.NotNil(t, iso.Policy.Egress)
	assert.Equal(t, egress.DefaultSandboxProxyPort, iso.Policy.Egress.ProxyPort)
	require.Len(t, iso.Policy.Egress.Bridges, 2)
	assert.Equal(t, 8090, iso.Policy.Egress.Bridges[1].Port)
	assert.NotEmpty(t, iso.Policy.Egress.Forwarder)
	assert.Contains(t, iso.Describe(), "network: egress allowlist via proxy (example.com); local ports: 8090")

	env := turnRunEnv(RunEnv{Vars: []string{"A=1"}, Unset: []string{"GH_TOKEN"}}, iso)
	assert.Contains(t, env.Vars, "A=1")
	assert.Contains(t, env.Vars, "HTTPS_PROXY=http://127.0.0.1:3128")
	assert.Contains(t, env.Unset, "GH_TOKEN")
	assert.Contains(t, env.Unset, "SSH_AUTH_SOCK")
	stripped := env.strip([]string{"SSH_AUTH_SOCK=" + agentSock, "HOME=" + home})
	assert.Equal(t, []string{"HOME=" + home}, stripped)

	dir := iso.Egress.Dir
	iso.Close()
	assert.NoDirExists(t, dir, "Close removes the run's sockets")
	iso.Close()

	// Without egress nothing changes.
	assert.Equal(t, RunEnv{Vars: []string{"A=1"}}, turnRunEnv(RunEnv{Vars: []string{"A=1"}}, nil))
}

// TestBubblewrapEgressArgs: an egress policy adds the network and pid
// namespaces and puts the forwarder first inside the sandbox.
func TestBubblewrapEgressArgs(t *testing.T) {
	b := bubblewrap{path: "/usr/bin/bwrap"}
	p := IsolationPolicy{Egress: &EgressPolicy{ProxyPort: 3128, Forwarder: "/opt/itervox",
		Bridges: []egress.Bridge{{Port: 3128, Socket: "/r/proxy.sock"}}}}
	argv, err := b.Wrap([]string{"/bin/sh", "-c", "true"}, p)
	require.NoError(t, err)
	joined := strings.Join(argv, " ")
	assert.Contains(t, joined, "--unshare-net --unshare-pid -- /opt/itervox "+egress.ForwardEntrypoint+" --listen 3128=/r/proxy.sock -- /bin/sh -c true")
	plain, err := b.Wrap([]string{"/bin/sh"}, IsolationPolicy{})
	require.NoError(t, err)
	assert.NotContains(t, strings.Join(plain, " "), "--unshare-net", "without egress the network is unchanged")

	_, err = b.Wrap([]string{"/bin/sh"}, IsolationPolicy{Egress: &EgressPolicy{}})
	assert.Error(t, err, "no forwarder: refuse rather than run with an open network")
}

// TestSandboxExecEgressProfile: an egress policy denies network* and
// allows outbound connections only to the proxy and the local ports.
func TestSandboxExecEgressProfile(t *testing.T) {
	prof := sandboxExecProfile(IsolationPolicy{Egress: &EgressPolicy{ProxyPort: 50123, LocalPorts: []int{8090}}})
	assert.Contains(t, prof, "(deny network*)")
	assert.Contains(t, prof, `(allow network-outbound (remote ip "localhost:50123"))`)
	assert.Contains(t, prof, `(allow network-outbound (remote ip "localhost:8090"))`)
	assert.NotContains(t, prof, `(remote ip "localhost:*")`, "no blanket loopback outbound")
	assert.NotContains(t, sandboxExecProfile(IsolationPolicy{}), "network", "without egress the network is unchanged")
}

// egressFixture is the real-process egress setup: an upstream named
// allowed.test (reached through the proxy's injected dialer), a fake daemon
// API on the host's loopback, a bare repository outside the workspace and
// a planted git credential.
type egressFixture struct {
	isolationFixture
	upstream, daemonAPI *httptest.Server
	bare, repo, creds   string
	mu                  sync.Mutex
	decisions           []egress.Decision
}

func newEgressFixture(t *testing.T) *egressFixture {
	t.Helper()
	f := &egressFixture{isolationFixture: newIsolationFixture(t)}
	f.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "allowed-body")
	}))
	t.Cleanup(f.upstream.Close)
	f.daemonAPI = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "daemon-api")
	}))
	t.Cleanup(f.daemonAPI.Close)
	f.creds = filepath.Join(f.home, ".git-credentials")
	require.NoError(t, os.WriteFile(f.creds, []byte("https://user:"+plantedSecret+"@git.push.test\n"), 0o600))

	f.bare = filepath.Join(f.sibling, "remote.git")
	f.repo = filepath.Join(f.ws, "repo")
	git := func(dir string, args ...string) {
		out, err := gitexec.Command(context.Background(), dir, args...).CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	require.NoError(t, os.MkdirAll(f.repo, 0o755))
	git(f.sibling, "init", "-q", "--bare", f.bare)
	git(f.repo, "init", "-q")
	require.NoError(t, os.WriteFile(filepath.Join(f.repo, "f.txt"), []byte("x"), 0o644))
	git(f.repo, "add", "f.txt")
	git(f.repo, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "-m", "c")
	// Plain HOME: the sandboxed git must not read the developer's config.
	return f
}

func (f *egressFixture) onDecision(d egress.Decision) {
	f.mu.Lock()
	f.decisions = append(f.decisions, d)
	f.mu.Unlock()
}

func (f *egressFixture) refused() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, d := range f.decisions {
		if !d.Allowed {
			out = append(out, d.Target())
		}
	}
	return out
}

// runEgress runs `sh -c script` in iso the way a runner does: wrapped, with
// the turn's run environment applied.
func runEgress(t *testing.T, iso *Isolation, home, script string) (string, error) {
	t.Helper()
	argv, err := iso.wrap([]string{"/bin/sh", "-c", script})
	require.NoError(t, err)
	cmd := exec.Command(argv[0], argv[1:]...)
	env := turnRunEnv(RunEnv{}, iso)
	base := append(os.Environ(), "HOME="+home, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	cmd.Env = env.strip(append(base, env.direct()...))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestEgressRealProcessBubblewrap is the Linux real-process acceptance for
// agent.egress: allowlist. A fake agent (sh + curl + git) inside the real
// bubblewrap sandbox reaches the allowlisted host through the proxy and the
// daemon's API port through the bridge, and nothing else: a host off the
// allowlist is refused by the proxy, a direct TCP connect has no route, a
// git push to a bare repository (file URL), over https to the push remote,
// and over ssh all fail, and ~/.ssh and ~/.git-credentials are unreadable.
// The same direct connect succeeds in the sandbox without egress, so the
// test tells the two apart. Skipped when bwrap, its network/pid namespaces,
// curl or git are unavailable here.
func TestEgressRealProcessBubblewrap(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("bubblewrap is Linux only")
	}
	b := newBubblewrap().(bubblewrap)
	if err := b.Available(); err != nil {
		t.Skipf("bubblewrap unavailable: %v", err)
	}
	if err := b.EgressAvailable(); err != nil {
		t.Skipf("bubblewrap cannot create network/pid namespaces here: %v", err)
	}
	for _, tool := range []string{"curl", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	f := newEgressFixture(t)
	upstreamAddr := f.upstream.Listener.Addr().String()
	apiPort := f.daemonAPI.Listener.Addr().(*net.TCPAddr).Port
	dialUpstream := func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, upstreamAddr)
	}
	detect := func() (IsolationMechanism, error) { return b, nil }
	in := IsolationInputs{Workspace: f.ws, WorkspaceRoot: filepath.Dir(f.ws), Home: f.home, TempDir: f.tmp}

	// Control: without egress the sandbox reaches the host's loopback.
	open, err := PrepareIsolation("process", in, detect)
	require.NoError(t, err)
	out, err := runEgress(t, open, f.home, "curl -sS -m 5 --noproxy '*' http://"+upstreamAddr+"/")
	require.NoError(t, err, "without egress a direct connect works: %s", out)
	assert.Equal(t, "allowed-body", out)

	in.Egress = &EgressInputs{Allow: []string{"allowed.test"}, Deny: []string{"git.push.test"},
		LocalPorts: []int{apiPort}, Label: "ENG-1", OnDecision: f.onDecision, Dial: dialUpstream,
		ParentDir: filepath.Join(shortDir(t), "eg")}
	iso, err := PrepareIsolation("process", in, detect)
	require.NoError(t, err)
	defer iso.Close()

	out, err = runEgress(t, iso, f.home, "curl -sS -m 10 http://allowed.test/")
	require.NoError(t, err, "the allowlisted host is reachable through the proxy: %s", out)
	assert.Equal(t, "allowed-body", out)

	out, err = runEgress(t, iso, f.home, "curl -sS -m 10 --noproxy '*' http://127.0.0.1:"+strconv.Itoa(apiPort)+"/")
	require.NoError(t, err, "the daemon's API port is bridged in: %s", out)
	assert.Equal(t, "daemon-api", out)

	out, err = runEgress(t, iso, f.home, "curl -sS -m 10 -o /dev/null -w '%{http_code}' http://denied.test/")
	require.NoError(t, err, out)
	assert.Equal(t, "403", out, "a host off the allowlist is refused by the proxy")
	out, err = runEgress(t, iso, f.home, "curl -sS -m 10 https://denied.test/")
	assert.Error(t, err, "CONNECT to a host off the allowlist is refused: %s", out)

	out, err = runEgress(t, iso, f.home, "curl -sS -m 5 --noproxy '*' http://"+upstreamAddr+"/")
	assert.Error(t, err, "a direct TCP connect to the host's loopback has no route: %s", out)
	assert.NotContains(t, out, "allowed-body")
	out, err = runEgress(t, iso, f.home, "curl -sS -m 5 --noproxy '*' http://192.0.2.1/")
	assert.Error(t, err, "a direct TCP connect off the machine has no route: %s", out)

	out, err = runEgress(t, iso, f.home, "cd "+ShellQuote(f.repo)+" && git push "+ShellQuote("file://"+f.bare)+" HEAD:refs/heads/leak")
	assert.Error(t, err, "a push to a bare repository outside the workspace is impossible: %s", out)
	refs, gerr := gitexec.Command(context.Background(), f.bare, "for-each-ref").Output()
	require.NoError(t, gerr)
	assert.Empty(t, strings.TrimSpace(string(refs)), "nothing reached the bare repository")
	out, err = runEgress(t, iso, f.home, "cd "+ShellQuote(f.repo)+" && git push https://git.push.test/org/repo.git HEAD:refs/heads/leak")
	assert.Error(t, err, "a push to the push remote over https is refused: %s", out)
	out, err = runEgress(t, iso, f.home, "cd "+ShellQuote(f.repo)+" && git push ssh://git@git.push.test/org/repo.git HEAD:refs/heads/leak")
	assert.Error(t, err, "a push over ssh has no route: %s", out)

	out, err = runEgress(t, iso, f.home, "cat "+ShellQuote(f.sshKey))
	assert.Error(t, err, "~/.ssh is unreadable")
	assert.NotContains(t, out, plantedSecret)
	out, err = runEgress(t, iso, f.home, "cat "+ShellQuote(f.creds))
	assert.Error(t, err, "~/.git-credentials is unreadable")
	assert.NotContains(t, out, plantedSecret)

	t.Setenv("SSH_AUTH_SOCK", filepath.Join(f.tmp, "agent.sock"))
	out, err = runEgress(t, iso, f.home, `echo "sock=${SSH_AUTH_SOCK:-}"`)
	require.NoError(t, err, out)
	assert.Equal(t, "sock=\n", out, "the SSH agent socket is not handed to the agent")

	refused := f.refused()
	assert.Contains(t, refused, "denied.test:80")
	assert.Contains(t, refused, "denied.test:443")
	assert.Contains(t, refused, "git.push.test:443", "the push remote is refused even through the proxy")
}
