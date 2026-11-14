package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

// TestPushConfigRoundtrip: agent.push and agent.profiles.<name>.push parse,
// a profile without push inherits agent.push, and absent everywhere the mode
// is "agent" (behaviour as before).
func TestPushConfigRoundtrip(t *testing.T) {
	path := workflowWithContent(t, minimalV2WithProfileFiles(t,
		"agent:\n"+
			"  push: daemon\n"+
			"  profiles:\n"+
			"    patches:\n"+
			"      command: claude\n"+
			"      push: daemon\n"+
			"    pusher:\n"+
			"      command: claude\n"+
			"      push: agent\n"+
			"    inherits:\n"+
			"      command: claude\n"))
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, config.PushDaemon, cfg.Agent.Push)
	assert.Equal(t, config.PushDaemon, cfg.Agent.Profiles["patches"].Push)
	assert.Equal(t, config.PushAgent, cfg.Agent.Profiles["pusher"].Push)
	assert.Empty(t, cfg.Agent.Profiles["inherits"].Push, "unset inherits agent.push")
	assert.Equal(t, config.PushDaemon, config.ResolvePush(cfg.Agent.Profiles["inherits"].Push, cfg.Agent.Push))
	assert.Equal(t, config.PushAgent, config.ResolvePush(cfg.Agent.Profiles["pusher"].Push, cfg.Agent.Push))
	assert.Equal(t, []string{"(default)", "inherits", "patches"}, config.PatchOutProfiles(cfg.Agent))

	// Absent: agent, and profiles carry nothing.
	cfg, err = config.Load(workflowWithContent(t, minimalV2WithProfileFiles(t,
		"agent:\n  profiles:\n    plain:\n      command: claude\n")))
	require.NoError(t, err)
	assert.Equal(t, config.PushAgent, cfg.Agent.Push, "the default is the agent pushes, as before")
	assert.Empty(t, cfg.Agent.Profiles["plain"].Push)
	assert.Equal(t, config.PushAgent, config.ResolvePush(cfg.Agent.Profiles["plain"].Push, cfg.Agent.Push))
	assert.Empty(t, config.PatchOutProfiles(cfg.Agent))
}

// TestPushConfigRejectsBadValues: an unknown mode is a load error (falling
// back to "agent" would hand back the push ability), and push: daemon needs
// guardrails.enabled and refuses SSH workers.
func TestPushConfigRejectsBadValues(t *testing.T) {
	for name, yaml := range map[string]string{
		"agent_mode":   "agent:\n  push: referee\n",
		"profile_mode": "agent:\n  profiles:\n    p:\n      command: claude\n      push: nobody\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := config.Load(workflowWithContent(t, minimalV2WithProfileFiles(t, yaml)))
			require.Error(t, err)
		})
	}
	_, err := config.Load(workflowWithContent(t, minimalV2WithProfileFiles(t,
		"guardrails:\n  enabled: false\nagent:\n  profiles:\n    p:\n      command: claude\n      push: daemon\n")))
	require.ErrorIs(t, err, config.ErrPushDaemonNeedsGuardrails)
	assert.Contains(t, err.Error(), "p set push: daemon")
	_, err = config.Load(workflowWithContent(t, minimalV2WithProfileFiles(t,
		"agent:\n  ssh_hosts: [build-1]\n  push: daemon\n")))
	require.ErrorIs(t, err, config.ErrPushDaemonOnSSHHost)
	_, err = config.Load(workflowWithContent(t, minimalV2WithProfileFiles(t,
		"guardrails:\n  enabled: false\nagent:\n  profiles:\n    p:\n      command: claude\n      push: daemon\n      enabled: false\n")))
	require.NoError(t, err, "a disabled profile is never dispatched")
}
