package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/workflow"
)

const reloadFenceFixture = `---
itervox_schema_version: 2
tracker:
  kind: linear
  api_key: key
  project_slug: proj
agent:
  command: claude
  dispatch_strategy: round-robin
---

Prompt.
`

// generationAdapter builds the adapter a run generation would: its config
// comes from the main loop's load step, and it carries that step's settings
// generation.
func generationAdapter(t *testing.T, path string) *orchestratorAdapter {
	t.Helper()
	cfg, gen, err := loadSettingsGeneration(path)
	require.NoError(t, err)
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := orchestrator.New(cfg, mt, &agenttest.FakeRunner{}, nil)
	return &orchestratorAdapter{orch: orch, cfg: cfg, tr: mt, workflowPath: path, settingsGen: gen, notify: func() {}}
}

func fileDispatchStrategy(t *testing.T, path string) string {
	t.Helper()
	cfg, err := config.Load(path)
	require.NoError(t, err)
	return cfg.Agent.DispatchStrategy
}

// M1-close C2 (V4 across a reload): run() stops accepting connections but
// does not wait for in-flight handlers, so a settings save from generation N
// could write WORKFLOW.md AFTER generation N+1 loaded its config. The value
// then lived only in the file and in N's dead orchestrator, and since the
// write was the daemon's own it never triggered a reload. After a reload,
// the file and the new generation's config must agree, or the save must
// fail visibly.
func TestSettingsSaveInFlightAcrossReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "WORKFLOW.md")
	require.NoError(t, os.WriteFile(path, []byte(reloadFenceFixture), 0o644))
	gen1 := generationAdapter(t, path)

	// (a) A save in flight: it has started (holds its settings slot) but its
	// file write is held back by the WORKFLOW.md edit lock, which the test
	// owns. The reload's load step runs meanwhile.
	held := make(chan struct{})
	release := make(chan struct{})
	editDone := make(chan error, 1)
	go func() {
		editDone <- workflow.WithEditLock(path, func() error { close(held); <-release; return nil })
	}()
	<-held
	saveDone := make(chan error, 1)
	go func() { saveDone <- gen1.SetDispatchStrategy("least-loaded") }()
	time.Sleep(100 * time.Millisecond) // let the save reach the edit lock

	var gen2 *orchestratorAdapter
	reloaded := make(chan struct{})
	go func() { gen2 = generationAdapter(t, path); close(reloaded) }()
	time.Sleep(100 * time.Millisecond)
	close(release)
	require.NoError(t, <-editDone)
	saveErr := <-saveDone
	<-reloaded

	file := fileDispatchStrategy(t, path)
	if saveErr == nil {
		assert.Equal(t, file, gen2.orch.DispatchStrategyCfg(),
			"an in-flight save landed after the reload loaded WORKFLOW.md: file and running config diverged")
	} else {
		assert.ErrorIs(t, saveErr, server.ErrSettingsReloading)
		assert.Equal(t, "round-robin", file, "a refused save must not have written")
	}

	// (b) A save that starts after the reload, from the superseded
	// generation, must be refused without writing.
	before := fileDispatchStrategy(t, path)
	other := map[string]string{"round-robin": "least-loaded", "least-loaded": "round-robin"}[before]
	err := gen1.SetDispatchStrategy(other)
	require.Error(t, err, "a superseded generation's save must be refused")
	assert.True(t, errors.Is(err, server.ErrSettingsReloading), "want ErrSettingsReloading, got %v", err)
	assert.Equal(t, before, fileDispatchStrategy(t, path), "a refused save must not write")
	assert.Equal(t, fileDispatchStrategy(t, path), gen2.orch.DispatchStrategyCfg())

	// The new generation saves normally.
	require.NoError(t, gen2.SetDispatchStrategy(other))
	assert.Equal(t, other, fileDispatchStrategy(t, path))
}

// M1-close C2 (BH3 window) through the daemon's own pieces: the load step's
// config carries the hash of the bytes it parsed, and an edit injected
// between that load and the watcher start (the test hook) reloads.
func TestEditBetweenLoadAndWatchReloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "WORKFLOW.md")
	require.NoError(t, os.WriteFile(path, []byte(reloadFenceFixture), 0o644))
	cfg, _, err := loadSettingsGeneration(path)
	require.NoError(t, err)
	require.Equal(t, sha256.Sum256([]byte(reloadFenceFixture)), cfg.WorkflowHash)

	require.NoError(t, os.WriteFile(path, []byte(reloadFenceFixture+"operator edit\n"), 0o644)) // hook

	// Production poll/debounce intervals (1 s / 2 s): a reload is due ~3 s in.
	ctx, cancel := context.WithCancel(context.Background())
	var reloads atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = workflow.WatchFrom(ctx, path, cfg.WorkflowHash, func() { reloads.Add(1) })
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	require.Eventually(t, func() bool { return reloads.Load() == 1 }, 8*time.Second, 10*time.Millisecond,
		"an edit between config load and watcher start was never reloaded")
}
