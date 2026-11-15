package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/websocket"

	"github.com/vnovick/itervox/internal/hub"
)

const (
	testAppToken = "xapp" + "-1-A0TEST-1234567890-abcdefabcdefabcdefabcdef"
	testBotToken = "xoxb" + "-1234567890-0987654321-ABCDEFGHIJKLMNOPQRSTUVWX"
)

// postedMsg is a chat.postMessage or chat.update the fake Web API got.
type postedMsg struct {
	Method  string           `json:"-"`
	Channel string           `json:"channel"`
	TS      string           `json:"ts"`
	Text    string           `json:"text"`
	Blocks  []map[string]any `json:"blocks"`
}

// fakeSlack is a fake Socket Mode server and Web API on one httptest
// server: apps.connections.open, the WebSocket, chat.postMessage,
// chat.update and a response_url endpoint.
type fakeSlack struct {
	t  *testing.T
	ts *httptest.Server

	opens       atomic.Int64
	connections atomic.Int64
	// failOpens answers that many apps.connections.open calls with ok: false.
	failOpens atomic.Int64
	// failPosts answers that many chat.postMessage calls with HTTP 500.
	failPosts atomic.Int64

	mu        sync.Mutex
	ws        *websocket.Conn
	posts     []postedMsg
	updates   []postedMsg
	responses []string
	acks      []string
	seq       int
}

func newFakeSlack(t *testing.T) *fakeSlack {
	t.Helper()
	f := &fakeSlack{t: t}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/apps.connections.open", func(w http.ResponseWriter, r *http.Request) {
		f.opens.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+testAppToken {
			writeAPI(w, map[string]any{"ok": false, "error": "invalid_auth"})
			return
		}
		if f.failOpens.Load() > 0 {
			f.failOpens.Add(-1)
			writeAPI(w, map[string]any{"ok": false, "error": "internal_error", "echo": r.Header.Get("Authorization")})
			return
		}
		writeAPI(w, map[string]any{"ok": true, "url": "ws://" + r.Host + "/socket?ticket=t"})
	})
	mux.Handle("/socket", websocket.Handler(func(ws *websocket.Conn) {
		f.connections.Add(1)
		f.mu.Lock()
		f.ws = ws
		f.mu.Unlock()
		_ = websocket.Message.Send(ws, `{"type":"hello","num_connections":1}`)
		for {
			var frame string
			if err := websocket.Message.Receive(ws, &frame); err != nil {
				return
			}
			var ack struct {
				EnvelopeID string `json:"envelope_id"`
			}
			_ = json.Unmarshal([]byte(frame), &ack)
			f.mu.Lock()
			f.acks = append(f.acks, ack.EnvelopeID)
			f.mu.Unlock()
		}
	}))
	for _, method := range []string{"chat.postMessage", "chat.update"} {
		mux.HandleFunc("POST /api/"+method, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+testBotToken {
				writeAPI(w, map[string]any{"ok": false, "error": "invalid_auth"})
				return
			}
			if method == "chat.postMessage" && f.failPosts.Load() > 0 {
				f.failPosts.Add(-1)
				http.Error(w, "boom "+r.Header.Get("Authorization"), http.StatusInternalServerError)
				return
			}
			var m postedMsg
			body, _ := io.ReadAll(r.Body)
			require.NoError(t, json.Unmarshal(body, &m))
			m.Method = method
			f.mu.Lock()
			f.seq++
			if method == "chat.postMessage" {
				m.TS = "1700000000.00000" + string(rune('0'+f.seq%10))
				f.posts = append(f.posts, m)
			} else {
				f.updates = append(f.updates, m)
			}
			f.mu.Unlock()
			writeAPI(w, map[string]any{"ok": true, "channel": m.Channel, "ts": m.TS})
		})
	}
	mux.HandleFunc("POST /respond", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.responses = append(f.responses, body.Text)
		f.mu.Unlock()
	})
	f.ts = httptest.NewServer(mux)
	t.Cleanup(f.ts.Close)
	return f
}

func writeAPI(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// push sends an envelope over the current connection.
func (f *fakeSlack) push(envType, id string, payload any) {
	f.t.Helper()
	data, err := json.Marshal(map[string]any{"type": envType, "envelope_id": id, "payload": payload})
	require.NoError(f.t, err)
	f.mu.Lock()
	ws := f.ws
	f.mu.Unlock()
	require.NotNil(f.t, ws, "no Socket Mode connection")
	require.NoError(f.t, websocket.Message.Send(ws, string(data)))
}

func (f *fakeSlack) snapshot() (posts, updates []postedMsg, responses, acks []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]postedMsg(nil), f.posts...), append([]postedMsg(nil), f.updates...),
		append([]string(nil), f.responses...), append([]string(nil), f.acks...)
}

func (f *fakeSlack) responseURL() string { return f.ts.URL + "/respond" }

// memberCall is one MemberActions.Call a fake member received.
type memberCall struct {
	Method, Path string
	Body         string
}

// fakeProject is one member behind fakeMembers.
type fakeProject struct {
	ref     hub.MemberRef
	mu      sync.Mutex
	calls   []memberCall
	pending map[string]string // identifier → question
	approve map[string]bool   // identifier → the pending request is an approval
	live    map[string]bool   // identifier → a live run
	down    atomic.Bool       // the member does not answer
}

func (p *fakeProject) writes() []memberCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []memberCall
	for _, c := range p.calls {
		if c.Method != http.MethodGet {
			out = append(out, c)
		}
	}
	return out
}

// fakeMembers implements hub.MemberActions over in-memory projects.
type fakeMembers struct {
	mu       sync.Mutex
	projects map[string]*fakeProject
}

func newFakeMembers(names ...string) *fakeMembers {
	m := &fakeMembers{projects: map[string]*fakeProject{}}
	for _, n := range names {
		m.projects[n] = &fakeProject{
			ref:     hub.MemberRef{Project: n, Workflow: "/work/" + n + "/WORKFLOW.md"},
			pending: map[string]string{}, approve: map[string]bool{}, live: map[string]bool{},
		}
	}
	return m
}

func (m *fakeMembers) project(name string) *fakeProject {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.projects[name]
}

func (m *fakeMembers) Projects() []hub.MemberRef {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []hub.MemberRef
	for _, p := range m.projects {
		out = append(out, p.ref)
	}
	return out
}

func (m *fakeMembers) Member(project string) (hub.MemberRef, bool) {
	p := m.project(project)
	if p == nil {
		return hub.MemberRef{}, false
	}
	return p.ref, true
}

func (m *fakeMembers) Call(_ context.Context, ref hub.MemberRef, method, path string, body any) (int, []byte, error) {
	p := m.project(ref.Project)
	if p == nil || p.ref.Workflow != ref.Workflow {
		return 0, nil, hub.ErrMemberActionRefused
	}
	if p.down.Load() {
		return 0, nil, errors.New("connection refused")
	}
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, memberCall{Method: method, Path: path, Body: string(raw)})
	parts := strings.Split(path, "/") // "", api, v1, issues|sessions, id, verb
	switch {
	case method == http.MethodGet && len(parts) == 6 && parts[3] == "issues" && parts[5] == "context":
		id := parts[4]
		q, ok := p.pending[id]
		if !ok && !p.live[id] {
			return http.StatusNotFound, []byte(`{"error":{"code":"no_run_context"}}`), nil
		}
		resp, _ := json.Marshal(map[string]any{"identifier": id, "run": map[string]any{
			"runId": "run-" + id, "live": p.live[id], "pendingInput": q, "pendingApproval": p.approve[id],
		}})
		return http.StatusOK, resp, nil
	case method == http.MethodPost && len(parts) == 6 && parts[3] == "issues" && parts[5] == "provide-input":
		delete(p.pending, parts[4])
		return http.StatusAccepted, []byte(`{"ok":true}`), nil
	case method == http.MethodPost && len(parts) == 6 && parts[3] == "sessions" && parts[5] == "message":
		return http.StatusAccepted, []byte(`{"message":{}}`), nil
	}
	return http.StatusNotFound, nil, nil
}

func (*fakeMembers) AbsoluteURL(link string) string { return "http://127.0.0.1:8089" + link }

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// rig is a running Slack channel over a fake Slack and fake members.
type rig struct {
	t       *testing.T
	slack   *fakeSlack
	members *fakeMembers
	ch      *Channel
	logs    *syncBuffer
	cancel  context.CancelFunc
	done    chan struct{}
}

// newRig starts a channel; tweak adjusts the config before New.
func newRig(t *testing.T, tweak func(*Config), projects ...string) *rig {
	t.Helper()
	fs := newFakeSlack(t)
	logs := &syncBuffer{}
	cfg := Config{
		AppToken: testAppToken, BotToken: testBotToken,
		AllowedUsers:   []string{"U0ALLOWED"},
		Channels:       map[string]string{},
		KeyPath:        filepath.Join(t.TempDir(), "slack_actions.key"),
		APIBase:        fs.ts.URL + "/api/",
		Logger:         slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		ReconnectMin:   20 * time.Millisecond,
		ReconnectMax:   100 * time.Millisecond,
		DefaultChannel: "",
	}
	if tweak != nil {
		tweak(&cfg)
	}
	ch, err := New(cfg)
	require.NoError(t, err)
	r := &rig{t: t, slack: fs, members: newFakeMembers(projects...), ch: ch, logs: logs, done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	go func() { defer close(r.done); ch.Run(ctx, r.members) }()
	t.Cleanup(r.stop)
	require.Eventually(t, ch.Connected, 5*time.Second, 10*time.Millisecond, "Socket Mode never connected")
	return r
}

func (r *rig) stop() {
	r.cancel()
	<-r.done
}

// send delivers n through the channel.
func (r *rig) send(n hub.Notification) {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(r.t, r.ch.Send(ctx, n))
}

// lastPost is the newest chat.postMessage.
func (r *rig) lastPost() postedMsg {
	r.t.Helper()
	posts, _, _, _ := r.slack.snapshot()
	require.NotEmpty(r.t, posts)
	return posts[len(posts)-1]
}

// control returns the signed value of a posted message's button (by action
// id) or reply box (actionReply: the input block's block_id).
func control(t *testing.T, m postedMsg, actionID string) string {
	t.Helper()
	for _, b := range m.Blocks {
		switch b["type"] {
		case "input":
			if el, _ := b["element"].(map[string]any); el["action_id"] == actionID {
				return b["block_id"].(string)
			}
		case "actions":
			els, _ := b["elements"].([]any)
			for _, e := range els {
				el, _ := e.(map[string]any)
				if el["action_id"] == actionID {
					v, _ := el["value"].(string)
					return v
				}
			}
		}
	}
	t.Fatalf("no %s control in the message: %v", actionID, m.Blocks)
	return ""
}

// click sends a block_actions envelope for one action.
func (r *rig) click(envelopeID, user, actionID, blockID, value string, msg postedMsg) {
	r.t.Helper()
	r.slack.push("interactive", envelopeID, map[string]any{
		"type": "block_actions", "user": map[string]any{"id": user},
		"channel":      map[string]any{"id": msg.Channel},
		"container":    map[string]any{"type": "message", "channel_id": msg.Channel, "message_ts": msg.TS},
		"response_url": r.slack.responseURL(),
		"actions":      []any{map[string]any{"action_id": actionID, "block_id": blockID, "value": value}},
	})
}

// waitResponses waits until n ephemeral answers arrived and returns them.
func (r *rig) waitResponses(n int) []string {
	r.t.Helper()
	var out []string
	require.Eventually(r.t, func() bool {
		_, _, out, _ = r.slack.snapshot()
		return len(out) >= n
	}, 5*time.Second, 10*time.Millisecond, "expected %d ephemeral answers", n)
	return out
}

// waitWrites waits until project p received n non-GET calls.
func (r *rig) waitWrites(p *fakeProject, n int) []memberCall {
	r.t.Helper()
	var out []memberCall
	require.Eventually(r.t, func() bool { out = p.writes(); return len(out) >= n }, 5*time.Second, 10*time.Millisecond,
		"expected %d member writes", n)
	return out
}
