package orchestrator_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/logbuffer"
	"github.com/vnovick/itervox/internal/logging"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
)

var stderrSentinels = []string{
	"sk-proj-" + "Q9x2LmN4pR7tV1wY3zA6bC8dE0fG2hJ5kL7mN9pQ",
	"ghp_" + "aB3dE5fG7hJ9kL1mN3pQ5rS7tU9vW1xY3zA5",
	"u7Hq2Zx9Lm4Kp8Rt1Vw6Yb3Nc5Df0Gj2Ah7Ks9Q",
	"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
}

// leakyRunner fails with a FailureText that embeds raw secrets in its stderr
// part — what a runner that forgot to redact (or a future one) would return.
type leakyRunner struct{}

func (leakyRunner) RunTurn(context.Context, agent.Logger, func(agent.TurnResult), *string, string, string, string, string, string, int, int, agent.PermissionMode) (agent.TurnResult, error) {
	stderr := strings.Join([]string{
		"Error: 401 Unauthorized",
		"export OPENAI_API_KEY=" + stderrSentinels[0],
		"remote: " + stderrSentinels[1],
		"cookie=" + stderrSentinels[2],
		"AWS_SECRET_ACCESS_KEY=" + stderrSentinels[3],
	}, "\n")
	return agent.TurnResult{
		Failed: true, FailureText: "agent failed | stderr: " + stderr,
		InputTokens: 10, OutputTokens: 5, TotalTokens: 15,
	}, nil
}

// TestAgentStderrSecretsNeverReachLogsOrSnapshot (CORE-167): a sentinel
// secret of each shape in agent stderr must not appear in the daemon log at
// the default level (INFO, through the production RedactingHandler), in the
// retry row's error on the snapshot, or in the per-issue dashboard log.
func TestAgentStderrSecretsNeverReachLogsOrSnapshot(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxRetries = 5
	cfg.Agent.MaxRetryBackoffMs = 60_000 // keep the first retry pending

	mt := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	buf := &lockedBuf{}
	orch := orchestrator.New(cfg, mt, leakyRunner{}, nil)
	orch.Logger = slog.New(logging.NewRedactingHandler(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	logBuf := logbuffer.New()
	orch.SetLogBuffer(logBuf)

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
	}, 4*time.Second, 10*time.Millisecond, "the failure never reached the retry queue")

	logged := buf.String()
	require.Contains(t, logged, "worker: turn failed")
	dash := strings.Join(logBuf.Get("ENG-1"), "\n")
	for _, s := range stderrSentinels {
		assert.NotContains(t, logged, s, "daemon log")
		assert.NotContains(t, retryErr, s, "snapshot retry error (FailureText)")
		assert.NotContains(t, dash, s, "dashboard per-issue log")
	}
	assert.Contains(t, retryErr, "401 Unauthorized", "the diagnostic survives")
}
