package orchestrator_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
)

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) levelsOf(msg string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, line := range strings.Split(l.b.String(), "\n") {
		var rec struct {
			Level string `json:"level"`
			Msg   string `json:"msg"`
		}
		if json.Unmarshal([]byte(line), &rec) == nil && rec.Msg == msg {
			out = append(out, rec.Level)
		}
	}
	return out
}

// TestWorkerRetryLogLevels (CORE-044): a worker failure that schedules a
// retry is a real failure and logs at Warn (it logged at Info, below most
// operators' filters); exhausting retries strands the issue and logs at
// Error so the level=ERROR alert fires.
func TestWorkerRetryLogLevels(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxRetries = 1
	cfg.Agent.MaxRetryBackoffMs = 10

	mt := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)
	buf := &lockedBuf{}
	orch := orchestrator.New(cfg, mt, &alwaysFailRunner{}, nil)
	orch.Logger = slog.New(slog.NewJSONHandler(buf, nil))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); _ = orch.Run(ctx) }()
	defer func() { cancel(); <-done }()

	require.Eventually(t, func() bool {
		return len(buf.levelsOf("worker: max retries exhausted")) > 0
	}, 4*time.Second, 20*time.Millisecond, "retries were never exhausted")

	assert.Equal(t, []string{"WARN"}, buf.levelsOf("orchestrator: worker failed, retry scheduled"))
	assert.Equal(t, []string{"ERROR"}, buf.levelsOf("worker: max retries exhausted"))
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
