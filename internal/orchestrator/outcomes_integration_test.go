package orchestrator_test

import (
	"context"
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

// cacheUsageRunner replays a Claude-shaped turn through agent.ApplyEvent:
// two assistant messages and a result event whose usage carries the prompt
// cache counts. Later turns report nothing (the session concluded).
type cacheUsageRunner struct{ calls atomic.Int32 }

func (r *cacheUsageRunner) RunTurn(_ context.Context, _ agent.Logger, _ func(agent.TurnResult), _ *string, _, _, _, _, _ string, _, _ int, _ agent.PermissionMode) (agent.TurnResult, error) {
	if r.calls.Add(1) > 1 {
		return agent.TurnResult{SessionID: "sess-cache"}, nil
	}
	var res agent.TurnResult
	for _, line := range []string{
		`{"type":"system","subtype":"init","session_id":"sess-cache"}`,
		`{"type":"assistant","session_id":"sess-cache","message":{"content":[{"type":"text","text":"working"}],"usage":{"input_tokens":30,"output_tokens":10,"cache_read_input_tokens":900}}}`,
		`{"type":"assistant","session_id":"sess-cache","message":{"content":[{"type":"text","text":"done"}],"usage":{"input_tokens":20,"output_tokens":15,"cache_read_input_tokens":950}}}`,
		`{"type":"result","subtype":"success","is_error":false,"session_id":"sess-cache","result":"done","usage":{"input_tokens":50,"output_tokens":25,"cache_read_input_tokens":1850,"cache_creation_input_tokens":400}}`,
	} {
		ev, err := agent.ParseLine([]byte(line))
		if err != nil {
			return res, err
		}
		res = agent.ApplyEvent(res, ev)
	}
	return res, nil
}

// TestRunCacheTokensReachCompletedRunThroughEventLoop: a real dispatch
// through Run() — worker, recordRunUsage, the exit's settleRunUsage and
// the event loop's recordHistory — writes the backend's prompt-cache tokens
// (from the turn's result event only), the turn count, the wall time and the
// attempt onto the CompletedRun, while the token totals stay input+output.
func TestRunCacheTokensReachCompletedRunThroughEventLoop(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 3
	cfg.Tracker.CompletionState = "Done"
	mt := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates,
	)
	orch := orchestrator.New(cfg, mt, &cacheUsageRunner{}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	go orch.Run(ctx) //nolint:errcheck

	var hist []orchestrator.CompletedRun
	require.Eventually(t, func() bool {
		hist = orch.RunHistory()
		return len(hist) >= 1
	}, 6*time.Second, 20*time.Millisecond, "the run reached history")
	run := hist[0]
	assert.Equal(t, "succeeded", run.Status)
	assert.Equal(t, 1850, run.CacheReadTokens)
	assert.Equal(t, 400, run.CacheWriteTokens)
	assert.Equal(t, 50, run.InputTokens)
	assert.Equal(t, 25, run.OutputTokens)
	assert.Equal(t, 75, run.TotalTokens, "cache counts stay out of the token total")
	assert.GreaterOrEqual(t, run.TurnCount, 1)
	assert.GreaterOrEqual(t, run.ElapsedMs, int64(0))
	assert.Zero(t, run.Attempt, "a first run is attempt 0")
}
