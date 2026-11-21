package orchestrator

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	assert.Equal(t, "@@itervox-backend=claude export ANTHROPIC_BASE_URL='http://127.0.0.1:8000' || exit 1; claude", remote)
	assert.Equal(t, "claude", agent.BackendFromCommand(remote), "the runner still picks the command's backend")
	hinted := withRemoteModelServer("@@itervox-backend=claude ./wrap.sh", kv, "worker-1")
	assert.Equal(t, "@@itervox-backend=claude export ANTHROPIC_BASE_URL='http://127.0.0.1:8000' || exit 1; ./wrap.sh", hinted)
	codex := withRemoteModelServer("codex", "OPENAI_BASE_URL=http://127.0.0.1:8000/v1", "worker-1")
	assert.Equal(t, "codex", agent.BackendFromCommand(codex), "a codex command is not run by the default (claude) runner")
	assert.Equal(t, "export ANTHROPIC_BASE_URL='http://127.0.0.1:8000' || exit 1; ./wrap.sh", withRemoteModelServer("./wrap.sh", kv, "worker-1"))
}

// TestRemoteModelServerReachesCompoundCommands runs the SSH turn's command
// line (backend hint removed, as the runner does) in bash, the remote
// shell, with a fake claude that prints its ANTHROPIC_BASE_URL: a compound
// profile command gets the model server for the CLI, not only for its
// first command (`ANTHROPIC_BASE_URL=… source …nvm.sh && claude` left the
// CLI on the hosted API).
func TestRemoteModelServerReachesCompoundCommands(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not installed")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "url.out")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "claude"),
		[]byte("#!/bin/sh\nprintf '%s' \"$ANTHROPIC_BASE_URL\" > "+out+"\n"), 0o755))
	setup := filepath.Join(dir, "nvm.sh")
	require.NoError(t, os.WriteFile(setup, []byte("PATH="+dir+":$PATH\n"), 0o644))
	const url = "http://127.0.0.1:8000"
	for _, command := range []string{
		"source " + setup + " && claude",
		"cd " + dir + " && ./claude",
		"exec " + filepath.Join(dir, "claude"),
	} {
		t.Run(command, func(t *testing.T) {
			_ = os.Remove(out)
			line := withRemoteModelServer(command, "ANTHROPIC_BASE_URL="+url, "worker-1")
			_, line = config.ParseBackendHint(line)
			cmd := exec.Command(bash, "-c", line)
			cmd.Env = []string{"PATH=/usr/bin:/bin", "ANTHROPIC_BASE_URL="}
			outBytes, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", outBytes)
			got, err := os.ReadFile(out)
			require.NoError(t, err)
			assert.Equal(t, url, string(got))
		})
	}
}

// TestRunStallTimeoutsReadsProfiles: the event loop's per-run stall map
// holds only the runs whose stall timeout differs from the agent level: a
// model_server profile on its own backend, and a profile's own value. A
// model-server profile switched to another backend, a command-form profile
// (`ANTHROPIC_BASE_URL=… claude`) and a run without a profile keep the
// agent level.
func TestRunStallTimeoutsReadsProfiles(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.Command = "ANTHROPIC_BASE_URL=http://127.0.0.1:8000 claude"
	cfg.Agent.StallTimeoutMs = 300_000
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"local":   {Command: "claude", ModelServer: "http://127.0.0.1:8000", ModelServerURL: "http://127.0.0.1:8000"},
		"own":     {Command: "claude", StallTimeoutMs: 42_000},
		"plain":   {Command: "claude"},
		"command": {Command: "ANTHROPIC_BASE_URL=http://127.0.0.1:8000 claude"},
	}
	o := &Orchestrator{cfg: cfg}
	state := State{Running: map[string]*RunEntry{
		"a": {ProfileName: "local", Backend: "claude"},
		"b": {ProfileName: "own", Backend: "claude"},
		"c": {ProfileName: "plain", Backend: "claude"},
		"d": {ProfileName: "local", Backend: "codex"},
		"e": {ProfileName: "command", Backend: "claude"},
		"f": {ProfileName: "", Backend: "claude"},
		"g": {ProfileName: "local"},
	}}
	assert.Equal(t, map[string]int{"a": config.ModelServerStallTimeoutMs, "b": 42_000, "g": config.ModelServerStallTimeoutMs},
		o.runStallTimeouts(state))
	assert.Nil(t, o.runStallTimeouts(State{}))
}
