package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

// TestSnapshotHistoryCarriesPromptDigestRunIDAndSteers (#186): the history
// rows the API and dashboard read carry each run's prompt digest (with its
// parts), run ID, profile and steering history, under the documented wire
// keys; a legacy row carries none of them.
func TestSnapshotHistoryCarriesPromptDigestRunIDAndSteers(t *testing.T) {
	digest := strings.Repeat("ab", 32)
	runs := []map[string]any{
		{"Identifier": "ENG-9", "Status": "succeeded", "StartedAt": "2026-01-01T00:00:00Z", "FinishedAt": "2026-01-01T00:01:00Z"},
		{
			"Identifier": "ENG-1", "Status": "failed", "StartedAt": "2026-01-02T00:00:00Z", "FinishedAt": "2026-01-02T00:01:00Z",
			"profileName": "implementer", "runID": "run-1", "promptDigest": digest,
			"promptParts":   map[string]string{"soul": "1111111111111111", "rules": "2222222222222222"},
			"steers":        []map[string]string{{"text": "use pnpm", "at": "2026-01-02T00:00:30Z", "delivery": "live"}},
			"steersDropped": 2,
		},
	}
	data, err := json.Marshal(runs)
	require.NoError(t, err)
	historyFile := filepath.Join(t.TempDir(), "history.json")
	require.NoError(t, os.WriteFile(historyFile, data, 0o644))

	cfg := &config.Config{Tracker: config.TrackerConfig{ActiveStates: []string{"Todo"}, TerminalStates: []string{"Done"}}}
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := orchestrator.New(cfg, mt, &agenttest.FakeRunner{}, nil)
	orch.SetHistoryFile(historyFile)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_ = orch.Run(ctx) // loads the history file, then stops
	ob, err := outbox.New("")
	require.NoError(t, err)
	snap := buildSnapFunc(orch, mt, cfg, "sess-1", logbuffer.New(), t.TempDir()+"/WORKFLOW.md", nil, ob)

	got := snap()
	require.Len(t, got.History, 2)
	legacy, row := got.History[0], got.History[1]
	assert.Empty(t, legacy.PromptDigest)
	assert.Nil(t, legacy.PromptParts)
	assert.Nil(t, legacy.Steers)
	assert.Equal(t, "run-1", row.RunID)
	assert.Equal(t, digest, row.PromptDigest)
	assert.Equal(t, "implementer", row.ProfileName)
	require.NotNil(t, row.PromptParts)
	assert.Equal(t, "1111111111111111", row.PromptParts.Soul)
	require.Len(t, row.Steers, 1)
	assert.Equal(t, "use pnpm", row.Steers[0].Text)
	assert.Equal(t, "live", row.Steers[0].Delivery)
	assert.Equal(t, 2, row.SteersDropped)

	wire, err := json.Marshal(row)
	require.NoError(t, err)
	for _, key := range []string{`"runId"`, `"promptDigest"`, `"promptParts"`, `"steers"`, `"steersDropped"`, `"profileName"`} {
		assert.Contains(t, string(wire), key, "wire key (web/src/types/schemas.ts)")
	}
}
