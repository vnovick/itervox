package depsanalysis

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
)

// envRunner records the run environment and timeouts the analyzer turn got.
type envRunner struct {
	set        []string
	read, turn int
}

func (r *envRunner) RunTurn(ctx context.Context, _ agent.Logger, _ func(agent.TurnResult), _ *string,
	_, _, _, _, _ string, read, turn int, _ agent.PermissionMode,
) (agent.TurnResult, error) {
	r.set, r.read, r.turn = agent.RunEnvFrom(ctx).Set, read, turn
	return agent.TurnResult{ResultText: `{"edges":[]}`}, nil
}

// TestRunAgentPassUsesTheProfileModelServer: an analyzer profile with a
// model_server gets its variable and the longer timeouts; one without gets
// neither.
func TestRunAgentPassUsesTheProfileModelServer(t *testing.T) {
	local := config.AgentProfile{Command: "claude", ModelServer: "http://127.0.0.1:8000"}
	require.NoError(t, config.ApplyProfileModelServer("deps-analyzer-local", &local, "claude"))
	runner := &envRunner{}
	_, err := RunAgentPass(context.Background(), AgentPassInput{
		Runner: runner, Profile: local, ProfileName: "deps-analyzer-local",
		ReadTimeoutMs: 30_000, TurnTimeoutMs: 3_600_000,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"ANTHROPIC_BASE_URL=http://127.0.0.1:8000"}, runner.set)
	assert.Equal(t, config.ModelServerReadTimeoutMs, runner.read)
	assert.Equal(t, config.ModelServerTurnTimeoutMs, runner.turn)

	runner = &envRunner{}
	_, err = RunAgentPass(context.Background(), AgentPassInput{
		Runner: runner, Profile: config.AgentProfile{Command: "claude"}, ProfileName: "deps-analyzer",
		ReadTimeoutMs: 30_000, TurnTimeoutMs: 3_600_000,
	})
	require.NoError(t, err)
	assert.Empty(t, runner.set)
	assert.Equal(t, 30_000, runner.read)
	assert.Equal(t, 3_600_000, runner.turn)
}
