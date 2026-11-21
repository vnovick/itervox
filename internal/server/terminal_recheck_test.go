package server

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/apitokens"
)

// rawTerminal is a hand-rolled WebSocket client of the terminal socket: the
// x/net client hides a close frame's code and reason, which these tests
// assert on.
type rawTerminal struct {
	conn net.Conn
	r    *bufio.Reader
}

// dialRawTerminal opens path (a ticket's URL) on ts and completes the
// handshake.
func dialRawTerminal(t *testing.T, ts *httptest.Server, path string) *rawTerminal {
	t.Helper()
	conn, err := net.Dial("tcp", ts.Listener.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	_, err = fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\nOrigin: %s\r\n\r\n",
		path, ts.Listener.Addr().String(), ts.URL)
	require.NoError(t, err)
	r := bufio.NewReader(conn)
	resp, err := http.ReadResponse(r, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
	return &rawTerminal{conn: conn, r: r}
}

// frame reads one server frame (never masked): its opcode and payload.
func (c *rawTerminal) frame(t *testing.T, deadline time.Duration) (byte, []byte, error) {
	t.Helper()
	require.NoError(t, c.conn.SetReadDeadline(time.Now().Add(deadline)))
	var h [2]byte
	if _, err := io.ReadFull(c.r, h[:]); err != nil {
		return 0, nil, err
	}
	n := uint64(h[1] & 0x7f)
	switch n {
	case 126:
		var b [2]byte
		if _, err := io.ReadFull(c.r, b[:]); err != nil {
			return 0, nil, err
		}
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err := io.ReadFull(c.r, b[:]); err != nil {
			return 0, nil, err
		}
		n = binary.BigEndian.Uint64(b[:])
	}
	payload := make([]byte, n)
	_, err := io.ReadFull(c.r, payload)
	return h[0] & 0x0f, payload, err
}

// readUntilData reads frames until a data frame carries want.
func (c *rawTerminal) readUntilData(t *testing.T, want string) {
	t.Helper()
	var got []byte
	for !strings.Contains(string(got), want) {
		op, payload, err := c.frame(t, 5*time.Second)
		require.NoError(t, err, "got so far %q", got)
		require.NotEqual(t, byte(0x8), op, "the socket closed before %q", want)
		got = append(got, payload...)
	}
}

// closeWithin reads frames until a close frame, which must come within d,
// and returns its code and reason.
func (c *rawTerminal) closeWithin(t *testing.T, d time.Duration) (int, string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		op, payload, err := c.frame(t, time.Until(deadline))
		require.NoError(t, err, "no close frame within %v", d)
		if op == 0x8 {
			require.GreaterOrEqual(t, len(payload), 2)
			return int(binary.BigEndian.Uint16(payload[:2])), string(payload[2:])
		}
	}
}

// staysOpen reads for d and fails on a close frame or a dropped connection.
func (c *rawTerminal) staysOpen(t *testing.T, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		op, _, err := c.frame(t, time.Until(deadline))
		var ne net.Error
		if err != nil && errors.As(err, &ne) && ne.Timeout() {
			return
		}
		require.NoError(t, err, "the socket dropped")
		require.NotEqual(t, byte(0x8), op, "the socket was closed")
	}
}

// withRecheck sets the recheck interval for the servers the test makes.
func withRecheck(t *testing.T, d time.Duration) {
	t.Helper()
	old := terminalRecheckInterval
	terminalRecheckInterval = d
	t.Cleanup(func() { terminalRecheckInterval = old })
}

// openRawTerminal mints a ticket with token in mode and opens its socket.
func (r terminalRig) openRawTerminal(t *testing.T, ts *httptest.Server, token, mode string) (*rawTerminal, *fakeTerminalConn) {
	t.Helper()
	rec, body := r.ticket(t, token, mode)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	c := dialRawTerminal(t, ts, body.URL)
	c.readUntilData(t, "hello")
	return c, r.sessions.lastConn()
}

// setTokenScope rewrites tokens.json by hand (another process's edit) so
// the token id has scope.
func setTokenScope(t *testing.T, store *apitokens.Store, id string, scope apitokens.Scope) {
	t.Helper()
	raw, err := os.ReadFile(store.FilePath())
	require.NoError(t, err)
	var f map[string]any
	require.NoError(t, json.Unmarshal(raw, &f))
	for _, tok := range f["tokens"].([]any) {
		if m := tok.(map[string]any); m["id"] == id {
			m["scope"] = string(scope)
		}
	}
	out, err := json.Marshal(f)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(store.FilePath(), out, 0o600))
}

// A token revoked while its terminal is open closes the terminal at once
// (the token store's change signal; the periodic recheck is an hour away
// here), with the access-lost close code and the reason, and the detach
// records the reason.
func TestOpenTerminalClosesWhenItsTokenIsRevoked(t *testing.T) {
	withRecheck(t, time.Hour)
	r := newTerminalRig(t, false)
	ts := httptest.NewServer(r.srv)
	defer ts.Close()
	c, conn := r.openRawTerminal(t, ts, r.secrets[apitokens.ScopeOperator], "read")

	_, err := r.store.Revoke(r.tokenID(t, apitokens.ScopeOperator), "test")
	require.NoError(t, err)
	code, reason := c.closeWithin(t, 2*time.Second)
	assert.Equal(t, terminalCloseAccessLost, code)
	assert.Equal(t, terminalLostTokenRevoked, reason)
	_, _, err = c.frame(t, 2*time.Second)
	assert.ErrorIs(t, err, io.EOF, "one close frame, then the connection ends")
	require.Eventually(t, conn.isClosed, 5*time.Second, 10*time.Millisecond, "the attach is detached")
	assert.Equal(t, terminalLostTokenRevoked, conn.closeReason())
	require.Eventually(t, func() bool { return r.srv.terminalsOpen.Load() == 0 }, 5*time.Second, 10*time.Millisecond)
}

// A revoke by another process (`itervox token revoke`) is not seen by this
// daemon's store until it reads: the periodic recheck closes the terminal
// within its interval.
func TestOpenTerminalClosesWithinTheIntervalOnAnotherProcessRevoke(t *testing.T) {
	withRecheck(t, 200*time.Millisecond)
	r := newTerminalRig(t, false)
	ts := httptest.NewServer(r.srv)
	defer ts.Close()
	c, conn := r.openRawTerminal(t, ts, r.secrets[apitokens.ScopeOperator], "read")

	_, err := apitokens.Open(r.store.FilePath()).Revoke(r.tokenID(t, apitokens.ScopeOperator), "cli")
	require.NoError(t, err)
	code, reason := c.closeWithin(t, 2*time.Second)
	assert.Equal(t, terminalCloseAccessLost, code)
	assert.Equal(t, terminalLostTokenRevoked, reason)
	require.Eventually(t, conn.isClosed, 5*time.Second, 10*time.Millisecond)
}

// An admin's read-write terminal whose token is downgraded to operator is
// closed (not downgraded in place: the attach client was started
// read-write); the operator can reconnect read-only.
func TestOpenWriteTerminalClosesWhenItsTokenIsDowngraded(t *testing.T) {
	withRecheck(t, 200*time.Millisecond)
	r := newTerminalRig(t, true)
	ts := httptest.NewServer(r.srv)
	defer ts.Close()
	adminID := r.tokenID(t, apitokens.ScopeAdmin)
	c, conn := r.openRawTerminal(t, ts, r.secrets[apitokens.ScopeAdmin], "write")

	setTokenScope(t, r.store, adminID, apitokens.ScopeOperator)
	code, reason := c.closeWithin(t, 2*time.Second)
	assert.Equal(t, terminalCloseAccessLost, code)
	assert.Equal(t, terminalLostScope, reason)
	require.Eventually(t, conn.isClosed, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, terminalLostScope, conn.closeReason())

	// The same token, now operator, still gets a read-only terminal.
	rec, _ := r.ticket(t, r.secrets[apitokens.ScopeAdmin], "write")
	assert.Equal(t, http.StatusForbidden, rec.Code)
	c, _ = r.openRawTerminal(t, ts, r.secrets[apitokens.ScopeAdmin], "read")
	c.staysOpen(t, 600*time.Millisecond)
}

// Rotating the token ends its terminal: the secret that opened it is dead.
func TestOpenTerminalClosesWhenItsTokenIsRotated(t *testing.T) {
	withRecheck(t, time.Hour)
	r := newTerminalRig(t, false)
	ts := httptest.NewServer(r.srv)
	defer ts.Close()
	c, _ := r.openRawTerminal(t, ts, r.secrets[apitokens.ScopeOperator], "read")
	_, _, err := r.store.Rotate(r.tokenID(t, apitokens.ScopeOperator), "test")
	require.NoError(t, err)
	code, reason := c.closeWithin(t, 2*time.Second)
	assert.Equal(t, terminalCloseAccessLost, code)
	assert.Equal(t, terminalLostTokenRotated, reason)
}

// A token that keeps its access keeps its terminal across many rechecks
// and store changes (another token created and revoked); so does the
// bootstrap token's, which is not watched at all.
func TestOpenTerminalWithAnUnchangedTokenStaysOpen(t *testing.T) {
	withRecheck(t, 50*time.Millisecond)
	r := newTerminalRig(t, true)
	ts := httptest.NewServer(r.srv)
	defer ts.Close()
	c, conn := r.openRawTerminal(t, ts, r.secrets[apitokens.ScopeAdmin], "write")
	boot, bootConn := r.openRawTerminal(t, ts, scopesTestBootstrap, "write")

	other, _, err := r.store.Create("other", apitokens.ScopeViewer, nil, "test")
	require.NoError(t, err)
	_, err = r.store.Revoke(other.ID, "test")
	require.NoError(t, err)
	_, err = r.store.Revoke(r.tokenID(t, apitokens.ScopeViewer), "test")
	require.NoError(t, err)
	c.staysOpen(t, 500*time.Millisecond)
	boot.staysOpen(t, 100*time.Millisecond)
	assert.False(t, conn.isClosed())
	assert.False(t, bootConn.isClosed())
	conn.out <- []byte("still here")
	c.readUntilData(t, "still here")
}

// A revoke that lands right after a passing check (while the watcher has
// not yet gone back to waiting) still closes the terminal at once: the
// watcher holds the store's change signal from before the check, so the
// revoke fires it. The periodic recheck is an hour away here.
func TestOpenTerminalSeesARevokeRightAfterItsCheck(t *testing.T) {
	withRecheck(t, time.Hour)
	r := newTerminalRig(t, false)
	id := r.tokenID(t, apitokens.ScopeOperator)
	var once sync.Once
	r.srv.terminalChecked = func() {
		once.Do(func() {
			_, err := r.store.Revoke(id, "test")
			assert.NoError(t, err)
		})
	}
	ts := httptest.NewServer(r.srv)
	defer ts.Close()
	c, conn := r.openRawTerminal(t, ts, r.secrets[apitokens.ScopeOperator], "read")

	code, reason := c.closeWithin(t, 2*time.Second)
	assert.Equal(t, terminalCloseAccessLost, code)
	assert.Equal(t, terminalLostTokenRevoked, reason)
	require.Eventually(t, conn.isClosed, 5*time.Second, 10*time.Millisecond)
}
