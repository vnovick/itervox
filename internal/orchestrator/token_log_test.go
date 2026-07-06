package orchestrator_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
)

// syncBuffer is a goroutine-safe bytes.Buffer for capturing slog output.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func (s *syncBuffer) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf.Reset()
}

// runWorkerToCompletion sets up an orchestrator with a fake runner, waits for
// the runner to complete, then returns captured slog output.
func runWorkerToCompletion(t *testing.T, identifier string, events []agent.StreamEvent) string {
	t.Helper()

	logBuf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	cfg := baseConfig()
	cfg.Polling.IntervalMs = 50

	mt := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id1", identifier, "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)

	done := make(chan struct{})
	wrapped := &trackingRunner{
		Runner: agenttest.NewFakeRunner(events),
		done:   done,
	}

	orch := orchestrator.New(cfg, mt, wrapped, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	orchDone := make(chan struct{})
	go func() {
		_ = orch.Run(ctx)
		close(orchDone)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("expected worker to complete within 3s")
	}

	// Allow the event loop to process the exit event and log.
	time.Sleep(200 * time.Millisecond)
	cancel()
	// Wait for orch.Run to fully exit BEFORE returning. Otherwise the
	// orchestrator goroutine outlives this test and the next call to
	// runWorkerToCompletion replaces slog.Default — the still-draining
	// goroutine then writes "worker: completed" with this test's totals
	// into the next test's logBuf, breaking the next test's assertions.
	<-orchDone

	return logBuf.String()
}

func TestWorkerCompletedLogHasTokenFields(t *testing.T) {
	logs := runWorkerToCompletion(t, "ENG-1", []agent.StreamEvent{
		{Type: "system", SessionID: "s1"},
		{Type: "result", SessionID: "s1", Usage: agent.UsageSnapshot{
			InputTokens:  2500,
			OutputTokens: 1200,
		}},
	})

	assert.Contains(t, logs, "worker: completed", "should log completion summary")
	assert.Contains(t, logs, "input_tokens=", "completion log should include input_tokens")
	assert.Contains(t, logs, "output_tokens=", "completion log should include output_tokens")
	assert.Contains(t, logs, "status=succeeded", "completion log should include status")
	assert.Contains(t, logs, "turns=", "completion log should include turns")
	assert.Contains(t, logs, "elapsed=", "completion log should include elapsed time")
	assert.Contains(t, logs, "ENG-1", "logs should reference the issue identifier")
}

func TestWorkerCompletedLogHasCorrectTokenValues(t *testing.T) {
	// The FakeRunner replays the same result events every turn and baseConfig
	// sets maxTurns=5, so tokens accumulate: 1000*5=5000 input, 500*5=2500 output.
	logs := runWorkerToCompletion(t, "TOK-1", []agent.StreamEvent{
		{Type: "system", SessionID: "s1"},
		{Type: "result", SessionID: "s1", Usage: agent.UsageSnapshot{
			InputTokens:  1000,
			OutputTokens: 500,
		}},
	})

	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, "worker: completed") {
			assert.Contains(t, line, "input_tokens=5000", "should have accumulated input tokens (1000×5 turns)")
			assert.Contains(t, line, "output_tokens=2500", "should have accumulated output tokens (500×5 turns)")
			return
		}
	}
	t.Fatal("did not find 'worker: completed' log line")
}

func TestOrchestratorSuccessLogIncludesTokens(t *testing.T) {
	logs := runWorkerToCompletion(t, "TKN-1", []agent.StreamEvent{
		{Type: "system", SessionID: "s1"},
		{Type: "result", SessionID: "s1", Usage: agent.UsageSnapshot{
			InputTokens:  7500,
			OutputTokens: 4200,
		}},
	})

	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, "orchestrator: worker succeeded") {
			assert.Contains(t, line, "input_tokens=", "should include input_tokens")
			assert.Contains(t, line, "output_tokens=", "should include output_tokens")
			assert.Contains(t, line, "turns=", "should include turns")
			return
		}
	}
	t.Fatal("did not find 'orchestrator: worker succeeded' log line")
}
