//go:build unix

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/websocket"

	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
)

// The live terminal (#298), end to end on real tmux (private socket): the
// rig's server runs unauthenticated, so every caller is admin.

// terminalTicket mints a ticket for id in mode and returns its socket path.
func (r *tmuxRig) terminalTicket(id, mode string) (int, string, string) {
	r.t.Helper()
	w := r.do(http.MethodPost, "/api/v1/sessions/"+id+"/terminal/ticket", `{"mode":"`+mode+`"}`)
	var body struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w.Code, body.URL, w.Body.String()
}

func dialRigTerminal(t *testing.T, ts *httptest.Server, path string) *websocket.Conn {
	t.Helper()
	cfg, err := websocket.NewConfig("ws"+strings.TrimPrefix(ts.URL, "http")+path, ts.URL)
	require.NoError(t, err)
	ws, err := websocket.DialConfig(cfg)
	require.NoError(t, err)
	return ws
}

func readTerminalUntil(t *testing.T, ws *websocket.Conn, want string) {
	t.Helper()
	require.NoError(t, ws.SetReadDeadline(time.Now().Add(10*time.Second)))
	var got []byte
	for !strings.Contains(string(got), want) {
		var frame []byte
		require.NoError(t, websocket.Message.Receive(ws, &frame), "the terminal never drew %q; got %q", want, got)
		got = append(got, frame...)
	}
}

// With sessions.terminal_write, an admin attaches a read-write terminal to
// a session itervox started: it draws the CLI, a typed line reaches it,
// closing the socket detaches and the session keeps running, and the feed
// records the attach and the detach (never the bytes).
func TestLiveTerminalAttachesTypesAndDetaches(t *testing.T) {
	r := newTmuxRig(t, "sessions:\n  terminal_write: true\n", true)
	s := r.startSession()
	r.screenShows(s.ID, "ready")
	assert.True(t, r.list().Tmux.Terminal)
	assert.True(t, r.list().Tmux.TerminalWrite)
	ts := httptest.NewServer(r.srv)
	defer ts.Close()

	code, path, body := r.terminalTicket(s.ID, "write")
	require.Equal(t, http.StatusCreated, code, body)
	ws := dialRigTerminal(t, ts, path+"&cols=100&rows=30")
	readTerminalUntil(t, ws, "ready")
	require.NoError(t, websocket.Message.Send(ws, append([]byte{0x00}, "typed in the browser\r"...)))
	readTerminalUntil(t, ws, "got: typed in the browser")
	r.screenShows(s.ID, "got: typed in the browser")
	require.NoError(t, ws.Close())

	require.Eventually(t, func() bool {
		var attached, detached bool
		for _, ev := range r.orch.Snapshot().Feed {
			if ev.Identifier != s.ID {
				continue
			}
			switch ev.Kind {
			case orchestrator.FeedTerminalAttached:
				attached = ev.Payload.Reason == "read_write" && ev.Payload.Via == "dashboard:unauthenticated"
			case orchestrator.FeedTerminalDetached:
				detached = ev.Payload.Reason == "read_write"
			}
		}
		return attached && detached
	}, 5*time.Second, 20*time.Millisecond, "the feed records the attach and the detach")
	assert.True(t, r.tmuxHas(s.ID), "detaching leaves the session running")
	for _, ev := range r.orch.Snapshot().Feed {
		raw, _ := json.Marshal(ev)
		assert.NotContains(t, string(raw), "typed in the browser", "no terminal byte reaches the feed")
	}
}

// Without the opt-in a read-write terminal is refused even for admin; a
// read-only one draws the session but nothing typed reaches it.
func TestLiveTerminalIsReadOnlyWithoutTheOptIn(t *testing.T) {
	r := newTmuxRig(t, "", true)
	s := r.startSession()
	r.screenShows(s.ID, "ready")
	assert.False(t, r.list().Tmux.TerminalWrite)
	code, _, body := r.terminalTicket(s.ID, "write")
	assert.Equal(t, http.StatusForbidden, code)
	assert.Contains(t, body, "terminal_write_disabled")
	// The second check, below the server: a read-write attach is refused.
	_, err := r.ts.attachTerminal(server.TerminalAttachRequest{ID: s.ID, Write: true})
	assert.ErrorIs(t, err, server.ErrTerminalWriteDisabled)

	ts := httptest.NewServer(r.srv)
	defer ts.Close()
	code, path, body := r.terminalTicket(s.ID, "read")
	require.Equal(t, http.StatusCreated, code, body)
	ws := dialRigTerminal(t, ts, path)
	defer func() { _ = ws.Close() }()
	readTerminalUntil(t, ws, "ready")
	require.NoError(t, websocket.Message.Send(ws, append([]byte{0x00}, "should not arrive\r"...)))
	time.Sleep(300 * time.Millisecond)
	w := r.do(http.MethodGet, "/api/v1/sessions/"+s.ID+"/screen?lines=50", "")
	assert.NotContains(t, w.Body.String(), "got: should not arrive")
}

// A hand-started session in tmux gets a terminal only with
// sessions.tmux_control, like the screen and keys; an unknown session none.
func TestLiveTerminalFollowsTheTmuxControlRules(t *testing.T) {
	r := newTmuxRig(t, "", true)
	r.handStarted(true)
	r.waitRow(handSessionID, func(s server.SessionRow) bool { return s.TmuxPane != nil })
	code, _, body := r.terminalTicket(handSessionID, "read")
	assert.Equal(t, http.StatusForbidden, code)
	assert.Contains(t, body, "not_controllable")
	code, _, _ = r.terminalTicket("nope", "read")
	assert.Equal(t, http.StatusNotFound, code)

	on := newTmuxRig(t, "sessions:\n  tmux_control: true\n", true)
	on.handStarted(true)
	on.waitRow(handSessionID, func(s server.SessionRow) bool { return s.Controllable })
	code, path, body := on.terminalTicket(handSessionID, "read")
	require.Equal(t, http.StatusCreated, code, body)
	ts := httptest.NewServer(on.srv)
	defer ts.Close()
	ws := dialRigTerminal(t, ts, path)
	defer func() { _ = ws.Close() }()
	readTerminalUntil(t, ws, "ready")
}

// A terminal the daemon ends because its token lost access feeds the
// detach with that reason as its outcome, and `itervox watch` prints it.
func TestLiveTerminalDetachFeedsTheAccessReason(t *testing.T) {
	r := newTmuxRig(t, "", true)
	s := r.startSession()
	r.screenShows(s.ID, "ready")
	conn, err := r.ts.attachTerminal(server.TerminalAttachRequest{ID: s.ID, Actor: "dashboard:tok_1"})
	require.NoError(t, err)
	require.NoError(t, conn.CloseWithReason("token_revoked"))
	require.NoError(t, conn.Close(), "a second close records nothing more")
	var detached []orchestrator.FeedEvent
	require.Eventually(t, func() bool {
		detached = detached[:0]
		for _, ev := range r.orch.Snapshot().Feed {
			if ev.Identifier == s.ID && ev.Kind == orchestrator.FeedTerminalDetached {
				detached = append(detached, ev)
			}
		}
		return len(detached) == 1
	}, 5*time.Second, 20*time.Millisecond)
	assert.Equal(t, "token_revoked", detached[0].Payload.Outcome)
	assert.Equal(t, "read_only", detached[0].Payload.Reason)
	assert.True(t, r.tmuxHas(s.ID), "the session keeps running")

	line, _ := (&watcher{p: paint(false)}).eventText(server.FeedEvent{Kind: "terminal_detached", Identifier: s.ID,
		Payload: server.FeedPayload{Reason: "read_only", Outcome: "token_revoked", Via: "dashboard:tok_1"}})
	assert.Contains(t, line, "live terminal detached (read-only): token revoked")
}
