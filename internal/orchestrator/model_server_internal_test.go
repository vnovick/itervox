package orchestrator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
)

// TestModelServerLocalAndRemote: a local run gets the assignment in its run
// environment's Set list and an unchanged command; an SSH run gets it in
// front of the turn's command (after a backend hint) and no local entry.
func TestModelServerLocalAndRemote(t *testing.T) {
	const kv = "ANTHROPIC_BASE_URL=http://127.0.0.1:8000"
	ctx := withLocalModelServer(context.Background(), kv, "")
	assert.Equal(t, []string{kv}, agent.RunEnvFrom(ctx).Set)
	assert.Empty(t, agent.RunEnvFrom(ctx).Vars)
	assert.Empty(t, agent.RunEnvFrom(withLocalModelServer(context.Background(), kv, "worker-1")).Set)
	assert.Empty(t, agent.RunEnvFrom(withLocalModelServer(context.Background(), "", "")).Set)

	assert.Equal(t, "claude", withRemoteModelServer("claude", kv, ""))
	assert.Equal(t, "claude", withRemoteModelServer("claude", "", "worker-1"))
	remote := withRemoteModelServer("claude", kv, "worker-1")
	assert.Contains(t, remote, "ANTHROPIC_BASE_URL=")
	assert.Contains(t, remote, "http://127.0.0.1:8000")
	assert.Regexp(t, `^ANTHROPIC_BASE_URL=\S+ claude$`, remote)
	hinted := withRemoteModelServer("@@itervox-backend=claude ./wrap.sh", kv, "worker-1")
	assert.Regexp(t, `^@@itervox-backend=claude ANTHROPIC_BASE_URL=\S+ \./wrap\.sh$`, hinted)
}

// TestProfileStallTimeoutsReadsProfiles: the event loop's per-profile stall
// map holds only the profiles whose stall timeout differs from the agent
// level, and "" for agent.command when it points at a model server.
func TestProfileStallTimeoutsReadsProfiles(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.Command = "claude"
	cfg.Agent.StallTimeoutMs = 300_000
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"local": {Command: "claude", ModelServer: "http://127.0.0.1:8000", ModelServerURL: "http://127.0.0.1:8000"},
		"own":   {Command: "claude", StallTimeoutMs: 42_000},
		"plain": {Command: "claude"},
	}
	o := &Orchestrator{cfg: cfg}
	assert.Equal(t, map[string]int{"local": config.ModelServerStallTimeoutMs, "own": 42_000}, o.profileStallTimeouts())

	cfg.Agent.Command = "ANTHROPIC_BASE_URL=http://127.0.0.1:8000 claude"
	assert.Equal(t, config.ModelServerStallTimeoutMs, o.profileStallTimeouts()[""])
}
