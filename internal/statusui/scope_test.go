package statusui

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/vnovick/itervox/internal/server"
)

// TestPickerAssignedToOption (#182): with a combinable scope the project
// picker offers "Assigned to …" after "All issues"; it toggles on its own
// and combines with the selected projects (AND), and its initial state
// follows the scope's assignee.
func TestPickerAssignedToOption(t *testing.T) {
	var applied []string
	snap := newTestSnap(server.StateSnapshot{TrackerKind: "linear", TrackerScope: "alpha", ActiveProjectFilter: []string{"alpha"}})
	m := readyModel(snap)
	m.cfg.SetProjectFilter = func(slugs []string) { applied = slugs }
	newM, _ := m.Update(pickerLoadedMsg{projects: []ProjectItem{{Name: "Alpha", Slug: "alpha"}}})
	m = newM.(Model)

	rows := m.pickerRows()
	labels := make([]string, 0, len(rows))
	for _, r := range rows {
		labels = append(labels, r.label)
	}
	assert.Equal(t, []string{"All issues", "Assigned to me", "No project", "Alpha"}, labels)
	assert.Equal(t, assigneeScopeSlug, m.pickerSlugAt(1))
	assert.Equal(t, "alpha", m.pickerSlugAt(3))
	assert.False(t, m.pickerSel[assigneeScopeSlug], "no assignee in the scope yet")
	assert.Contains(t, m.renderProjectPicker(), "Assigned to me")

	m.togglePickerRow(assigneeScopeSlug)
	assert.Equal(t, map[string]bool{"alpha": true, assigneeScopeSlug: true}, m.pickerSel, "combines with the project")
	m.applyPickerFilter()
	assert.Equal(t, []string{"alpha", assigneeScopeSlug}, applied, "project AND assigned to")

	m.pickerSel = map[string]bool{"": true, assigneeScopeSlug: true}
	m.applyPickerFilter()
	assert.Equal(t, []string{assigneeScopeSlug}, applied, "every project AND assigned to")

	// A scope with an assignee opens with the row checked and named.
	withAssignee := readyModel(newTestSnap(server.StateSnapshot{TrackerKind: "linear", TrackerScope: "x",
		ScopeAssignee: "ada@example.com", ScopeAssigneeName: "Ada Lovelace"}))
	newM, _ = withAssignee.Update(pickerLoadedMsg{projects: nil})
	withAssignee = newM.(Model)
	assert.True(t, withAssignee.pickerSel[assigneeScopeSlug])
	assert.Equal(t, "Assigned to Ada Lovelace", withAssignee.pickerRows()[1].label)

	// Without a combinable scope the row is not offered.
	plain := readyModel(newTestSnap(server.StateSnapshot{TrackerKind: "linear"}))
	newM, _ = plain.Update(pickerLoadedMsg{projects: nil})
	plain = newM.(Model)
	assert.Equal(t, "__no_project__", plain.pickerSlugAt(1))
}

// TestHeaderShowsTrackerScope (#182): the header names the combined scope.
func TestHeaderShowsTrackerScope(t *testing.T) {
	m := readyModel(newTestSnap(server.StateSnapshot{TrackerKind: "linear", TrackerScope: "ENG · assigned to me · label: agent"}))
	assert.Regexp(t, `SCOPE\s+▸ ENG · assigned to me · label: agent`, m.View())
	plain := readyModel(newTestSnap(server.StateSnapshot{TrackerKind: "linear"}))
	assert.NotContains(t, plain.View(), "SCOPE", "no scope line without a scope")
}
