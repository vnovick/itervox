package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

// TestWorkspaceCopyFilesConfigRoundtrip (#250): workspace.copy_files and
// copy_files_max_bytes parse from the front matter and from the project
// settings file (written by the settings writer), default to empty / 10 MB,
// and an entry outside the repository root fails the load.
func TestWorkspaceCopyFilesConfigRoundtrip(t *testing.T) {
	t.Setenv(config.UserSettingsEnv, "off")

	cfg, err := config.Load(workflowWithContent(t, minimal("workspace:\n  copy_files: [\".env\", \"./config/local/*.json\"]\n  copy_files_max_bytes: 2048\n")))
	require.NoError(t, err)
	assert.Equal(t, []string{".env", "config/local/*.json"}, cfg.Workspace.CopyFiles, "cleaned")
	assert.Equal(t, 2048, cfg.Workspace.CopyFilesMaxBytes)

	path := workflowWithContent(t, minimal(""))
	cfg, err = config.Load(path)
	require.NoError(t, err)
	assert.Empty(t, cfg.Workspace.CopyFiles, "empty by default: nothing changes until it is set")
	assert.Equal(t, config.DefaultCopyFilesMaxBytes, cfg.Workspace.CopyFilesMaxBytes)
	assert.Equal(t, 10*1024*1024, config.DefaultCopyFilesMaxBytes)

	require.NoError(t, config.SetSettings(config.ProjectSettingsPath(path),
		config.SettingAssignment{Key: "workspace.copy_files", Value: []any{".env", ".env.local"}},
		config.SettingAssignment{Key: "workspace.copy_files_max_bytes", Value: 4096}))
	cfg, err = config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, []string{".env", ".env.local"}, cfg.Workspace.CopyFiles)
	assert.Equal(t, 4096, cfg.Workspace.CopyFilesMaxBytes)
	assert.NoError(t, config.ValidateSettingValue("workspace.copy_files", []any{".env"}))

	for _, bad := range []string{"../x.env", "/abs/.env", ""} {
		_, err := config.Load(workflowWithContent(t, minimal("workspace:\n  copy_files: [\""+bad+"\"]\n")))
		assert.Error(t, err, "%q", bad)
	}
}

// TestWorkspacePortRangeConfigRoundtrip (#250): workspace.port_range parses
// from the front matter and the project settings file, defaults to
// 41000-41999, and a malformed or out-of-bounds range fails the load.
func TestWorkspacePortRangeConfigRoundtrip(t *testing.T) {
	t.Setenv(config.UserSettingsEnv, "off")

	cfg, err := config.Load(workflowWithContent(t, minimal("workspace:\n  port_range: \"5000-5009\"\n")))
	require.NoError(t, err)
	assert.Equal(t, "5000-5009", cfg.Workspace.PortRange)
	assert.Equal(t, 5000, cfg.Workspace.PortMin)
	assert.Equal(t, 5009, cfg.Workspace.PortMax)

	path := workflowWithContent(t, minimal(""))
	cfg, err = config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, "41000-41999", cfg.Workspace.PortRange)
	assert.Equal(t, 41000, cfg.Workspace.PortMin)
	assert.Equal(t, 41999, cfg.Workspace.PortMax)

	require.NoError(t, config.SetSetting(config.ProjectSettingsPath(path), "workspace.port_range", "42000-42000"))
	cfg, err = config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, 42000, cfg.Workspace.PortMin)
	assert.Equal(t, 42000, cfg.Workspace.PortMax)

	for _, bad := range []string{"41000", "x-y", "50-60", "42000-41000", "60000-70000"} {
		_, err := config.Load(workflowWithContent(t, minimal("workspace:\n  port_range: \""+bad+"\"\n")))
		assert.Error(t, err, "%q", bad)
	}
}
