package orchestrator_test

// #178 step 3 against a real orchestrator loop: sessions started by hand
// are listed read-only, "running" (with the reason) or "exited", and
// operator messages to them are refused.

import (
	"path/filepath"
	"sync"
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

func (f *fakeExternalSource) Discover(time.Time) ([]extsessions.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]extsessions.Session(nil), f.sessions...)
	for i := range out {
		if reason, ok := f.running[out[i].ID]; ok {
			out[i].Running, out[i].RunningReason = true, reason
		}
	}
	return out, nil
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
