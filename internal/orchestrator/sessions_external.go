package orchestrator

import (
	"context"
	"log/slog"
	"time"

	"github.com/vnovick/itervox/internal/extsessions"
)

// Sessions panel step 3 (#178): sessions started by hand.
//
// Discovery runs off the event loop (startExternalSessionDiscovery) and
// sends each pass as EventExternalSessionsDiscovered; the loop keeps the
// list in State.ExternalSessions, minus every session itervox owns. Those
// sessions are read-only (owner "external"): SendSessionMessage refuses
// them with ErrSessionNotOwned. Their status is "running" while the CLI
// that wrote them may still run (extsessions liveness, with the reason) and
// "exited" otherwise; the Sessions panel shows their transcript only.

// EventExternalSessionsDiscovered carries one discovery pass.
const EventExternalSessionsDiscovered EventType = "ExternalSessionsDiscovered"

// SessionStatusExited is the status of a hand-started session whose CLI is
// no longer running.
const SessionStatusExited = "exited"

// DefaultExternalSessionsInterval is how often discovery runs.
const DefaultExternalSessionsInterval = 15 * time.Second

// ExternalSessionSource discovers hand-started sessions and lists tmux
// panes. cmd/itervox implements it over extsessions.Scanner and
// internal/tmuxctl; it only reads.
type ExternalSessionSource interface {
	Discover(now time.Time) (ExternalScan, error)
}

// SetExternalSessionSource enables discovery of hand-started sessions every
// interval (<= 0: DefaultExternalSessionsInterval). Must be called before
// Run; nil leaves it off.
func (o *Orchestrator) SetExternalSessionSource(src ExternalSessionSource, interval time.Duration) {
	o.extSessions = src
	o.extSessionsInterval = interval
}

// startExternalSessionDiscovery runs discovery off the loop until ctx ends.
func (o *Orchestrator) startExternalSessionDiscovery(ctx context.Context) {
	src := o.extSessions
	if src == nil {
		return
	}
	interval := o.extSessionsInterval
	if interval <= 0 {
		interval = DefaultExternalSessionsInterval
	}
	goSafe(&o.extSessionsWg, "external-session-discovery", "", func() {
		timer := time.NewTimer(0)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			scan, err := src.Discover(time.Now())
			if err != nil {
				slog.Debug("orchestrator: session discovery incomplete", "error", err)
			}
			select {
			case o.events <- OrchestratorEvent{Type: EventExternalSessionsDiscovered, ExternalScan: &scan}:
			case <-ctx.Done():
				return
			}
			timer.Reset(interval)
		}
	}, nil)
}

// ownedSessionIDs are the CLI session IDs itervox owns in state.
func ownedSessionIDs(state State) map[string]bool {
	owned := map[string]bool{}
	for _, r := range state.Running {
		if r != nil && r.AgentSessionID != "" {
			owned[r.AgentSessionID] = true
		}
	}
	for _, e := range state.InputRequiredIssues {
		if e != nil && e.SessionID != "" {
			owned[e.SessionID] = true
		}
	}
	for _, e := range state.PendingInputResumes {
		if e != nil && e.SessionID != "" {
			owned[e.SessionID] = true
		}
	}
	return owned
}

// applyExternalSessions stores a discovery pass, minus owned sessions: the
// CLI sessions of itervox's runs, and any session running inside a tmux
// session itervox started. Event loop only.
func applyExternalSessions(state *State, found []extsessions.Session) {
	owned := ownedSessionIDs(*state)
	list := make([]extsessions.Session, 0, len(found))
	for _, s := range found {
		if s.TmuxPane.Session != "" && state.TmuxSessions[s.TmuxPane.Session] != nil {
			continue
		}
		if !owned[s.ID] {
			list = append(list, s)
		}
	}
	state.ExternalSessions = list
}

// findExternalSession returns the discovered session id, if listed.
func findExternalSession(state State, id string) (extsessions.Session, bool) {
	for _, s := range state.ExternalSessions {
		if s.ID == id {
			return s, true
		}
	}
	return extsessions.Session{}, false
}

// TranscriptSession returns the hand-started session whose transcript the
// Sessions panel may show for id, as last discovered (owned sessions are
// never listed). Safe from any goroutine.
func (o *Orchestrator) TranscriptSession(id string) (extsessions.Session, bool) {
	return findExternalSession(o.Snapshot(), id)
}
