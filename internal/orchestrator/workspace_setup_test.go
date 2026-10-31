package orchestrator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/vnovick/itervox/internal/agent"
)

// TestWorkspacePortForSSHWorkers (#250): an SSH worker gets ITERVOX_PORT as
// an assignment in front of the turn's command (the local environment does
// not reach the remote shell), after the backend hint; a local run gets it
// through the run environment and its command is unchanged; no port, no
// change.
func TestWorkspacePortForSSHWorkers(t *testing.T) {
	assert.Equal(t, "ITERVOX_PORT='41002' claude --verbose", withRemoteWorkspacePort("claude --verbose", 41002, "worker-1"))
	assert.Equal(t, "@@itervox-backend=codex ITERVOX_PORT='41002' run-codex",
		withRemoteWorkspacePort("@@itervox-backend=codex run-codex", 41002, "worker-1"))
	assert.Equal(t, "claude", withRemoteWorkspacePort("claude", 41002, ""), "local: the command is unchanged")
	assert.Equal(t, "claude", withRemoteWorkspacePort("claude", 0, "worker-1"))

	ctx := agent.WithRunEnv(context.Background(), agent.RunEnv{Vars: []string{"A=1"}, PathPrepend: "/shims"})
	local := agent.RunEnvFrom(withLocalWorkspacePort(ctx, 41002, ""))
	assert.Equal(t, []string{"A=1", "ITERVOX_PORT=41002"}, local.Vars)
	assert.Equal(t, "/shims", local.PathPrepend, "the guardrail PATH is kept")
	assert.Equal(t, []string{"A=1"}, agent.RunEnvFrom(withLocalWorkspacePort(ctx, 41002, "worker-1")).Vars)
	assert.Equal(t, []string{"A=1"}, agent.RunEnvFrom(withLocalWorkspacePort(ctx, 0, "")).Vars)
}
