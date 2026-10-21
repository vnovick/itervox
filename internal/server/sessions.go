package server

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// Sessions panel API (#178). A session is an agent CLI session identified by
// the CLI's own session ID. Steps 1–2 list the sessions of this daemon's own
// runs (owner "itervox") and accept operator messages for running ones; a
// session waiting for input takes a reply through
// POST /api/v1/issues/{identifier}/provide-input instead. Step 3
// (sessions_external.go) adds sessions started by hand on the daemon's host
// (owner "external", read-only) and their transcripts. sessions_tmux.go adds
// control of sessions running in tmux and "New session".

// Session owners. Itervox writes only to sessions it owns.
const (
	SessionOwnerItervox  = "itervox"
	SessionOwnerExternal = "external"
)

// SessionMessageRow is one operator message sent to a session.
type SessionMessageRow struct {
	ID   string `json:"id"`
	Text string `json:"text"`
	// Status is "queued", "delivered" or "dropped".
	Status string `json:"status"`
	// Delivery is "live" (written into the running turn) or "resume" (the
	// prompt of the next resumed turn) once delivered.
	Delivery    string     `json:"delivery,omitempty"`
	Reason      string     `json:"reason,omitempty"`
	QueuedAt    time.Time  `json:"queuedAt"`
	DeliveredAt *time.Time `json:"deliveredAt,omitempty"`
}

// SessionRow is one session of GET /api/v1/sessions.
type SessionRow struct {
	// ID is the CLI's session ID; empty until the CLI reported it (the
	// first seconds of a run), when RunID addresses the session instead.
	ID    string `json:"id"`
	RunID string `json:"runId,omitempty"`
	// Owner is "itervox" for this daemon's runs, "external" for sessions
	// started by hand (read-only).
	Owner      string `json:"owner"`
	Identifier string `json:"identifier"`
	Title      string `json:"title,omitempty"`
	Profile    string `json:"profile,omitempty"`
	Backend    string `json:"backend,omitempty"`
	WorkerHost string `json:"workerHost,omitempty"`
	Kind       string `json:"kind,omitempty"`
	// Status is "running", "input_required" or "pending_input_resume";
	// "exited" for a hand-started session whose CLI is no longer running.
	Status    string     `json:"status"`
	StartedAt *time.Time `json:"startedAt,omitempty"`
	QueuedAt  *time.Time `json:"queuedAt,omitempty"`
	TurnCount int        `json:"turnCount,omitempty"`
	LastEvent string     `json:"lastEvent,omitempty"`
	// InputContext is the agent's question while it waits for input.
	InputContext string `json:"inputContext,omitempty"`
	// Steering is how a message reaches a running session: "live" or
	// "resume"; empty when the session takes no message.
	Steering string              `json:"steering,omitempty"`
	Messages []SessionMessageRow `json:"messages,omitempty"`
	// Cwd is the directory of a session started by hand (#178 step 3);
	// such sessions have no identifier.
	Cwd string `json:"cwd,omitempty"`
	// UpdatedAt is a hand-started session's last transcript write.
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
	// InUseReason says why a hand-started session counts as still running
	// (status "running", owner "external").
	InUseReason string `json:"inUseReason,omitempty"`
	// TmuxPane is the tmux pane the session runs in; null when none.
	TmuxPane *TmuxPaneRow `json:"tmuxPane"`
	// Controllable: the panel may show the pane's screen and type into it
	// (an itervox tmux session, or a hand-started one in a matched pane
	// with sessions.tmux_control on), and tmux is available.
	Controllable bool `json:"controllable"`
	// AttachCommand attaches a terminal to an itervox tmux session.
	AttachCommand string `json:"attachCommand,omitempty"`
}

// SessionsResponse is the GET /api/v1/sessions body.
type SessionsResponse struct {
	GeneratedAt time.Time    `json:"generatedAt"`
	Sessions    []SessionRow `json:"sessions"`
	// Tmux says whether tmux control and "New session" are possible.
	Tmux TmuxStatusRow `json:"tmux"`
	// NewSession lists what the "New session" form offers.
	NewSession NewSessionOptions `json:"newSession"`
}

// SessionsClient backs the sessions endpoints. cmd/itervox implements it
// over the orchestrator.
type SessionsClient interface {
	// ListSessions returns the sessions in panel order: waiting for input
	// first.
	ListSessions() []SessionRow
	// SendSessionMessage queues an operator message for the running session
	// id (its CLI session ID, or its run ID), or — for a controllable tmux
	// session — pastes text into its pane, pressing Enter when submit.
	// Errors: ErrSessionNotFound, ErrSessionInputRequired,
	// ErrSessionNotOwned, ErrInvalidSessionMessage, ErrBusy,
	// ErrTmuxUnavailable, ErrPaneGone.
	SendSessionMessage(id, text string, submit bool) (SessionMessageRow, error)
	// SessionTranscript returns a page of the transcript of a hand-started
	// session, ending before byte offset before (< 0: the end). Errors:
	// ErrSessionNotFound.
	SessionTranscript(id string, before int64, limit int) (TranscriptResponse, error)
	// TmuxStatus reports tmux availability (re-detected on every discovery
	// pass) and whether sessions.tmux_control is on.
	TmuxStatus() TmuxStatusRow
	// NewSessionOptions lists the folders and profiles "New session" offers.
	NewSessionOptions() NewSessionOptions
	// SessionScreen captures the last lines of a controllable session's
	// pane. Errors: ErrSessionNotFound, ErrNotControllable,
	// ErrTmuxUnavailable, ErrPaneGone.
	SessionScreen(id string, lines int) (ScreenResponse, error)
	// SendSessionKey presses one allowlisted key in a controllable
	// session's pane. Errors: those of SessionScreen, ErrKeyNotAllowed.
	SendSessionKey(id, key string) error
	// StartSession starts an interactive agent CLI in a new tmux session.
	// Errors: ErrTmuxUnavailable, ErrBadSessionRequest, ErrUnknownProfile,
	// ErrTooManySessions, ErrBusy.
	StartSession(req NewSessionRequest) (SessionRow, error)
	// KillSession kills a tmux session itervox started. Errors:
	// ErrSessionNotFound, ErrSessionNotOwned, ErrTmuxUnavailable.
	KillSession(id string) error
}

// Errors a SessionsClient returns.
var (
	ErrSessionNotFound       = errors.New("server: no running session with that id")
	ErrSessionInputRequired  = errors.New("server: the session is waiting for input")
	ErrSessionNotOwned       = errors.New("server: the session was not started by itervox")
	ErrInvalidSessionMessage = errors.New("server: invalid session message")
)

// maxSessionMessageBytes mirrors orchestrator.MaxSessionMessageBytes.
const maxSessionMessageBytes = 10 * 1024

func (s *Server) handleListSessions(w http.ResponseWriter, _ *http.Request) {
	if s.sessions == nil {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "sessions are not available")
		return
	}
	rows := s.sessions.ListSessions()
	if rows == nil {
		rows = []SessionRow{}
	}
	opts := s.sessions.NewSessionOptions()
	if opts.Folders == nil {
		opts.Folders = []string{}
	}
	if opts.Profiles == nil {
		opts.Profiles = []string{}
	}
	writeJSON(w, http.StatusOK, SessionsResponse{GeneratedAt: time.Now().UTC(), Sessions: rows,
		Tmux: s.sessions.TmuxStatus(), NewSession: opts})
}

// handleSessionMessage queues an operator message for a running session.
// POST /api/v1/sessions/{id}/message {"message":"..."}
// 202 {"message":{...}} — queued; its status moves to delivered or dropped.
func (s *Server) handleSessionMessage(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "sessions are not available")
		return
	}
	id := chi.URLParam(r, "id")
	var body struct {
		Message string `json:"message"`
		// Text and Submit are the tmux form ({text, submit?}); Message is
		// accepted for every session.
		Text   string `json:"text"`
		Submit *bool  `json:"submit"`
	}
	if err := decodeJSONBody(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	text := strings.TrimSpace(body.Message)
	if text == "" {
		text = strings.TrimSpace(body.Text)
	}
	submit := body.Submit == nil || *body.Submit
	if text == "" {
		writeErrorWithField(w, http.StatusBadRequest, "bad_request", "message is required", "message")
		return
	}
	if len(text) > maxSessionMessageBytes {
		writeErrorWithField(w, http.StatusBadRequest, "bad_request", "message exceeds 10 KiB", "message")
		return
	}
	msg, err := s.sessions.SendSessionMessage(id, text, submit)
	if writeTmuxError(w, err) {
		return
	}
	switch {
	case err == nil:
		writeJSON(w, http.StatusAccepted, map[string]any{"message": msg})
	case errors.Is(err, ErrInvalidSessionMessage):
		writeErrorWithField(w, http.StatusBadRequest, "bad_request", "message must be non-empty and at most 10 KiB", "message")
	case errors.Is(err, ErrSessionInputRequired):
		writeError(w, http.StatusConflict, "input_required",
			"the session is waiting for input: reply with POST /api/v1/issues/{identifier}/provide-input")
	case errors.Is(err, ErrSessionNotOwned):
		writeError(w, http.StatusForbidden, "not_owned", "itervox did not start this session; it is read-only")
	case errors.Is(err, ErrSessionNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no running session "+id)
	default:
		writeBusyOr404(w, err, "not_found", "no running session "+id)
	}
}
