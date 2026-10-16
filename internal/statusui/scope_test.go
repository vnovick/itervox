package statusui

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/vnovick/itervox/internal/server"
)

// TestPickerAssignedToOption (#182): with an assignee scope available the
// project picker offers "Assigned to …" after "All issues"; choosing it
// clears every project (and a project clears it), and applying it sends the
// assignee entry alone.
func TestPickerAssignedToOption(t *testing.T) {
	var applied []string
	snap := newTestSnap(server.StateSnapshot{TrackerKind: "linear", AssigneeScopeLabel: "Itervox Bot"})
	m := readyModel(snap)
	m.cfg.SetProjectFilter = func(slugs []string) { applied = slugs }
	newM, _ := m.Update(pickerLoadedMsg{projects: []ProjectItem{{Name: "Alpha", Slug: "alpha"}}})
	m = newM.(Model)

	rows := m.pickerRows()
	labels := make([]string, 0, len(rows))
	for _, r := range rows {
		labels = append(labels, r.label)
	}
	assert.Equal(t, []string{"All issues", "Assigned to Itervox Bot", "No project", "Alpha"}, labels)
	assert.Equal(t, assigneeScopeSlug, m.pickerSlugAt(1))
	assert.Equal(t, "alpha", m.pickerSlugAt(3))
	assert.Contains(t, m.renderProjectPicker(), "Assigned to Itervox Bot")

	m.togglePickerRow("alpha")
	m.togglePickerRow(assigneeScopeSlug)
	assert.Equal(t, map[string]bool{assigneeScopeSlug: true}, m.pickerSel, "the assignee scope stands alone")
	m.applyPickerFilter()
	assert.Equal(t, []string{assigneeScopeSlug}, applied)

	m.togglePickerRow("alpha")
	assert.Equal(t, map[string]bool{"alpha": true}, m.pickerSel, "a project clears the assignee scope")

	// Without an assignee scope the row is not offered.
	plain := readyModel(newTestSnap(server.StateSnapshot{TrackerKind: "linear"}))
	newM, _ = plain.Update(pickerLoadedMsg{projects: nil})
	plain = newM.(Model)
	assert.Equal(t, "__no_project__", plain.pickerSlugAt(1))
}

// TestHeaderShowsTrackerScope (#182): the header names the active scope the
// adapter reports, and the issues header marks an assignee scope.
func TestHeaderShowsTrackerScope(t *testing.T) {
	m := readyModel(newTestSnap(server.StateSnapshot{
		TrackerKind: "linear", TrackerScope: "assigned to Itervox Bot · team ENG",
		ActiveProjectFilter: []string{assigneeScopeSlug},
	}))
	view := m.View()
	assert.Regexp(t, `SCOPE\s+▸ assigned to Itervox Bot · team ENG`, view)
	assert.Contains(t, view, "(assigned)")
	plain := readyModel(newTestSnap(server.StateSnapshot{TrackerKind: "linear"}))
	assert.NotContains(t, plain.View(), "SCOPE", "no scope line without a scope")
}
