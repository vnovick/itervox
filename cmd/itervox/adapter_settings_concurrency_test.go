package main

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/workflow"
)

const settingsConcurrencyFixture = `---
itervox_schema_version: 2
tracker:
  kind: linear
  api_key: key
  project_slug: proj
agent:
  command: claude
  max_concurrent_agents: 3
workspace:
  root: ./ws
---

Prompt.
`

func newSettingsTestAdapter(t *testing.T) (*orchestratorAdapter, string) {
	t.Helper()
	workflowPath := filepath.Join(t.TempDir(), "WORKFLOW.md")
	require.NoError(t, os.WriteFile(workflowPath, []byte(settingsConcurrencyFixture), 0o644))
	cfg, err := config.Load(workflowPath)
	require.NoError(t, err)
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := orchestrator.New(cfg, mt, &agenttest.FakeRunner{}, nil)
	return &orchestratorAdapter{orch: orch, cfg: cfg, tr: mt, workflowPath: workflowPath, notify: func() {}}, workflowPath
}

// V4: now that the daemon's own settings writes no longer reload (CORE-116),
// nothing re-syncs WORKFLOW.md and the in-memory cfg if two saves interleave.
// Concurrent SSH-host adds each read the host list from memory, write it, and
// then apply their own host to memory: the file keeps only the last writer's
// list while memory accumulates every host. File and memory must agree.
func TestConcurrentSettingsSavesKeepFileAndMemoryInSync(t *testing.T) {
	for round := range 5 {
		adapter, path := newSettingsTestAdapter(t)
		const n = 8
		var wg sync.WaitGroup
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				assert.NoError(t, adapter.AddSSHHost(fmt.Sprintf("host-%d", i), ""))
			}()
		}
		wg.Wait()

		loaded, err := config.Load(path)
		require.NoError(t, err)
		memHosts, _ := adapter.orch.SSHHostsCfg()
		fileHosts := slices.Clone(loaded.Agent.SSHHosts)
		slices.Sort(memHosts)
		slices.Sort(fileHosts)
		require.Equal(t, memHosts, fileHosts, "round %d: WORKFLOW.md and the in-memory cfg diverged", round)
		require.Len(t, fileHosts, n, "round %d: a concurrent save was lost", round)
	}
}

// V4, scalar form: two saves of different values to one field, repeated.
// Whatever order they land in, the file and memory must name the same value.
func TestConcurrentScalarSettingsSavesAgree(t *testing.T) {
	adapter, path := newSettingsTestAdapter(t)
	for round := range 50 {
		var wg sync.WaitGroup
		for _, s := range []string{"round-robin", "least-loaded"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				assert.NoError(t, adapter.SetDispatchStrategy(s))
			}()
		}
		wg.Wait()
		loaded, err := config.Load(path)
		require.NoError(t, err)
		require.Equal(t, adapter.orch.DispatchStrategyCfg(), loaded.Agent.DispatchStrategy, "round %d", round)
	}
}

// V4: a save whose WORKFLOW.md write fails changes neither the file nor
// memory (file first, then memory).
func TestSettingsSaveWriteFailureLeavesMemoryUntouched(t *testing.T) {
	adapter, path := newSettingsTestAdapter(t)
	before := adapter.orch.DispatchStrategyCfg()
	dir := filepath.Dir(path)
	require.NoError(t, os.Chmod(dir, 0o500)) // atomic write cannot create its temp file
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := adapter.SetDispatchStrategy("least-loaded")
	require.Error(t, err)
	assert.Equal(t, before, adapter.orch.DispatchStrategyCfg(), "memory must not change when the write failed")
	got, rerr := os.ReadFile(path)
	require.NoError(t, rerr)
	assert.Equal(t, settingsConcurrencyFixture, string(got))
}

// V4: when a save's in-memory apply fails and the rollback write ALSO fails,
// the file now holds a value memory does not. That used to be discarded
// (`_ = persist(prev)`); it must be logged at ERROR and returned.
func TestSettingsRollbackFailureIsLoggedAndReturned(t *testing.T) {
	_, path := newSettingsTestAdapter(t)
	require.NoError(t, workflow.PatchWorkspaceBoolField(path, "auto_clear", true)) // the forward write landed
	dir := filepath.Dir(path)
	require.NoError(t, os.Chmod(dir, 0o500)) // ...and the rollback write cannot
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	applyErr := errors.New("apply rejected")
	err := rollbackSettingsWrite("workspace.auto_clear", applyErr, func() error {
		return workflow.PatchWorkspaceBoolField(path, "auto_clear", false)
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, applyErr)
	assert.Contains(t, err.Error(), "rollback")
	assert.Contains(t, logs.String(), "level=ERROR")
	assert.True(t, strings.Contains(logs.String(), "workspace.auto_clear"), logs.String())
}
