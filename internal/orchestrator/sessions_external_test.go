package orchestrator_test

// #178 step 3 against a real orchestrator loop: sessions started by hand
// are listed read-only, "running" (with the reason) or "exited", and
// operator messages to them are refused.

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/vnovick/itervox/internal/extsessions"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
)

// fakeExternalSource serves a fixed discovery list; running marks the
// sessions whose CLI may still run (id -> reason).
type fakeExternalSource struct {
	mu       sync.Mutex
	sessions []extsessions.Session
	running  map[string]string
}

func (f *fakeExternalSource) Discover(time.Time) (orchestrator.ExternalScan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]extsessions.Session(nil), f.sessions...)
	for i := range out {
		if reason, ok := f.running[out[i].ID]; ok {
			out[i].Running, out[i].RunningReason = true, reason
		}
	}
	return orchestrator.ExternalScan{Sessions: out}, nil
}

func extSession(id, backend, cwd string) extsessions.Session {
	return extsessions.Session{ID: id, Backend: backend, Cwd: cwd, Path: filepath.Join(cwd, id+".jsonl"),
		Title: "hand " + id, UpdatedAt: time.Now().Add(-time.Hour)}
}

func sessionByID(o *orchestrator.Orchestrator, id string) (orchestrator.AgentSession, bool) {
	for _, s := range orchestrator.Sessions(o.Snapshot(), false) {
		if s.ID == id {
			return s, true
		}
	}
	return orchestrator.AgentSession{}, false
}

func externalLoop(t *testing.T, src *fakeExternalSource) *orchestrator.Orchestrator {
	t.Helper()
	cfg := sessionsConfig()
	cfg.Agent.Command = "claude"
	cfg.Agent.ReadTimeoutMs, cfg.Agent.TurnTimeoutMs = 30000, 60000
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	o := orchestrator.New(cfg, mt, nil, nil)
	o.SetExternalSessionSource(src, 20*time.Millisecond)
	startLoop(t, o)
	return o
}

// Sessions started by hand appear read-only (owner external), with their
// directory; one still running says why; messaging one is refused.
func TestExternalSessionsListedReadOnly(t *testing.T) {
	src := &fakeExternalSource{
		sessions: []extsessions.Session{extSession("ext-exited", "claude", "/work/app"), extSession("ext-busy", "codex", "/work/app/web")},
		running:  map[string]string{"ext-busy": "codex process 42 has the transcript open"},
	}
	o := externalLoop(t, src)
	waitFor(t, "discovery", 2*time.Second, func() bool {
		_, ok := sessionByID(o, "ext-busy")
		return ok
	})
	exited, _ := sessionByID(o, "ext-exited")
	busy, _ := sessionByID(o, "ext-busy")
	assert.Equal(t, orchestrator.SessionOwnerExternal, exited.Owner)
	assert.Equal(t, orchestrator.SessionStatusExited, exited.Status)
	assert.Equal(t, "/work/app", exited.Cwd)
	assert.Equal(t, orchestrator.SessionOwnerExternal, busy.Owner)
	assert.Equal(t, orchestrator.SessionStatusRunning, busy.Status)
	assert.Contains(t, busy.InUseReason, "transcript open")
	assert.Empty(t, exited.Steering, "a session itervox does not own takes no message")

	_, err := o.SendSessionMessage("ext-exited", "hello")
	assert.ErrorIs(t, err, orchestrator.ErrSessionNotOwned)
}

// countingExternalSource counts discovery passes over a fakeExternalSource.
type countingExternalSource struct {
	*fakeExternalSource
	passes atomic.Int64
}

func (c *countingExternalSource) Discover(now time.Time) (orchestrator.ExternalScan, error) {
	defer c.passes.Add(1)
	return c.fakeExternalSource.Discover(now)
}

// TestDiscoveryNotifiesOnlyWhenChanged (#210): a discovery pass that finds
// what the previous one found does not store a snapshot or wake the
// dashboards; one that finds a new session does.
func TestDiscoveryNotifiesOnlyWhenChanged(t *testing.T) {
	src := &countingExternalSource{fakeExternalSource: &fakeExternalSource{
		sessions: []extsessions.Session{extSession("ext-1", "claude", "/work/app")},
	}}
	cfg := sessionsConfig()
	cfg.Agent.Command = "claude"
	cfg.Polling.IntervalMs = int((time.Hour).Milliseconds()) // only the startup tick
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	o := orchestrator.New(cfg, mt, nil, nil)
	var notifies atomic.Int64
	o.OnStateChange = func() { notifies.Add(1) }
	o.SetExternalSessionSource(src, 10*time.Millisecond)
	startLoop(t, o)

	waitFor(t, "first discovery", 2*time.Second, func() bool {
		_, ok := sessionByID(o, "ext-1")
		return ok
	})
	time.Sleep(50 * time.Millisecond) // let the startup tick settle
	n0, p0 := notifies.Load(), src.passes.Load()
	waitFor(t, "unchanged passes", 3*time.Second, func() bool { return src.passes.Load() >= p0+10 })
	assert.Equal(t, n0, notifies.Load(), "unchanged discovery passes must not notify")

	src.mu.Lock()
	src.sessions = append(src.sessions, extSession("ext-2", "codex", "/work/app"))
	src.mu.Unlock()
	waitFor(t, "changed pass", 2*time.Second, func() bool {
		_, ok := sessionByID(o, "ext-2")
		return ok
	})
	assert.Greater(t, notifies.Load(), n0, "a pass that found a new session notifies")
}
