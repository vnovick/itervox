package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/tracker"
	ghclient "github.com/vnovick/itervox/internal/tracker/github"
)

// TestWithTrackerStore (#113): the store goes in front of a GitHub or Linear
// adapter only when tracker.store.enabled is set, and the scope changes with
// the project and the active states.
func TestWithTrackerStore(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tracker.Kind = "github"
	cfg.Tracker.ProjectSlug = "owner/repo"
	cfg.Tracker.ActiveStates = []string{"todo"}
	cfg.Tracker.TerminalStates = []string{"done"}
	cfg.Tracker.Store.SyncIntervalMs = 60000
	gh := ghclient.NewClient(ghclient.ClientConfig{APIKey: "ghp_test", ProjectSlug: "owner/repo", Endpoint: "http://127.0.0.1:1"})
	workflow := t.TempDir() + "/WORKFLOW.md"

	assert.Same(t, gh, withTrackerStore(t.Context(), cfg, workflow, gh), "off by default")

	cfg.Tracker.Store.Enabled = true
	wrapped := withTrackerStore(t.Context(), cfg, workflow, gh)
	assert.NotSame(t, tracker.Tracker(gh), wrapped)
	_, ok := wrapped.(tracker.IdempotentCommenter)
	assert.True(t, ok)
	cc, ok := commentCommandsTracker(wrapped).(commentCommandTracker)
	require.True(t, ok, "comment commands reach GitHub's repository-comment reads behind the store")
	// Issue writes from a command go through the store, so the next tick
	// sees `/itervox run` without waiting for a sync.
	assert.Same(t, wrapped, cc.(storeCommentCommands).Tracker)
	assert.Same(t, gh, commentCommandsTracker(gh))

	cfg.Tracker.Kind = "local"
	mem := tracker.NewMemoryTracker(nil, nil, nil)
	assert.Same(t, mem, withTrackerStore(t.Context(), cfg, workflow, mem), "local trackers need no store")

	cfg.Tracker.Kind = "github"
	scope := trackerStoreScope(cfg)
	cfg.Tracker.ActiveStates = []string{"TODO"}
	require.Equal(t, scope, trackerStoreScope(cfg), "case does not change the scope")
	cfg.Tracker.ActiveStates = []string{"todo", "doing"}
	assert.NotEqual(t, scope, trackerStoreScope(cfg))
}
