package main

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

// M4-close D4 — a project-filter save on a WORKFLOW.md whose tracker block
// has no project_slug line used to write nothing, return no error and still
// change the in-memory filter. The key is saved (#239: to the settings file,
// tracker.* being layered), so memory and config agree; a save that does not
// persist leaves memory unchanged.
func TestProjectFilterSaveWithoutProjectSlugLine(t *testing.T) {
	t.Run("saves the key", func(t *testing.T) {
		body := strings.Replace(reloadFenceFixture, "  project_slug: proj\n", "", 1)
		require.NotContains(t, body, "project_slug")
		path := writeFixture(t, body)
		_, gen, err := loadSettingsGeneration(path)
		require.NoError(t, err)
		fpm := &fakeProjectManager{}
		m := &linearProjectManager{pm: fpm, workflowPath: path, settingsGen: gen}

		require.NoError(t, m.SetProjectFilter([]string{"alpha"}))
		data, err := config.ReadSettingsFile(config.ProjectSettingsPath(path))
		require.NoError(t, err)
		assert.Equal(t, "alpha", data["tracker"].(map[string]any)["project_slug"], "the key is saved to the settings file")
		assert.Equal(t, body, readString(t, path), "WORKFLOW.md had no project_slug to remove")
		assert.Equal(t, []string{"alpha"}, fpm.GetProjectFilter(), "memory == config")
		cfg, _, err := loadSettingsGeneration(path)
		require.NoError(t, err, "the config still loads")
		assert.Equal(t, "alpha", cfg.Tracker.ProjectSlug)
	})

	t.Run("a failed save leaves memory unchanged", func(t *testing.T) {
		path := writeFixture(t, "---\nitervox_schema_version: 2\ntracker:\n  kind: linear\n  api_key: key\n  project_slug: old\nagent:\n  command: claude\n---\n\nPrompt.\n")
		fpm := &fakeProjectManager{}
		m := &linearProjectManager{pm: fpm, workflowPath: path, settingsGen: currentSettingsGeneration(path)}
		fail := func(string) error { return errors.New("injected WORKFLOW.md failure") }
		settingsWorkflowStep.Store(&fail)
		t.Cleanup(func() { settingsWorkflowStep.Store(nil) })
		err := m.SetProjectFilter([]string{"alpha"})
		require.Error(t, err)
		assert.False(t, fpm.set, "a save that did not persist must not change memory")
		assert.NotContains(t, readString(t, path), "alpha")
		_, statErr := os.Stat(config.ProjectSettingsPath(path))
		assert.ErrorIs(t, statErr, os.ErrNotExist, "the settings file is put back")
	})
}
