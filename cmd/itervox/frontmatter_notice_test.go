package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

// captureSlog sends the default logger to a buffer for the test.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// frontMatterFixture sets the four sections #239 makes layered in the front
// matter, as every workflow before it did.
const frontMatterFixture = `---
itervox_schema_version: 2
tracker:
  kind: memory
  active_states: [Todo, Doing]
  terminal_states: [Done]
  failed_state: Todo
hooks:
  timeout_ms: 4321
  after_create: echo hi
dependencies:
  analysis_mode: manual
automations:
  - id: nightly
    enabled: false
    profile: helper
    trigger:
      type: cron
      cron: "0 3 * * *"
---
Prompt.
`

// TestFrontMatterStillLoadsWithDeprecationNotice (#239): a WORKFLOW.md that
// keeps tracker, hooks, dependencies and automations in its front matter
// loads and behaves as before; `itervox doctor` names each key as deprecated
// and where it moves, and the startup log does too, once.
func TestFrontMatterStillLoadsWithDeprecationNotice(t *testing.T) {
	t.Setenv(config.UserSettingsEnv, "off")
	path := filepath.Join(t.TempDir(), "WORKFLOW.md")
	require.NoError(t, os.WriteFile(path, []byte(frontMatterFixture), 0o644))

	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, "memory", cfg.Tracker.Kind)
	assert.Equal(t, []string{"Todo", "Doing"}, cfg.Tracker.ActiveStates)
	assert.Equal(t, "Todo", cfg.Tracker.FailedState)
	assert.Equal(t, 4321, cfg.Hooks.TimeoutMs)
	assert.Equal(t, "echo hi", cfg.Hooks.AfterCreate)
	assert.Equal(t, "manual", cfg.Dependencies.AnalysisMode)
	require.Len(t, cfg.Automations, 1)
	assert.Equal(t, "nightly", cfg.Automations[0].ID)
	want := []string{
		"automations", "dependencies.analysis_mode", "hooks.after_create", "hooks.timeout_ms",
		"tracker.active_states", "tracker.failed_state", "tracker.kind", "tracker.terminal_states",
	}
	assert.Equal(t, want, cfg.FrontMatter.Deprecated)
	assert.Empty(t, cfg.FrontMatter.Conflicts)

	// itervox doctor: one line per key, naming where it moves.
	report, _ := collectDoctorReport(path)
	assert.Equal(t, want, report.FrontMatter.Deprecated)
	text := renderDoctorReport(report)
	for _, key := range want {
		assert.Contains(t, text, "deprecated: WORKFLOW.md front matter key "+key+" is deprecated: it still applies, and moves to .itervox/settings.jsonc")
	}
	assert.NotContains(t, text, "ERROR: WORKFLOW.md", "a deprecation is a notice, not an error")

	// The startup log: named once, even across reloads.
	logs := captureSlog(t)
	logFrontMatterReport(path, cfg.FrontMatter)
	logFrontMatterReport(path, cfg.FrontMatter) // a WORKFLOW.md reload
	out := logs.String()
	assert.Equal(t, 1, strings.Count(out, "front-matter settings are deprecated"), out)
	assert.Contains(t, out, "moves_to=.itervox/settings.jsonc")
	for _, key := range want {
		assert.Contains(t, out, key)
	}
}

// TestSettingsFileWinsOverFrontMatterAndConflictIsReported (#239): when a key
// is set both in WORKFLOW.md's front matter and in a settings file, the
// settings file's value is the one in effect (the highest file that sets
// it), and the conflict is reported once: one entry per key in the load
// report, one doctor warning, one startup log line however often the
// workflow reloads. A front-matter key no file sets still applies.
func TestSettingsFileWinsOverFrontMatterAndConflictIsReported(t *testing.T) {
	user := filepath.Join(t.TempDir(), "settings.jsonc")
	t.Setenv(config.UserSettingsEnv, user)
	path := filepath.Join(t.TempDir(), "WORKFLOW.md")
	require.NoError(t, os.WriteFile(path, []byte(frontMatterFixture), 0o644))
	writeLayer := func(p, text string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(text), 0o644))
	}
	writeLayer(user, `{"hooks": {"timeout_ms": 1000}}`)
	writeLayer(config.ProjectSettingsPath(path), `{"tracker": {"active_states": ["Ready"]}, "hooks": {"timeout_ms": 2000}, "automations": []}`)
	writeLayer(config.LocalSettingsPath(path), `{"hooks": {"timeout_ms": 3000}}`)

	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, []string{"Ready"}, cfg.Tracker.ActiveStates, "the project file wins over the front matter")
	assert.Equal(t, 3000, cfg.Hooks.TimeoutMs, "the highest file wins")
	assert.Empty(t, cfg.Automations, "an empty list in a file replaces the front matter's")
	assert.Equal(t, []string{"Done"}, cfg.Tracker.TerminalStates, "a key no file sets still applies")
	assert.Equal(t, "echo hi", cfg.Hooks.AfterCreate)

	assert.Equal(t, []config.SettingsConflict{
		{Key: "automations", Layer: config.LayerProject, Path: config.ProjectSettingsPath(path)},
		{Key: "hooks.timeout_ms", Layer: config.LayerLocal, Path: config.LocalSettingsPath(path)},
		{Key: "tracker.active_states", Layer: config.LayerProject, Path: config.ProjectSettingsPath(path)},
	}, cfg.FrontMatter.Conflicts, "one entry per key, naming the layer in effect")

	text := renderDoctorReport(DoctorReport{Workflow: path, FrontMatter: cfg.FrontMatter})
	assert.Equal(t, 1, strings.Count(text, "WARNING: hooks.timeout_ms is set both in WORKFLOW.md front matter and in the local settings file"), text)
	assert.Contains(t, text, "the settings file wins")

	logs := captureSlog(t)
	for range 3 { // the first load and two reloads
		reloaded, err := config.Load(path)
		require.NoError(t, err)
		logFrontMatterReport(path, reloaded.FrontMatter)
	}
	out := logs.String()
	for _, key := range []string{"automations", "hooks.timeout_ms", "tracker.active_states"} {
		assert.Equal(t, 1, strings.Count(out, "key="+key+" "), "%s is reported once:\n%s", key, out)
	}
	assert.Equal(t, 3, strings.Count(out, "the settings file wins"))

	// Explain names the winning file, never the front matter.
	s := explainKey(t, path, "hooks.timeout_ms")
	assert.Equal(t, config.LayerLocal, s.Layer)
}
