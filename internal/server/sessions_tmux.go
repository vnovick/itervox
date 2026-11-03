package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// Sessions panel, tmux (#178): the screen and keys of sessions running in a
// tmux pane, and "New session" / kill for the tmux sessions itervox starts.
// Writing into a session itervox did not start needs sessions.tmux_control;
// every write needs tmux on the daemon's PATH.

// TmuxPaneRow names the tmux pane a session runs in.
type TmuxPaneRow struct {
	ID    string `json:"id"`    // "%12"
	Label string `json:"label"` // "session:window.pane"
}

// TmuxStatusRow is GET /api/v1/sessions' top-level "tmux" object.
type TmuxStatusRow struct {
	Available bool   `json:"available"`
	Version   string `json:"version,omitempty"`
	// InstallHint says how to install tmux on the daemon's platform.
	InstallHint string `json:"installHint"`
	// Control is sessions.tmux_control: hand-started sessions in tmux may
	// be controlled.
	Control bool `json:"control"`
}

// NewSessionOptions is what the "New session" form offers.
type NewSessionOptions struct {
	// ProjectRoot is the default folder (the WORKFLOW.md directory).
	ProjectRoot string `json:"projectRoot"`
	// Folders are the project root followed by itervox's workspaces.
	Folders  []string `json:"folders"`
	Profiles []string `json:"profiles"`
	Max      int      `json:"max"`
}

// NewSessionRequest is the POST /api/v1/sessions body.
type NewSessionRequest struct {
	Backend string `json:"backend"`
	Cwd     string `json:"cwd"`
	Profile string `json:"profile"`
	Prompt  string `json:"prompt"`
}

// ScreenResponse is the GET /api/v1/sessions/{id}/screen body.
type ScreenResponse struct {
	ID         string    `json:"id"`
	Pane       string    `json:"pane"`
	Label      string    `json:"label"`
	Text       string    `json:"text"`
	Lines      int       `json:"lines"`
	CapturedAt time.Time `json:"capturedAt"`
}

// Errors of the tmux endpoints.
var (
	ErrNotControllable   = errors.New("server: the session cannot be controlled from the panel")
	ErrPaneGone          = errors.New("server: the session's tmux pane is gone or no longer runs it")
	ErrKeyNotAllowed     = errors.New("server: key not allowed")
	ErrBadSessionRequest = errors.New("server: invalid new session request")
	ErrUnknownProfile    = errors.New("server: no such profile")
	ErrTooManySessions   = errors.New("server: too many tmux sessions are running")
)

// TmuxUnavailableError refuses a write while tmux is not installed; Hint
// says how to install it.
type TmuxUnavailableError struct{ Hint string }

func (e *TmuxUnavailableError) Error() string {
	return "tmux is not installed on the daemon's host: " + e.Hint
}

// BadSessionRequestError is ErrBadSessionRequest with the field at fault.
type BadSessionRequestError struct{ Field, Message string }

func (e *BadSessionRequestError) Error() string { return e.Message }

// Is matches ErrBadSessionRequest.
func (e *BadSessionRequestError) Is(target error) bool { return target == ErrBadSessionRequest }

// writeTmuxError answers the tmux refusals common to every write endpoint
// and reports whether it did.
func writeTmuxError(w http.ResponseWriter, err error) bool {
	var unavailable *TmuxUnavailableError
	switch {
	case err == nil:
		return false
	case errors.As(err, &unavailable):
		writeError(w, http.StatusConflict, "tmux_unavailable", unavailable.Error())
	case errors.Is(err, ErrNotControllable):
		writeError(w, http.StatusForbidden, "not_controllable",
			"this session is not running in a tmux pane itervox may control (see sessions.tmux_control)")
	case errors.Is(err, ErrPaneGone):
		writeError(w, http.StatusConflict, "pane_gone", "the session's tmux pane is gone or no longer runs this session")
	default:
		return false
	}
	return true
}

const maxScreenLines = 2000

// handleSessionScreen captures a controllable session's pane.
// GET /api/v1/sessions/{id}/screen?lines=<n>
func (s *Server) handleSessionScreen(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "sessions are not available")
		return
	}
	id := chi.URLParam(r, "id")
	lines := 0
	if v := r.URL.Query().Get("lines"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxScreenLines {
			writeErrorWithField(w, http.StatusBadRequest, "bad_request", "lines must be between 1 and 2000", "lines")
			return
		}
		lines = n
	}
	screen, err := s.sessions.SessionScreen(id, lines)
	if writeTmuxError(w, err) {
		return
	}
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, screen)
	case errors.Is(err, ErrSessionNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no session "+id)
	default:
		writeError(w, http.StatusInternalServerError, "screen_unreadable", "the pane could not be captured")
	}
}

// handleSessionKeys presses one allowlisted key in a controllable session.
// POST /api/v1/sessions/{id}/keys {"key":"Enter"}
func (s *Server) handleSessionKeys(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "sessions are not available")
		return
	}
	id := chi.URLParam(r, "id")
	var body struct {
		Key string `json:"key"`
	}
	if err := decodeJSONBody(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	err := s.sessions.SendSessionKey(id, body.Key)
	if writeTmuxError(w, err) {
		return
	}
	switch {
	case err == nil:
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "key": body.Key})
	case errors.Is(err, ErrKeyNotAllowed):
		writeErrorWithField(w, http.StatusBadRequest, "key_not_allowed",
			"allowed keys: Enter, Escape, Up, Down, Left, Right, Tab, C-c, 1-9", "key")
	case errors.Is(err, ErrSessionNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no session "+id)
	default:
		writeError(w, http.StatusInternalServerError, "send_failed", "the key could not be sent")
	}
}

// handleStartSession starts an interactive agent CLI in a new tmux session.
// POST /api/v1/sessions {"backend":"claude","cwd":"","profile":"","prompt":""}
// 201 {"session":{...}}
func (s *Server) handleStartSession(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "sessions are not available")
		return
	}
	var req NewSessionRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	req.Backend, req.Cwd, req.Profile = strings.TrimSpace(req.Backend), strings.TrimSpace(req.Cwd), strings.TrimSpace(req.Profile)
	if req.Backend != "claude" && req.Backend != "codex" {
		writeErrorWithField(w, http.StatusBadRequest, "bad_request", `backend must be "claude" or "codex"`, "backend")
		return
	}
	if len(req.Prompt) > maxSessionMessageBytes {
		writeErrorWithField(w, http.StatusBadRequest, "bad_request", "prompt exceeds 10 KiB", "prompt")
		return
	}
	row, err := s.sessions.StartSession(req)
	if writeTmuxError(w, err) {
		return
	}
	var bad *BadSessionRequestError
	switch {
	case err == nil:
		writeJSON(w, http.StatusCreated, map[string]any{"session": row})
	case errors.As(err, &bad):
		writeErrorWithField(w, http.StatusBadRequest, "bad_request", bad.Message, bad.Field)
	case errors.Is(err, ErrUnknownProfile):
		writeErrorWithField(w, http.StatusBadRequest, "bad_request", "unknown profile", "profile")
	case errors.Is(err, ErrTooManySessions):
		writeError(w, http.StatusTooManyRequests, "too_many_sessions", "too many tmux sessions are running; kill one first")
	case errors.Is(err, ErrBusy):
		writeBusyOr404(w, err, "not_found", "")
	default:
		writeError(w, http.StatusInternalServerError, "start_failed", "the session could not be started: "+err.Error())
	}
}

// handleKillSession kills a tmux session itervox started.
// DELETE /api/v1/sessions/{id}
func (s *Server) handleKillSession(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "sessions are not available")
		return
	}
	id := chi.URLParam(r, "id")
	err := s.sessions.KillSession(id)
	if writeTmuxError(w, err) {
		return
	}
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case errors.Is(err, ErrSessionNotOwned):
		writeError(w, http.StatusForbidden, "not_owned", "only a tmux session itervox started can be killed from here")
	case errors.Is(err, ErrSessionNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no session "+id)
	default:
		writeError(w, http.StatusInternalServerError, "kill_failed", "the session could not be killed")
	}
}
