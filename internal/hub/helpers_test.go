package hub

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/apitokens"
	"github.com/vnovick/itervox/internal/server"
)

const memberBootstrap = "member-bootstrap-admin-token-0123456789"

// fakeDispatch is a member's feed and dispatch pause.
type fakeDispatch struct {
	mu     sync.Mutex
	events []server.FeedEvent
	seq    int64
	pauses atomic.Int32
}

func (d *fakeDispatch) add(kind, identifier string, ts time.Time) int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seq++
	d.events = append(d.events, server.FeedEvent{Seq: d.seq, TS: ts, Kind: kind, Identifier: identifier})
	return d.seq
}

func (d *fakeDispatch) Feed(since int64, limit int) server.FeedPage {
	d.mu.Lock()
	defer d.mu.Unlock()
	if limit <= 0 {
		limit = 100
	}
	p := server.FeedPage{Events: []server.FeedEvent{}, HeadSeq: d.seq, NextSeq: min(since, d.seq)}
	start := sort.Search(len(d.events), func(i int) bool { return d.events[i].Seq > since })
	end := min(start+limit, len(d.events))
	p.Events = append(p.Events, d.events[start:end]...)
	p.HasMore = end < len(d.events)
	if n := len(p.Events); n > 0 {
		p.NextSeq = p.Events[n-1].Seq
	}
	return p
}

func (d *fakeDispatch) PauseDispatch(context.Context, string, time.Time) (*server.DispatchPauseRow, error) {
	d.pauses.Add(1)
	return &server.DispatchPauseRow{PausedAt: time.Now()}, nil
}

func (d *fakeDispatch) ResumeDispatch(context.Context) (*server.DispatchPauseRow, error) {
	return nil, nil
}

// fakeMember is a real daemon API (internal/server) with its own token
// store, on an httptest server. lastAuth records the Authorization header
// of the last request the member received.
type fakeMember struct {
	dir      string
	workflow string
	store    *apitokens.Store
	srv      *server.Server
	ts       *httptest.Server
	disp     *fakeDispatch
	lastAuth atomic.Value
	requests atomic.Int64 // every request the member received
	writes   atomic.Int64 // every non-GET request (#230 member actions)
	// writeAuth is the Authorization of the last non-GET request.
	writeAuth atomic.Value
	rec       MemberRecord
}

func newFakeMember(t *testing.T, name string, startedAt time.Time) *fakeMember {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	wf := filepath.Join(dir, "WORKFLOW.md")
	m := &fakeMember{dir: dir, workflow: wf, disp: &fakeDispatch{}}
	m.store = apitokens.Open(apitokens.Path(wf))
	m.srv = server.New(server.Config{
		Snapshot: func() server.StateSnapshot {
			return server.StateSnapshot{ProjectName: name, Capabilities: []string{server.CapabilityDispatchPause}}
		},
		RefreshChan: make(chan struct{}, 1),
		APIToken:    memberBootstrap,
		Tokens:      m.store,
		Dispatch:    m.disp,
		Version:     "test-" + name,
	})
	m.ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.requests.Add(1)
		if r.Method != http.MethodGet {
			m.writes.Add(1)
			m.writeAuth.Store(r.Header.Get("Authorization"))
		}
		m.lastAuth.Store(r.Header.Get("Authorization"))
		m.srv.ServeHTTP(w, r)
	}))
	t.Cleanup(m.ts.Close)
	m.rec = MemberRecord{
		PID: 1000 + len(name), Workflow: wf, ProjectDir: dir, URL: m.ts.URL + "/",
		Version: "test-" + name, StartedAt: startedAt.UTC().Format(time.RFC3339), TokenRequired: true,
	}
	return m
}

func (m *fakeMember) auth() string {
	v, _ := m.lastAuth.Load().(string)
	return v
}

// storeTokenFunc creates member tokens directly in the member's store, as
// cmd/itervox's ensureStoredToken does.
func storeTokenFunc() TokenFunc {
	var mu sync.Mutex
	return func(m MemberRecord, scope apitokens.Scope) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		_, secret, err := apitokens.Open(apitokens.Path(m.Workflow)).Create("hub-"+string(scope), scope, nil, "test")
		return secret, err
	}
}

// memberList is a registry stand-in that daemons register into.
type memberList struct {
	mu   sync.Mutex
	recs []MemberRecord
}

func (l *memberList) add(r MemberRecord) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.recs = append(l.recs, r)
}

func (l *memberList) list() []MemberRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]MemberRecord(nil), l.recs...)
}

// testHub is a started hub on an httptest server with a hub token of
// every scope.
type testHub struct {
	h       *Hub
	ts      *httptest.Server
	secrets map[apitokens.Scope]string
	cancel  context.CancelFunc
}

func startTestHub(t *testing.T, cfg Config) *testHub {
	t.Helper()
	if cfg.StateDir == "" {
		cfg.StateDir = filepath.Join(t.TempDir(), "hub")
	}
	if cfg.MemberToken == nil {
		cfg.MemberToken = storeTokenFunc()
	}
	if cfg.FeedPoll == 0 {
		cfg.FeedPoll = 50 * time.Millisecond
	}
	if cfg.Rescan == 0 {
		cfg.Rescan = 100 * time.Millisecond
	}
	if cfg.RegistryDir == "" {
		cfg.RegistryDir = t.TempDir()
	}
	h, err := newHub(cfg)
	require.NoError(t, err)
	secrets := map[apitokens.Scope]string{}
	for _, sc := range apitokens.Scopes {
		if _, err := h.tokens.List(); err != nil {
			t.Fatal(err)
		}
		_, secret, err := h.tokens.Create(string(sc)+"-hub", sc, nil, "test")
		require.NoError(t, err)
		secrets[sc] = secret
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.start(ctx)
	th := &testHub{h: h, ts: httptest.NewServer(h.Handler()), secrets: secrets, cancel: cancel}
	t.Cleanup(func() { th.close() })
	return th
}

// close stops the hub and saves its cursors (idempotent).
func (th *testHub) close() {
	th.cancel()
	th.ts.Close()
	th.h.stop()
}

func doHub(t *testing.T, url, method, token string, body io.Reader) *http.Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	require.NoError(t, err)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(b)
}

// eventually polls cond every 20 ms until it holds or d passes.
func eventually(t *testing.T, d time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s: %s", d, msg)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
