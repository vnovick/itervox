package server

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/websocket"

	"github.com/vnovick/itervox/internal/apitokens"
)

// fakeTerminalSessions answers the terminal methods; every other
// SessionsClient method is unused here (the embedded nil interface).
type fakeTerminalSessions struct {
	SessionsClient
	status    TmuxStatusRow
	checkErr  error
	attachErr error

	mu       sync.Mutex
	attached []TerminalAttachRequest
	conn     *fakeTerminalConn
}

func (f *fakeTerminalSessions) TmuxStatus() TmuxStatusRow  { return f.status }
func (f *fakeTerminalSessions) CheckTerminal(string) error { return f.checkErr }
func (f *fakeTerminalSessions) AttachTerminal(req TerminalAttachRequest) (TerminalConn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.attachErr != nil {
		return nil, f.attachErr
	}
	f.attached = append(f.attached, req)
	f.conn = newFakeTerminalConn()
	return f.conn, nil
}

func (f *fakeTerminalSessions) attachedReqs() []TerminalAttachRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]TerminalAttachRequest(nil), f.attached...)
}

func (f *fakeTerminalSessions) lastConn() *fakeTerminalConn {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conn
}

// fakeTerminalConn draws "hello" and then whatever out carries; it records
// what was typed and the resizes.
type fakeTerminalConn struct {
	out    chan []byte
	mu     sync.Mutex
	typed  []byte
	sizes  [][2]int
	closed chan struct{}
	once   sync.Once
}

func newFakeTerminalConn() *fakeTerminalConn {
	c := &fakeTerminalConn{out: make(chan []byte, 8), closed: make(chan struct{})}
	c.out <- []byte("hello")
	return c
}

func (c *fakeTerminalConn) Read(p []byte) (int, error) {
	select {
	case b := <-c.out:
		return copy(p, b), nil
	case <-c.closed:
		return 0, io.EOF
	}
}

func (c *fakeTerminalConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.typed = append(c.typed, p...)
	return len(p), nil
}

func (c *fakeTerminalConn) Resize(cols, rows int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sizes = append(c.sizes, [2]int{cols, rows})
	return nil
}

func (c *fakeTerminalConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func (c *fakeTerminalConn) snapshot() (string, [][2]int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return string(c.typed), append([][2]int(nil), c.sizes...)
}

func (c *fakeTerminalConn) isClosed() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

// terminalRig is a token-mode server (a stored token of every scope) with
// the fake sessions client.
type terminalRig struct {
	scopedServer
	sessions *fakeTerminalSessions
}

func newTerminalRig(t *testing.T, terminalWrite bool) terminalRig {
	t.Helper()
	ss := newScopedServer(t)
	f := &fakeTerminalSessions{status: TmuxStatusRow{Available: true, Terminal: true, TerminalWrite: terminalWrite}}
	ss.srv.sessions = f
	ss.srv.allowedOrigins = []string{"https://dash.example"}
	return terminalRig{scopedServer: ss, sessions: f}
}

// ticket POSTs a ticket request with token and returns the response.
func (r terminalRig) ticket(t *testing.T, token, mode string) (*httptest.ResponseRecorder, terminalTicketResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/itervox-abc123/terminal/ticket", strings.NewReader(`{"mode":"`+mode+`"}`))
	req.Host = "127.0.0.1:8090"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	r.srv.ServeHTTP(rec, req)
	var body terminalTicketResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec, body
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	return body.Error.Code
}

// #298: a ticket is single-use, expires, and is bound to its session.
func TestTerminalTicketIsSingleUseExpiresAndIsBoundToItsSession(t *testing.T) {
	store := newTerminalTickets()
	now := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	p := Principal{Scope: apitokens.ScopeOperator, Source: principalToken, TokenID: "tok1"}

	secret, expires, err := store.mint("s1", false, p)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(secret, terminalTicketPrefix))
	assert.Equal(t, now.Add(terminalTicketTTL), expires)
	got, err := store.redeem(secret, "s1")
	require.NoError(t, err)
	assert.Equal(t, "s1", got.sessionID)
	assert.Equal(t, p, got.principal)
	_, err = store.redeem(secret, "s1")
	assert.ErrorIs(t, err, errTicketInvalid, "a ticket redeems once")

	secret, _, err = store.mint("s1", true, p)
	require.NoError(t, err)
	_, err = store.redeem(secret, "s2")
	assert.ErrorIs(t, err, errTicketWrongSession)
	_, err = store.redeem(secret, "s1")
	assert.ErrorIs(t, err, errTicketInvalid, "a ticket tried on the wrong session is spent")

	secret, _, err = store.mint("s1", false, p)
	require.NoError(t, err)
	now = now.Add(terminalTicketTTL)
	_, err = store.redeem(secret, "s1")
	assert.ErrorIs(t, err, errTicketExpired)

	_, err = store.redeem("not-a-ticket", "s1")
	assert.ErrorIs(t, err, errTicketInvalid)

	for range maxTerminalTickets {
		_, _, err = store.mint("s1", false, p)
		require.NoError(t, err)
	}
	_, _, err = store.mint("s1", false, p)
	assert.ErrorIs(t, err, errTicketStoreFull)
	now = now.Add(terminalTicketTTL)
	_, _, err = store.mint("s1", false, p)
	assert.NoError(t, err, "expired tickets are pruned")
}

// #298 scopes: a viewer gets no terminal; an operator a read-only one; a
// read-write one needs admin AND sessions.terminal_write.
func TestTerminalTicketScopes(t *testing.T) {
	off := newTerminalRig(t, false)
	rec, _ := off.ticket(t, off.secrets[apitokens.ScopeViewer], "read")
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "insufficient_scope", errCode(t, rec))

	rec, body := off.ticket(t, off.secrets[apitokens.ScopeOperator], "read")
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, "read", body.Mode)
	assert.Contains(t, body.URL, "/api/v1/sessions/itervox-abc123/terminal?ticket=itvt_")

	rec, _ = off.ticket(t, off.secrets[apitokens.ScopeOperator], "write")
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "insufficient_scope", errCode(t, rec))
	assert.Equal(t, "admin", rec.Header().Get(RequiredScopeHeader))

	rec, _ = off.ticket(t, off.secrets[apitokens.ScopeAdmin], "write")
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "terminal_write_disabled", errCode(t, rec), "admin without the opt-in stays read-only")

	rec, body = off.ticket(t, off.secrets[apitokens.ScopeAdmin], "read")
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "read", body.Mode)

	on := newTerminalRig(t, true)
	rec, body = on.ticket(t, on.secrets[apitokens.ScopeAdmin], "write")
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, "write", body.Mode)
	rec, _ = on.ticket(t, on.secrets[apitokens.ScopeOperator], "write")
	assert.Equal(t, http.StatusForbidden, rec.Code, "the opt-in never lifts an operator to read-write")

	rec, _ = on.ticket(t, on.secrets[apitokens.ScopeOperator], "sideways")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// The ticket route refuses what the session or platform cannot do.
func TestTerminalTicketRefusals(t *testing.T) {
	r := newTerminalRig(t, false)
	r.sessions.status.Terminal = false
	rec, _ := r.ticket(t, r.secrets[apitokens.ScopeAdmin], "read")
	assert.Equal(t, http.StatusNotImplemented, rec.Code)
	assert.Equal(t, "terminal_unsupported", errCode(t, rec))

	r.sessions.status.Terminal = true
	for err, want := range map[error]string{
		ErrSessionNotFound: "not_found",
		ErrNotControllable: "not_controllable",
		ErrPaneGone:        "pane_gone",
		&TmuxUnavailableError{Hint: "install it"}: "tmux_unavailable",
	} {
		r.sessions.checkErr = err
		rec, _ = r.ticket(t, r.secrets[apitokens.ScopeAdmin], "read")
		assert.Equal(t, want, errCode(t, rec), "%v", err)
	}
}

// dialTerminal opens the socket of a ticket through a real listener.
func dialTerminal(t *testing.T, ts *httptest.Server, path, origin, bearer string) (*websocket.Conn, error) {
	t.Helper()
	if origin == "" {
		origin = ts.URL // same origin (the x/net client always sends one)
	}
	cfg, err := websocket.NewConfig("ws"+strings.TrimPrefix(ts.URL, "http")+path, origin)
	require.NoError(t, err)
	if bearer != "" {
		cfg.Header = http.Header{"Authorization": {"Bearer " + bearer}}
	}
	return websocket.DialConfig(cfg)
}

func resizeFrame(cols, rows int) []byte {
	b := []byte{terminalOpResize, 0, 0, 0, 0}
	binary.BigEndian.PutUint16(b[1:3], uint16(cols))
	binary.BigEndian.PutUint16(b[3:5], uint16(rows))
	return b
}

func readUntil(t *testing.T, ws *websocket.Conn, want string) {
	t.Helper()
	require.NoError(t, ws.SetReadDeadline(time.Now().Add(5*time.Second)))
	var got []byte
	for !strings.Contains(string(got), want) {
		var frame []byte
		require.NoError(t, websocket.Message.Receive(ws, &frame), "got so far %q", got)
		got = append(got, frame...)
	}
}

// End to end over a real socket: an operator's read-only terminal streams
// output and resizes, but nothing typed reaches the session; an admin's
// read-write terminal types. Closing the socket detaches.
func TestTerminalSocketStreamsAndHonoursTheMode(t *testing.T) {
	r := newTerminalRig(t, true)
	ts := httptest.NewServer(r.srv)
	defer ts.Close()

	rec, body := r.ticket(t, r.secrets[apitokens.ScopeOperator], "read")
	require.Equal(t, http.StatusCreated, rec.Code)
	ws, err := dialTerminal(t, ts, body.URL+"&cols=100&rows=30", "https://dash.example", "")
	require.NoError(t, err)
	readUntil(t, ws, "hello")
	require.NoError(t, websocket.Message.Send(ws, append([]byte{terminalOpInput}, "rm -rf /\r"...)))
	require.NoError(t, websocket.Message.Send(ws, resizeFrame(90, 25)))
	conn := r.sessions.lastConn()
	require.Eventually(t, func() bool { _, sizes := conn.snapshot(); return len(sizes) == 1 }, 5*time.Second, 10*time.Millisecond)
	typed, sizes := conn.snapshot()
	assert.Empty(t, typed, "a read-only terminal types nothing")
	assert.Equal(t, [][2]int{{90, 25}}, sizes)
	assert.Equal(t, TerminalAttachRequest{ID: "itervox-abc123", Write: false, Cols: 100, Rows: 30, Actor: "dashboard:" + r.tokenID(t, apitokens.ScopeOperator)},
		r.sessions.attachedReqs()[0])
	require.NoError(t, ws.Close())
	require.Eventually(t, conn.isClosed, 5*time.Second, 10*time.Millisecond, "closing the socket detaches")
	require.Eventually(t, func() bool { return r.srv.terminalsOpen.Load() == 0 }, 5*time.Second, 10*time.Millisecond)

	_, body = r.ticket(t, r.secrets[apitokens.ScopeAdmin], "write")
	ws, err = dialTerminal(t, ts, body.URL, "", "")
	require.NoError(t, err)
	defer func() { _ = ws.Close() }()
	readUntil(t, ws, "hello")
	conn = r.sessions.lastConn()
	require.NoError(t, websocket.Message.Send(ws, append([]byte{terminalOpInput}, "ls\r"...)))
	require.Eventually(t, func() bool { typed, _ := conn.snapshot(); return typed == "ls\r" }, 5*time.Second, 10*time.Millisecond)

	// A malformed frame ends the socket (and detaches).
	require.NoError(t, websocket.Message.Send(ws, []byte{0x7f}))
	require.Eventually(t, conn.isClosed, 5*time.Second, 10*time.Millisecond)
}

func (r terminalRig) tokenID(t *testing.T, scope apitokens.Scope) string {
	t.Helper()
	tok, ok := r.store.Authenticate(r.secrets[scope])
	require.True(t, ok)
	return tok.ID
}

// upgradeGet sends a WebSocket handshake by hand and returns the answer
// (a refusal is an HTTP status before any upgrade). hdr values replace the
// handshake's headers; "" deletes one.
func upgradeGet(t *testing.T, ts *httptest.Server, path string, hdr map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	require.NoError(t, err)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	return resp
}

// The socket refuses a used ticket, a foreign Origin, another principal's
// bearer, a revoked token, and a plain (non-upgrade) GET — without
// spending the ticket on the last.
func TestTerminalSocketRefusals(t *testing.T) {
	r := newTerminalRig(t, false)
	ts := httptest.NewServer(r.srv)
	defer ts.Close()
	get := func(path string, hdr map[string]string) *http.Response { return upgradeGet(t, ts, path, hdr) }
	opTok := r.secrets[apitokens.ScopeOperator]

	_, body := r.ticket(t, opTok, "read")
	assert.Equal(t, http.StatusBadRequest, get(body.URL, map[string]string{"Upgrade": "", "Connection": ""}).StatusCode)
	assert.Equal(t, http.StatusForbidden, get(body.URL, map[string]string{"Origin": "https://evil.example"}).StatusCode)
	assert.Equal(t, http.StatusForbidden, get(body.URL, map[string]string{"Authorization": "Bearer " + r.secrets[apitokens.ScopeAdmin]}).StatusCode,
		"another principal's bearer")
	assert.Equal(t, http.StatusUnauthorized, get(body.URL, nil).StatusCode, "the ticket was spent by the wrong-principal attempt")

	_, body = r.ticket(t, opTok, "read")
	assert.Equal(t, http.StatusUnauthorized, get(strings.Replace(body.URL, "itvt_", "itvt_x", 1), nil).StatusCode)
	assert.Equal(t, http.StatusForbidden, get(strings.Replace(body.URL, "itervox-abc123", "itervox-zzz999", 1), nil).StatusCode,
		"a ticket is bound to its session")

	_, body = r.ticket(t, opTok, "read")
	_, err := r.store.Revoke(r.tokenID(t, apitokens.ScopeOperator), "test")
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, get(body.URL, nil).StatusCode, "a revoked token's ticket is refused")
	assert.Empty(t, r.sessions.attachedReqs(), "nothing was attached")

	_, err = dialTerminal(t, ts, "/api/v1/sessions/itervox-abc123/terminal?ticket=itvt_nope", "", "")
	assert.Error(t, err)
}

// The attach's errors (including its own second check of
// sessions.terminal_write) map to HTTP answers before any upgrade.
func TestTerminalSocketMapsAttachErrors(t *testing.T) {
	r := newTerminalRig(t, true)
	ts := httptest.NewServer(r.srv)
	defer ts.Close()
	for err, want := range map[error]int{
		ErrPaneGone:              http.StatusConflict,
		ErrTerminalWriteDisabled: http.StatusForbidden,
		ErrTerminalUnsupported:   http.StatusNotImplemented,
		errors.New("boom"):       http.StatusInternalServerError,
	} {
		r.sessions.attachErr = err
		_, body := r.ticket(t, r.secrets[apitokens.ScopeAdmin], "write")
		assert.Equal(t, want, upgradeGet(t, ts, body.URL, nil).StatusCode, "%v", err)
	}
}

// Shutdown (a reload or exit) closes every open terminal.
func TestShutdownClosesOpenTerminals(t *testing.T) {
	r := newTerminalRig(t, false)
	ts := httptest.NewServer(r.srv)
	defer ts.Close()
	_, body := r.ticket(t, r.secrets[apitokens.ScopeOperator], "read")
	ws, err := dialTerminal(t, ts, body.URL, "", "")
	require.NoError(t, err)
	defer func() { _ = ws.Close() }()
	readUntil(t, ws, "hello")
	r.srv.Shutdown()
	require.Eventually(t, r.sessions.lastConn().isClosed, 5*time.Second, 10*time.Millisecond)
}

// The route table declares the socket as a ticket route and the ticket
// POST as operator, and the hub-facing table says so.
func TestTerminalRoutesAreDeclared(t *testing.T) {
	assert.Equal(t, accessTicket, routeScopes["GET /api/v1/sessions/{id}/terminal"])
	assert.Equal(t, accessOperator, routeScopes["POST /api/v1/sessions/{id}/terminal/ticket"])
	assert.Equal(t, "ticket", RouteScopes()["GET /api/v1/sessions/{id}/terminal"])
	_, declared := scopedRouteScope(http.MethodGet, "/api/v1/sessions/{id}/terminal")
	assert.False(t, declared, "a ticket route is never a bearer-scoped route")
}
