package server

import (
	"encoding/binary"
	"log/slog"
	"time"

	"golang.org/x/net/websocket"
)

// An open live terminal keeps re-checking its ticket's principal (#298
// follow-up): a token revoked, expired or rotated, or whose scope no longer
// allows the socket's mode (read-only needs operator; read-write admin, and
// sessions.terminal_write, which is load-time), ends the terminal instead
// of surviving until the browser closes it. The check runs every
// terminalRecheckInterval and at once when the token store's in-memory
// index changes (apitokens.Store.Changed: a revoke or rotate through this
// daemon at once, one by `itervox token` from another process once a read
// notices it, within the store's 200 ms check). The bootstrap token and
// server.allow_unauthenticated hold for the server's life, so their
// terminals are not watched.
//
// A terminal that lost access is closed with WebSocket close code
// terminalCloseAccessLost and a reason naming why (a terminalLost* code,
// which the dashboard maps to a sentence); the detach is fed with the same
// reason as its outcome. A scope downgrade closes the terminal rather than
// downgrading it in place: the attach client was started read-write, and
// making it read-only would mean another tmux command against the session.
// The browser reconnects with a fresh ticket in whatever mode it now gets.

// terminalRecheckInterval is how often an open terminal re-validates its
// principal. A variable so tests can shorten it; New copies it into the
// Server, so a test sets it before making its server.
var terminalRecheckInterval = defaultTerminalRecheck

const defaultTerminalRecheck = 30 * time.Second

// terminalCloseAccessLost is the WebSocket close code of a terminal ended
// because its caller lost access (4000-4999: application codes).
const terminalCloseAccessLost = 4403

// watchTerminalAccess re-checks the ticket's principal until done closes,
// and calls revoke with the reason once it no longer holds the ticket.
func (s *Server) watchTerminalAccess(t terminalTicket, done <-chan struct{}, revoke func(reason string)) {
	if t.principal.Source != principalToken {
		return
	}
	interval := s.terminalRecheck
	if interval <= 0 {
		interval = defaultTerminalRecheck
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		var changed <-chan struct{}
		if s.tokens != nil {
			changed = s.tokens.Changed()
		}
		select {
		case <-done:
			return
		case <-tick.C:
		case <-changed:
		}
		if reason := s.terminalAccessLost(t); reason != "" {
			slog.Info("server: live terminal closed: its token lost access", "session_id", t.sessionID,
				"actor", t.actor(), "write", t.write, "reason", reason)
			revoke(reason)
			return
		}
	}
}

// closeTerminalSocket sends a close frame with terminalCloseAccessLost and
// reason, then closes the socket. x/net/websocket's Close always writes a
// 1000 close frame of its own, so the write deadline is pulled in first:
// that second frame fails to write and only the connection is closed.
func closeTerminalSocket(ws *websocket.Conn, reason string) {
	payload := make([]byte, 2, 2+len(reason))
	binary.BigEndian.PutUint16(payload, terminalCloseAccessLost)
	payload = append(payload, reason...) // the codes are ASCII, far under the 123-byte limit
	closeFrame := websocket.Codec{Marshal: func(any) ([]byte, byte, error) {
		return payload, websocket.CloseFrame, nil
	}}
	_ = ws.SetWriteDeadline(time.Now().Add(time.Second))
	_ = closeFrame.Send(ws, nil)
	_ = ws.SetWriteDeadline(time.Now())
	_ = ws.Close()
}
