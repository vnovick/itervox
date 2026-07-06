package orchestrator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
)

func TestWriteFileAtomicallyReplacesExistingFileWithoutLeavingTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "input_required.json")

	require.NoError(t, os.WriteFile(path, []byte(`{"old":true}`), 0o644))

	require.NoError(t, writeFileAtomically(path, []byte(`{"new":true}`), 0o644))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.JSONEq(t, `{"new":true}`, string(data))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "input_required.json", entries[0].Name())
}

// F-1 propagation: AutomationID, TriggerType, and CommentCount must survive
// the deep-copy in storeSnap → Snapshot. Without these fields propagating, every
// later automation visualization (T-2 activity card, T-5 timeline filter,
// T-6 comment-count badge) would be impossible.
func TestSnapshotPreservesAutomationFieldsOnRunningEntries(t *testing.T) {
	o := New(&config.Config{}, nil, nil, nil)

	state := NewState(&config.Config{})
	state.Running["ENG-1"] = &RunEntry{
		Issue:        domain.Issue{ID: "u1", Identifier: "ENG-1", Title: "T", State: "In Progress"},
		Kind:         "automation",
		AutomationID: "pr-on-input",
		TriggerType:  "input_required",
		CommentCount: 3,
	}
	o.storeSnap(state)

	snap := o.Snapshot()
	require.Contains(t, snap.Running, "ENG-1")
	got := snap.Running["ENG-1"]
	assert.Equal(t, "pr-on-input", got.AutomationID)
	assert.Equal(t, "input_required", got.TriggerType)
	assert.Equal(t, 3, got.CommentCount)
	// Manual runs (no automation context) leave the fields zero-valued; the
	// JSON tag on server.RunningRow uses `omitempty` so they vanish from the
	// wire format. Verify we don't leak a default value.
	state2 := NewState(&config.Config{})
	state2.Running["ENG-2"] = &RunEntry{
		Issue: domain.Issue{ID: "u2", Identifier: "ENG-2", Title: "T2", State: "In Progress"},
	}
	o.storeSnap(state2)
	snap2 := o.Snapshot()
	require.Contains(t, snap2.Running, "ENG-2")
	manual := snap2.Running["ENG-2"]
	assert.Empty(t, manual.AutomationID)
	assert.Empty(t, manual.TriggerType)
	assert.Equal(t, 0, manual.CommentCount)
}
