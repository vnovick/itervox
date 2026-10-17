package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/workflow"
)

// CORE-160 — the last three settings saves that went through
// workflow.WriteAndReload (tracker state lists, the Linear project filter,
// the dashboard model refresh) now have in-memory setters and are self-writes:
// saving them never reloads WORKFLOW.md, so it can never touch an in-flight
// turn.

// watchNoReload starts a real watcher on path from its current bytes and
// returns a func that waits out one poll + debounce (plus margin) and
// reports how many reloads fired.
func watchNoReload(t *testing.T, path string) func() int32 {
	t.Helper()
	cfg, err := config.Load(path) // no generation bump: the adapter's fence stays open
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	var fired atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = workflow.WatchFrom(ctx, path, cfg.WorkflowHash, func() { fired.Add(1) })
	}()
	t.Cleanup(func() { cancel(); <-done })
	return func() int32 {
		time.Sleep(5500 * time.Millisecond)
		return fired.Load()
	}
}

func writeFixture(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "WORKFLOW.md")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	return path
}

// TestTrackerStatesSaveIsLiveWithoutReload: the dashboard tracker-states save
// reaches the tracker client's state lists in memory (the client used to copy
// them once per generation) and does not reload.
func TestTrackerStatesSaveIsLiveWithoutReload(t *testing.T) {
	t.Parallel()
	path := writeFixture(t, reloadFenceFixture)
	a := generationAdapter(t, path)
	mt := tracker.NewMemoryTracker([]domain.Issue{{ID: "1", Identifier: "ENG-1", State: "Doing"}}, []string{"Todo"}, []string{"Done"})
	a.tr = mt
	reloads := watchNoReload(t, path)

	require.NoError(t, a.UpdateTrackerStates([]string{"Todo", "Doing"}, []string{"Done"}, "Done"))

	got, err := mt.FetchCandidateIssues(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 1, "the tracker client sees the new active_states without a reload")
	active, _, _ := a.orch.TrackerStatesCfg()
	assert.Equal(t, []string{"Todo", "Doing"}, active)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "Doing", "persisted to WORKFLOW.md")
	assert.Zero(t, reloads(), "a tracker-states save must not reload WORKFLOW.md")
}

// fakeProjectManager records the filter it was given.
type fakeProjectManager struct {
	mu     sync.Mutex
	filter []string
	set    bool
}

func (f *fakeProjectManager) FetchProjects(context.Context) ([]domain.Project, error) {
	return nil, nil
}

func (f *fakeProjectManager) SetProjectFilter(slugs []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.set = true
	if slugs == nil {
		f.filter = nil
		return
	}
	f.filter = append([]string{}, slugs...)
}

func (f *fakeProjectManager) GetProjectFilter() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.filter
}

// TestProjectFilterSaveIsLiveWithoutReload: the Linear project filter save is
// applied to the tracker client in memory and does not reload. A reset
// (nil) comments project_slug out on disk, so memory is set to the explicit
// "all issues" filter the file now describes, not to the stale load-time
// slug.
func TestProjectFilterSaveIsLiveWithoutReload(t *testing.T) {
	t.Parallel()
	path := writeFixture(t, reloadFenceFixture)
	_, gen, err := loadSettingsGeneration(path)
	require.NoError(t, err)
	fpm := &fakeProjectManager{}
	m := &linearProjectManager{pm: fpm, workflowPath: path, settingsGen: gen}
	reloads := watchNoReload(t, path)

	require.NoError(t, m.SetProjectFilter([]string{"alpha", "beta"}))
	assert.Equal(t, []string{"alpha", "beta"}, fpm.GetProjectFilter())
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "project_slug: alpha, beta")

	require.NoError(t, m.SetProjectFilter(nil))
	assert.Equal(t, []string{}, fpm.GetProjectFilter(), "reset = all issues, matching the commented-out project_slug")
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "# project_slug:")

	assert.Zero(t, reloads(), "a project-filter save must not reload WORKFLOW.md")
}

// TestProjectFilterSaveRefusedAfterReload: the project-filter save honours
// the M1-close generation fence like every other settings save.
func TestProjectFilterSaveRefusedAfterReload(t *testing.T) {
	path := writeFixture(t, reloadFenceFixture)
	_, gen, err := loadSettingsGeneration(path)
	require.NoError(t, err)
	fpm := &fakeProjectManager{}
	m := &linearProjectManager{pm: fpm, workflowPath: path, settingsGen: gen}
	_, _, err = loadSettingsGeneration(path) // a reload supersedes gen
	require.NoError(t, err)

	err = m.SetProjectFilter([]string{"alpha"})
	assert.ErrorIs(t, err, server.ErrSettingsReloading, "M4-close D4: the refusal reaches the HTTP client")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "alpha", "a superseded generation must not write")
	assert.False(t, fpm.set, "nor apply the filter to a dying tracker client")
}

// TestModelsRefreshIsLiveWithoutReload: the dashboard model refresh updates
// the in-memory agent.available_models (it had no setter) and does not reload.
func TestModelsRefreshIsLiveWithoutReload(t *testing.T) {
	body := strings.Replace(reloadFenceFixture, "  dispatch_strategy: round-robin\n",
		"  dispatch_strategy: round-robin\n  available_models:\n    claude:\n      - id: stale-model\n        label: Stale\n", 1)
	path := writeFixture(t, body)
	a := generationAdapter(t, path)
	require.Equal(t, "stale-model", a.orch.AvailableModelsCfg()["claude"][0].ID)
	reloads := watchNoReload(t, path)

	t.Setenv("ANTHROPIC_API_KEY", "") // default catalog, no network
	out, err := a.RefreshAvailableModels(context.Background(), "claude")
	require.NoError(t, err)
	require.NotEmpty(t, out["claude"])

	live := a.orch.AvailableModelsCfg()["claude"]
	require.NotEmpty(t, live)
	assert.NotEqual(t, "stale-model", live[0].ID, "the refreshed list is live without a reload")
	assert.Equal(t, out["claude"][0].ID, live[0].ID)
	assert.Zero(t, reloads(), "a model refresh must not reload WORKFLOW.md")
}

// TestSettingsSavesDuringTurnDoNotReload drives the real daemon: while an
// agent turn is in flight, the dashboard saves tracker states and refreshes
// models. Neither save reloads WORKFLOW.md (no drain, no reload line) and the
// turn runs to completion.
func TestSettingsSavesDuringTurnDoNotReload(t *testing.T) {
	p := newDrainProject(t, 8)
	_, stderrPath := p.startHeadless(t, []string{"--shutdown-grace", "30s"})
	waitFor(t, 20*time.Second, "first agent turn started", func() bool { return countLines(p.startedPath) >= 1 })
	base := strings.TrimSuffix(readTrim(t, dashboardURLFilePath(p.Workflow)), "/")

	do := func(method, path, body string) int {
		req, err := http.NewRequest(method, base+path, strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer drain-test-token")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	assert.Equal(t, http.StatusOK, do(http.MethodPut, "/api/v1/settings/tracker/states",
		`{"activeStates":["Todo","In Progress","Doing"],"terminalStates":["Done"],"completionState":""}`))
	assert.Equal(t, http.StatusOK, do(http.MethodPost, "/api/v1/settings/models/refresh", `{"backend":"claude"}`))

	waitFor(t, 20*time.Second, "the in-flight turn finished", func() bool { return countLines(p.finishedPath) >= 1 })
	time.Sleep(2 * time.Second) // past the watcher's poll + debounce after the last save
	stderr := readTrim(t, stderrPath)
	assert.NotContains(t, stderr, "reload: WORKFLOW.md changed", "a settings save must not reload")
	data, err := os.ReadFile(p.Workflow)
	require.NoError(t, err)
	assert.Contains(t, string(data), "Doing")
	assert.Contains(t, string(data), "available_models")
}
