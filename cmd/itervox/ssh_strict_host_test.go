package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
)

// TestSSHStrictHostDefaultResetsWhenKeyRemovedOnReload (CORE-140 residue):
// the per-generation apply must be symmetric. An operator who sets
// agent.ssh_strict_host_checking and later deletes the key must get the safe
// "accept-new" default back on the next reload — not keep the old mode for
// the rest of the process's life. Both generations go through config.Load so
// the "key absent" shape is exactly what the parser hands run().
func TestSSHStrictHostDefaultResetsWhenKeyRemovedOnReload(t *testing.T) {
	t.Cleanup(func() {
		agent.SetSSHStrictHostDefault(agent.DefaultSSHStrictHostMode)
		agent.SetSSHStrictHostOverrides(nil)
	})
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	load := func(sshLine string) *config.Config {
		t.Helper()
		content := "---\nitervox_schema_version: 2\ntracker:\n  kind: memory\nagent:\n  command: claude\n" +
			sshLine + "---\nbody\n"
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
		cfg, err := config.Load(path)
		require.NoError(t, err)
		return cfg
	}

	// Generation 1 (startup): the operator turns host-key checking off.
	applySSHStrictHostConfig(load("  ssh_strict_host_checking: \"no\"\n"))
	require.Equal(t, "no", agent.SSHStrictHostDefault())

	// Generation 2 (reload): the key is gone.
	applySSHStrictHostConfig(load(""))
	require.Equal(t, agent.DefaultSSHStrictHostMode, agent.SSHStrictHostDefault(),
		"removing agent.ssh_strict_host_checking on reload kept the previous mode instead of restoring accept-new")
}
