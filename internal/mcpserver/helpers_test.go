package mcpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeDaemon is a scripted /api/v1 for the tool tests: whoami answers
// scope, GET /state answers state, GET /issues/{id}/context answers
// contexts[id] (404 when absent). Every request is recorded.
type fakeDaemon struct {
	mu       sync.Mutex
	scope    string
	state    map[string]any
	contexts map[string]any
	sessions []map[string]any
	requests []recorded
}

type recorded struct {
	method, path string
	body         map[string]any
}

func newFakeDaemon(t *testing.T, scope string) (*fakeDaemon, *Client) {
	t.Helper()
	f := &fakeDaemon{scope: scope, state: map[string]any{"activeStates": []string{"Todo", "In Progress"}}, contexts: map[string]any{}}
	ts := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(ts.Close)
	return f, NewClient(ts.URL, "test-token")
}

func (f *fakeDaemon) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var body map[string]any
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1")
	f.requests = append(f.requests, recorded{method: r.Method, path: path, body: body})
	reply := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	switch {
	case r.Method == http.MethodGet && path == "/whoami":
		reply(200, map[string]any{"scope": f.scope, "source": "token"})
	case r.Method == http.MethodGet && path == "/state":
		reply(200, f.state)
	case r.Method == http.MethodGet && path == "/sessions":
		reply(200, map[string]any{"sessions": f.sessions})
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/context"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/issues/"), "/context")
		if c, ok := f.contexts[id]; ok {
			reply(200, c)
			return
		}
		reply(404, map[string]any{"error": map[string]any{"code": "no_run_context", "message": "the issue has no run and no worktree"}})
	case r.Method == http.MethodGet && path == "/memory":
		reply(200, map[string]any{"lessons": map[string]any{"scope": "private"}})
	case r.Method == http.MethodPost && path == "/memory":
		reply(201, map[string]any{"id": "m1", "status": body["status"], "scope": body["scope"]})
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/dispatch/"):
		reply(200, map[string]any{"dispatchPaused": nil})
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/message"):
		reply(202, map[string]any{"message": map[string]any{"id": "msg1", "status": "queued"}})
	default:
		reply(202, map[string]any{"ok": true})
	}
}

// writes lists the recorded non-GET requests as "METHOD path".
func (f *fakeDaemon) writes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.requests {
		if r.method != http.MethodGet {
			out = append(out, r.method+" "+r.path)
		}
	}
	return out
}

func (f *fakeDaemon) lastBody(method, path string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.requests) - 1; i >= 0; i-- {
		if f.requests[i].method == method && f.requests[i].path == path {
			return f.requests[i].body
		}
	}
	return nil
}

// mcpSession is a scripted MCP client over an in-memory stdio pair.
type mcpSession struct {
	t    *testing.T
	in   *io.PipeWriter
	out  *bufio.Scanner
	done chan error
	next int
}

func startSession(t *testing.T, srv *Server) *mcpSession {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	s := &mcpSession{t: t, in: inW, out: bufio.NewScanner(outR), done: make(chan error, 1)}
	s.out.Buffer(make([]byte, 64<<10), 8<<20)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		err := srv.Serve(ctx, inR, outW)
		_ = outW.Close()
		s.done <- err
	}()
	t.Cleanup(func() {
		_ = inW.Close()
		cancel()
		select {
		case <-s.done:
		case <-time.After(5 * time.Second):
			t.Error("the server did not stop after stdin closed")
		}
	})
	return s
}

func (s *mcpSession) send(msg any) {
	s.t.Helper()
	raw, err := json.Marshal(msg)
	require.NoError(s.t, err)
	_, err = s.in.Write(append(raw, '\n'))
	require.NoError(s.t, err)
}

func (s *mcpSession) read() map[string]any {
	s.t.Helper()
	require.True(s.t, s.out.Scan(), "expected a response line")
	var m map[string]any
	require.NoError(s.t, json.Unmarshal(s.out.Bytes(), &m), s.out.Text())
	return m
}

// request sends a request and returns its response, checking the id.
func (s *mcpSession) request(method string, params any) map[string]any {
	s.t.Helper()
	s.next++
	msg := map[string]any{"jsonrpc": "2.0", "id": s.next, "method": method}
	if params != nil {
		msg["params"] = params
	}
	s.send(msg)
	resp := s.read()
	require.Equal(s.t, "2.0", resp["jsonrpc"])
	require.EqualValues(s.t, s.next, resp["id"])
	return resp
}

func (s *mcpSession) notify(method string) {
	s.send(map[string]any{"jsonrpc": "2.0", "method": method})
}

// toolNames returns the names tools/list lists.
func (s *mcpSession) toolNames() []string {
	s.t.Helper()
	resp := s.request("tools/list", map[string]any{})
	require.Nil(s.t, resp["error"])
	var names []string
	for _, tl := range resp["result"].(map[string]any)["tools"].([]any) {
		names = append(names, tl.(map[string]any)["name"].(string))
	}
	return names
}

// callTool runs tools/call and returns the result text and isError.
func (s *mcpSession) callTool(name string, args map[string]any) (string, bool) {
	s.t.Helper()
	resp := s.request("tools/call", map[string]any{"name": name, "arguments": args})
	require.Nil(s.t, resp["error"], "tools/call %s", name)
	res := resp["result"].(map[string]any)
	content := res["content"].([]any)
	require.Len(s.t, content, 1)
	isErr, _ := res["isError"].(bool)
	return content[0].(map[string]any)["text"].(string), isErr
}
