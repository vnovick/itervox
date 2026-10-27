package tasktemplate

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTaskTemplateRoundTripAndVars (#251): a template saved under
// .itervox/tasks/<name>.md is YAML frontmatter for its fields plus the
// instructions as the body; it loads back field for field, lists its
// `{{var}}` placeholders once each in order, fills them from vars and
// reports the ones without a value; create refuses an existing name,
// replace overwrites, delete removes, and bad names and files are refused
// or reported.
func TestTaskTemplateRoundTripAndVars(t *testing.T) {
	dir := Dir(filepath.Join(t.TempDir(), "WORKFLOW.md"))
	assert.Equal(t, RelDir, filepath.Join(filepath.Base(filepath.Dir(dir)), filepath.Base(dir)))

	list, problems := Load(dir)
	assert.Empty(t, list, "a missing directory is no templates")
	assert.Empty(t, problems)

	tpl := Template{
		Name:         "upgrade-dep",
		Title:        "Upgrade {{package}} to {{ version }}",
		Description:  "Bump one dependency.",
		Profile:      "implementer",
		Playbook:     "dependency-upgrade",
		Backend:      "codex",
		BaseBranch:   "release/2.x",
		Instructions: "Upgrade {{package}}.\n\nRun `make test` and fix what {{package}} broke.\nCall it {{codename}}.",
	}
	require.NoError(t, Write(dir, tpl, false))
	raw, err := os.ReadFile(filepath.Join(dir, "upgrade-dep.md"))
	require.NoError(t, err)
	assert.Contains(t, string(raw), "---\ntitle: Upgrade {{package}} to {{ version }}\n")
	assert.Contains(t, string(raw), "base: release/2.x\n")
	assert.Contains(t, string(raw), "---\n\nUpgrade {{package}}.\n")

	got, err := Read(dir, "upgrade-dep")
	require.NoError(t, err)
	assert.Equal(t, tpl, got, "every field survives the round trip")
	assert.Equal(t, []string{"package", "version", "codename"}, got.Vars())

	title, instructions, missing := got.Render(map[string]string{"package": "lodash", "version": "4.17.21"})
	assert.Equal(t, "Upgrade lodash to 4.17.21", title)
	assert.Equal(t, "Upgrade lodash.\n\nRun `make test` and fix what lodash broke.\nCall it {{codename}}.", instructions)
	assert.Equal(t, []string{"codename"}, missing, "a placeholder without a value is reported and left in place")
	_, _, missing = got.Render(map[string]string{"package": "a", "version": "b", "codename": "c"})
	assert.Empty(t, missing)

	// Create refuses an existing name; replace overwrites it.
	assert.ErrorIs(t, Write(dir, tpl, false), ErrExists)
	tpl.Title = "Upgrade {{package}}"
	tpl.Instructions = ""
	require.NoError(t, Write(dir, tpl, true))
	got, err = Read(dir, "upgrade-dep")
	require.NoError(t, err)
	assert.Equal(t, "Upgrade {{package}}", got.Title)
	assert.Empty(t, got.Instructions)
	assert.Equal(t, []string{"package"}, got.Vars())

	// A hand-written file with no frontmatter fields but a title loads; a
	// file without a title and a misnamed file are reported, not loaded.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "docs.md"), []byte("---\ntitle: Write the docs\n---\nFor {{audience}}.\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "broken.md"), []byte("---\ndescription: no title\n---\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Bad Name.md"), []byte("---\ntitle: x\n---\n"), 0o644))
	list, problems = Load(dir)
	require.Len(t, list, 2)
	assert.Equal(t, "docs", list[0].Name, "sorted by name")
	assert.Equal(t, "upgrade-dep", list[1].Name)
	assert.Equal(t, []string{"audience"}, list[0].Vars())
	assert.Len(t, problems, 2)

	// Validation and names.
	assert.ErrorIs(t, Write(dir, Template{Name: "../x", Title: "t"}, true), ErrInvalidName)
	var inv *InvalidError
	assert.True(t, errors.As(Write(dir, Template{Name: "no-title"}, true), &inv))
	assert.True(t, errors.As(Write(dir, Template{Name: "nl", Title: "t", Profile: "a\nb"}, true), &inv))
	_, err = Read(dir, "missing")
	assert.ErrorIs(t, err, ErrNotFound)

	require.NoError(t, Delete(dir, "upgrade-dep"))
	assert.ErrorIs(t, Delete(dir, "upgrade-dep"), ErrNotFound)
	assert.ErrorIs(t, Delete(dir, "../etc"), ErrInvalidName)
}
