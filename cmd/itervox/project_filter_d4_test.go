package main

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// M4-close D4 — a project-filter save on a WORKFLOW.md whose tracker block
// has no project_slug line used to write nothing, return no error and still
// change the in-memory filter. Now the key is inserted under tracker:, so
// memory and file agree; a front matter with no tracker: block is refused
// without touching memory.
func TestProjectFilterSaveWithoutProjectSlugLine(t *testing.T) {
	t.Run("inserts the key under tracker", func(t *testing.T) {
		body := strings.Replace(reloadFenceFixture, "  project_slug: proj\n", "", 1)
		require.NotContains(t, body, "project_slug")
		path := writeFixture(t, body)
		_, gen, err := loadSettingsGeneration(path)
		require.NoError(t, err)
		fpm := &fakeProjectManager{}
		m := &linearProjectManager{pm: fpm, workflowPath: path, settingsGen: gen}

		require.NoError(t, m.SetProjectFilter([]string{"alpha"}))
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Contains(t, string(data), "tracker:\n  kind: linear\n  api_key: key\n  project_slug: alpha\nagent:",
			"the key lands inside the tracker block with its indentation")
		assert.Equal(t, []string{"alpha"}, fpm.GetProjectFilter(), "memory == file")
		cfg, _, err := loadSettingsGeneration(path)
		require.NoError(t, err, "the rewritten WORKFLOW.md still loads")
		assert.Equal(t, "alpha", cfg.Tracker.ProjectSlug)
	})

	t.Run("no tracker block is an error and memory is unchanged", func(t *testing.T) {
		path := writeFixture(t, "---\nitervox_schema_version: 2\nagent:\n  command: claude\n---\n\nPrompt.\n")
		fpm := &fakeProjectManager{}
		m := &linearProjectManager{pm: fpm, workflowPath: path, settingsGen: currentSettingsGeneration(path)}
		err := m.SetProjectFilter([]string{"alpha"})
		require.Error(t, err)
		assert.False(t, fpm.set, "a save that did not persist must not change memory")
		data, rerr := os.ReadFile(path)
		require.NoError(t, rerr)
		assert.NotContains(t, string(data), "alpha")
	})
}
