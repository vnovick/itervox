package orchestrator

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
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

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// TestTickLogCountsHeldIssuesSeparately (CORE-173 b): an issue held by the
// backend breaker is not "dispatched" in the tick summary, and the Info-level
// "dispatch held" line is logged when the hold starts or changes, not again
// on every tick while it stays the same.
func TestTickLogCountsHeldIssuesSeparately(t *testing.T) {
	issue := domain.Issue{ID: "id-h", Identifier: "ENG-H", Title: "Held", State: "In Progress"}
	o, _ := breakerOrchestrator(t, config.BackendFallbackConfig{}, issue)
	var buf lockedBuf
	o.Logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	prev := slog.Default()
	slog.SetDefault(o.Logger)
	t.Cleanup(func() { slog.SetDefault(prev) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state := NewState(o.cfg)
	state.BackendHealth[BackendHealthKey("claude", "")] = BackendHealthEntry{
		Backend: "claude", Status: BackendStatusLimited, LimitedUntil: time.Now().Add(time.Hour), ResetKnown: true,
	}
	for i := 0; i < 3; i++ {
		state = o.onTick(ctx, state)
	}
	require.NotContains(t, state.Running, issue.ID, "precondition: the issue is held")
	require.Contains(t, state.BackendLimitedHolds, issue.Identifier)

	logs := buf.String()
	assert.Equal(t, 1, strings.Count(logs, `msg="orchestrator: dispatch held, backend limited"`),
		"the unchanged hold is logged at Info once, not every tick:\n%s", logs)
	assert.NotContains(t, logs, "dispatched=1", "a held issue is not counted as dispatched")
	assert.Contains(t, logs, "dispatched=0")
	assert.Contains(t, logs, "held=1")
}
