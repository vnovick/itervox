package hub

import (
	"bufio"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/apitokens"
	"github.com/vnovick/itervox/internal/server"
)

// termSessions is a member's sessions client with a terminal: every other
// SessionsClient method is unused (the embedded nil interface).
type termSessions struct {
	server.SessionsClient
	mu       sync.Mutex
	attached []server.TerminalAttachRequest
	conns    []*termConn
}

func (f *termSessions) TmuxStatus() server.TmuxStatusRow {
	return server.TmuxStatusRow{Available: true, Terminal: true, TerminalWrite: true}
}
func (f *termSessions) CheckTerminal(string) error { return nil }
func (f *termSessions) AttachTerminal(req server.TerminalAttachRequest) (server.TerminalConn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attached = append(f.attached, req)
	c := &termConn{out: make(chan []byte, 8), closed: make(chan struct{})}
	c.out <- []byte("hello")
	f.conns = append(f.conns, c)
	return c, nil
}

func (f *termSessions) reqs() []server.TerminalAttachRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]server.TerminalAttachRequest(nil), f.attached...)
}

func (f *termSessions) last() *termConn {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conns[len(f.conns)-1]
}

// termConn draws "hello"; it records what was typed and the resizes.
type termConn struct {
	out    chan []byte
	mu     sync.Mutex
	typed  []byte
	sizes  [][2]int
	closed chan struct{}
	once   sync.Once
}

func (c *termConn) Read(p []byte) (int, error) {
	select {
	case b := <-c.out:
		return copy(p, b), nil
	case <-c.closed:
		return 0, io.EOF
	}
}

func (c *termConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.typed = append(c.typed, p...)
	return len(p), nil
}

func (c *termConn) Resize(cols, rows int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sizes = append(c.sizes, [2]int{cols, rows})
	return nil
}

func (c *termConn) Close() error { return c.CloseWithReason("") }
func (c *termConn) CloseWithReason(string) error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func (c *termConn) snapshot() (string, [][2]int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return string(c.typed), append([][2]int(nil), c.sizes...)
}

// termMember is a real daemon API with a terminal-capable sessions client.
type termMember struct {
	*fakeMember
	sessions *termSessions
}

func newTermMember(t *testing.T, name string) termMember {
	t.Helper()
	m := newFakeMember(t, name, time.Now().Add(-time.Hour))
	ss := &termSessions{}
	m.srv = server.New(server.Config{
		Snapshot:    func() server.StateSnapshot { return server.StateSnapshot{ProjectName: name} },
		RefreshChan: make(chan struct{}, 1),
		APIToken:    memberBootstrap,
		Tokens:      m.store,
		Sessions:    ss,
		Version:     "test-" + name,
	})
	return termMember{fakeMember: m, sessions: ss}
}

// hubMemberTokenID is the id of the hub's operator member token.
func (m termMember) hubMemberTokenID(t *testing.T) string {
	t.Helper()
	list, err := m.store.List()
	require.NoError(t, err)
	for _, tok := range list {
		if tok.Kind == apitokens.KindHub && tok.Scope == apitokens.ScopeOperator {
			return tok.ID
		}
	}
	t.Fatal("no hub operator member token")
	return ""
}

// terminalHub is a hub with one terminal-capable member, "alpha".
func terminalHub(t *testing.T) (*testHub, termMember) {
	t.Helper()
	alpha := newTermMember(t, "alpha")
	reg := &memberList{}
	reg.add(alpha.rec)
	th := startTestHub(t, Config{Members: reg.list})
	waitProxied(t, th, "alpha")
	return th, alpha
}

const termTicketPath = "/hub/p/alpha/api/v1/sessions/itervox-abc123/terminal/ticket"

// brokerTicket asks the hub for a ticket with token and mode.
func brokerTicket(t *testing.T, th *testHub, token, mode string) (int, string, string) {
	t.Helper()
	resp := doHub(t, th.ts.URL+termTicketPath, http.MethodPost, token, strings.NewReader(`{"mode":"`+mode+`"}`))
	body := readBody(t, resp)
	var tk struct {
		URL  string `json:"url"`
		Mode string `json:"mode"`
	}
	_ = json.Unmarshal([]byte(body), &tk)
	return resp.StatusCode, body, tk.URL
}

// rawWS is a hand-rolled WebSocket client: it sends masked frames and
// reads close codes, which the x/net client hides.
type rawWS struct {
	conn net.Conn
	r    *bufio.Reader
}

// dialRawWS opens path on addr with origin; status is the handshake's.
func dialRawWS(t *testing.T, addr, path, origin string) (*rawWS, int) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	_, err = fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\nOrigin: %s\r\n\r\n", path, addr, origin)
	require.NoError(t, err)
	r := bufio.NewReader(conn)
	resp, err := http.ReadResponse(r, nil)
	require.NoError(t, err)
	if resp.StatusCode != http.StatusSwitchingProtocols {
		_ = resp.Body.Close()
	}
	return &rawWS{conn: conn, r: r}, resp.StatusCode
}

// send writes one masked binary frame.
func (c *rawWS) send(t *testing.T, payload []byte) {
	t.Helper()
	var mask [4]byte
	_, _ = rand.Read(mask[:])
	frame := []byte{0x80 | wsOpBinary, 0x80 | byte(len(payload))}
	frame = append(frame, mask[:]...)
	for i, b := range payload {
		frame = append(frame, b^mask[i%4])
	}
	_, err := c.conn.Write(frame)
	require.NoError(t, err)
}

// readUntil reads frames until the data seen contains want; it returns the
// close frame's code and reason if one comes first.
func (c *rawWS) readUntil(t *testing.T, want string) (closeCode int, reason string) {
	t.Helper()
	var got []byte
	require.NoError(t, c.conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	for want == "" || !strings.Contains(string(got), want) {
		f, err := readWSFrame(c.r, false, 1<<20)
		require.NoError(t, err, "got so far %q", got)
		if f.opcode == wsOpClose {
			if len(f.payload) >= 2 {
				return int(binary.BigEndian.Uint16(f.payload)), string(f.payload[2:])
			}
			return 1005, ""
		}
		got = append(got, f.payload...)
	}
	return 0, ""
}

func resizeFrame(cols, rows uint16) []byte {
	b := []byte{terminalOpResize, 0, 0, 0, 0}
	binary.BigEndian.PutUint16(b[1:3], cols)
	binary.BigEndian.PutUint16(b[3:5], rows)
	return b
}

// openHubTerminal brokers a ticket with token and opens its socket on the hub.
func openHubTerminal(t *testing.T, th *testHub, token string) *rawWS {
	t.Helper()
	status, body, u := brokerTicket(t, th, token, "read")
	require.Equal(t, http.StatusCreated, status, body)
	ws, code := dialRawWS(t, th.ts.Listener.Addr().String(), "/hub/p/alpha"+u+"&cols=100&rows=30", th.ts.URL)
	require.Equal(t, http.StatusSwitchingProtocols, code)
	ws.readUntil(t, "hello")
	return ws
}

// TestHubCallerHeaderMatchesServer: the hub and the member name the hub
// caller header the same.
func TestHubCallerHeaderMatchesServer(t *testing.T) {
	assert.Equal(t, server.HubCallerHeader, hubCallerHeader)
}

// TestHubTerminalViewerRefused: a viewer hub token gets no terminal; the
// member is never asked.
func TestHubTerminalViewerRefused(t *testing.T) {
	th, alpha := terminalHub(t)
	writes := alpha.writes.Load()
	status, body, _ := brokerTicket(t, th, th.secrets[apitokens.ScopeViewer], "read")
	assert.Equal(t, http.StatusForbidden, status)
	assert.Contains(t, body, "insufficient_scope")
	assert.Equal(t, writes, alpha.writes.Load(), "the member was never asked")
}

// TestHubTerminalReadOnlyPassThrough: an operator (or admin) hub token gets
// a read-only terminal through the hub: output streams, a resize goes
// through, typed input reaches nothing, and the member's feed names the
// person behind the hub token.
func TestHubTerminalReadOnlyPassThrough(t *testing.T) {
	th, alpha := terminalHub(t)
	for i, scope := range []apitokens.Scope{apitokens.ScopeOperator, apitokens.ScopeAdmin} {
		ws := openHubTerminal(t, th, th.secrets[scope])
		ws.send(t, append([]byte{0x00}, "rm -rf /\r"...))
		ws.send(t, resizeFrame(90, 25))
		conn := alpha.sessions.last()
		eventually(t, 5*time.Second, func() bool { _, s := conn.snapshot(); return len(s) == 1 }, "resize relayed")
		typed, sizes := conn.snapshot()
		assert.Empty(t, typed, "nothing typed through the hub reaches the session")
		assert.Equal(t, [][2]int{{90, 25}}, sizes)
		reqs := alpha.sessions.reqs()
		require.Len(t, reqs, i+1)
		assert.False(t, reqs[i].Write, "a read-only attach")
		assert.Equal(t, "hub:"+string(scope)+"-hub", reqs[i].Actor, "the feed names the hub caller")
		assert.Equal(t, 100, reqs[i].Cols)
		_ = ws.conn.Close()
	}
}

// TestHubTerminalWriteModeRefused: write mode is never brokered, and the
// hub always asks the member for a read ticket with its operator token.
func TestHubTerminalWriteModeRefused(t *testing.T) {
	th, alpha := terminalHub(t)
	writes := alpha.writes.Load()
	for _, scope := range []apitokens.Scope{apitokens.ScopeOperator, apitokens.ScopeAdmin} {
		status, body, _ := brokerTicket(t, th, th.secrets[scope], "write")
		assert.Equal(t, http.StatusForbidden, status)
		assert.Contains(t, body, "terminal_write_not_proxied")
	}
	assert.Equal(t, writes, alpha.writes.Load(), "the member was never asked")
	status, body, u := brokerTicket(t, th, th.secrets[apitokens.ScopeAdmin], "")
	require.Equal(t, http.StatusCreated, status, body)
	assert.Contains(t, body, `"mode":"read"`)
	assert.True(t, strings.HasPrefix(u, "/api/v1/sessions/itervox-abc123/terminal?ticket=itvt_"), u)
}

// TestHubTerminalSocketRefusals: the hub's socket opens only for a ticket
// it brokered, once, for its project and session, from an allowed Origin;
// a brokered ticket is useless on the member without the hub's token.
func TestHubTerminalSocketRefusals(t *testing.T) {
	th, alpha := terminalHub(t)
	addr := th.ts.Listener.Addr().String()
	op := th.secrets[apitokens.ScopeOperator]
	sock := "/hub/p/alpha/api/v1/sessions/itervox-abc123/terminal"

	_, code := dialRawWS(t, addr, sock, th.ts.URL)
	assert.Equal(t, http.StatusUnauthorized, code, "no ticket")
	_, code = dialRawWS(t, addr, sock+"?ticket=itvt_made-up", th.ts.URL)
	assert.Equal(t, http.StatusUnauthorized, code, "a ticket the hub did not broker")

	// A ticket minted on the member directly (not brokered) is refused by
	// the hub and never reaches the member.
	req, _ := http.NewRequest(http.MethodPost, alpha.ts.URL+"/api/v1/sessions/itervox-abc123/terminal/ticket", strings.NewReader(`{"mode":"read"}`))
	req.Header.Set("Authorization", "Bearer "+memberBootstrap)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	var direct struct {
		URL string `json:"url"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&direct))
	_ = resp.Body.Close()
	_, code = dialRawWS(t, addr, "/hub/p/alpha"+direct.URL, th.ts.URL)
	assert.Equal(t, http.StatusUnauthorized, code)
	assert.Empty(t, alpha.sessions.reqs())

	// A foreign Origin is refused by the hub (and the ticket is kept).
	_, _, u := brokerTicket(t, th, op, "read")
	_, code = dialRawWS(t, addr, "/hub/p/alpha"+u, "https://evil.example")
	assert.Equal(t, http.StatusForbidden, code)
	// The other session: refused, and the ticket is spent.
	_, _, u = brokerTicket(t, th, op, "read")
	_, code = dialRawWS(t, addr, "/hub/p/alpha"+strings.Replace(u, "itervox-abc123", "itervox-ffffff", 1), th.ts.URL)
	assert.Equal(t, http.StatusForbidden, code)
	_, code = dialRawWS(t, addr, "/hub/p/alpha"+u, th.ts.URL)
	assert.Equal(t, http.StatusUnauthorized, code, "spent by the failed attempt")

	// Single use.
	_, _, u = brokerTicket(t, th, op, "read")
	ws, code := dialRawWS(t, addr, "/hub/p/alpha"+u, th.ts.URL)
	require.Equal(t, http.StatusSwitchingProtocols, code)
	ws.readUntil(t, "hello")
	_, code = dialRawWS(t, addr, "/hub/p/alpha"+u, th.ts.URL)
	assert.Equal(t, http.StatusUnauthorized, code, "a second use")

	// A brokered ticket straight on the member (as a browser holding it
	// would): no hub member token in Authorization, refused.
	_, _, u = brokerTicket(t, th, op, "read")
	maddr := alpha.ts.Listener.Addr().String()
	_, code = dialRawWS(t, maddr, u, alpha.ts.URL)
	assert.Equal(t, http.StatusForbidden, code, "ticket_wrong_principal")
	// A foreign Origin straight on the member: refused, as before.
	_, _, u = brokerTicket(t, th, op, "read")
	_, code = dialRawWS(t, maddr, u, "https://evil.example")
	assert.Equal(t, http.StatusForbidden, code, "origin_not_allowed")
	assert.Len(t, alpha.sessions.reqs(), 1, "only the one good attach reached the session")
}

// TestHubTerminalClosedWhenHubTokenRevoked: revoking the caller's hub token
// closes the open pass-through with 4403 and a hub_ reason.
func TestHubTerminalClosedWhenHubTokenRevoked(t *testing.T) {
	th, _ := terminalHub(t)
	ws := openHubTerminal(t, th, th.secrets[apitokens.ScopeOperator])
	list, err := th.h.tokens.List()
	require.NoError(t, err)
	for _, tok := range list {
		if tok.Name == "operator-hub" {
			_, err = th.h.tokens.Revoke(tok.ID, "test")
			require.NoError(t, err)
		}
	}
	code, reason := ws.readUntil(t, "never")
	assert.Equal(t, terminalCloseAccessLost, code)
	assert.Equal(t, "hub_token_revoked", reason)
}

// TestHubTerminalClosedWhenHubTokenDowngraded: a hub token whose scope
// drops below operator (an edit of the hub's tokens.json by another
// process) closes the pass-through on the next re-check.
func TestHubTerminalClosedWhenHubTokenDowngraded(t *testing.T) {
	saved := terminalRecheckInterval
	terminalRecheckInterval = 50 * time.Millisecond
	t.Cleanup(func() { terminalRecheckInterval = saved })
	th, _ := terminalHub(t)
	ws := openHubTerminal(t, th, th.secrets[apitokens.ScopeOperator])
	path := th.h.tokens.FilePath()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var file map[string]any
	require.NoError(t, json.Unmarshal(raw, &file))
	for _, tok := range file["tokens"].([]any) {
		if m := tok.(map[string]any); m["name"] == "operator-hub" {
			m["scope"] = "viewer"
		}
	}
	data, err := json.Marshal(file)
	require.NoError(t, err)
	tmp := path + ".tmp"
	require.NoError(t, os.WriteFile(tmp, data, 0o600))
	require.NoError(t, os.Rename(tmp, path))
	code, reason := ws.readUntil(t, "never")
	assert.Equal(t, terminalCloseAccessLost, code)
	assert.Equal(t, "hub_scope_insufficient", reason)
}

// TestHubTerminalClosedWhenMemberTokenRevoked: revoking the hub's member
// token on the member closes the terminal there (its own re-check); the hub
// relays the 4403 with a member_ reason.
func TestHubTerminalClosedWhenMemberTokenRevoked(t *testing.T) {
	th, alpha := terminalHub(t)
	ws := openHubTerminal(t, th, th.secrets[apitokens.ScopeOperator])
	_, err := alpha.store.Revoke(alpha.hubMemberTokenID(t), "test")
	require.NoError(t, err)
	code, reason := ws.readUntil(t, "never")
	assert.Equal(t, terminalCloseAccessLost, code)
	assert.Equal(t, "member_token_revoked", reason)
}

// TestHubTerminalEndsWhenTheHubStops: hijacked pass-throughs end with the hub.
func TestHubTerminalEndsWhenTheHubStops(t *testing.T) {
	th, _ := terminalHub(t)
	ws := openHubTerminal(t, th, th.secrets[apitokens.ScopeOperator])
	th.h.endLife()
	require.NoError(t, ws.conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, err := readWSFrame(ws.r, false, 1<<20)
	for err == nil {
		_, err = readWSFrame(ws.r, false, 1<<20)
	}
	assert.NotContains(t, err.Error(), "timeout")
}

// TestHubTerminalRelayDropsInput: the hub's own filter, without the
// member's read-only attach behind it: of the browser's frames only a
// resize and control frames reach the member; typed input, text frames and
// fragments never do.
func TestHubTerminalRelayDropsInput(t *testing.T) {
	browser, browserPeer := net.Pipe()
	member, memberPeer := net.Pipe()
	rl := newTerminalRelay(browserPeer, bufio.NewReader(browserPeer), memberPeer, bufio.NewReader(memberPeer))
	done := make(chan struct{})
	go func() {
		defer close(done)
		rl.browserToMember()
	}()
	got := make(chan wsFrame, 8)
	go func() {
		r := bufio.NewReader(member)
		for {
			f, err := readWSFrame(r, true, 1<<20)
			if err != nil {
				close(got)
				return
			}
			got <- f
		}
	}()
	ws := &rawWS{conn: browser}
	ws.send(t, append([]byte{0x00}, "typed"...))               // input: dropped
	ws.send(t, resizeFrame(80, 24))                            // resize: relayed
	ws.send(t, []byte{0x01, 0, 80})                            // a short resize: dropped
	textFrame := []byte{0x80 | 0x1, 0x80 | 1, 0, 0, 0, 0, 'x'} // text: dropped
	_, err := browser.Write(textFrame)
	require.NoError(t, err)
	fragment := []byte{wsOpBinary, 0x80 | 5, 0, 0, 0, 0, 0x01, 0, 80, 0, 24} // not FIN: dropped
	_, err = browser.Write(fragment)
	require.NoError(t, err)
	last := []byte{0x80 | wsOpContinuation, 0x80 | 1, 0, 0, 0, 0, 'y'}
	_, err = browser.Write(last)
	require.NoError(t, err)
	ping := []byte{0x80 | wsOpPing, 0x80, 0, 0, 0, 0} // control: relayed
	_, err = browser.Write(ping)
	require.NoError(t, err)
	f := <-got
	assert.Equal(t, wsOpBinary, f.opcode)
	assert.Equal(t, resizeFrame(80, 24), f.payload)
	f = <-got
	assert.Equal(t, wsOpPing, f.opcode)
	_ = browser.Close()
	<-done
	rl.end()
	_, more := <-got
	assert.False(t, more, "nothing else reached the member")
}
