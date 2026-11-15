package orchestrator

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"time"

	"github.com/vnovick/itervox/internal/extsessions"
	"github.com/vnovick/itervox/internal/metrics"
)

// Sessions panel, tmux sessions itervox started ("New session", #178).
//
// cmd/itervox runs tmux (internal/tmuxctl) off the event loop; the loop
// only records ownership in State.TmuxSessions:
//
//   - ReserveTmuxSession sends EventTmuxSessionReserve and waits for the
//     loop's decision (the cap); the entry is "starting".
//   - FinishTmuxSessionStart sends EventTmuxSessionStarted once
//     `tmux new-session` returned: the entry runs, or is dropped on failure.
//   - EndTmuxSession sends EventTmuxSessionEnded after `tmux kill-session`.
//   - Each discovery pass (EventExternalSessionsDiscovered) carries the
//     server's pane list: the loop attaches each session's pane and drops a
//     session whose tmux session is gone (the operator quit the CLI or
//     killed it by hand).
//
// They count toward neither max_concurrent_agents nor tracker state.
// Session-scoped: cmd/itervox carries them across WORKFLOW.md reloads
// (SeedTmuxSessions); after a daemon restart they are plain hand-started
// sessions that happen to run in tmux.

// Event types of itervox's tmux sessions.
const (
	EventTmuxSessionReserve EventType = "TmuxSessionReserve"
	EventTmuxSessionStarted EventType = "TmuxSessionStarted"
	EventTmuxSessionEnded   EventType = "TmuxSessionEnded"
)

// SessionKindTmux is AgentSession.Kind of a tmux session itervox started.
const SessionKindTmux = "tmux"

// Status of a tmux session between reservation and `tmux new-session`.
const SessionStatusStarting = "starting"

// MaxTmuxSessions caps the tmux sessions itervox runs at once.
const MaxTmuxSessions = 8

// tmuxReplyTimeout bounds how long a caller waits for the loop.
const tmuxReplyTimeout = 10 * time.Second

// ErrTooManyTmuxSessions refuses a reservation over MaxTmuxSessions.
var ErrTooManyTmuxSessions = errors.New("orchestrator: too many tmux sessions are running")

// TmuxSession is a tmux session itervox started. No reference fields
// (State clones it by value).
type TmuxSession struct {
	// ID is the tmux session name ("itervox-<shortid>"), which is also the
	// id clients address it by.
	ID      string
	Backend string
	Cwd     string
	Profile string
	// Status is SessionStatusStarting or SessionStatusRunning.
	Status    string
	StartedAt time.Time
	// Pane is the session's pane as of the last discovery pass.
	Pane extsessions.TmuxPane
}

// TmuxSessionEvent is the payload of the tmux session events.
type TmuxSessionEvent struct {
	Session TmuxSession
	Failed  bool
	// Reply receives the loop's decision on a reservation (buffered).
	Reply chan error
}

// ExternalScan is one discovery pass: the hand-started sessions in scope
// and, when tmux could be listed, every pane of its server.
type ExternalScan struct {
	Sessions []extsessions.Session
	// TmuxListed is true when TmuxPanes is the server's full pane list; a
	// pass without it never drops a tmux session.
	TmuxListed bool
	TmuxPanes  []extsessions.TmuxPane
	// ScannedAt is when the pane list was taken.
	ScannedAt time.Time
}

// SeedTmuxSessions installs the tmux sessions a previous run() generation
// owned (WORKFLOW.md reload). Must be called before Run.
func (o *Orchestrator) SeedTmuxSessions(list []TmuxSession) {
	if o.started.Load() {
		return
	}
	o.seedTmux = append([]TmuxSession(nil), list...)
}

// TmuxSessionList returns the tmux sessions of a snapshot (for the reload
// carry).
func TmuxSessionList(snap State) []TmuxSession {
	out := make([]TmuxSession, 0, len(snap.TmuxSessions))
	for _, s := range snap.TmuxSessions {
		if s != nil {
			out = append(out, *s)
		}
	}
	return out
}

// FindTmuxSession returns itervox's tmux session id. Safe from any
// goroutine.
func (o *Orchestrator) FindTmuxSession(id string) (TmuxSession, bool) {
	if s := o.Snapshot().TmuxSessions[id]; s != nil {
		return *s, true
	}
	return TmuxSession{}, false
}

// ReserveTmuxSession asks the loop for a slot for s before tmux starts it.
// Errors: ErrTooManyTmuxSessions, ErrBusy. Blocks until the loop decides.
func (o *Orchestrator) ReserveTmuxSession(s TmuxSession) error {
	ev := &TmuxSessionEvent{Session: s, Reply: make(chan error, 1)}
	select {
	case o.events <- OrchestratorEvent{Type: EventTmuxSessionReserve, Identifier: s.ID, Tmux: ev}:
	default:
		metrics.EventDropped() // CORE-045
		return ErrBusy
	}
	done := context.Background()
	if p := o.runCtx.Load(); p != nil {
		done = *p
	}
	timer := time.NewTimer(tmuxReplyTimeout)
	defer timer.Stop()
	select {
	case err := <-ev.Reply:
		return err
	case <-done.Done():
		return ErrBusy
	case <-timer.C:
		return ErrBusy
	}
}

// FinishTmuxSessionStart reports how `tmux new-session` went for a
// reserved session. Safe from any goroutine.
func (o *Orchestrator) FinishTmuxSessionStart(id string, failed bool) {
	o.sendTmuxEvent(EventTmuxSessionStarted, TmuxSessionEvent{Session: TmuxSession{ID: id}, Failed: failed})
}

// EndTmuxSession forgets a tmux session itervox killed. Safe from any
// goroutine.
func (o *Orchestrator) EndTmuxSession(id string) {
	o.sendTmuxEvent(EventTmuxSessionEnded, TmuxSessionEvent{Session: TmuxSession{ID: id}})
}

func (o *Orchestrator) sendTmuxEvent(t EventType, ev TmuxSessionEvent) {
	done := context.Background()
	if p := o.runCtx.Load(); p != nil {
		done = *p
	}
	timer := time.NewTimer(tmuxReplyTimeout)
	defer timer.Stop()
	select {
	case o.events <- OrchestratorEvent{Type: t, Identifier: ev.Session.ID, Tmux: &ev}:
	case <-done.Done():
	case <-timer.C:
		metrics.EventDropped() // CORE-045
		slog.Warn("orchestrator: tmux session event dropped: event queue full", "event", t, "session", ev.Session.ID)
	}
}

// applyTmuxSessionEvent handles the three tmux session events. Event loop
// only.
func applyTmuxSessionEvent(state *State, t EventType, ev *TmuxSessionEvent, now time.Time) {
	if ev == nil {
		return
	}
	id := ev.Session.ID
	if state.TmuxSessions == nil {
		state.TmuxSessions = map[string]*TmuxSession{}
	}
	switch t {
	case EventTmuxSessionReserve:
		var err error
		switch {
		case state.TmuxSessions[id] != nil:
			err = errors.New("orchestrator: tmux session " + id + " exists")
		case len(state.TmuxSessions) >= MaxTmuxSessions:
			err = ErrTooManyTmuxSessions
		default:
			s := ev.Session
			s.Status, s.StartedAt = SessionStatusStarting, now
			state.TmuxSessions[id] = &s
		}
		if ev.Reply != nil {
			ev.Reply <- err
		}
	case EventTmuxSessionStarted:
		s := state.TmuxSessions[id]
		switch {
		case s == nil:
		case ev.Failed:
			delete(state.TmuxSessions, id)
		default:
			s.Status, s.StartedAt = SessionStatusRunning, now
			slog.Info("orchestrator: tmux session started", "session", id, "backend", s.Backend, "cwd", s.Cwd)
		}
	case EventTmuxSessionEnded:
		delete(state.TmuxSessions, id)
	}
}

// applyTmuxPanes attaches each tmux session's pane from a discovery pass
// and drops the sessions whose tmux session is gone. Event loop only.
func applyTmuxPanes(state *State, scan ExternalScan) {
	if !scan.TmuxListed {
		return
	}
	for id, s := range state.TmuxSessions {
		pane, ok := firstPaneOf(scan.TmuxPanes, id)
		switch {
		case ok:
			s.Pane = pane
		case s.Status == SessionStatusRunning && s.StartedAt.Before(scan.ScannedAt):
			slog.Info("orchestrator: tmux session ended outside itervox", "session", id)
			delete(state.TmuxSessions, id)
		}
	}
}

// firstPaneOf returns the first listed pane of tmux session name.
func firstPaneOf(panes []extsessions.TmuxPane, name string) (extsessions.TmuxPane, bool) {
	for _, p := range panes {
		if p.Session == name {
			return p, true
		}
	}
	return extsessions.TmuxPane{}, false
}

// tmuxSessionRows lists itervox's tmux sessions for Sessions().
func tmuxSessionRows(snap State) []AgentSession {
	out := make([]AgentSession, 0, len(snap.TmuxSessions))
	for _, s := range snap.TmuxSessions {
		if s == nil {
			continue
		}
		out = append(out, AgentSession{
			ID: s.ID, Owner: SessionOwnerItervox, Kind: SessionKindTmux, Profile: s.Profile,
			Backend: s.Backend, Cwd: s.Cwd, Status: s.Status, StartedAt: s.StartedAt, TmuxPane: s.Pane,
		})
	}
	return out
}

// seedTmuxState installs the carried tmux sessions into a fresh state.
func seedTmuxState(state *State, list []TmuxSession) {
	if len(list) == 0 {
		return
	}
	m := make(map[string]*TmuxSession, len(list))
	for i := range list {
		s := list[i]
		m[s.ID] = &s
	}
	if state.TmuxSessions == nil {
		state.TmuxSessions = map[string]*TmuxSession{}
	}
	maps.Copy(state.TmuxSessions, m)
}
