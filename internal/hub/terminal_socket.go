package hub

import (
	"bufio"
	"context"
	"crypto/sha1" //nolint:gosec // RFC 6455's Sec-WebSocket-Accept is defined over SHA-1
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/vnovick/itervox/internal/apitokens"
	"github.com/vnovick/itervox/internal/cors"
)

// websocketGUID is RFC 6455's handshake constant.
const websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// memberHandshakeTimeout bounds dialling the member and its 101.
const memberHandshakeTimeout = 10 * time.Second

// handleTerminalSocket serves GET /hub/p/{project}/api/v1/sessions/{id}/terminal
// (terminal.go): a brokered ticket's read-only pass-through.
func (h *Hub) handleTerminalSocket(w http.ResponseWriter, r *http.Request) {
	project, id := chi.URLParam(r, "project"), chi.URLParam(r, "id")
	secret := r.URL.Query().Get("ticket")
	if secret == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "the terminal socket needs a ticket the hub brokered")
		return
	}
	if r.URL.RawPath != "" || id == "" || id == "." || id == ".." {
		writeError(w, http.StatusBadRequest, "bad_path", "the path must be a plain /api/v1/... path")
		return
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || r.Header.Get("Sec-WebSocket-Version") != "13" || !validWebSocketKey(key) {
		writeError(w, http.StatusBadRequest, "websocket_required", "this route is a WebSocket")
		return
	}
	if !cors.WebSocketOriginAllowed(r, h.cfg.AllowedOrigins) {
		writeError(w, http.StatusForbidden, "origin_not_allowed", "this origin is not the hub's own or in server.allowed_origins")
		return
	}
	t, err := h.terminals.redeem(secret, project, id)
	switch {
	case errors.Is(err, errHubTicketExpired):
		writeError(w, http.StatusUnauthorized, "ticket_expired", "the terminal ticket has expired; request a new one")
		return
	case errors.Is(err, errHubTicketWrong):
		writeError(w, http.StatusForbidden, "ticket_wrong_session", "the terminal ticket is for another project or session")
		return
	case err != nil:
		writeError(w, http.StatusUnauthorized, "ticket_invalid", "missing, unknown or already used terminal ticket")
		return
	}
	m, ok := h.members.get(project)
	if !ok || m.rec.Workflow != t.workflow || !m.rec.TokenRequired {
		writeError(w, http.StatusForbidden, "ticket_wrong_session", "the project behind this terminal ticket has changed")
		return
	}
	if reason := h.terminalAccessLost(t); reason != "" {
		writeError(w, http.StatusForbidden, "ticket_wrong_principal", "the hub token that asked for this terminal no longer holds it ("+reason+")")
		return
	}
	if h.terminalsOpen.Add(1) > maxHubTerminals {
		h.terminalsOpen.Add(-1)
		writeError(w, http.StatusTooManyRequests, "too_many_terminals", "too many live terminals are open through the hub")
		return
	}
	defer h.terminalsOpen.Add(-1)
	if h.life.Err() != nil {
		writeError(w, http.StatusServiceUnavailable, "hub_stopping", "the hub is stopping")
		return
	}

	memberSecret, err := h.client.token(r.Context(), m.rec, apitokens.ScopeOperator)
	if err != nil || memberSecret == "" {
		writeError(w, http.StatusServiceUnavailable, "member_token_unavailable", "the hub has no token for this project")
		return
	}
	q := url.Values{"ticket": {secret}}
	for _, k := range []string{"cols", "rows"} {
		if v := r.URL.Query().Get(k); v != "" {
			q.Set(k, v)
		}
	}
	path := "/api/v1/sessions/" + url.PathEscape(id) + "/terminal?" + q.Encode()
	mconn, mr, resp, err := dialMemberTerminal(r.Context(), m.rec.URL, path, key, memberSecret)
	if err != nil {
		slog.Warn("hub: live terminal: the member's socket did not open", "project", project, "error", err)
		writeError(w, http.StatusBadGateway, "member_unreachable", "the project's daemon did not open the terminal")
		return
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		_ = mconn.Close()
		relayMemberRefusal(w, resp)
		return
	}
	bconn, brw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		_ = mconn.Close()
		writeError(w, http.StatusInternalServerError, "terminal_failed", "the terminal could not be attached")
		return
	}
	_ = bconn.SetDeadline(time.Time{})
	_, err = fmt.Fprintf(brw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n",
		websocketAccept(key))
	if err == nil {
		err = brw.Flush()
	}
	if err != nil {
		_ = mconn.Close()
		_ = bconn.Close()
		return
	}
	slog.Info("hub: live terminal opened (read-only)", "project", project, "session_id", id,
		"caller", t.caller.Name, "hub_token", t.caller.TokenID)
	rl := newTerminalRelay(bconn, brw.Reader, mconn, mr)
	rl.run(r.Context(), h, t)
	slog.Info("hub: live terminal closed", "project", project, "session_id", id, "caller", t.caller.Name)
}

// validWebSocketKey: a base64 nonce of 16 bytes (RFC 6455 §4.1).
func validWebSocketKey(key string) bool {
	b, err := base64.StdEncoding.DecodeString(key)
	return err == nil && len(b) == 16
}

// websocketAccept is the Sec-WebSocket-Accept of key.
func websocketAccept(key string) string {
	sum := sha1.Sum([]byte(key + websocketGUID)) //nolint:gosec // RFC 6455
	return base64.StdEncoding.EncodeToString(sum[:])
}

// dialMemberTerminal opens the member's terminal socket at path with the
// browser's handshake key (so the member's accept matches it) and the
// hub's member token. Nothing of the browser's request but the key, the
// session and the size reaches the member: no Origin, Cookie, Referer or
// credential. resp is the member's handshake answer; on a 101 the caller
// owns conn and r.
func dialMemberTerminal(ctx context.Context, base, path, key, secret string) (net.Conn, *bufio.Reader, *http.Response, error) {
	target, err := url.Parse(base)
	if err != nil || target.Host == "" {
		return nil, nil, nil, errors.New("hub: the member's record has no usable URL")
	}
	host := target.Host
	if target.Port() == "" {
		port := "80"
		if target.Scheme == "https" {
			port = "443"
		}
		host = net.JoinHostPort(target.Hostname(), port)
	}
	dctx, cancel := context.WithTimeout(ctx, memberHandshakeTimeout)
	defer cancel()
	var conn net.Conn
	switch target.Scheme {
	case "http":
		conn, err = (&net.Dialer{}).DialContext(dctx, "tcp", host)
	case "https":
		conn, err = (&tls.Dialer{Config: &tls.Config{ServerName: target.Hostname(), MinVersion: tls.VersionTLS12}}).DialContext(dctx, "tcp", host)
	default:
		return nil, nil, nil, fmt.Errorf("hub: unsupported member URL scheme %q", target.Scheme)
	}
	if err != nil {
		return nil, nil, nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(memberHandshakeTimeout))
	_, err = fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\nAuthorization: Bearer %s\r\n\r\n",
		path, target.Host, key, secret)
	if err != nil {
		_ = conn.Close()
		return nil, nil, nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		_ = conn.Close()
		return nil, nil, nil, err
	}
	if resp.StatusCode == http.StatusSwitchingProtocols {
		if resp.Header.Get("Sec-WebSocket-Accept") != websocketAccept(key) {
			_ = conn.Close()
			return nil, nil, nil, errors.New("hub: the member's WebSocket handshake did not match")
		}
		_ = conn.SetDeadline(time.Time{})
	}
	return conn, br, resp, nil
}

// relayMemberRefusal answers the browser with the member's refusal of the
// socket: its status and JSON error. The member's socket route has no
// bearer auth (its 401s are about the ticket), and a page never sees a
// refused WebSocket's status, so nothing here can log the dashboard out.
func relayMemberRefusal(w http.ResponseWriter, resp *http.Response) {
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}
