package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/logbuffer"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/outbox"
	"github.com/vnovick/itervox/internal/tracker"
)

// TestSnapshotCarriesWorkingState pins the CORE-070 Go producer: the snapshot
// publishes tracker.working_state as `workingState` so the dashboard's
// issue-detail profile lock honours a custom working state instead of falling
// back to an exact "In Progress" match. WorkingState is read-only after
// startup (no runtime setter, not on the cfgMu allowlist), so it is read
// straight from cfg like BacklogStates.
func TestSnapshotCarriesWorkingState(t *testing.T) {
	cfg := &config.Config{
		Tracker: config.TrackerConfig{
			ActiveStates:   []string{"Todo", "Doing"},
			TerminalStates: []string{"Done"},
			WorkingState:   "Doing",
		},
	}
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := orchestrator.New(cfg, mt, &agenttest.FakeRunner{}, nil)
	ob, err := outbox.New("")
	require.NoError(t, err)
	snap := buildSnapFunc(orch, mt, cfg, "sess-1", logbuffer.New(), t.TempDir()+"/WORKFLOW.md", nil, ob)

	got := snap()
	assert.Equal(t, "Doing", got.WorkingState)

	data, err := json.Marshal(got)
	require.NoError(t, err)
	var wire map[string]any
	require.NoError(t, json.Unmarshal(data, &wire))
	assert.Equal(t, "Doing", wire["workingState"], "wire key must be workingState (web/src/types/schemas.ts)")
}
