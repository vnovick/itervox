package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/atomicfs"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
)

// #245: the daemon watches the user, project and local settings files
// alongside WORKFLOW.md. These tests drive watchConfigFiles — the watcher
// main() runs for each generation — with shrunken timings, and wait on its
// Settled hook (every decision it makes) inside a deadline instead of
// sleeping for a debounce.

const configWatchFixture = `---
itervox_schema_version: 2
tracker:
  kind: linear
  api_key: key
  project_slug: proj
agent:
  command: claude
---

Prompt.
`

// configWatch is one running watcher over a workflow's config files.
type configWatch struct {
	path    string
	cfg     *config.Config
	reloads atomic.Int32

	mu     sync.Mutex
	judged map[string]int // path -> settles that judged it
}

// startConfigWatch loads the workflow the way the main loop does
// (loadSettingsGeneration) and starts the generation's watcher on it.
// requestReload only counts: the test reloads by calling
// loadSettingsGeneration itself, as main() would after its drain.
func startConfigWatch(t *testing.T, path string) *configWatch {
	t.Helper()
	return startConfigWatchDebounce(t, path, 60*time.Millisecond)
}

// startConfigWatchDebounce is startConfigWatch with the quiet period given.
// A test that needs two writes to land in ONE quiet period uses a long one,
// so a loaded machine descheduling the test between them cannot split them.
func startConfigWatchDebounce(t *testing.T, path string, debounce time.Duration) *configWatch {
	t.Helper()
	cfg, _, err := loadSettingsGeneration(path)
	require.NoError(t, err)
	w := &configWatch{path: path, cfg: cfg, judged: map[string]int{}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = watchConfigFiles(ctx, path, cfg, func() { w.reloads.Add(1) }, configWatchOptions{
			Poll:     5 * time.Millisecond,
			Debounce: debounce,
			Settled: func(judged []string) {
				w.mu.Lock()
				defer w.mu.Unlock()
				for _, p := range judged {
					w.judged[p]++
				}
			},
		})
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		publishConfigInvalid(nil)
	})
	return w
}

// mark is the number of settles that have judged each path so far.
func (w *configWatch) mark() map[string]int {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make(map[string]int, len(w.judged))
	for k, v := range w.judged {
		out[k] = v
	}
	return out
}

// waitJudged waits (bounded) until a settle after mark has judged every one
// of paths: the watcher has decided whether their latest change reloads.
func (w *configWatch) waitJudged(t *testing.T, mark map[string]int, paths ...string) {
	t.Helper()
	require.Eventually(t, func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		for _, p := range paths {
			if w.judged[p] <= mark[p] {
				return false
			}
		}
		return true
	}, 10*time.Second, 5*time.Millisecond, "the watcher never judged %v", paths)
}

func writeConfigWatchWorkflow(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func writeSettingsFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	// One rename: the watcher never reads a half-written file.
	require.NoError(t, atomicfs.WriteFile(path, []byte(content), 0o644))
}

func maxAgentsSettings(n string) string {
	return "{\n  \"agent\": {\n    \"max_concurrent_agents\": " + n + ",\n  },\n}\n"
}

// TestEditingProjectSettingsReloadsConfig: a hand edit of
// .itervox/settings.jsonc asks the daemon to reload once, and the reload
// loads the edited value.
func TestEditingProjectSettingsReloadsConfig(t *testing.T) {
	path := writeConfigWatchWorkflow(t, configWatchFixture)
	project := config.ProjectSettingsPath(path)
	writeSettingsFile(t, project, maxAgentsSettings("3"))
	w := startConfigWatch(t, path)
	require.Equal(t, 3, w.cfg.Agent.MaxConcurrentAgents)

	mark := w.mark()
	writeSettingsFile(t, project, maxAgentsSettings("7"))
	w.waitJudged(t, mark, project)

	require.Equal(t, int32(1), w.reloads.Load(), "a hand edit of the project settings file must reload exactly once")
	require.Nil(t, loadConfigInvalid())
	cfg, _, err := loadSettingsGeneration(path)
	require.NoError(t, err)
	require.Equal(t, 7, cfg.Agent.MaxConcurrentAgents)
}

// TestEditingUserSettingsReloadsConfig: the same for ~/.itervox/settings.jsonc
// (here ITERVOX_USER_SETTINGS, the go-test default leaving the layer off).
func TestEditingUserSettingsReloadsConfig(t *testing.T) {
	user := filepath.Join(t.TempDir(), "home", ".itervox", "settings.jsonc")
	t.Setenv(config.UserSettingsEnv, user)
	writeSettingsFile(t, user, maxAgentsSettings("4"))
	path := writeConfigWatchWorkflow(t, configWatchFixture)
	w := startConfigWatch(t, path)
	require.Equal(t, 4, w.cfg.Agent.MaxConcurrentAgents)

	mark := w.mark()
	writeSettingsFile(t, user, maxAgentsSettings("9"))
	w.waitJudged(t, mark, user)

	require.Equal(t, int32(1), w.reloads.Load(), "a hand edit of the user settings file must reload exactly once")
	cfg, _, err := loadSettingsGeneration(path)
	require.NoError(t, err)
	require.Equal(t, 9, cfg.Agent.MaxConcurrentAgents)
}

// TestCreatingLocalSettingsIsPickedUp: a settings file that does not exist
// (nor its .itervox directory) when the daemon loads is still watched, so
// creating it reloads; deleting it again reloads without that layer.
func TestCreatingLocalSettingsIsPickedUp(t *testing.T) {
	path := writeConfigWatchWorkflow(t, configWatchFixture)
	local := config.LocalSettingsPath(path)
	_, err := os.Stat(filepath.Dir(local))
	require.ErrorIs(t, err, os.ErrNotExist, "the fixture must start without .itervox/")
	w := startConfigWatch(t, path)
	defaultMax := w.cfg.Agent.MaxConcurrentAgents

	mark := w.mark()
	writeSettingsFile(t, local, maxAgentsSettings("6"))
	w.waitJudged(t, mark, local)
	require.Equal(t, int32(1), w.reloads.Load(), "creating the local settings file must reload")
	cfg, _, err := loadSettingsGeneration(path)
	require.NoError(t, err)
	require.Equal(t, 6, cfg.Agent.MaxConcurrentAgents)

	mark = w.mark()
	require.NoError(t, os.Remove(local))
	w.waitJudged(t, mark, local)
	require.Equal(t, int32(2), w.reloads.Load(), "deleting the local settings file must reload")
	cfg, _, err = loadSettingsGeneration(path)
	require.NoError(t, err)
	require.Equal(t, defaultMax, cfg.Agent.MaxConcurrentAgents, "the reload runs without the deleted layer")
}

// TestInvalidSettingsEditKeepsRunningConfig: an edit that does not parse does
// not ask for a reload (the running generation keeps its config), logs one
// error naming the file and line, and shows on the dashboard's banner. The
// next edit that fixes it reloads.
func TestInvalidSettingsEditKeepsRunningConfig(t *testing.T) {
	path := writeConfigWatchWorkflow(t, configWatchFixture)
	project := config.ProjectSettingsPath(path)
	writeSettingsFile(t, project, maxAgentsSettings("3"))
	logs := captureErrorLogs(t)
	w := startConfigWatch(t, path)

	mark := w.mark()
	writeSettingsFile(t, project, "{\n  \"agent\": {\n    \"max_concurrent_agents\": ,\n  },\n}\n")
	w.waitJudged(t, mark, project)

	require.Zero(t, w.reloads.Load(), "an invalid edit must keep the running config, not reload")
	require.Equal(t, 3, w.cfg.Agent.MaxConcurrentAgents)
	errs := logs.matching("config edit is invalid")
	require.Len(t, errs, 1, "one error per invalid edit")
	require.Equal(t, project, errs[0]["path"])
	require.Contains(t, errs[0]["error"], project)
	require.Contains(t, errs[0]["error"], "line 3")
	banner := loadConfigInvalid()
	require.NotNil(t, banner, "the invalid edit must show on the dashboard")
	require.Equal(t, project, banner.Path)
	require.Contains(t, banner.Error, "line 3")

	mark = w.mark()
	writeSettingsFile(t, project, maxAgentsSettings("5"))
	w.waitJudged(t, mark, project)
	require.Equal(t, int32(1), w.reloads.Load(), "fixing the file must reload")
	require.Len(t, logs.matching("config edit is invalid"), 1)
}

// TestSettingsPageSaveDoesNotDoubleReload: a Settings page save writes the
// project settings file and removes the key from WORKFLOW.md — two files —
// and applies it in memory, so the watcher reloads for neither. A foreign
// edit of both files at once (as `itervox init --update` makes, from its own
// process) settles into exactly one reload, never two.
func TestSettingsPageSaveDoesNotDoubleReload(t *testing.T) {
	path := writeConfigWatchWorkflow(t, strings.Replace(configWatchFixture,
		"  command: claude\n", "  command: claude\n  max_concurrent_agents: 2\n", 1))
	project := config.ProjectSettingsPath(path)
	w := startConfigWatchDebounce(t, path, time.Second)
	mt := tracker.NewMemoryTracker(nil, w.cfg.Tracker.ActiveStates, w.cfg.Tracker.TerminalStates)
	orch := orchestrator.New(w.cfg, mt, &agenttest.FakeRunner{}, nil)
	a := &orchestratorAdapter{orch: orch, cfg: w.cfg, tr: mt, workflowPath: path,
		settingsGen: currentSettingsGeneration(path), notify: func() {}}

	mark := w.mark()
	res, err := a.SaveSettingsLayer(config.LayerProject, server.SettingsLayerSaveRequest{
		Values: map[string]any{"agent.max_concurrent_agents": 5},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"agent.max_concurrent_agents"}, res.Applied)
	require.Equal(t, 5, orch.MaxWorkers(), "the save applies in memory")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "max_concurrent_agents", "the save also wrote WORKFLOW.md")
	w.waitJudged(t, mark, project, path)
	require.Zero(t, w.reloads.Load(), "a Settings page save must not reload")

	// Positive control: the same two files edited by another process.
	mark = w.mark()
	writeSettingsFile(t, project, maxAgentsSettings("8"))
	require.NoError(t, atomicfs.WriteFile(path, []byte(configWatchFixture+"\nMore.\n"), 0o644))
	w.waitJudged(t, mark, project, path)
	require.Equal(t, int32(1), w.reloads.Load(), "an edit of both files must reload exactly once")
}

// errorLogs captures the ERROR records the default logger receives.
type errorLogs struct {
	mu      sync.Mutex
	records []map[string]string
}

func (l *errorLogs) matching(msg string) []map[string]string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []map[string]string
	for _, r := range l.records {
		if strings.Contains(r["msg"], msg) {
			out = append(out, r)
		}
	}
	return out
}

type errorLogHandler struct{ logs *errorLogs }

func (h errorLogHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= slog.LevelError }
func (h errorLogHandler) Handle(_ context.Context, r slog.Record) error {
	rec := map[string]string{"msg": r.Message}
	r.Attrs(func(a slog.Attr) bool {
		rec[a.Key] = a.Value.String()
		return true
	})
	h.logs.mu.Lock()
	defer h.logs.mu.Unlock()
	h.logs.records = append(h.logs.records, rec)
	return nil
}
func (h errorLogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h errorLogHandler) WithGroup(string) slog.Handler      { return h }

func captureErrorLogs(t *testing.T) *errorLogs {
	t.Helper()
	logs := &errorLogs{}
	prev := slog.Default()
	slog.SetDefault(slog.New(errorLogHandler{logs: logs}))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return logs
}

// TestDoctorReportsWatchedFiles (#245): `itervox doctor` lists the files a
// running daemon reloads on, including the settings files that do not exist
// yet, and says when the user layer is off.
func TestDoctorReportsWatchedFiles(t *testing.T) {
	path := writeConfigWatchWorkflow(t, configWatchFixture)
	project := config.ProjectSettingsPath(path)
	writeSettingsFile(t, project, maxAgentsSettings("3"))

	report, _ := collectDoctorReport(path)
	out := renderDoctorReport(report)
	require.Contains(t, out, "watched: workflow "+path+"\n")
	require.Contains(t, out, "watched: project settings "+project+"\n")
	require.Contains(t, out, "watched: local settings "+config.LocalSettingsPath(path)+" (not present; creating it is picked up)\n")
	require.Contains(t, out, "watched: user settings off")

	user := filepath.Join(t.TempDir(), "settings.jsonc")
	t.Setenv(config.UserSettingsEnv, user)
	report, _ = collectDoctorReport(path)
	out = renderDoctorReport(report)
	require.Contains(t, out, "watched: user settings "+user+" (not present; creating it is picked up)\n")
	require.NotContains(t, out, "watched: user settings off")
}

// TestSettingsPageSaveClearsInvalidEditBanner (#245): fixing an invalid hand
// edit from the Settings page's JSON view is the daemon's own write, so it
// does not reload — and it clears the banner the hand edit raised.
func TestSettingsPageSaveClearsInvalidEditBanner(t *testing.T) {
	path := writeConfigWatchWorkflow(t, configWatchFixture)
	project := config.ProjectSettingsPath(path)
	writeSettingsFile(t, project, maxAgentsSettings("3"))
	w := startConfigWatch(t, path)
	mt := tracker.NewMemoryTracker(nil, w.cfg.Tracker.ActiveStates, w.cfg.Tracker.TerminalStates)
	orch := orchestrator.New(w.cfg, mt, &agenttest.FakeRunner{}, nil)
	a := &orchestratorAdapter{orch: orch, cfg: w.cfg, tr: mt, workflowPath: path,
		settingsGen: currentSettingsGeneration(path), notify: func() {}}

	broken := "{\n  \"agent\": {\n    \"max_concurrent_agents\": ,\n  },\n}\n"
	mark := w.mark()
	writeSettingsFile(t, project, broken)
	w.waitJudged(t, mark, project)
	require.NotNil(t, loadConfigInvalid())

	mark = w.mark()
	fixed := maxAgentsSettings("4")
	_, err := a.SaveSettingsLayer(config.LayerProject, server.SettingsLayerSaveRequest{
		Raw: &fixed, BaseVersion: settingsVersion([]byte(broken)),
	})
	require.NoError(t, err)
	require.Nil(t, loadConfigInvalid(), "a save that makes the files load clears the banner")
	require.Equal(t, 4, orch.MaxWorkers())
	w.waitJudged(t, mark, project)
	require.Zero(t, w.reloads.Load())
}
