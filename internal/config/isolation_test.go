package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

// TestIsolationConfigRoundtrip is #258's config-field acceptance: agent.isolation
// and the profile keys isolation, isolation_extra_write and
// isolation_extra_deny parse (~ expanded), and default to none / inherit /
// empty when absent.
func TestIsolationConfigRoundtrip(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	path := workflowWithContent(t, minimalV2WithProfileFiles(t,
		"agent:\n"+
			"  isolation: process\n"+
			"  profiles:\n"+
			"    boxed:\n"+
			"      command: claude\n"+
			"      isolation: process\n"+
			"      isolation_extra_write:\n"+
			"        - ~/.npm\n"+
			"        - /opt/cache\n"+
			"      isolation_extra_deny:\n"+
			"        - /srv/secrets\n"+
			"    open:\n"+
			"      command: claude\n"+
			"      isolation: none\n"+
			"    inherits:\n"+
			"      command: claude\n"))
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, config.IsolationProcess, cfg.Agent.Isolation)
	boxed := cfg.Agent.Profiles["boxed"]
	assert.Equal(t, config.IsolationProcess, boxed.Isolation)
	assert.Equal(t, []string{filepath.Join(home, ".npm"), "/opt/cache"}, boxed.IsolationExtraWrite)
	assert.Equal(t, []string{"/srv/secrets"}, boxed.IsolationExtraDeny)
	assert.Equal(t, config.IsolationNone, cfg.Agent.Profiles["open"].Isolation)
	assert.Empty(t, cfg.Agent.Profiles["inherits"].Isolation, "unset inherits agent.isolation")
	assert.Equal(t, config.IsolationProcess, config.ResolveIsolation(cfg.Agent.Profiles["inherits"].Isolation, cfg.Agent.Isolation))
	assert.Equal(t, config.IsolationNone, config.ResolveIsolation(cfg.Agent.Profiles["open"].Isolation, cfg.Agent.Isolation))

	// Absent: none, and profiles carry nothing.
	path = workflowWithContent(t, minimalV2WithProfileFiles(t,
		"agent:\n  profiles:\n    plain:\n      command: claude\n"))
	cfg, err = config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, config.IsolationNone, cfg.Agent.Isolation, "the default is no sandbox")
	plain := cfg.Agent.Profiles["plain"]
	assert.Empty(t, plain.Isolation)
	assert.Nil(t, plain.IsolationExtraWrite)
	assert.Nil(t, plain.IsolationExtraDeny)
	assert.Equal(t, config.IsolationNone, config.ResolveIsolation(plain.Isolation, cfg.Agent.Isolation))
}

// TestIsolationConfigRejectsBadValues: an unknown mode or a relative extra
// path fails the load (falling back to none would be less confined than
// asked).
func TestIsolationConfigRejectsBadValues(t *testing.T) {
	for name, yaml := range map[string]string{
		"agent_mode":    "agent:\n  isolation: container\n",
		"profile_mode":  "agent:\n  profiles:\n    p:\n      command: claude\n      isolation: strict\n",
		"relative_path": "agent:\n  profiles:\n    p:\n      command: claude\n      isolation_extra_write:\n        - cache\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := config.Load(workflowWithContent(t, minimalV2WithProfileFiles(t, yaml)))
			require.Error(t, err)
		})
	}
}

// TestIsolationRefusedOnSSHHost (#258): process isolation with
// agent.ssh_hosts set is a config load error naming the profiles, for a
// profile's own isolation and for agent.isolation; a disabled profile does
// not count.
func TestIsolationRefusedOnSSHHost(t *testing.T) {
	for name, tc := range map[string]struct {
		yaml, mentions string
	}{
		"profile": {"agent:\n  ssh_hosts: [build-1]\n  profiles:\n    boxed:\n      command: claude\n      isolation: process\n", "boxed"},
		"default": {"agent:\n  ssh_hosts: [build-1]\n  isolation: process\n", "(default)"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := config.Load(workflowWithContent(t, minimalV2WithProfileFiles(t, tc.yaml)))
			require.ErrorIs(t, err, config.ErrIsolationOnSSHHost)
			assert.Contains(t, err.Error(), tc.mentions)
			assert.Contains(t, err.Error(), "agent.ssh_hosts")
		})
	}
	_, err := config.Load(workflowWithContent(t, minimalV2WithProfileFiles(t,
		"agent:\n  ssh_hosts: [build-1]\n  profiles:\n    boxed:\n      command: claude\n      isolation: process\n      enabled: false\n")))
	require.NoError(t, err, "a disabled profile is never dispatched")
	_, err = config.Load(workflowWithContent(t, minimalV2WithProfileFiles(t,
		"agent:\n  ssh_hosts: [build-1]\n  profiles:\n    open:\n      command: claude\n")))
	require.NoError(t, err, "SSH hosts without isolation load as before")
}
