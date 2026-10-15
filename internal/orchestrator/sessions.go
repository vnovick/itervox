package orchestrator

import (
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/metrics"
)

// Sessions panel (#178). A session is an agent CLI session, identified by
// the CLI's own session ID (not by the issue). Steps 1–2 list the sessions
// of itervox's own runs — running, or waiting for input — and let an
// operator message a running one (steering, see internal/agent/steer.go).
//
// Invariant: itervox writes only to sessions it owns. A message goes to the
// stdin of the CLI process this daemon started for the run, or into the
// prompt of the run's next resumed turn — never to a terminal or another
// process. Every session listed here has Owner SessionOwnerItervox; a later
// step adds read-only SessionOwnerExternal sessions, which SendSessionMessage
// refuses.
//
// SessionMessages is event-loop state: SendSessionMessage only sends
// EventSessionMessage, and the worker reports deliveries with
// EventSessionMessageDelivered. Session-scoped, not persisted.

// Session ownership.
const (
	SessionOwnerItervox  = "itervox"
	SessionOwnerExternal = "external"
)

// Session status values (AgentSession.Status).
const (
	SessionStatusRunning            = "running"
	SessionStatusInputRequired      = "input_required"
	SessionStatusPendingInputResume = "pending_input_resume"
)

// How a running session takes an operator message (AgentSession.Steering).
const (
	SteeringLive   = "live"   // written into the running turn's stdin
	SteeringResume = "resume" // delivered as the prompt of the next resumed turn
)

// SessionMessageStatus is where an operator message is.
type SessionMessageStatus string

// Session message statuses.
const (
	SessionMessageQueued    SessionMessageStatus = "queued"
	SessionMessageDelivered SessionMessageStatus = "delivered"
	SessionMessageDropped   SessionMessageStatus = "dropped"
)

// Event types of the Sessions panel.
const (
	// EventSessionMessage carries an operator message for a running session
	// (SendSessionMessage). The loop records it and hands it to the run.
	EventSessionMessage EventType = "SessionMessage"
	// EventSessionMessageDelivered is sent by a worker once messages reached
	// its session (live, or as a resumed turn's prompt).
	EventSessionMessageDelivered EventType = "SessionMessageDelivered"
)

// MaxSessionMessageBytes bounds one operator message.
const MaxSessionMessageBytes = 10 * 1024

// maxSessionMessagesPerIssue bounds the history kept per issue.
const maxSessionMessagesPerIssue = 20

// steerInboxSize bounds the messages a run holds before its worker takes them.
const steerInboxSize = 16

// maxSteeringTurns caps the extra resumed turns operator messages can add
// to one run.
const maxSteeringTurns = 20

// Errors of SendSessionMessage.
var (
	ErrSessionNotFound       = errors.New("orchestrator: no running session with that id")
	ErrSessionInputRequired  = errors.New("orchestrator: the session is waiting for input; reply through provide-input")
	ErrSessionNotOwned       = errors.New("orchestrator: itervox did not start that session; it is read-only")
	ErrInvalidSessionMessage = errors.New("orchestrator: a session message must be non-empty and at most 10 KiB")
)

// SessionMessage is one operator message to a session.
type SessionMessage struct {
	ID         string
	Identifier string
	RunID      string
	// SessionID is the CLI session the message was addressed to ("" while
	// the CLI had not reported one yet).
	SessionID string
	Text      string
	Status    SessionMessageStatus
	// Delivery is SteeringLive or SteeringResume once delivered.
	Delivery    string
	Reason      string // why it was dropped
	QueuedAt    time.Time
	DeliveredAt time.Time
}

// SessionDelivery reports messages a worker delivered.
type SessionDelivery struct {
	Identifier string
	IDs        []string
	Delivery   string
}

// AgentSession is one row of the Sessions panel.
type AgentSession struct {
	ID         string // CLI session ID; "" until the CLI reported it
	RunID      string // itervox run; "" for a session waiting for input
	Owner      string
	Identifier string
	Title      string
	Profile    string
	Backend    string
	WorkerHost string
	Kind       string
	Status     string
	StartedAt  time.Time
	QueuedAt   time.Time // when it started waiting for input
	TurnCount  int
	LastEvent  string
	// InputContext is the agent's question while it waits for input.
	InputContext string
	// Steering says how a message reaches a running session; "" when the
	// session takes no message (it is waiting for input: reply instead).
	Steering string
	Messages []SessionMessage
}

// Key is the id clients address the session by: the CLI session ID, or the
// run ID until the CLI reports one.
func (s AgentSession) Key() string {
	if s.ID != "" {
		return s.ID
	}
	return s.RunID
}

// steeringFor is how a running session of backend on workerHost takes a
// message: live only for a local Claude worker with agent.live_steering.
func steeringFor(backend, workerHost string, liveSteering bool) string {
	if liveSteering && workerHost == "" && (backend == "" || backend == "claude") {
		return SteeringLive
	}
	return SteeringResume
}

// Sessions lists the sessions of snap: input-required first, then answered
// ones resuming, then running ones, oldest first within each group.
func Sessions(snap State, liveSteering bool) []AgentSession {
	out := make([]AgentSession, 0, len(snap.Running)+len(snap.InputRequiredIssues)+len(snap.PendingInputResumes))
	listed := map[string]bool{}
	for _, r := range snap.Running {
		if r == nil {
			continue
		}
		listed[r.Issue.Identifier] = true
		out = append(out, AgentSession{
			ID: r.AgentSessionID, RunID: r.RunID, Owner: SessionOwnerItervox,
			Identifier: r.Issue.Identifier, Title: r.Issue.Title, Profile: r.ProfileName,
			Backend: r.Backend, WorkerHost: r.WorkerHost, Kind: r.Kind,
			Status: SessionStatusRunning, StartedAt: r.StartedAt, TurnCount: r.TurnCount,
			LastEvent: r.LastMessage, Steering: steeringFor(r.Backend, r.WorkerHost, liveSteering),
			Messages: cloneSlice(snap.SessionMessages[r.Issue.Identifier]),
		})
	}
	for _, e := range snap.InputRequiredIssues {
		if e == nil || listed[e.Identifier] {
			continue
		}
		listed[e.Identifier] = true
		out = append(out, AgentSession{
			ID: e.SessionID, Owner: SessionOwnerItervox, Identifier: e.Identifier,
			Profile: e.ProfileName, Backend: e.Backend, WorkerHost: e.WorkerHost,
			Kind: reviewerKind(e.Reviewer), Status: SessionStatusInputRequired, QueuedAt: e.QueuedAt,
			InputContext: e.Context, Messages: cloneSlice(snap.SessionMessages[e.Identifier]),
		})
	}
	for _, e := range snap.PendingInputResumes {
		if e == nil || listed[e.Identifier] {
			continue
		}
		out = append(out, AgentSession{
			ID: e.SessionID, Owner: SessionOwnerItervox, Identifier: e.Identifier,
			Profile: e.ProfileName, Backend: e.Backend, WorkerHost: e.WorkerHost,
			Status: SessionStatusPendingInputResume, QueuedAt: e.QueuedAt, InputContext: e.Context,
			Messages: cloneSlice(snap.SessionMessages[e.Identifier]),
		})
	}
	SortSessions(out)
	return out
}

// sessionStatusRank orders the panel: waiting for input first.
func sessionStatusRank(status string) int {
	switch status {
	case SessionStatusInputRequired:
		return 0
	case SessionStatusPendingInputResume:
		return 1
	default:
		return 2
	}
}

// SortSessions orders sessions as the panel shows them.
func SortSessions(s []AgentSession) {
	sort.SliceStable(s, func(i, j int) bool {
		ri, rj := sessionStatusRank(s[i].Status), sessionStatusRank(s[j].Status)
		if ri != rj {
			return ri < rj
		}
		ti, tj := s[i].StartedAt, s[j].StartedAt
		if s[i].Status != SessionStatusRunning {
			ti, tj = s[i].QueuedAt, s[j].QueuedAt
		}
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		return s[i].Identifier < s[j].Identifier
	})
}

// findRunningSession returns the running entry addressed by key: its CLI
// session ID or its run ID.
func findRunningSession(state State, key string) *RunEntry {
	if key == "" {
		return nil
	}
	for _, r := range state.Running {
		if r != nil && (r.AgentSessionID == key || r.RunID == key) {
			return r
		}
	}
	return nil
}

// sessionWaitingForInput reports whether key names a session waiting for
// (or resuming from) a human reply.
func sessionWaitingForInput(state State, key string) bool {
	for _, e := range state.InputRequiredIssues {
		if e != nil && e.SessionID == key {
			return true
		}
	}
	for _, e := range state.PendingInputResumes {
		if e != nil && e.SessionID == key {
			return true
		}
	}
	return false
}

// SendSessionMessage queues an operator message for the running session
// addressed by key (CLI session ID, or run ID before the CLI reported one).
// It classifies the target against the latest snapshot — the event loop
// re-checks it — and returns the queued message. Safe from any goroutine.
func (o *Orchestrator) SendSessionMessage(key, text string) (SessionMessage, error) {
	text = strings.TrimSpace(text)
	if text == "" || len(text) > MaxSessionMessageBytes {
		return SessionMessage{}, ErrInvalidSessionMessage
	}
	snap := o.Snapshot()
	entry := findRunningSession(snap, key)
	if entry == nil {
		if sessionWaitingForInput(snap, key) {
			return SessionMessage{}, ErrSessionInputRequired
		}
		return SessionMessage{}, ErrSessionNotFound
	}
	msg := SessionMessage{
		ID:         "msg-" + strings.TrimPrefix(generateRunID(), "run-"),
		Identifier: entry.Issue.Identifier,
		RunID:      entry.RunID,
		SessionID:  entry.AgentSessionID,
		Text:       text,
		Status:     SessionMessageQueued,
		QueuedAt:   time.Now(),
	}
	select {
	case o.events <- OrchestratorEvent{Type: EventSessionMessage, Identifier: key, SessionMessage: &msg}:
		return msg, nil
	default:
		metrics.EventDropped() // CORE-045
		slog.Warn("orchestrator: session-message event channel full", "session", key)
		return SessionMessage{}, ErrBusy
	}
}

// appendSessionMessage records msg for its issue, keeping the newest
// maxSessionMessagesPerIssue. Event loop only.
func appendSessionMessage(state *State, msg SessionMessage) {
	if state.SessionMessages == nil {
		state.SessionMessages = map[string][]SessionMessage{}
	}
	list := append(state.SessionMessages[msg.Identifier], msg)
	if n := len(list) - maxSessionMessagesPerIssue; n > 0 {
		list = append([]SessionMessage(nil), list[n:]...)
	}
	state.SessionMessages[msg.Identifier] = list
}

// applySessionMessage records an operator message and hands it to the run.
// Event loop only.
func (o *Orchestrator) applySessionMessage(state *State, ev OrchestratorEvent) {
	if ev.SessionMessage == nil {
		return
	}
	msg := *ev.SessionMessage
	entry := findRunningSession(*state, ev.Identifier)
	if entry == nil || (msg.RunID != "" && entry.RunID != msg.RunID) {
		slog.Warn("orchestrator: session message for a session that is no longer running",
			"session", ev.Identifier, "message_id", msg.ID)
		return
	}
	switch {
	case entry.run == nil || entry.run.steer == nil:
		msg.Status, msg.Reason = SessionMessageDropped, "the run takes no messages"
	default:
		select {
		case entry.run.steer <- agent.SteerMessage{ID: msg.ID, Text: msg.Text}:
		default:
			msg.Status, msg.Reason = SessionMessageDropped,
				fmt.Sprintf("too many undelivered messages (%d)", steerInboxSize)
		}
	}
	appendSessionMessage(state, msg)
	slog.Info("orchestrator: operator message for session",
		"issue_identifier", msg.Identifier, "session_id", msg.SessionID, "run_id", msg.RunID,
		"message_id", msg.ID, "status", string(msg.Status))
}

// applySessionDelivery marks delivered messages. Event loop only.
func applySessionDelivery(state *State, d *SessionDelivery, now time.Time) {
	if d == nil {
		return
	}
	list := state.SessionMessages[d.Identifier]
	for i := range list {
		for _, id := range d.IDs {
			if list[i].ID == id && list[i].Status == SessionMessageQueued {
				list[i].Status = SessionMessageDelivered
				list[i].Delivery = d.Delivery
				list[i].DeliveredAt = now
			}
		}
	}
}

// settleSessionMessagesOnExit drops the messages a finished run never
// delivered: nothing will take them any more. Event loop only.
func settleSessionMessagesOnExit(state *State, identifier, runID string) {
	list := state.SessionMessages[identifier]
	for i := range list {
		if list[i].Status == SessionMessageQueued && (runID == "" || list[i].RunID == runID) {
			list[i].Status = SessionMessageDropped
			list[i].Reason = "the run ended before the message could be delivered"
		}
	}
}

// exitIdentifier is the issue identifier an exit event belongs to.
func exitIdentifier(live *RunEntry, ev OrchestratorEvent) string {
	switch {
	case ev.RunEntry != nil && ev.RunEntry.Issue.Identifier != "":
		return ev.RunEntry.Issue.Identifier
	case live != nil:
		return live.Issue.Identifier
	default:
		return ""
	}
}
