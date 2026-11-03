package config_test

import (
	"crypto/sha256"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/workflow"
)

// #245: Load reports every settings file it looked at, with the hash of the
// bytes it read, so the daemon can watch each one from exactly what it
// loaded — present or not.
func TestLoadReportsTheSettingsFilesItRead(t *testing.T) {
	project := "{\"agent\": {\"max_turns\": 4}}\n"
	wf := layeredProject(t, "", "", project)
	cfg, err := config.Load(wf)
	require.NoError(t, err)

	require.Len(t, cfg.SettingsFiles, 3, "user (named by the env), project and local")
	byLayer := map[string]config.SettingsFile{}
	for _, f := range cfg.SettingsFiles {
		byLayer[f.Layer] = f
	}
	assert.Equal(t, config.ProjectSettingsPath(wf), byLayer[config.LayerProject].Path)
	assert.True(t, byLayer[config.LayerProject].Exists)
	assert.Equal(t, [32]byte(sha256.Sum256([]byte(project))), byLayer[config.LayerProject].Hash)
	assert.False(t, byLayer[config.LayerLocal].Exists)
	assert.Equal(t, workflow.AbsentHash, byLayer[config.LayerLocal].Hash)
	assert.False(t, byLayer[config.LayerUser].Exists)

	t.Setenv(config.UserSettingsEnv, "off")
	cfg, err = config.Load(wf)
	require.NoError(t, err)
	assert.Len(t, cfg.SettingsFiles, 2, "a layer that is off is not watched")
}

// #245: a settings file whose section has the wrong shape names the line,
// as a syntax error already does, so the reload error points at the edit.
func TestSettingsSectionErrorNamesTheLine(t *testing.T) {
	wf := layeredProject(t, "", "", "{\n  \"agent\": {},\n  \"guardrails\": {}\n}\n")
	_, err := config.Load(wf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), config.ProjectSettingsPath(wf))
	assert.Contains(t, err.Error(), `line 3: "guardrails" cannot be set here`)

	require.NoError(t, os.WriteFile(config.ProjectSettingsPath(wf), []byte("{\n\n  \"agent\": 5\n}\n"), 0o644))
	_, err = config.Load(wf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `line 3: "agent" must be an object of settings`)
}
