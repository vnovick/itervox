package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/server"
)

// CORE-025 — SSE handlers must not survive a config reload attached to the
// old generation. Every run() generation serves its own server.Server on a
// view of the shared persistentListener (serveOnListener); before the fix, a
// reload's http.Server.Shutdown neither cancelled nor woke the streaming
// handlers, so an open /api/v1/events stream stayed pinned to the old
// generation's snapshot closure, received only keepalives, and kept the old
// http.Server (and, through the closure, the old Orchestrator) alive.
//
// The chosen design is (a) close-and-reconnect: Shutdown ends every stream
// of the old generation promptly, the client sees a clean EOF, and
// openAuthedEventStream reconnects (CORE-004) — to the NEW generation, since
// the old one no longer accepts. In-flight non-streaming requests still drain
// (they are not cancelled), which keeps the v0.2.0 promise that a reload does
// not drop in-flight requests.

// reloadStreamDeadline is how long a stream may take to leave the old
// generation. It is well inside the 5 s shutdown deadline on purpose: the
// streams must be ended by the reload's Shutdown itself, not merely
// force-closed by the srv.Close() fallback when the deadline passes (which
// would still leave every tab on stale data for 5 s and make every reload
// run to its deadline).
const reloadStreamDeadline = 2 * time.Second

// testGeneration is one run() generation's HTTP side: its server.Server, the
// Serve-exit channel, and a count of its handlers still running.
type testGeneration struct {
	srv      *server.Server
	done     <-chan error
	inFlight *atomic.Int64
}

// countingHandler counts running handlers. It deliberately does NOT forward
// Shutdown: serveOnListener takes the stream closer explicitly, so a wrapper
// no longer has to re-expose it.
type countingHandler struct {
	srv      *server.Server
	inFlight *atomic.Int64
}

func (c countingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.inFlight.Add(1)
	defer c.inFlight.Add(-1)
	c.srv.ServeHTTP(w, r)
}

func startTestGeneration(t *testing.T, ctx context.Context, p *persistentListener, gen int, logFile string, fetchIssue func(context.Context, string) (*server.TrackerIssue, error)) testGeneration {
	t.Helper()
	srv := server.New(server.Config{
		Snapshot:    func() server.StateSnapshot { return server.StateSnapshot{MaxConcurrentAgents: gen} },
		RefreshChan: make(chan struct{}, 1),
		LogFile:     logFile,
		FetchIssue:  fetchIssue,
		Client:      reloadSSEClient{},
	})
	var inFlight atomic.Int64
	done := serveOnListener(ctx, p.generation(), p.Addr().String(), countingHandler{srv: srv, inFlight: &inFlight}, srv.Shutdown)
	return testGeneration{srv: srv, done: done, inFlight: &inFlight}
}

// sseFrame is one parsed SSE frame, or the end of the stream (eof).
type sseFrame struct {
	event, data string
	eof         bool
}

// openStream GETs path and pumps its SSE frames into the returned channel
// until the body ends (a final eof frame) or the test finishes.
func openStream(t *testing.T, addr, path string) <-chan sseFrame {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+path, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, path)
	out := make(chan sseFrame, 64)
	go func() {
		defer func() { _ = resp.Body.Close() }()
		rd := bufio.NewReader(resp.Body)
		var cur sseFrame
		for {
			line, err := rd.ReadString('\n')
			if err != nil {
				out <- sseFrame{eof: true}
				return
			}
			line = strings.TrimRight(line, "\n")
			switch {
			case line == "":
				if cur.event != "" || cur.data != "" {
					out <- cur
				}
				cur = sseFrame{}
			case strings.HasPrefix(line, "event: "):
				cur.event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				cur.data = strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	return out
}

func snapshotGeneration(t *testing.T, f sseFrame) int {
	t.Helper()
	var snap struct {
		MaxConcurrentAgents int `json:"maxConcurrentAgents"`
	}
	require.NoError(t, json.Unmarshal([]byte(f.data), &snap), f.data)
	return snap.MaxConcurrentAgents
}

// reload cancels gen's run context the way main()'s reload loop does, waits
// for its Serve to return, and starts the next generation on the same socket.
func reload(t *testing.T, cancel context.CancelFunc, old testGeneration, start func() testGeneration) testGeneration {
	t.Helper()
	cancel()
	select {
	case err := <-old.done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("old generation's Serve did not return after its run context ended")
	}
	return start()
}

// TestReloadDeliversNewGenerationSnapshotToOpenStream: a client holding
// /api/v1/events open across a reload receives, within the shutdown
// deadline, either EOF (and a reconnect then reaches the new generation) or a
// snapshot produced by the NEW generation — never only keepalives or the old
// generation's data.
func TestReloadDeliversNewGenerationSnapshotToOpenStream(t *testing.T) {
	p := newTestPersistentListener(t)
	addr := p.Addr().String()

	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	g1 := startTestGeneration(t, ctx1, p, 1, "", nil)

	frames := openStream(t, addr, "/api/v1/events")
	first := <-frames
	require.False(t, first.eof)
	require.Equal(t, 1, snapshotGeneration(t, first))

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	g2 := reload(t, cancel1, g1, func() testGeneration { return startTestGeneration(t, ctx2, p, 2, "", nil) })

	// Keep the new generation publishing, so a stream that is attached to it
	// in any way sees a generation-2 snapshot.
	stopNotify := make(chan struct{})
	defer close(stopNotify)
	go func() {
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stopNotify:
				return
			case <-tick.C:
				g2.srv.Notify()
			}
		}
	}()

	deadline := time.After(reloadStreamDeadline)
	for {
		select {
		case f := <-frames:
			if f.eof {
				// Clean close: the client reconnects, and the reconnect must
				// reach the new generation.
				again := openStream(t, addr, "/api/v1/events")
				f2 := <-again
				require.False(t, f2.eof)
				assert.Equal(t, 2, snapshotGeneration(t, f2), "a reconnect after the reload must reach the new generation")
				return
			}
			if f.event == "keepalive" {
				continue
			}
			if snapshotGeneration(t, f) == 2 {
				return
			}
		case <-deadline:
			t.Fatalf("open /api/v1/events stream neither ended nor received a new-generation snapshot within %v of the reload (zombie stream pinned to the old generation)", reloadStreamDeadline)
		}
	}
}

// TestReloadReleasesOldGeneration: after a reload, no handler of the old
// generation is still running once the shutdown deadline has passed — for
// every streaming route (/events, /logs, /issues/{id}/log-stream,
// /issues/{id}/sublog-stream) — so nothing keeps the old http.Server, its
// snapshot closure or its Orchestrator reachable. Each stream ends with EOF.
func TestReloadReleasesOldGeneration(t *testing.T) {
	p := newTestPersistentListener(t)
	addr := p.Addr().String()
	logFile := filepath.Join(t.TempDir(), "itervox.log")
	require.NoError(t, os.WriteFile(logFile, []byte("boot\n"), 0o644))

	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	g1 := startTestGeneration(t, ctx1, p, 1, logFile, nil)

	paths := []string{"/api/v1/events", "/api/v1/logs", "/api/v1/issues/ENG-1/log-stream", "/api/v1/issues/ENG-1/sublog-stream"}
	streams := make([]<-chan sseFrame, len(paths))
	for i, path := range paths {
		streams[i] = openStream(t, addr, path)
	}
	require.Eventually(t, func() bool { return g1.inFlight.Load() == int64(len(paths)) },
		2*time.Second, 10*time.Millisecond, "every stream must be running on generation 1")

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	_ = reload(t, cancel1, g1, func() testGeneration { return startTestGeneration(t, ctx2, p, 2, logFile, nil) })

	for i, frames := range streams {
		deadline := time.After(reloadStreamDeadline)
	drain:
		for {
			select {
			case f := <-frames:
				if f.eof {
					break drain
				}
			case <-deadline:
				t.Fatalf("%s: stream still open %v after the reload", paths[i], reloadStreamDeadline)
			}
		}
	}
	assert.Eventually(t, func() bool { return g1.inFlight.Load() == 0 },
		time.Second, 10*time.Millisecond, "old-generation handlers still running after the reload: %d", g1.inFlight.Load())
}

// TestReloadDrainsInFlightRequests: closing the old generation's streams must
// not cancel its ordinary in-flight requests — a request already being served
// when the reload starts completes with its response (the v0.2.0 promise that
// a reload does not drop in-flight requests).
func TestReloadDrainsInFlightRequests(t *testing.T) {
	p := newTestPersistentListener(t)
	addr := p.Addr().String()

	entered := make(chan struct{})
	release := make(chan struct{})
	slow := func(ctx context.Context, id string) (*server.TrackerIssue, error) {
		close(entered)
		select {
		case <-release:
			return &server.TrackerIssue{Identifier: id, Title: "drained"}, nil
		case <-ctx.Done():
			return nil, errors.New("request context cancelled by the reload")
		}
	}
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	g1 := startTestGeneration(t, ctx1, p, 1, "", slow)

	type result struct {
		status int
		body   string
		err    error
	}
	res := make(chan result, 1)
	go func() {
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Get("http://" + addr + "/api/v1/issues/ENG-7")
		if err != nil {
			res <- result{err: err}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		b, err := io.ReadAll(resp.Body)
		res <- result{status: resp.StatusCode, body: string(b), err: err}
	}()
	<-entered

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	_ = reload(t, cancel1, g1, func() testGeneration { return startTestGeneration(t, ctx2, p, 2, "", nil) })
	time.Sleep(100 * time.Millisecond) // the reload is under way; the request is still in flight
	close(release)

	r := <-res
	require.NoError(t, r.err)
	assert.Equal(t, http.StatusOK, r.status, r.body)
	assert.Contains(t, r.body, "drained")
}

// TestReloadForceClosesStuckRequestsAtDeadline: a handler that ignores the
// reload (it only watches its own request context) is force-closed once the
// shutdown deadline passes — srv.Close() — instead of outliving its
// generation indefinitely.
func TestReloadForceClosesStuckRequestsAtDeadline(t *testing.T) {
	p := newTestPersistentListener(t)
	addr := p.Addr().String()

	var inFlight atomic.Int64
	entered := make(chan struct{}, 1)
	stuck := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inFlight.Add(1)
		defer inFlight.Add(-1)
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		entered <- struct{}{}
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := serveOnListener(ctx, p.generation(), addr, stuck, nil)

	reqCtx, reqCancel := context.WithCancel(context.Background())
	defer reqCancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, "http://"+addr+"/", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	<-entered

	cancel()
	<-done
	assert.Eventually(t, func() bool { return inFlight.Load() == 0 },
		shutdownTimeout+2*time.Second, 20*time.Millisecond,
		"a request still running at the shutdown deadline must be force-closed")
}

// TestServeOnListenerClosesStreamsOfWrappedHandler: the SSE streams must be
// ended by shutdown however the dashboard handler is wrapped. The stream
// closer used to be found by type-asserting the handler to streamCloser,
// which only matched an unwrapped *server.Server: any middleware around it
// (a request logger, a counting wrapper that forgot to forward Shutdown) hid
// the method, so every open stream kept its generation alive until the 5 s
// shutdown deadline force-closed it.
func TestServeOnListenerClosesStreamsOfWrappedHandler(t *testing.T) {
	p := newTestPersistentListener(t)
	addr := p.Addr().String()
	srv := server.New(server.Config{
		Snapshot:    func() server.StateSnapshot { return server.StateSnapshot{} },
		RefreshChan: make(chan struct{}, 1),
	})
	var inFlight atomic.Int64
	// A plain middleware: it does NOT expose Shutdown.
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inFlight.Add(1)
		defer inFlight.Add(-1)
		w.Header().Set("X-Test-Middleware", "1")
		srv.ServeHTTP(w, r)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := serveOnListener(ctx, p.generation(), addr, wrapped, srv.Shutdown)

	frames := openStream(t, addr, "/api/v1/events")
	require.Eventually(t, func() bool { return inFlight.Load() == 1 },
		2*time.Second, 10*time.Millisecond, "the stream must be running")

	start := time.Now()
	cancel()
	deadline := time.After(reloadStreamDeadline)
	for {
		select {
		case f := <-frames:
			if !f.eof {
				continue
			}
			select {
			case <-done:
			case <-time.After(reloadStreamDeadline - time.Since(start)):
				t.Fatalf("serve loop still running %v after shutdown began", reloadStreamDeadline)
			}
			assert.Less(t, time.Since(start), reloadStreamDeadline,
				"shutdown must end the stream itself, not wait for the %v force-close", shutdownTimeout)
			return
		case <-deadline:
			t.Fatalf("stream behind a wrapping middleware still open %v after shutdown began (closer not registered)", reloadStreamDeadline)
		}
	}
}

// reloadSSEClient serves the two log-stream calls these tests make; every
// other OrchestratorClient method is the nil embedded interface and panics
// if reached (server.FuncClient is test-only in package server, CORE-110).
type reloadSSEClient struct{ server.OrchestratorClient }

func (reloadSSEClient) GetSince(_ context.Context, _ string, _ uint32, cursor int64, _ bool) ([]string, uint32, int64, bool) {
	return nil, 1, cursor, false
}

func (reloadSSEClient) FetchSubLogs(context.Context, string) ([]domain.IssueLogEntry, error) {
	return nil, nil
}
