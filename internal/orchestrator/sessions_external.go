package orchestrator

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/extsessions"
	"github.com/vnovick/itervox/internal/logging"
	"github.com/vnovick/itervox/internal/metrics"
)

// Sessions panel step 3 (#178): sessions started by hand.
//
// Discovery runs off the event loop (startExternalSessionDiscovery) and
// sends each pass as EventExternalSessionsDiscovered; the loop keeps the
// list in State.ExternalSessions, minus every session itervox owns. Those
// sessions are read-only (owner "external"): SendSessionMessage refuses
// them with ErrSessionNotOwned.
//
// "Continue in itervox" (ContinueExternalSession) takes one over. The
// caller first checks — off the loop, read-only — that the CLI that wrote
// it is no longer running (extsessions.CheckLiveness; when unsure it counts
// as running) and refuses with a *SessionInUseError otherwise. The event
// loop then decides (EventContinueSession): the session must still be
// discovered and not owned, the daemon not draining, and fewer than
// maxManualSessions continued. It becomes a ManualSession, owned by
// itervox: a manual run with no issue, in the session's own directory (not
// an itervox worktree), one resumed turn (`claude --resume <id>`,
// `codex exec resume <id>`) per operator message. Messages that arrive
// while a turn runs are queued and become the next turn's prompt.

// Event types of step 3.
const (
	// EventExternalSessionsDiscovered carries one discovery pass.
	EventExternalSessionsDiscovered EventType = "ExternalSessionsDiscovered"
	// EventContinueSession asks the loop to take over a hand-started session.
	EventContinueSession EventType = "ContinueSession"
	// EventManualTurnExited reports the end of a manual session's turn.
	EventManualTurnExited EventType = "ManualTurnExited"
)

// Session status values of continued and hand-started sessions.
const (
	// SessionStatusIdle: a continued session waiting for the next operator
	// message.
	SessionStatusIdle = "idle"
	// SessionStatusExited: a hand-started session whose CLI is no longer
	// running; it can be continued in itervox.
	SessionStatusExited = "exited"
)

// SessionKindManual is AgentSession.Kind of a continued session.
const SessionKindManual = "manual"

// DefaultExternalSessionsInterval is how often discovery runs.
const DefaultExternalSessionsInterval = 15 * time.Second

// maxManualSessions caps the sessions continued under itervox at once.
const maxManualSessions = 8

// continueReplyTimeout bounds how long ContinueExternalSession waits for
// the loop's decision.
const continueReplyTimeout = 10 * time.Second

// defaultContinuePrompt is the first turn's prompt when the operator gave
// none.
const defaultContinuePrompt = "Continue where you left off."

// Errors of ContinueExternalSession.
var (
	ErrSessionAlreadyOwned = errors.New("orchestrator: itervox already owns that session")
	ErrSessionsDraining    = errors.New("orchestrator: the daemon is draining; no session can be continued")
	ErrTooManyManual       = errors.New("orchestrator: too many sessions are continued under itervox")
	ErrUnknownProfile      = errors.New("orchestrator: no such profile")
	// ErrSessionInUse is what a *SessionInUseError matches with errors.Is.
	ErrSessionInUse = errors.New("orchestrator: the session may still be running outside itervox")
)

// SessionInUseError refuses to continue a session whose CLI may still be
// running; Reason says what the check found.
type SessionInUseError struct{ Reason string }

func (e *SessionInUseError) Error() string { return ErrSessionInUse.Error() + ": " + e.Reason }

// Is matches ErrSessionInUse.
func (e *SessionInUseError) Is(target error) bool { return target == ErrSessionInUse }

// ExternalSessionSource discovers hand-started sessions and checks whether
// one is still in use. extsessions.Scanner implements it; both methods only
// read.
type ExternalSessionSource interface {
	Discover(now time.Time) ([]extsessions.Session, error)
	Liveness(s extsessions.Session, now time.Time) extsessions.Liveness
}

// SetExternalSessionSource enables discovery of hand-started sessions every
// interval (<= 0: DefaultExternalSessionsInterval). Must be called before
// Run; nil leaves it off.
func (o *Orchestrator) SetExternalSessionSource(src ExternalSessionSource, interval time.Duration) {
	o.extSessions = src
	o.extSessionsInterval = interval
}

// ManualSession is a hand-started session continued under itervox. It
// holds no reference fields (State clones it by value).
type ManualSession struct {
	// ID is the CLI session ID it was discovered with; clients address it
	// by this ID for as long as itervox owns it.
	ID string
	// ResumeID is the session ID the next turn resumes: ID, or the ID the
	// CLI reported if it moved the conversation to a new one.
	ResumeID string
	Backend  string
	Cwd      string
	Title    string
	// Path is the transcript file.
	Path string
	// Command is the runner command each turn runs (backend hint included).
	Command    string
	Profile    string
	Permission string
	Status     string // SessionStatusRunning or SessionStatusIdle
	StartedAt  time.Time
	TurnCount  int
	LastEvent  string
	LastError  string
}

// ContinueRequest is the payload of EventContinueSession.
type ContinueRequest struct {
	Session extsessions.Session
	Message string
	Profile string
	// Reply receives the loop's decision (buffered; the loop never blocks).
	Reply chan error
}

// ManualTurnResult is the payload of EventManualTurnExited.
type ManualTurnResult struct {
	ID string // ManualSession.ID
	// SessionID is the session ID the CLI reported for the turn.
	SessionID string
	// Path is the new transcript when the CLI moved the conversation to a
	// new session file; "" otherwise.
	Path     string
	Failed   bool
	Error    string
	LastText string
}

// manualKey is the SessionMessages key of a continued session.
func manualKey(id string) string { return "session:" + id }

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
			sessions, err := src.Discover(time.Now())
			if err != nil {
				slog.Debug("orchestrator: session discovery incomplete", "error", err)
			}
			select {
			case o.events <- OrchestratorEvent{Type: EventExternalSessionsDiscovered, ExternalSessions: sessions}:
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
	for id, m := range state.ManualSessions {
		owned[id] = true
		if m != nil && m.ResumeID != "" {
			owned[m.ResumeID] = true
		}
	}
	return owned
}

// applyExternalSessions stores a discovery pass, minus owned sessions.
// Event loop only.
func applyExternalSessions(state *State, found []extsessions.Session) {
	owned := ownedSessionIDs(*state)
	list := make([]extsessions.Session, 0, len(found))
	for _, s := range found {
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

// FindExternalSession returns the hand-started session id as last
// discovered (owned sessions are never listed). Safe from any goroutine.
func (o *Orchestrator) FindExternalSession(id string) (extsessions.Session, bool) {
	return findExternalSession(o.Snapshot(), id)
}

// TranscriptSession returns the session whose transcript the Sessions panel
// may show for id: a hand-started session as discovered, or one continued
// under itervox. Safe from any goroutine.
func (o *Orchestrator) TranscriptSession(id string) (extsessions.Session, bool) {
	snap := o.Snapshot()
	if s, ok := findExternalSession(snap, id); ok {
		return s, true
	}
	if m := snap.ManualSessions[id]; m != nil && m.Path != "" {
		return extsessions.Session{ID: m.ID, Backend: m.Backend, Cwd: m.Cwd, Path: m.Path, Title: m.Title}, true
	}
	return extsessions.Session{}, false
}

// ContinueExternalSession takes over the hand-started session id once its
// CLI has exited: the loop starts a resumed turn with message (or a short
// default) as its prompt. profile, when set, names the profile whose
// command and permission mode the turns use. Refused with a
// *SessionInUseError while the original process may still be running.
// Safe from any goroutine; it blocks until the loop has decided.
func (o *Orchestrator) ContinueExternalSession(id, message, profile string) (AgentSession, error) {
	message = strings.TrimSpace(message)
	if len(message) > MaxSessionMessageBytes {
		return AgentSession{}, ErrInvalidSessionMessage
	}
	snap := o.Snapshot()
	if snap.ManualSessions[id] != nil || findRunningSession(snap, id) != nil || sessionWaitingForInput(snap, id) {
		return AgentSession{}, ErrSessionAlreadyOwned
	}
	s, ok := findExternalSession(snap, id)
	if !ok || o.extSessions == nil {
		return AgentSession{}, ErrSessionNotFound
	}
	// Re-check now, off the loop: discovery's answer may be seconds old.
	if l := o.extSessions.Liveness(s, time.Now()); l.Running {
		return AgentSession{}, &SessionInUseError{Reason: l.Reason}
	}
	req := &ContinueRequest{Session: s, Message: message, Profile: profile, Reply: make(chan error, 1)}
	select {
	case o.events <- OrchestratorEvent{Type: EventContinueSession, Identifier: id, Continue: req}:
	default:
		metrics.EventDropped() // CORE-045
		return AgentSession{}, ErrBusy
	}
	done := context.Background()
	if p := o.runCtx.Load(); p != nil {
		done = *p
	}
	timer := time.NewTimer(continueReplyTimeout)
	defer timer.Stop()
	select {
	case err := <-req.Reply:
		if err != nil {
			return AgentSession{}, err
		}
	case <-done.Done():
		return AgentSession{}, ErrBusy
	case <-timer.C:
		return AgentSession{}, ErrBusy
	}
	return AgentSession{
		ID: s.ID, Owner: SessionOwnerItervox, Kind: SessionKindManual, Title: s.Title,
		Backend: s.Backend, Cwd: s.Cwd, Profile: profile, Status: SessionStatusRunning,
		StartedAt: time.Now(), UpdatedAt: s.UpdatedAt, Steering: SteeringResume,
	}, nil
}

// applyContinueSession decides a continue request. Event loop only.
func (o *Orchestrator) applyContinueSession(ctx context.Context, state *State, req *ContinueRequest) {
	if req == nil {
		return
	}
	err := o.continueSession(ctx, state, req)
	req.Reply <- err
	if err != nil {
		slog.Info("orchestrator: continue session refused", "session_id", req.Session.ID, "error", err)
	}
}

func (o *Orchestrator) continueSession(ctx context.Context, state *State, req *ContinueRequest) error {
	id := req.Session.ID
	switch {
	case state.Draining:
		return ErrSessionsDraining
	case ownedSessionIDs(*state)[id]:
		return ErrSessionAlreadyOwned
	case len(state.ManualSessions) >= maxManualSessions:
		return ErrTooManyManual
	}
	s, ok := findExternalSession(*state, id)
	if !ok {
		return ErrSessionNotFound
	}
	profileCommand, permission := "", string(agent.PermissionSandbox)
	if req.Profile != "" {
		o.cfgMu.RLock()
		p, ok := o.cfg.Agent.Profiles[req.Profile]
		o.cfgMu.RUnlock()
		if !ok {
			return ErrUnknownProfile
		}
		profileCommand, permission = p.Command, p.PermissionMode
	}
	target := resolveResumeTarget("", s.Backend, profileCommand, o.cfg.Agent.Command)
	m := &ManualSession{
		ID: s.ID, ResumeID: s.ID, Backend: s.Backend, Cwd: s.Cwd, Title: s.Title, Path: s.Path,
		Command: target.RunnerCommand, Profile: req.Profile, Permission: permission,
		Status: SessionStatusIdle, StartedAt: time.Now(),
	}
	if state.ManualSessions == nil {
		state.ManualSessions = map[string]*ManualSession{}
	}
	state.ManualSessions[id] = m
	list := state.ExternalSessions[:0:0]
	for _, e := range state.ExternalSessions {
		if e.ID != id {
			list = append(list, e)
		}
	}
	state.ExternalSessions = list
	slog.Info("orchestrator: hand-started session continued under itervox",
		"session_id", id, "backend", s.Backend, "cwd", s.Cwd, "profile", req.Profile)
	o.startManualTurn(ctx, m, cmp.Or(req.Message, defaultContinuePrompt))
	return nil
}

// lastEventRunes caps ManualSession.LastEvent / LastError.
const lastEventRunes = 500

// abbreviateRedacted redacts s and caps it for the Sessions list.
func abbreviateRedacted(s string) string {
	s = logging.RedactString(strings.TrimSpace(s))
	if r := []rune(s); len(r) > lastEventRunes {
		return string(r[:lastEventRunes]) + "…"
	}
	return s
}
