package orchestrator

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/egress"
	"github.com/vnovick/itervox/internal/gitexec"
	"github.com/vnovick/itervox/internal/logbuffer"
	"github.com/vnovick/itervox/internal/tracker"
)

// stubEgressMechanism is a sandbox that can close the network over the
// daemon's loopback (sandbox-exec's mode), so a test reaches the run's
// proxy directly. It runs nothing.
type stubEgressMechanism struct{ stubMechanism }

func (stubEgressMechanism) EgressAvailable() error { return nil }
func (stubEgressMechanism) EgressBridge() bool     { return false }

// egressProbeRunner is a fake agent: during its turn it asks the run's
// proxy for two destinations (twice each) and records what it saw.
type egressProbeRunner struct {
	mu       sync.Mutex
	iso      *agent.Isolation
	statuses map[string]string
	logSeen  string
	logBuf   *logbuffer.Buffer
}

func (r *egressProbeRunner) RunTurn(ctx context.Context, _ agent.Logger, _ func(agent.TurnResult), _ *string, _, _, _, _, _ string, _, _ int, _ agent.PermissionMode) (agent.TurnResult, error) {
	iso := agent.IsolationFrom(ctx)
	statuses := map[string]string{}
	if iso.EgressOn() {
		for _, target := range []string{"github.com:443", "denied.test:443", "github.com:443", "denied.test:443"} {
			statuses[target] = connectStatus(iso.Egress.ProxyPort, target)
		}
	}
	r.mu.Lock()
	r.iso, r.statuses = iso, statuses
	if r.logBuf != nil {
		r.logSeen = strings.Join(r.logBuf.Get("ENG-1"), "\n")
	}
	r.mu.Unlock()
	return agent.TurnResult{ResultText: "done", InputTokens: 1, OutputTokens: 1, TotalTokens: 2}, nil
}

func connectStatus(port int, target string) string {
	c, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		return "dial: " + err.Error()
	}
	defer func() { _ = c.Close() }()
	_, _ = fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return "read: " + err.Error()
	}
	if f := strings.Fields(line); len(f) > 1 {
		return f[1]
	}
	return line
}

// TestWorkerEgressRunReportsRefusals: a run of a profile with isolation:
// process and egress: allowlist gets the run's proxy in its sandbox; the
// workspace's push remote is refused even though the allowlist names it;
// the daemon's API port is bridged in; each refused destination is written
// to the run log and recorded once as an egress_refused feed event (host
// and port, never a URL); the push: agent combination is flagged; and the
// proxy is stopped when the run ends.
func TestWorkerEgressRunReportsRefusals(t *testing.T) {
	for _, k := range config.ModelServerEnvVars {
		t.Setenv(k, "") // the daemon's own model server would join the allowlist
	}
	ws := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "https://x:" + "tok@GitHub.com/org/repo.git"}} {
		out, err := gitexec.Command(context.Background(), ws, args...).CombinedOutput()
		require.NoError(t, err, "%s", out)
	}
	cfg := automationBaseCfg()
	cfg.Agent.MaxTurns = 1
	off := false
	cfg.Agent.Profiles["sandboxed"] = config.AgentProfile{Command: "claude", Isolation: config.IsolationProcess,
		Egress: config.EgressAllowlist, EgressAllow: []string{"github.com"}, EgressDefaults: &off}
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "T", State: "In Progress"}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	runner := &egressProbeRunner{}
	o := New(cfg, mt, runner, &stubWorkspaceProvider{path: ws})
	o.logBuf = logbuffer.New()
	runner.logBuf = o.logBuf
	o.agentActionBaseURL = "http://127.0.0.1:8090"
	o.isolationDetect = func() (agent.IsolationMechanism, error) { return stubEgressMechanism{}, nil }
	o.events = make(chan OrchestratorEvent, 64)
	o.workersWg.Add(issue.Identifier)
	o.runWorker(t.Context(), issue, 0, "", "claude", "claude", "sandboxed", true, nil, nil, nil)
	var events []OrchestratorEvent
	for len(o.events) > 0 {
		events = append(events, <-o.events)
	}

	runner.mu.Lock()
	iso, statuses, log := runner.iso, runner.statuses, runner.logSeen
	runner.mu.Unlock()
	require.True(t, iso.EgressOn(), "the turn runs with the run's egress proxy")
	assert.Equal(t, []int{8090}, iso.Egress.LocalPorts, "the daemon's API port is bridged in")
	assert.Equal(t, "403", statuses["github.com:443"], "the workspace's push remote is refused even when allowed")
	assert.Equal(t, "403", statuses["denied.test:443"])
	assert.Contains(t, log, "network: egress allowlist via proxy (github.com)")
	assert.Contains(t, log, "set push: daemon", "push: agent with egress is flagged")
	assert.Contains(t, log, "worker: egress refused: github.com:443 (push_remote)")
	assert.Contains(t, log, "worker: egress refused: denied.test:443 (not_allowed)")
	assert.Equal(t, 1, strings.Count(log, "egress refused: denied.test:443"), "one log line per destination")
	assert.NotContains(t, log, "tok@", "no remote URL with its credential reaches the log")

	var feed []FeedEvent
	for _, ev := range events {
		if ev.Type == EventFeedRecorded && ev.feed != nil && ev.feed.Kind == FeedEgressRefused {
			feed = append(feed, *ev.feed)
		}
	}
	require.Len(t, feed, 2, "one feed event per refused destination per run")
	assert.Equal(t, "ENG-1", feed[0].Identifier)
	assert.Equal(t, FeedPayload{RunID: feed[0].Payload.RunID, Kind: egress.ReasonPushRemote, Reason: "github.com:443"}, feed[0].Payload)
	assert.Equal(t, "denied.test:443", feed[1].Payload.Reason)

	// The proxy is gone with the run.
	assert.True(t, strings.HasPrefix(connectStatus(iso.Egress.ProxyPort, "github.com:443"), "dial:"),
		"the run's proxy stops when the run ends")
}

// TestWorkerEgressOffLeavesNetworkOpen: without egress the run's sandbox
// has no proxy.
func TestWorkerEgressOffLeavesNetworkOpen(t *testing.T) {
	runner, _, _ := runIsolatedWorker(t, "", func() (agent.IsolationMechanism, error) { return stubEgressMechanism{}, nil })
	calls := runner.calls()
	require.NotEmpty(t, calls)
	assert.False(t, calls[0].EgressOn())
	assert.Contains(t, calls[0].Describe(), "network: allowed")
}

func TestWorkspaceRemoteHosts(t *testing.T) {
	ws := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "git@github.com:org/r.git"},
		{"remote", "add", "fork", "https://gitlab.example.com/me/r.git"},
		{"config", "remote.origin.pushurl", "ssh://git@push.example.org/r.git"}, {"remote", "add", "local", "/srv/r.git"}} {
		out, err := gitexec.Command(context.Background(), ws, args...).CombinedOutput()
		require.NoError(t, err, "%s", out)
	}
	got := workspaceRemoteHosts(t.Context(), ws)
	assert.ElementsMatch(t, []string{"github.com", "gitlab.example.com", "push.example.org"}, got)
	assert.Empty(t, workspaceRemoteHosts(t.Context(), ""))
}
