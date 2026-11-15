package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/workflow"
)

// splitWorkflowText returns a WORKFLOW.md's decoded front matter and its body.
func splitWorkflowText(t *testing.T, text string) (map[string]any, string) {
	t.Helper()
	rest, ok := strings.CutPrefix(text, "---\n")
	require.True(t, ok, "no front matter:\n%s", text)
	front, body, ok := strings.Cut(rest, "\n---\n")
	require.True(t, ok)
	var m map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(front), &m))
	return m, strings.TrimSpace(body)
}

func copySymphonyFixture(t *testing.T) (string, []byte) {
	t.Helper()
	original, err := os.ReadFile(filepath.Join("testdata", "symphony", "WORKFLOW.md"))
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "WORKFLOW.md")
	require.NoError(t, os.WriteFile(path, original, 0o644))
	return path, original
}

// TestImportExportSymphonyRoundTrip (#158): `itervox import` turns the
// Symphony fixture into an Itervox workflow that loads (settings — the
// tracker and hooks included since #239 — in .itervox/settings.jsonc, the
// prompt in WORKFLOW.md, the
// original in WORKFLOW.md.bak, the Symphony-only codex keys reported), and
// `itervox export --symphony` writes back the same Symphony core keys and
// prompt.
func TestImportExportSymphonyRoundTrip(t *testing.T) {
	t.Setenv("LINEAR_API_KEY", "lin_fixture")
	path, original := copySymphonyFixture(t)

	var out, errOut bytes.Buffer
	require.Equal(t, 0, importCmd([]string{path}, time.Unix(0, 0), &out, &errOut), errOut.String())
	report := out.String()
	assert.Contains(t, report, "(Symphony workflow)")
	assert.Contains(t, report, "codex.approval_policy: Itervox has no equivalent")
	assert.Contains(t, report, "codex.thread_sandbox: Itervox has no equivalent")
	assert.Contains(t, report, "codex.turn_sandbox_policy: Itervox has no equivalent")
	assert.NotContains(t, report, "warning:")

	bak, err := os.ReadFile(path + ".bak")
	require.NoError(t, err)
	assert.Equal(t, string(original), string(bak), "the original is kept as WORKFLOW.md.bak")

	settings, err := config.ReadSettingsFile(config.ProjectSettingsPath(path))
	require.NoError(t, err)
	agent := settings["agent"].(map[string]any)
	assert.Equal(t, 15, agent["max_turns"])
	assert.Equal(t, "codex", agent["command"], "Itervox runs `codex exec`, not the app server")
	assert.Equal(t, "codex", agent["backend"])
	assert.Equal(t, 1800000, agent["turn_timeout_ms"])
	assert.Equal(t, 45000, settings["polling"].(map[string]any)["interval_ms"])

	imported := readString(t, path)
	front, body := splitWorkflowText(t, imported)
	assert.Equal(t, 2, front["itervox_schema_version"])
	assert.NotContains(t, front, "polling", "an emptied section is dropped")
	assert.NotContains(t, front, "agent")
	// #239: the tracker and hooks are layered settings now; they move too,
	// the API key as its $VAR reference.
	assert.NotContains(t, front, "tracker")
	assert.NotContains(t, front, "hooks")
	tracker := settings["tracker"].(map[string]any)
	assert.Equal(t, "linear", tracker["kind"])
	assert.Equal(t, "$LINEAR_API_KEY", tracker["api_key"])
	assert.NotContains(t, readString(t, config.ProjectSettingsPath(path)), "lin_fixture", "the key is never resolved into the file")
	assert.Contains(t, front["codex"], "approval_policy", "unmapped codex keys stay for the export")
	assert.NotContains(t, front["codex"], "command")
	assert.Contains(t, imported, "# A Symphony workflow", "WORKFLOW.md keeps its comments")
	_, wantBody := splitWorkflowText(t, string(original))
	assert.Equal(t, wantBody, body, "the worker prompt stays the body")

	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, 15, cfg.Agent.MaxTurns)
	assert.Equal(t, 4, cfg.Agent.MaxConcurrentAgents)
	assert.Equal(t, "codex", cfg.Agent.Command)
	assert.Equal(t, 240000, cfg.Agent.StallTimeoutMs)
	assert.Equal(t, 90000, cfg.Hooks.TimeoutMs)
	assert.Equal(t, 8191, *cfg.Server.Port)

	exported := filepath.Join(filepath.Dir(path), "out", "WORKFLOW.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(exported), 0o755))
	out.Reset()
	errOut.Reset()
	require.Equal(t, 0, exportCmd([]string{"--symphony", "--workflow", path, "--out", exported}, &out, &errOut), errOut.String())
	assert.Contains(t, out.String(), "No Itervox-only features to drop.")

	gotFront, gotBody := splitWorkflowText(t, readString(t, exported))
	wantFront, _ := splitWorkflowText(t, string(original))
	assert.Equal(t, wantFront, gotFront, "every Symphony core key round-trips")
	assert.Equal(t, wantBody, gotBody, "the prompt round-trips")
	for _, k := range symphonyCoreKeys {
		if strings.HasPrefix(k.symphony, "codex.") || strings.HasPrefix(k.symphony, "tracker.") {
			continue
		}
		_, ok := lookupSetting(gotFront, k.symphony)
		assert.True(t, ok, "%s is exported", k.symphony)
	}
}

// TestExportSymphonyListsDroppedFeatures (#158): an Itervox workflow exports
// its Symphony core keys and names every Itervox-only feature it leaves out;
// a secret and a non-Codex agent command are never written.
func TestExportSymphonyListsDroppedFeatures(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(path, []byte(`---
itervox_schema_version: 2
tracker:
  kind: linear
  api_key: lin_literal_secret
  project_slug: proj
  failed_state: Backlog
agent:
  command: claude
  max_turns: 9
  ssh_hosts: [build-1]
automations:
  - id: nightly
    profile: coder
    trigger: {type: cron, cron: "0 3 * * *"}
---
Work on {{ issue.identifier }} and write {{ run.handoff_path }}.
`), 0o644))
	require.NoError(t, config.SetSetting(config.ProjectSettingsPath(path), "polling.interval_ms", 60000))

	var out, errOut bytes.Buffer
	require.Equal(t, 0, exportCmd([]string{"--symphony", "--workflow", path, "--out", "-"}, &out, &errOut), errOut.String())
	front, body := splitWorkflowText(t, out.String())
	assert.Equal(t, "$LINEAR_API_KEY", front["tracker"].(map[string]any)["api_key"])
	assert.NotContains(t, out.String(), "lin_literal_secret")
	assert.Equal(t, 60000, front["polling"].(map[string]any)["interval_ms"], "settings-file keys are exported")
	assert.Equal(t, 9, front["agent"].(map[string]any)["max_turns"])
	assert.NotContains(t, front, "codex", "a Claude command is not a codex.command")
	assert.Equal(t, "Work on {{ issue.identifier }} and write {{ run.handoff_path }}.", body)

	report := errOut.String()
	for _, want := range []string{"automations", "agent.ssh_hosts", "tracker.failed_state", "agent.command \"claude\" does not run Codex", "literal secret", "Itervox-only Liquid"} {
		assert.Contains(t, report, want)
	}
	assert.NotContains(t, report, "  agent.max_turns")
}

// TestImportItervoxV0Workflow (#158): an Itervox v0.x workflow's profile
// prompts move to prompt files, its layered settings to the settings file,
// and keys Itervox does not read are reported and left in place.
func TestImportItervoxV0Workflow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(path, []byte(`---
tracker:
  kind: github
  api_key: $GITHUB_TOKEN
  project_slug: acme/app
agent:
  command: claude
  max_concurrent_agents: 2 # laptop
  not_a_setting: 1
  profiles:
    coder:
      command: claude
      prompt: You are the coder. Write the code.
workspace:
  auto_clear: true
---
Fix {{ issue.identifier }}.
`), 0o644))
	t.Setenv("GITHUB_TOKEN", "fixture")
	var out, errOut bytes.Buffer
	require.Equal(t, 0, importCmd([]string{path}, time.Unix(0, 0), &out, &errOut), errOut.String())
	report := out.String()
	assert.Contains(t, report, "Itervox (schema 0) workflow")
	assert.Contains(t, report, "agent.not_a_setting: Itervox does not read it")
	assert.Contains(t, report, "Profile coder: prompt files")

	instructions, err := os.ReadFile(filepath.Join(dir, ".itervox", "agents", "coder", "INSTRUCTIONS.md"))
	require.NoError(t, err)
	assert.Contains(t, string(instructions), "Write the code.")
	settings, err := config.ReadSettingsFile(config.ProjectSettingsPath(path))
	require.NoError(t, err)
	assert.Equal(t, 2, settings["agent"].(map[string]any)["max_concurrent_agents"])
	assert.Equal(t, true, settings["workspace"].(map[string]any)["auto_clear"])
	wf, err := workflow.Load(path)
	require.NoError(t, err)
	assert.Contains(t, wf.Config["agent"], "profiles", "profiles stay in WORKFLOW.md for the Agents page")
	assert.NotContains(t, wf.Config["agent"], "max_concurrent_agents")
	_, err = config.Load(path)
	require.NoError(t, err)

	// A second import refuses to overwrite the backup.
	require.Equal(t, 1, importCmd([]string{path}, time.Unix(0, 0), &out, &errOut))
	assert.Contains(t, errOut.String(), ".bak already exists")
}
