package orchestrator

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// CORE-008 — goSafe / RecoverGoroutine panic containment.

// captureSlog swaps the default slog logger for a buffer-backed one for the
// duration of the test. Tests in this file do not call t.Parallel.
func captureSlog(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestGoSafeRecoversAndCounts(t *testing.T) {
	logs := captureSlog(t)
	before := GoroutinePanicCount()

	var wg sync.WaitGroup
	onPanicRan := make(chan struct{})
	goSafe(&wg, "test-leaf", "ENG-7", func() {
		panic("boom from leaf")
	}, func() { close(onPanicRan) })
	wg.Wait() // returns only because Done runs after the recover + onPanic

	select {
	case <-onPanicRan:
	default:
		t.Fatal("onPanic must have run before wg.Done")
	}
	assert.Equal(t, before+1, GoroutinePanicCount(), "one recovered panic")
	out := logs.String()
	assert.Contains(t, out, "goroutine panic recovered")
	assert.Contains(t, out, "goroutine=test-leaf")
	assert.Contains(t, out, "identifier=ENG-7")
	assert.Contains(t, out, "boom from leaf")
	assert.Contains(t, out, "stack=")

	// A panic inside onPanic is contained too, logged and counted.
	goSafe(&wg, "test-leaf-2", "ENG-8", func() {
		panic("first")
	}, func() { panic("second, inside onPanic") })
	wg.Wait()
	assert.Equal(t, before+3, GoroutinePanicCount(), "leaf panic + onPanic panic both counted")
	assert.Contains(t, logs.String(), "goroutine onPanic callback panicked")

	// A clean goroutine is not counted and never runs onPanic.
	goSafe(&wg, "test-clean", "", func() {}, func() { t.Error("onPanic must not run without a panic") })
	wg.Wait()
	assert.Equal(t, before+3, GoroutinePanicCount())
}

// panickingTracker panics inside UpdateIssueState — the call both async
// discard goroutines make (asyncDiscardAndTransition directly on o.tracker,
// asyncDiscardAndTransitionTo through the direct write sink).
type panickingTracker struct {
	*tracker.MemoryTracker
}

func (p *panickingTracker) UpdateIssueState(context.Context, string, string) error {
	panic("tracker double panicked in UpdateIssueState")
}

func TestAsyncDiscardPanicStillSendsDiscardComplete(t *testing.T) {
	captureSlog(t)
	cases := []struct {
		name   string
		launch func(o *Orchestrator, s State) State
	}{
		{"asyncDiscardAndTransition", func(o *Orchestrator, s State) State {
			return o.asyncDiscardAndTransition(s, "id1", "ENG-1")
		}},
		{"asyncDiscardAndTransitionTo", func(o *Orchestrator, s State) State {
			return o.asyncDiscardAndTransitionTo(s, "id1", "ENG-1", "Cancelled", "In Progress")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := automationBaseCfg()
			cfg.Tracker.BacklogStates = []string{"Backlog"}
			issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "T", State: "In Progress"}
			mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
			o := New(cfg, &panickingTracker{MemoryTracker: mt}, &agenttest.FakeRunner{}, nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			o.runCtx.Store(&ctx)

			state := NewState(cfg)
			before := GoroutinePanicCount()
			state = tc.launch(o, state)
			require.Contains(t, state.DiscardingIdentifiers, "ENG-1")
			require.Equal(t, "discarding", IneligibleReason(issue, state, cfg))

			var ev OrchestratorEvent
			select {
			case ev = <-o.events:
			case <-time.After(5 * time.Second):
				t.Fatal("no event: the panicking discard goroutine left ENG-1 stuck in DiscardingIdentifiers")
			}
			o.discardWg.Wait()
			require.Equal(t, EventDiscardComplete, ev.Type)
			assert.Equal(t, "ENG-1", ev.Identifier)
			assert.Equal(t, before+1, GoroutinePanicCount())

			// The next event-loop pass releases the identifier.
			state = o.handleEvent(ctx, state, ev)
			assert.NotContains(t, state.DiscardingIdentifiers, "ENG-1")
			assert.NotEqual(t, "discarding", IneligibleReason(issue, state, cfg))
		})
	}
}

func TestGoSafeOnPanicIsBoundedUnderFullQueue(t *testing.T) {
	logs := captureSlog(t)
	prevBound := onPanicSendBound
	onPanicSendBound = 200 * time.Millisecond
	t.Cleanup(func() { onPanicSendBound = prevBound })

	rows := []struct {
		name       string
		cancelRun  bool
		wantReason string
	}{
		{"cancelled shutdown context", true, "shutting down"},
		{"live context, queue stays full", false, "event queue full"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			o := &Orchestrator{events: make(chan OrchestratorEvent, 1)}
			o.events <- OrchestratorEvent{Type: EventDiscardComplete, Identifier: "FILLER"} // queue full
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if row.cancelRun {
				cancel()
			}
			o.runCtx.Store(&ctx)

			var wg sync.WaitGroup
			start := time.Now()
			goSafe(&wg, "discard-test", "ENG-42", func() {
				panic("boom")
			}, o.discardCompleteOnPanic("ENG-42", 1))

			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("goSafe onPanic blocked past the bounded send window")
			}
			assert.Less(t, time.Since(start), 3*time.Second)
			require.Len(t, o.events, 1, "the full queue must not have accepted the event")
			out := logs.String()
			assert.Contains(t, out, "panic reconcile event dropped: "+row.wantReason)
			assert.Contains(t, out, "identifier=ENG-42")
		})
	}
}
