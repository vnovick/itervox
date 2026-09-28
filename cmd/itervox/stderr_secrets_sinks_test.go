package main

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

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/logbuffer"
	"github.com/vnovick/itervox/internal/logging"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/outbox"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
)

// sinkSecrets are the CORE-167 sentinels plus every shape the M2-close
// verifier's end-to-end probe found reaching the snapshot and the INFO log.
// Built by concatenation so this file does not itself trip secret scanners.
var sinkSecrets = []string{
	"sk-proj-" + "Q9x2LmN4pR7tV1wY3zA6bC8dE0fG2hJ5kL7mN9pQ",
	"ghp_" + "aB3dE5fG7hJ9kL1mN3pQ5rS7tU9vW1xY3zA5",
	"u7Hq2Zx9Lm4Kp8Rt1Vw6Yb3Nc5Df0Gj2Ah7Ks9Q",
	"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
	"dXNlcjpzM2NyZXRQYXNz", // Basic user:s3cretPass
	"9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
	"1f2e3d4c5b6a79881f2e3d4c5b6a7988",
	"S3cr3tP@ssw0rd!",
	"Hunter2PassWord9",
	"AbCdEfGhIjKlMnOpQrStUvWxYzAbCdEfGhIjKl",
}

func sinkStderr() string {
	return strings.Join([]string{
		"Error: 401 Unauthorized",
		"export OPENAI_API_KEY=" + sinkSecrets[0],
		"remote: " + sinkSecrets[1],
		"cookie=" + sinkSecrets[2],
		"AWS_SECRET_ACCESS_KEY=" + sinkSecrets[3],
		"> Authorization: Basic " + sinkSecrets[4],
		"api_key=" + sinkSecrets[5],
		"> x-api-key: " + sinkSecrets[6],
		"password=" + sinkSecrets[7],
		"clone https://:" + sinkSecrets[8] + "@git.example.com/r.git failed",
		"tok " + sinkSecrets[9] + " end",
	}, "\n")
}

type leakySinkRunner struct{}

func (leakySinkRunner) RunTurn(context.Context, agent.Logger, func(agent.TurnResult), *string, string, string, string, string, string, int, int, agent.PermissionMode) (agent.TurnResult, error) {
	return agent.TurnResult{
		Failed: true, FailureText: "agent failed | stderr: " + sinkStderr(),
		InputTokens: 10, OutputTokens: 5, TotalTokens: 15,
	}, nil
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// TestAgentStderrSecretsNeverReachAnySink (CORE-167, M2-close): a runner
// whose FailureText carries every secret shape in its stderr part must leak
// none of them into any sink — the daemon log at INFO through the
// production RedactingHandler, the snapshot retry row, HEARTBEAT's "Last
// error", the per-issue dashboard log, RecentFailures, the run history, and
// the max-retries tracker comment as queued in the write-ahead outbox.
func TestAgentStderrSecretsNeverReachAnySink(t *testing.T) {
	cfg := metricsTestCfg()
	cfg.Agent.MaxRetries = 1
	cfg.Agent.MaxRetryBackoffMs = 10
	mt := tracker.NewMemoryTracker(
		[]domain.Issue{{ID: "id-s1", Identifier: "SEC-1", Title: "T", State: "Todo"}},
		cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	ob, err := outbox.New(t.TempDir() + "/outbox.json")
	require.NoError(t, err)

	logs := &syncBuf{}
	orch := orchestrator.New(cfg, mt, leakySinkRunner{}, nil)
	orch.Logger = slog.New(logging.NewRedactingHandler(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	logBuf := logbuffer.New()
	orch.SetLogBuffer(logBuf)
	orch.SetWriteSink(orchestrator.NewOutboxWriteSink(ob))
	orch.SetOutbox(ob)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); _ = orch.Run(ctx) }()
	defer func() { cancel(); <-done }()

	// Sink: the retry row and HEARTBEAT built from it, while it exists.
	var heartbeat, retryErr string
	require.Eventually(t, func() bool {
		rows := sortedRetryRows(orch.Snapshot().RetryAttempts)
		if len(rows) == 0 || rows[0].Error == "" {
			return false
		}
		retryErr = rows[0].Error
		heartbeat = renderHeartbeat(server.StateSnapshot{Retrying: rows}, heartbeatOptions{}, time.Now())
		return true
	}, 5*time.Second, 5*time.Millisecond, "no retry row")

	// Sink: the max-retries comment, queued in the outbox.
	var comment string
	require.Eventually(t, func() bool {
		for _, e := range ob.Snapshot() {
			if e.Kind == outbox.KindCreateComment && strings.Contains(e.Body, "maximum retries exhausted") {
				comment = e.Body
				return true
			}
		}
		return false
	}, 8*time.Second, 10*time.Millisecond, "the max-retries comment was never queued")

	snap := orch.Snapshot()
	var ring []string
	for _, f := range snap.RecentFailures {
		ring = append(ring, f.Message)
	}
	sinks := map[string]string{
		"daemon log (INFO)":   logs.String(),
		"snapshot retry row":  retryErr,
		"HEARTBEAT":           heartbeat,
		"per-issue log":       strings.Join(logBuf.Get("SEC-1"), "\n"),
		"RecentFailures":      strings.Join(ring, "\n"),
		"max-retries comment": comment,
	}
	require.Contains(t, sinks["daemon log (INFO)"], "worker: turn failed")
	require.Contains(t, heartbeat, "- Last error: ")
	for name, text := range sinks {
		for _, s := range sinkSecrets {
			assert.NotContains(t, text, s, "%s leaks %.12s…", name, s)
		}
	}
	assert.Contains(t, comment, "401 Unauthorized", "the diagnostic survives in the comment")
	assert.Contains(t, retryErr, "401 Unauthorized")
}
