package server

import (
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/net/websocket"

	"github.com/vnovick/itervox/internal/apitokens"
	"github.com/vnovick/itervox/internal/cors"
)

// The live terminal of a tmux session (#298): `tmux attach-session` in a
// PTY the daemon owns, streamed over a WebSocket and rendered by xterm.js.
//
//   - POST /api/v1/sessions/{id}/terminal/ticket {"mode":"read"|"write"}
//     (operator) mints a ticket (terminal_tickets.go). "write" needs the
//     admin scope AND sessions.terminal_write; a viewer gets no terminal.
//   - GET /api/v1/sessions/{id}/terminal?ticket=…&cols=…&rows=… is the
//     socket (access class "ticket": no bearer header; the hub refuses
//     it). Its Origin is checked against server.allowed_origins.
//
// Wire format. Daemon → browser: binary frames, the raw bytes tmux draws.
// Browser → daemon: binary frames whose first byte is the op:
// 0x00 + bytes typed (dropped for a read-only attach; the daemon never
// types into one), 0x01 + cols (uint16, big-endian) + rows (uint16) for a
// resize. Anything else closes the socket. Terminal bytes are never logged
// or fed; the attach and the detach are (by the SessionsClient).

// Wire ops of browser → daemon frames.
const (
	terminalOpInput  byte = 0x00
	terminalOpResize byte = 0x01
)

// Limits of the live terminal.
const (
	// maxTerminals bounds the attachments one daemon serves at once.
	maxTerminals = 16
	// maxTerminalFrame bounds one browser → daemon frame.
	maxTerminalFrame = 64 << 10
	// terminalReadBuf is one PTY read (one daemon → browser frame at most).
	terminalReadBuf = 32 << 10
)

// Errors of the live terminal.
var (
	// ErrTerminalUnsupported: the platform has no live terminal (Windows).
	ErrTerminalUnsupported = errors.New("server: the live terminal is not supported on this platform")
	// ErrTerminalWriteDisabled: a read-write attach while
	// sessions.terminal_write is off.
	ErrTerminalWriteDisabled = errors.New("server: typing into the live terminal is off (sessions.terminal_write)")
)

// TerminalAttachRequest is one attach, already authorized.
type TerminalAttachRequest struct {
	ID         string
	Write      bool
	Cols, Rows int
	// Actor names the caller for the audit log and the feed (never a
	// secret): "dashboard:<token id>" or "dashboard:<source>".
	Actor string
}

// TerminalConn is a live attachment: Read what tmux draws, Write keys
// (read-write only), Resize the PTY. Close detaches (the tmux session keeps
// running); it is idempotent and records the detach. CloseWithReason is
// Close when the daemon ends the terminal because its caller lost access
// (the reason is one of the terminalLost* codes, recorded with the
// detach); whichever of the two runs first records it.
type TerminalConn interface {
	io.ReadWriter
	Resize(cols, rows int) error
	Close() error
	CloseWithReason(reason string) error
}

// terminalTicketResponse is the POST …/terminal/ticket body.
type terminalTicketResponse struct {
	Ticket    string    `json:"ticket"`
	Mode      string    `json:"mode"`
	ExpiresAt time.Time `json:"expiresAt"`
	// URL is the socket's path and query (without scheme and host).
	URL string `json:"url"`
}

// writeTerminalError answers the refusals common to both routes and
// reports whether it did.
func writeTerminalError(w http.ResponseWriter, err error, id string) bool {
	if writeTmuxError(w, err) {
		return true
	}
	switch {
	case err == nil:
		return false
	case errors.Is(err, ErrTerminalUnsupported):
		writeError(w, http.StatusNotImplemented, "terminal_unsupported",
			"the live terminal is not supported on this daemon's platform (it needs tmux, which does not run on Windows)")
	case errors.Is(err, ErrTerminalWriteDisabled):
		writeError(w, http.StatusForbidden, "terminal_write_disabled",
			"typing into the live terminal is off (sessions.terminal_write); a read-only terminal is available")
	case errors.Is(err, ErrSessionNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no session "+id)
	case errors.Is(err, ErrSessionNotOwned):
		writeError(w, http.StatusForbidden, "not_controllable",
			"this session is not running in a tmux pane itervox may control (see sessions.tmux_control)")
	default:
		writeError(w, http.StatusInternalServerError, "terminal_failed", "the terminal could not be attached")
	}
	return true
}

// handleTerminalTicket mints a single-use socket ticket for the session.
// POST /api/v1/sessions/{id}/terminal/ticket {"mode":"read"|"write"}
func (s *Server) handleTerminalTicket(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "sessions are not available")
		return
	}
	id := chi.URLParam(r, "id")
	var body struct {
		Mode string `json:"mode"`
	}
	if r.ContentLength != 0 {
		if err := decodeJSONBody(w, r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid body")
			return
		}
	}
	write := false
	switch body.Mode {
	case "", "read":
	case "write":
		write = true
	default:
		writeErrorWithField(w, http.StatusBadRequest, "bad_request", `mode must be "read" or "write"`, "mode")
		return
	}
	status := s.sessions.TmuxStatus()
	if !status.Terminal {
		writeTerminalError(w, ErrTerminalUnsupported, id)
		return
	}
	p, _ := principalFrom(r.Context())
	if write {
		if !p.Scope.Allows(apitokens.ScopeAdmin) {
			writeInsufficientScope(w, apitokens.ScopeAdmin, p.Scope)
			return
		}
		if !status.TerminalWrite {
			writeTerminalError(w, ErrTerminalWriteDisabled, id)
			return
		}
	}
	if s.terminalsOpen.Load() >= maxTerminals {
		writeError(w, http.StatusTooManyRequests, "too_many_terminals", "too many live terminals are open on this daemon")
		return
	}
	if writeTerminalError(w, s.sessions.CheckTerminal(id), id) {
		return
	}
	secret, expires, err := s.terminalTickets.mint(id, write, p, mintingTokenHash(r, p))
	if err != nil {
		writeError(w, http.StatusTooManyRequests, "too_many_terminals", "too many terminal tickets are pending; try again shortly")
		return
	}
	mode := "read"
	if write {
		mode = "write"
	}
	q := url.Values{"ticket": {secret}}
	writeJSON(w, http.StatusCreated, terminalTicketResponse{Ticket: secret, Mode: mode, ExpiresAt: expires,
		URL: "/api/v1/sessions/" + url.PathEscape(id) + "/terminal?" + q.Encode()})
}

// handleSessionTerminal is the live terminal's WebSocket.
// GET /api/v1/sessions/{id}/terminal?ticket=…&cols=…&rows=…
func (s *Server) handleSessionTerminal(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "sessions are not available")
		return
	}
	id := chi.URLParam(r, "id")
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		writeError(w, http.StatusBadRequest, "websocket_required", "this route is a WebSocket")
		return
	}
	if !cors.WebSocketOriginAllowed(r, s.allowedOrigins) {
		writeError(w, http.StatusForbidden, "origin_not_allowed", "this origin is not in server.allowed_origins")
		return
	}
	ticket, err := s.terminalTickets.redeem(r.URL.Query().Get("ticket"), id)
	switch {
	case errors.Is(err, errTicketExpired):
		writeError(w, http.StatusUnauthorized, "ticket_expired", "the terminal ticket has expired; request a new one")
		return
	case errors.Is(err, errTicketWrongSession):
		writeError(w, http.StatusForbidden, "ticket_wrong_session", "the terminal ticket is for another session")
		return
	case err != nil:
		writeError(w, http.StatusUnauthorized, "ticket_invalid", "missing, unknown or already used terminal ticket")
		return
	}
	if !s.ticketPrincipalMatches(r, ticket) {
		writeError(w, http.StatusForbidden, "ticket_wrong_principal",
			"the terminal ticket's token is no longer valid for this terminal, or another token presented it")
		return
	}
	if ticket.write && !s.sessions.TmuxStatus().TerminalWrite {
		writeTerminalError(w, ErrTerminalWriteDisabled, id)
		return
	}
	if s.terminalsOpen.Add(1) > maxTerminals {
		s.terminalsOpen.Add(-1)
		writeError(w, http.StatusTooManyRequests, "too_many_terminals", "too many live terminals are open on this daemon")
		return
	}
	defer s.terminalsOpen.Add(-1)
	cols, _ := strconv.Atoi(r.URL.Query().Get("cols"))
	rows, _ := strconv.Atoi(r.URL.Query().Get("rows"))
	conn, err := s.sessions.AttachTerminal(TerminalAttachRequest{ID: id, Write: ticket.write, Cols: cols, Rows: rows,
		Actor: ticket.principal.actor()})
	if writeTerminalError(w, err, id) {
		return
	}
	defer func() { _ = conn.Close() }()
	ws := websocket.Server{
		// The Origin was checked above; the ticket is the authentication.
		Handshake: func(*websocket.Config, *http.Request) error { return nil },
		Handler: func(ws *websocket.Conn) {
			ws.MaxPayloadBytes = maxTerminalFrame
			s.pumpTerminal(ws, conn, ticket)
		},
	}
	ws.ServeHTTP(w, r)
}

// ticketPrincipalMatches re-checks the principal that minted the ticket:
// still valid and still holding the scope the mode needs. A socket request
// that also carries a bearer token must carry the same principal's.
func (s *Server) ticketPrincipalMatches(r *http.Request, t terminalTicket) bool {
	if r.Header.Get("Authorization") != "" {
		p, ok := s.authenticate(r)
		if !ok || !samePrincipal(p, t.principal) {
			return false
		}
	}
	return s.terminalAccessLost(t) == ""
}

// mintingTokenHash is the hash of the stored token that authenticated r
// ("" for any other principal).
func mintingTokenHash(r *http.Request, p Principal) string {
	if p.Source != principalToken {
		return ""
	}
	secret, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || secret == "" {
		return ""
	}
	return apitokens.HashSecret(secret)
}

// pumpTerminal copies the PTY to the socket and the socket to the PTY
// until either side ends, the server shuts down, or the ticket's principal
// loses access (watchTerminalAccess).
func (s *Server) pumpTerminal(ws *websocket.Conn, conn TerminalConn, ticket terminalTicket) {
	write := ticket.write
	done := make(chan struct{})
	var stop sync.Once
	end := func() {
		stop.Do(func() {
			close(done)
			_ = ws.Close()
			_ = conn.Close()
		})
	}
	defer end()
	revoke := func(reason string) {
		stop.Do(func() {
			close(done)
			closeTerminalSocket(ws, reason)
			_ = conn.CloseWithReason(reason)
		})
	}
	go func() {
		defer recoverServerGoroutine("terminal-access", func(any) { end() })
		s.watchTerminalAccess(ticket, done, revoke)
	}()
	go func() {
		defer recoverServerGoroutine("terminal-output", func(any) { end() })
		defer end()
		buf := make([]byte, terminalReadBuf)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				if werr := websocket.Message.Send(ws, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		defer recoverServerGoroutine("terminal-shutdown", nil)
		select {
		case <-s.streamsDone:
			end()
		case <-done:
		}
	}()
	for {
		var frame []byte
		if err := websocket.Message.Receive(ws, &frame); err != nil {
			return
		}
		if !applyTerminalFrame(conn, frame, write) {
			return
		}
	}
}

// applyTerminalFrame applies one browser → daemon frame. false ends the
// socket (a malformed frame or a write the PTY refused).
func applyTerminalFrame(conn TerminalConn, frame []byte, write bool) bool {
	if len(frame) == 0 {
		return false
	}
	switch frame[0] {
	case terminalOpInput:
		if !write || len(frame) == 1 {
			return true // a read-only attach types nothing
		}
		_, err := conn.Write(frame[1:])
		return err == nil
	case terminalOpResize:
		if len(frame) != 5 {
			return false
		}
		cols := int(binary.BigEndian.Uint16(frame[1:3]))
		rows := int(binary.BigEndian.Uint16(frame[3:5]))
		if err := conn.Resize(cols, rows); err != nil {
			slog.Debug("server: terminal resize failed", "error", err)
		}
		return true
	}
	return false
}
