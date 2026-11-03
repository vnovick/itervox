package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/apitokens"
)

// daemon is one simulated daemon taking part in the election: its own
// goroutine, its own lock descriptor, its own workflow.
type daemon struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func startDaemon(t *testing.T, cfg Config) *daemon {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	d := &daemon{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(d.done)
		_ = Run(ctx, cfg)
	}()
	t.Cleanup(d.stop)
	return d
}

func (d *daemon) stop() {
	d.cancel()
	<-d.done
}

func electionConfig(t *testing.T, regDir, stateDir string, m *fakeMember, reg *memberList) Config {
	t.Helper()
	return Config{
		RegistryDir: regDir, StateDir: stateDir, Bind: "127.0.0.1", Port: 0,
		Workflow: m.workflow, Version: "test", Members: reg.list, MemberToken: storeTokenFunc(),
		Rescan: 100 * time.Millisecond, FeedPoll: 100 * time.Millisecond, // ElectionPoll: the production default (1s)
	}
}

// hubToken creates a hub token in the hub state dir's store.
func hubToken(t *testing.T, stateDir string, scope apitokens.Scope) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(stateDir, 0o700))
	_, secret, err := apitokens.Open(filepath.Join(stateDir, TokenStoreFileName)).Create("t-"+string(scope), scope, nil, "test")
	require.NoError(t, err)
	return secret
}

func lockOwner(t *testing.T, regDir string) (LockInfo, bool) {
	t.Helper()
	info, ok, err := ReadLockInfo(regDir)
	require.NoError(t, err)
	return info, ok
}

// TestFirstDaemonBecomesHubAndOthersRegister (#228): of three daemons the
// first one started serves the hub (hub.lock names it, with the URL it
// bound); the later ones stay members, and every member, the hub's own
// project included, is listed and proxied at the one hub URL.
func TestFirstDaemonBecomesHubAndOthersRegister(t *testing.T) {
	regDir, stateDir := t.TempDir(), filepath.Join(t.TempDir(), "hub")
	viewer := hubToken(t, stateDir, apitokens.ScopeViewer)
	reg := &memberList{}
	a := newFakeMember(t, "alpha", time.Now().Add(-time.Minute))
	b := newFakeMember(t, "beta", time.Now().Add(-time.Minute))
	c := newFakeMember(t, "gamma", time.Now().Add(-time.Minute))

	reg.add(a.rec)
	startDaemon(t, electionConfig(t, regDir, stateDir, a, reg))
	eventually(t, 5*time.Second, func() bool { _, ok := lockOwner(t, regDir); return ok }, "the first daemon becomes the hub")
	for _, m := range []*fakeMember{b, c} {
		reg.add(m.rec) // each registers (its registry record)
		startDaemon(t, electionConfig(t, regDir, stateDir, m, reg))
	}
	time.Sleep(time.Second) // several election polls: nobody else takes over
	info, ok := lockOwner(t, regDir)
	require.True(t, ok)
	assert.Equal(t, a.workflow, info.Workflow, "the first daemon stays the hub")
	assert.Equal(t, os.Getpid(), info.PID)
	assert.NotZero(t, info.Port)

	var projects ProjectsResponse
	eventually(t, 5*time.Second, func() bool {
		resp := doHub(t, info.URL+"api/v1/projects", http.MethodGet, viewer, nil)
		if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&projects) != nil {
			return false
		}
		ready := 0
		for _, p := range projects.Projects {
			if p.Health == healthOK && p.Proxied {
				ready++
			}
		}
		return ready == 3
	}, "every daemon is a healthy, proxied member")
	var names []string
	for _, p := range projects.Projects {
		names = append(names, p.Name)
		assert.Contains(t, p.Capabilities, "dispatch_pause", "capabilities come from the member's summary")
		assert.Equal(t, "test-"+p.Name, p.Version)
	}
	assert.Equal(t, []string{"alpha", "beta", "gamma"}, names)
	resp := doHub(t, info.URL+"p/gamma/api/v1/summary", http.MethodGet, viewer, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "a non-hub member is reachable through the hub URL")
	assert.Contains(t, readBody(t, resp), `"projectName":"gamma"`)
}

// TestHubFailoverWithinFiveSeconds (#228): when the hub stops — cleanly, or
// killed outright (SIGKILL of a real process) — a live member daemon takes
// over within five seconds.
func TestHubFailoverWithinFiveSeconds(t *testing.T) {
	t.Run("clean stop", func(t *testing.T) {
		regDir, stateDir := t.TempDir(), filepath.Join(t.TempDir(), "hub")
		reg := &memberList{}
		a := newFakeMember(t, "alpha", time.Now())
		b := newFakeMember(t, "beta", time.Now())
		reg.add(a.rec)
		reg.add(b.rec)
		da := startDaemon(t, electionConfig(t, regDir, stateDir, a, reg))
		eventually(t, 5*time.Second, func() bool { i, ok := lockOwner(t, regDir); return ok && i.Workflow == a.workflow }, "alpha is the hub")
		startDaemon(t, electionConfig(t, regDir, stateDir, b, reg))
		stopped := time.Now()
		da.stop()
		eventually(t, 5*time.Second, func() bool { i, ok := lockOwner(t, regDir); return ok && i.Workflow == b.workflow }, "beta takes over")
		t.Logf("clean failover took %s", time.Since(stopped))
	})

	t.Run("hub process killed", func(t *testing.T) {
		if !lockSupported {
			t.Skip("no flock on this platform")
		}
		regDir, stateDir := t.TempDir(), filepath.Join(t.TempDir(), "hub")
		helper := exec.Command(os.Args[0], "-test.run=^TestHubHelperProcess$")
		helper.Env = append(os.Environ(), "ITERVOX_HUB_HELPER=1", "ITERVOX_HUB_HELPER_REG="+regDir, "ITERVOX_HUB_HELPER_STATE="+stateDir)
		require.NoError(t, helper.Start())
		t.Cleanup(func() { _ = helper.Process.Kill(); _ = helper.Wait() })
		eventually(t, 10*time.Second, func() bool { i, ok := lockOwner(t, regDir); return ok && i.PID == helper.Process.Pid }, "the helper process is the hub")

		reg := &memberList{}
		b := newFakeMember(t, "beta", time.Now())
		reg.add(b.rec)
		startDaemon(t, electionConfig(t, regDir, stateDir, b, reg))
		time.Sleep(500 * time.Millisecond)
		i, _ := lockOwner(t, regDir)
		require.Equal(t, helper.Process.Pid, i.PID, "no takeover while the hub lives")

		require.NoError(t, helper.Process.Signal(syscall.SIGKILL))
		killed := time.Now()
		_ = helper.Wait()
		eventually(t, 5*time.Second, func() bool { i, ok := lockOwner(t, regDir); return ok && i.Workflow == b.workflow }, "beta takes over after SIGKILL")
		t.Logf("failover after SIGKILL took %s", time.Since(killed))
	})
}

// TestHubHelperProcess is the hub daemon of TestHubFailoverWithinFiveSeconds,
// run as a separate process; a no-op otherwise.
func TestHubHelperProcess(t *testing.T) {
	if os.Getenv("ITERVOX_HUB_HELPER") != "1" {
		return
	}
	_ = Run(context.Background(), Config{
		RegistryDir: os.Getenv("ITERVOX_HUB_HELPER_REG"), StateDir: os.Getenv("ITERVOX_HUB_HELPER_STATE"),
		Bind: "127.0.0.1", Port: 0, Workflow: "/helper/WORKFLOW.md", ElectionPoll: 200 * time.Millisecond,
	})
}
