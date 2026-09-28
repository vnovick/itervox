package orchestrator_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
)

type countingRunner struct{ calls atomic.Int64 }

func (r *countingRunner) RunTurn(context.Context, agent.Logger, func(agent.TurnResult), *string, string, string, string, string, string, int, int, agent.PermissionMode) (agent.TurnResult, error) {
	r.calls.Add(1)
	return agent.TurnResult{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}, nil
}

// M2-close: a rendered prompt over the Claude Code piped-input cap fails the
// run before the CLI starts, with an error naming the cap — never a silent
// truncation, never a CLI error buried in stderr.
func TestPromptOverBackendCapFailsBeforeDispatch(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxRetries = 1
	cfg.Agent.MaxRetryBackoffMs = 60_000
	cfg.PromptTemplate = "{{ issue.description }}"
	huge := strings.Repeat("x", agent.ClaudePromptMaxUTF16Units+1)
	issue := makeIssue("id1", "ENG-1", "In Progress", nil, nil)
	issue.Description = &huge
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	runner := &countingRunner{}
	orch := orchestrator.New(cfg, mt, runner, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); _ = orch.Run(ctx) }()
	defer func() { cancel(); <-done }()

	var retryErr string
	require.Eventually(t, func() bool {
		for _, r := range orch.Snapshot().RetryAttempts {
			if r.Error != nil {
				retryErr = *r.Error
				return true
			}
		}
		return false
	}, 4*time.Second, 10*time.Millisecond)
	assert.Zero(t, runner.calls.Load(), "the CLI must never start with an oversized prompt")
	assert.Contains(t, retryErr, "10485760")
	assert.Contains(t, retryErr, "Claude Code")
}
