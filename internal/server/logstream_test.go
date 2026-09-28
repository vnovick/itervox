package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/logbuffer"
)

// fakeLogStreamTicker lets tests drive handleIssueLogStream's poll loop
// deterministically via a hand-fed channel, instead of racing a real 500ms
// wall-clock ticker across multiple batches/ticks (which would make
// multi-tick tests both slow and flaky under -race/-count=5).
type fakeLogStreamTicker struct {
	ch chan time.Time
}

func (f *fakeLogStreamTicker) C() <-chan time.Time { return f.ch }
func (f *fakeLogStreamTicker) Stop()               {}

// installFakeLogStreamTicker overrides newLogStreamTicker for the life of
// the test. It returns the channel tests use to feed ticks, and a channel
// that's closed once the handler has created its ticker — i.e. the initial
// sendNew() call has already completed and the handler is now parked in its
// select loop, ready to receive the first tick. Because tickCh is
// unbuffered, a subsequent send on it only completes once the handler is
// back at the select statement, which is only true once the *previous*
// sendNew() call has fully finished — so sequential sends on tickCh
// naturally serialize with the handler's processing, with no sleeps needed.
func installFakeLogStreamTicker(t *testing.T) (tickCh chan time.Time, started chan struct{}) {
	t.Helper()
	tickCh = make(chan time.Time)
	started = make(chan struct{})
	old := newLogStreamTicker
	newLogStreamTicker = func() logStreamTicker {
		close(started)
		return &fakeLogStreamTicker{ch: tickCh}
	}
	t.Cleanup(func() { newLogStreamTicker = old })
	return tickCh, started
}

// jsonLogLine builds a minimal valid domain.BufLogEntry JSON line so
// parseLogLine classifies it as an "info" event instead of skipping it as
// unparseable (a real agent log line is always JSON; plain strings are not).
func jsonLogLine(msg string) string {
	return fmt.Sprintf(`{"level":"INFO","msg":%q}`, msg)
}

func newLogStreamTestServer(client OrchestratorClient) *Server {
	return New(Config{
		Snapshot:    func() StateSnapshot { return StateSnapshot{} },
		RefreshChan: make(chan struct{}, 1),
		Client:      client,
		// httptest.NewRequest addresses example.com (CORE-162 Host guard).
		AllowedHosts: testAllowedHosts,
	})
}

// TestIssueLogStreamContinuesPast500Lines pins the CORE-003 fix: 600 lines
// delivered in 6 batches of 100 (a poll tick between each batch, so every
// batch lands inside the 500-line retained window) must all reach the
// client as "event: log" frames, with zero gap frames — the old positional
// cursor permanently stalled after the first 500 because it compared
// cumulative-sent-count to len(FetchLogs()) (always <=500) instead of a
// real sequence number.
func TestIssueLogStreamContinuesPast500Lines(t *testing.T) {
	buf := logbuffer.New()
	t.Cleanup(func() { _ = buf.Close(context.Background()) }) // CORE-112: stop the disk writer goroutine
	identifier := "ENG-1"

	tickCh, started := installFakeLogStreamTicker(t)
	srv := newLogStreamTestServer(&FuncClient{GetSinceFn: buf.GetSinceContext})

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/issues/"+identifier+"/log-stream", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.ServeHTTP(w, req)
	}()

	<-started // buffer is still empty here; initial sendNew delivered 0 frames

	for batch := 0; batch < 6; batch++ {
		for i := 0; i < 100; i++ {
			buf.Add(identifier, jsonLogLine(fmt.Sprintf("line-%d", batch*100+i)))
		}
		tickCh <- time.Now()
	}

	cancel()
	<-done

	body := w.Body.String()
	assert.Equal(t, 600, strings.Count(body, "event: log"), "all 600 appended lines must be delivered")
	assert.Zero(t, strings.Count(body, "event: gap"), "no gap expected when polling keeps up with the window")
	assert.Contains(t, body, `"line-0"`)
	assert.Contains(t, body, `"line-599"`)
}

// TestIssueLogStreamEmitsGapWhenCursorFallsOutOfWindow pins the gap
// contract: appending more than the 500-line window without an intervening
// poll must not silently drop the missed lines — the client gets exactly
// one "event: gap" frame followed by a full replay of the current window,
// and further ticks with no new data must not emit spurious extra gaps.
func TestIssueLogStreamEmitsGapWhenCursorFallsOutOfWindow(t *testing.T) {
	buf := logbuffer.New()
	t.Cleanup(func() { _ = buf.Close(context.Background()) }) // CORE-112: stop the disk writer goroutine
	identifier := "ENG-1"

	tickCh, started := installFakeLogStreamTicker(t)
	srv := newLogStreamTestServer(&FuncClient{GetSinceFn: buf.GetSinceContext})

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/issues/"+identifier+"/log-stream", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.ServeHTTP(w, req)
	}()

	<-started // initial sendNew ran against an empty buffer: 0 frames so far

	// 700 lines with no poll in between: the retained window becomes lines
	// 200..699 (700-500 trimmed), which no longer overlaps the client's
	// seq-0 cursor at all.
	for i := 0; i < 700; i++ {
		buf.Add(identifier, jsonLogLine(fmt.Sprintf("line-%d", i)))
	}
	tickCh <- time.Now()

	// Three further ticks with no new lines: the cursor is now caught up,
	// so these must add zero additional gap (or log) frames.
	for i := 0; i < 3; i++ {
		tickCh <- time.Now()
	}

	cancel()
	<-done

	body := w.Body.String()
	assert.Equal(t, 1, strings.Count(body, "event: gap"), "exactly one gap frame expected")
	assert.Equal(t, 500, strings.Count(body, "event: log"), "the gap replay must be exactly the retained 500-line window")
	assert.Contains(t, body, `"line-200"`)
	assert.Contains(t, body, `"line-699"`)
	assert.NotContains(t, body, `"line-199"`, "line-199 fell out of the retained window and must not be replayed")

	gapIdx := strings.Index(body, "event: gap")
	firstLogIdx := strings.Index(body, "event: log")
	require.True(t, gapIdx >= 0 && firstLogIdx >= 0)
	assert.Less(t, gapIdx, firstLogIdx, "the gap frame must precede the window replay")
}

// TestIssueLogStreamForeignEpochCursorEmitsGap pins the restart contract: a
// Last-Event-ID whose epoch does not match the buffer's current epoch (a
// daemon restart rebuilt the log buffer, or a pre-CORE-003 client echoed a
// bare-integer id) must always be treated as a gap, even when the numeric
// cursor value would otherwise look perfectly in-range.
func TestIssueLogStreamForeignEpochCursorEmitsGap(t *testing.T) {
	buf := logbuffer.New()
	t.Cleanup(func() { _ = buf.Close(context.Background()) }) // CORE-112: stop the disk writer goroutine
	identifier := "ENG-1"
	buf.Add(identifier, jsonLogLine("hello"))
	buf.Add(identifier, jsonLogLine("world"))

	srv := newLogStreamTestServer(&FuncClient{GetSinceFn: buf.GetSinceContext})

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/issues/"+identifier+"/log-stream", nil).WithContext(ctx)
	// A foreign epoch with a cursor (2) that exactly matches the buffer's
	// real seq — if epoch weren't checked, this would look "fully caught
	// up" and wrongly suppress the replay.
	req.Header.Set("Last-Event-ID", fmt.Sprintf("%d-%d", buf.Epoch()^1, 2))
	// The handler's initial sendNew() runs unconditionally before the loop
	// checks ctx.Done(), so canceling up front is enough to end the stream
	// right after that first (gap-producing) call — no ticks needed.
	cancel()

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	body := w.Body.String()
	assert.Equal(t, 1, strings.Count(body, "event: gap"))
	assert.Equal(t, 2, strings.Count(body, "event: log"), "full window replay (2 lines) expected")
	assert.Contains(t, body, `"hello"`)
	assert.Contains(t, body, `"world"`)
}

// lastSSEID extracts the seq portion of the LAST "id: <epoch>-<seq>" line in
// an SSE response body — exactly what a real client (fetch-event-source)
// would remember and echo back via Last-Event-ID on reconnect.
func lastSSEID(t *testing.T, body string) string {
	t.Helper()
	var last string
	for _, line := range strings.Split(body, "\n") {
		if id, ok := strings.CutPrefix(line, "id: "); ok {
			last = id
		}
	}
	require.NotEmpty(t, last, "no id: line found in SSE body")
	return last
}

// TestIssueLogStreamIDRoundTripsAsLastEventID pins CORE-003 review round
// G1's Minor #1: the resume contract lives in the *wire format* ("id:
// <epoch>-<seq>", written by handleIssueLogStream and read back by
// parseLogStreamCursor), not in the handler's in-memory closure state. A
// regression that reverted the emitted id to a bare positional counter (the
// pre-CORE-003 bug) would still pass every other log-stream test here,
// because they all carry the cursor through the closure rather than
// through an actual second HTTP request replaying a real emitted id. This
// test asserts the literal "<epoch>-<seq>" text AND performs a second,
// independent request that replays exactly the id the first response
// emitted.
func TestIssueLogStreamIDRoundTripsAsLastEventID(t *testing.T) {
	buf := logbuffer.New()
	t.Cleanup(func() { _ = buf.Close(context.Background()) }) // CORE-112: stop the disk writer goroutine
	identifier := "ENG-1"
	buf.Add(identifier, jsonLogLine("hello"))
	buf.Add(identifier, jsonLogLine("world"))

	srv := newLogStreamTestServer(&FuncClient{GetSinceFn: buf.GetSinceContext})

	ctx1, cancel1 := context.WithCancel(context.Background())
	req1 := httptest.NewRequest(http.MethodGet, "/api/v1/issues/"+identifier+"/log-stream", nil).WithContext(ctx1)
	cancel1() // the initial sendNew() is all this request needs
	w1 := httptest.NewRecorder()
	srv.ServeHTTP(w1, req1)

	body1 := w1.Body.String()
	wantID := fmt.Sprintf("id: %d-2", buf.Epoch())
	require.Contains(t, body1, wantID, "emitted id must be the wire-format <epoch>-<seq>, not a bare positional counter")

	lastID := lastSSEID(t, body1)
	require.Equal(t, fmt.Sprintf("%d-2", buf.Epoch()), lastID)

	// A brand new request replays exactly that id via Last-Event-ID.
	buf.Add(identifier, jsonLogLine("third"))
	ctx2, cancel2 := context.WithCancel(context.Background())
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/issues/"+identifier+"/log-stream", nil).WithContext(ctx2)
	req2.Header.Set("Last-Event-ID", lastID)
	cancel2()
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, req2)

	body2 := w2.Body.String()
	assert.Equal(t, 1, strings.Count(body2, "event: log"), "replaying the server's own id must resume exactly after it — only the one new line")
	assert.Zero(t, strings.Count(body2, "event: gap"))
	assert.Contains(t, body2, `"third"`)
	assert.NotContains(t, body2, `"hello"`)
	assert.NotContains(t, body2, `"world"`)
}

// TestIssueLogStreamGapFrameIDIsIdempotentUnderMidReplayDisconnect pins
// CORE-003 review round G1's Minor #3: the gap frame's id must be the seq
// immediately BEFORE the replay it introduces, not `next` (the seq of the
// replay's LAST line). Otherwise a disconnect between the gap frame and the
// "event: log" frames that follow it loses the replay for good: the client
// would reconnect at `next`, which GetSince sees as already caught up, and
// silently withhold the window it was promised.
func TestIssueLogStreamGapFrameIDIsIdempotentUnderMidReplayDisconnect(t *testing.T) {
	buf := logbuffer.New()
	t.Cleanup(func() { _ = buf.Close(context.Background()) }) // CORE-112: stop the disk writer goroutine
	identifier := "ENG-1"
	for i := 0; i < 700; i++ {
		buf.Add(identifier, jsonLogLine(fmt.Sprintf("line-%d", i)))
	}

	srv := newLogStreamTestServer(&FuncClient{GetSinceFn: buf.GetSinceContext})

	// A stale, in-range-looking cursor forces the gap path.
	ctx1, cancel1 := context.WithCancel(context.Background())
	req1 := httptest.NewRequest(http.MethodGet, "/api/v1/issues/"+identifier+"/log-stream", nil).WithContext(ctx1)
	req1.Header.Set("Last-Event-ID", fmt.Sprintf("%d-1", buf.Epoch()))
	cancel1()
	w1 := httptest.NewRecorder()
	srv.ServeHTTP(w1, req1)

	body1 := w1.Body.String()
	require.Equal(t, 1, strings.Count(body1, "event: gap"))
	require.Equal(t, 500, strings.Count(body1, "event: log"))

	// Simulate a disconnect that happened right after the gap frame landed
	// but before any of the replay's "event: log" frames were received:
	// reconnect with exactly the gap frame's own id.
	gapLine := ""
	for _, line := range strings.Split(body1, "\n") {
		if strings.HasPrefix(line, "id: ") {
			gapLine = strings.TrimPrefix(line, "id: ")
			break // the FIRST id: line in the body belongs to the gap frame
		}
	}
	require.NotEmpty(t, gapLine)
	require.Equal(t, fmt.Sprintf("%d-200", buf.Epoch()), gapLine,
		"gap frame id must be the seq before the replay (700-500), not `next` (700)")

	ctx2, cancel2 := context.WithCancel(context.Background())
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/issues/"+identifier+"/log-stream", nil).WithContext(ctx2)
	req2.Header.Set("Last-Event-ID", gapLine)
	cancel2()
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, req2)

	body2 := w2.Body.String()
	assert.Equal(t, 500, strings.Count(body2, "event: log"), "reconnecting at the gap frame's own id must deliver the FULL replay again, not nothing")
	assert.Contains(t, body2, `"line-200"`)
	assert.Contains(t, body2, `"line-699"`)
}

// TestIssueLogStreamQuietPollsAfterDiskSourcedGap is the CORE-003 review
// round G5 regression test: a completed (Removed), disk-backed issue —
// production's actual configuration for every completed issue
// (internal/orchestrator/worker.go:1012-1014 calls Remove on every
// successful worker completion; cmd/itervox/main.go always configures a
// log directory) — must gap AT MOST ONCE for a genuine discontinuity, then
// go quiet: repeated polling of the same, unchanged disk-sourced window
// must emit neither further gap frames nor duplicate log frames. An
// earlier version (round G1's fix for a DIFFERENT bug) forced a gap on
// EVERY poll of any disk-sourced window, which — because a completed
// issue's log-stream sources from disk on every single poll — meant any
// client holding the stream open on a completed issue got a full gap+
// replay every 500ms forever. Reproduced against that shipped code (10
// lines, Removed, 5 dormant poll ticks -> 5 gap frames); fixed by making
// disk-sourced sequence numbers absolute (see logbuffer.Buffer.GetSince's
// doc comment) so a caught-up cursor stays caught up across polls
// regardless of whether the window is sourced from memory or disk.
func TestIssueLogStreamQuietPollsAfterDiskSourcedGap(t *testing.T) {
	dir := t.TempDir()
	buf := logbuffer.New()
	t.Cleanup(func() { _ = buf.Close(context.Background()) }) // CORE-112: stop the disk writer goroutine
	buf.SetLogDir(dir)
	identifier := "ENG-1"
	for i := 0; i < 600; i++ {
		buf.Add(identifier, jsonLogLine(fmt.Sprintf("line-%d", i)))
	}
	epoch := buf.Epoch()
	buf.Remove(identifier) // matches worker.go's post-completion eviction; disk keeps all 600 lines

	tickCh, started := installFakeLogStreamTicker(t)
	srv := newLogStreamTestServer(&FuncClient{GetSinceFn: buf.GetSinceContext})

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/issues/"+identifier+"/log-stream", nil).WithContext(ctx)
	// A genuinely stale cursor (seq 1, below the retained window's absolute
	// base of 100) forces exactly one legitimate gap on first contact with
	// the disk-sourced window — see TestGetSince_DiskFallbackCursorBelowRetainedWindowIsGap
	// for the logbuffer-level version of this same discontinuity.
	req.Header.Set("Last-Event-ID", fmt.Sprintf("%d-1", epoch))
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.ServeHTTP(w, req)
	}()

	<-started // the initial sendNew() (the one legitimate gap + its replay) has already run

	// 5 further ticks with zero new data: a dormant, disk-sourced window
	// must NOT re-gap on every poll (the round G5 regression) once the
	// handler's re-armed cursor has caught up.
	for i := 0; i < 5; i++ {
		tickCh <- time.Now()
	}

	cancel()
	<-done

	body := w.Body.String()
	assert.Equal(t, 1, strings.Count(body, "event: gap"), "exactly one gap for the one real discontinuity, not one per poll")
	assert.Equal(t, 500, strings.Count(body, "event: log"), "the one gap's replay, and nothing more from 5 dormant polls")
}

// TestIssueLogStreamQuietPollsAfterClearAddRemove reproduces M0-close G2 at
// the SSE layer, in the style of TestIssueLogStreamQuietPollsAfterDiskSourcedGap:
// seq 5 → Clear (file deleted, seq preserved) → Add (seq 6, 1-line file) →
// Remove (memory empty). A client resuming from a pre-Clear cursor must get
// exactly ONE gap and ONE log frame across the first poll plus 5 dormant
// polls. Pre-fix the disk window was irreconcilable on every poll and the
// handler re-armed its cursor at next=1, so every poll emitted gap + replay.
func TestIssueLogStreamQuietPollsAfterClearAddRemove(t *testing.T) {
	dir := t.TempDir()
	buf := logbuffer.New()
	t.Cleanup(func() { _ = buf.Close(context.Background()) }) // CORE-112: stop the disk writer goroutine
	buf.SetLogDir(dir)
	identifier := "ENG-1"
	for i := 0; i < 5; i++ {
		buf.Add(identifier, jsonLogLine(fmt.Sprintf("line-%d", i)))
	}
	epoch := buf.Epoch()
	require.NoError(t, buf.Clear(identifier))
	buf.Add(identifier, jsonLogLine("after-clear"))
	buf.Remove(identifier)

	tickCh, started := installFakeLogStreamTicker(t)
	srv := newLogStreamTestServer(&FuncClient{GetSinceFn: buf.GetSinceContext})

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/issues/"+identifier+"/log-stream", nil).WithContext(ctx)
	req.Header.Set("Last-Event-ID", fmt.Sprintf("%d-2", epoch)) // pre-Clear, behind
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.ServeHTTP(w, req)
	}()
	<-started
	for i := 0; i < 5; i++ {
		tickCh <- time.Now()
	}
	cancel()
	<-done

	body := w.Body.String()
	assert.Equal(t, 1, strings.Count(body, "event: gap"), "one discontinuity, one gap — not one per poll")
	assert.Equal(t, 1, strings.Count(body, "event: log"), "the one line, delivered once")
	assert.Contains(t, body, fmt.Sprintf("id: %d-6\nevent: log", epoch), "after-clear carries its absolute seq 6")
}
