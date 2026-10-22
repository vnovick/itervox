package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// fanoutWriter is an in-process SSE client for the fan-out benchmark and
// tests: it discards the bytes, remembers the last data frame, and reports
// every flush (one per frame) on flushed.
type fanoutWriter struct {
	header  http.Header
	flushed chan<- struct{}

	mu        sync.Mutex
	last      []byte
	lastFrame []byte
}

func (w *fanoutWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *fanoutWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	w.last = b
	w.mu.Unlock()
	return len(b), nil
}

func (w *fanoutWriter) WriteHeader(int) {}

func (w *fanoutWriter) Flush() {
	w.mu.Lock()
	w.lastFrame = w.last
	w.mu.Unlock()
	w.flushed <- struct{}{}
}

func (w *fanoutWriter) frame() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastFrame
}

// benchSnapshot builds a dashboard-sized snapshot the way cmd/itervox does:
// every call allocates fresh rows (3 running agents, 100 history rows, 300
// dependency-audit rows and graph nodes).
func benchSnapshot() StateSnapshot {
	snap := StateSnapshot{GeneratedAt: time.Now(), MaxConcurrentAgents: 3}
	for i := range 3 {
		snap.Running = append(snap.Running, RunningRow{
			Identifier: fmt.Sprintf("DEMO-%d", i+1), State: "In Progress", TurnCount: i,
			LastEvent: "Reading DEMO and the code it touches.", SessionID: fmt.Sprintf("demo-%d", i),
			StartedAt: time.Now(),
		})
	}
	for i := range 100 {
		snap.History = append(snap.History, HistoryRow{
			Identifier: fmt.Sprintf("DEMO-%d", i+10), Title: "Perf issue: optimise the thing with a moderately long title",
			StartedAt: time.Now(), FinishedAt: time.Now(), Status: "succeeded", SessionID: fmt.Sprintf("s-%d", i),
		})
	}
	for i := range 300 {
		id := fmt.Sprintf("DEMO-%d", i+200)
		snap.DependencyAudit = append(snap.DependencyAudit, DependencyAuditRow{
			Identifier: id, IssueState: "Todo", Status: "unblocked",
		})
		snap.DependencyGraphNodes = append(snap.DependencyGraphNodes, DependencyGraphNodeRow{
			ID: id, Identifier: id, Title: "Perf issue: optimise the thing", State: "Todo", Status: "unblocked",
		})
	}
	return snap
}

// startFanoutClients connects n in-process SSE clients to s's /events
// handler and waits until each has its first frame and is subscribed.
func startFanoutClients(tb testing.TB, s *Server, n int) ([]*fanoutWriter, <-chan struct{}, func()) {
	tb.Helper()
	flushed := make(chan struct{}, 4*n)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	writers := make([]*fanoutWriter, n)
	for i := range n {
		w := &fanoutWriter{flushed: flushed}
		writers[i] = w
		req := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil).WithContext(ctx)
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.handleEvents(w, req)
		}()
	}
	for range n {
		<-flushed // each client's first frame
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.bc.mu.Lock()
		subscribed := len(s.bc.clients)
		s.bc.mu.Unlock()
		if subscribed == n {
			break
		}
		if time.Now().After(deadline) {
			tb.Fatalf("only %d of %d SSE clients subscribed", subscribed, n)
		}
		time.Sleep(time.Millisecond)
	}
	stop := func() {
		cancel()
		go func() { // drain flushes still in flight so no handler blocks
			for range flushed {
			}
		}()
		wg.Wait()
		close(flushed)
	}
	return writers, flushed, stop
}

// BenchmarkSSEFanout10Clients measures one state change delivered to ten
// dashboard streams: one op is a Notify plus all ten clients flushing the
// new frame (#210). Notify coalescing is turned off so every op is a new
// version.
func BenchmarkSSEFanout10Clients(b *testing.B) {
	const clients = 10
	s := New(Config{Snapshot: benchSnapshot, RefreshChan: make(chan struct{}, 1)})
	disableNotifyCoalescing(s)
	_, flushed, stop := startFanoutClients(b, s, clients)
	defer stop()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		s.Notify()
		for range clients {
			<-flushed
		}
	}
	b.StopTimer()
}
