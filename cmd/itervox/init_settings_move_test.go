package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

// moveFixture is a schema-2 workflow whose tracker, hooks, dependencies,
// automations and agent settings still live in the front matter, with
// comments on and around them.
const moveFixture = `---
itervox_schema_version: 2
# How Itervox reaches the team's issues.
tracker:
  kind: linear # the team's tracker
  api_key: $LINEAR_API_KEY
  project_slug: core
  # States the agents pick up.
  active_states: [Todo, In Progress]
  terminal_states: [Done, Cancelled]
hooks:
  after_create: |
    echo created
  timeout_ms: 90000
dependencies:
  analysis_mode: manual
agent:
  max_concurrent_agents: 3
  deps_analyzer_profile: ""
automations:
  # Nightly sweep.
  - id: nightly
    enabled: false
    profile: helper
    trigger:
      type: cron
      cron: "0 3 * * *"
---
Work on {{ issue.identifier }}: {{ issue.title }}.
`

func writeMoveFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "WORKFLOW.md")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

// effectiveOf is what a move must not change.
type effectiveOf struct {
	Kind, APIKey, Project, Mode, AfterCreate, Prompt string
	Active, Terminal                                 []string
	HookTimeout, Workers, Automations                int
}

func loadEffective(t *testing.T, path string) effectiveOf {
	t.Helper()
	cfg, err := config.Load(path)
	require.NoError(t, err)
	return effectiveOf{
		Kind: cfg.Tracker.Kind, APIKey: cfg.Tracker.APIKey, Project: cfg.Tracker.ProjectSlug,
		Mode: cfg.Dependencies.AnalysisMode, AfterCreate: cfg.Hooks.AfterCreate, Prompt: cfg.PromptTemplate,
		Active: cfg.Tracker.ActiveStates, Terminal: cfg.Tracker.TerminalStates,
		HookTimeout: cfg.Hooks.TimeoutMs, Workers: cfg.Agent.MaxConcurrentAgents, Automations: len(cfg.Automations),
	}
}

// TestInitUpdateMovesFrontMatterToSettingsAndKeepsPrompt (#239): `itervox
// init --update` moves the layered front-matter keys to
// .itervox/settings.jsonc — WORKFLOW.md.bak first, the YAML comments carried
// over as // comments, an existing settings file's comments kept — leaves the
// prompt (the body) in WORKFLOW.md, and changes no effective value.
// --dry-run prints the new settings file and writes nothing.
func TestInitUpdateMovesFrontMatterToSettingsAndKeepsPrompt(t *testing.T) {
	t.Setenv("LINEAR_API_KEY", "fixture-not-a-real-key")
	t.Setenv(config.UserSettingsEnv, "off")

	t.Run("move", func(t *testing.T) {
		path := writeMoveFixture(t, moveFixture)
		settingsPath := config.ProjectSettingsPath(path)
		require.NoError(t, os.MkdirAll(filepath.Dir(settingsPath), 0o755))
		writeFileString(t, settingsPath, "// my own notes\n{\n  // kept as written\n  \"agent\": {\"max_turns\": 4},\n}\n")
		before := loadEffective(t, path)

		var out, errOut bytes.Buffer
		require.NoError(t, initUpdate(path, false, false, -1, time.Unix(0, 0), &out, &errOut), errOut.String())

		bak, err := os.ReadFile(path + ".bak")
		require.NoError(t, err, "WORKFLOW.md.bak is written first")
		assert.Equal(t, moveFixture, string(bak), "the backup is the original")

		wf := readString(t, path)
		front, body := splitWorkflowText(t, wf)
		assert.Equal(t, 2, front["itervox_schema_version"], "schema 2 stays valid; no bump")
		for _, section := range []string{"tracker", "hooks", "dependencies", "automations"} {
			assert.NotContains(t, front, section, "%s left the front matter", section)
		}
		assert.Equal(t, "Work on {{ issue.identifier }}: {{ issue.title }}.", body, "the prompt stays the body")
		assert.Contains(t, front["agent"], "profiles", "the profiles stay where the profile editor writes them")
		assert.NotContains(t, front["agent"], "max_concurrent_agents")

		text := readString(t, settingsPath)
		for _, c := range []string{
			"// my own notes", "// kept as written", // the file's own comments
			"// How Itervox reaches the team's issues.", "// the team's tracker", "// States the agents pick up.", "// Nightly sweep.",
		} {
			assert.Contains(t, text, c, "comments are kept")
		}
		data, err := config.ReadSettingsFile(settingsPath)
		require.NoError(t, err)
		tr := data["tracker"].(map[string]any)
		assert.Equal(t, "linear", tr["kind"])
		assert.Equal(t, "$LINEAR_API_KEY", tr["api_key"], "the $VAR reference moves as written")
		assert.Equal(t, []any{"Todo", "In Progress"}, tr["active_states"])
		assert.Equal(t, 90000, data["hooks"].(map[string]any)["timeout_ms"])
		assert.Equal(t, "manual", data["dependencies"].(map[string]any)["analysis_mode"])
		assert.Len(t, data["automations"], 1)
		assert.Equal(t, 4, data["agent"].(map[string]any)["max_turns"], "the file's own values stay")
		assert.Equal(t, 3, data["agent"].(map[string]any)["max_concurrent_agents"])

		assert.Equal(t, before, loadEffective(t, path), "no effective value changes")
		cfg, err := config.Load(path)
		require.NoError(t, err)
		assert.Empty(t, cfg.FrontMatter.Deprecated, "nothing deprecated is left")
		assert.Empty(t, cfg.FrontMatter.Conflicts)
		assert.Contains(t, out.String(), "moved")

		// A second run has nothing left to move and writes nothing.
		settingsBefore := readString(t, settingsPath)
		require.NoError(t, initUpdate(path, true, false, -1, time.Unix(0, 0), &out, &errOut))
		assert.Equal(t, settingsBefore, readString(t, settingsPath))
		assert.Equal(t, wf, readString(t, path))
	})

	t.Run("refuses to overwrite a backup without --force", func(t *testing.T) {
		path := writeMoveFixture(t, moveFixture)
		require.NoError(t, os.WriteFile(path+".bak", []byte("older backup"), 0o644))
		_, err := moveFrontMatterToSettings(path, false, false, false, nil)
		require.Error(t, err)
		assert.Equal(t, moveFixture, readString(t, path), "nothing written")
		assert.Equal(t, "older backup", readString(t, path+".bak"))
	})

	t.Run("dry run", func(t *testing.T) {
		path := writeMoveFixture(t, moveFixture)
		var out, errOut bytes.Buffer
		require.NoError(t, initUpdate(path, false, true, -1, time.Unix(0, 0), &out, &errOut))
		assert.Equal(t, moveFixture, readString(t, path), "--dry-run leaves WORKFLOW.md alone")
		for _, p := range []string{config.ProjectSettingsPath(path), path + ".bak", filepath.Join(filepath.Dir(path), ".itervox")} {
			_, err := os.Stat(p)
			assert.ErrorIs(t, err, os.ErrNotExist, "--dry-run writes nothing: %s", p)
		}
		printed := out.String()
		assert.Contains(t, printed, "nothing was written")
		assert.Regexp(t, `"kind":\s+"linear"`, printed, "the new settings file is printed")
		assert.Contains(t, printed, "// the team's tracker")
		assert.Contains(t, printed, "tracker.active_states")
		assert.NotContains(t, printed, "fixture-not-a-real-key", "a $VAR is never resolved into the output")
	})
}

// TestSecretsStayOutOfMigratedSettings (#239): a secret moves only as a $VAR
// reference — never its resolved value — and a key holding an inline secret
// (or a URL with a password) stays in WORKFLOW.md, reported by key, with the
// value printed nowhere. A settings save of an inline secret is refused.
func TestSecretsStayOutOfMigratedSettings(t *testing.T) {
	const inline = "inline-fixture-value-0000"
	const resolved = "resolved-fixture-value-1111"
	t.Setenv("ITERVOX_TEST_WEBHOOK_SECRET", resolved)
	t.Setenv(config.UserSettingsEnv, "off")
	content := `---
itervox_schema_version: 2
tracker:
  kind: github
  api_key: ` + inline + `
  project_slug: owner/repo
  webhooks:
    enabled: true
    secret: $ITERVOX_TEST_WEBHOOK_SECRET
workspace:
  clone_url: https://bot:` + inline + `@example.com/r.git
polling:
  interval_ms: 45000
---
Prompt.
`
	path := writeMoveFixture(t, content)
	before, err := config.Load(path)
	require.NoError(t, err)

	var out, errOut bytes.Buffer
	require.NoError(t, initUpdate(path, false, true, -1, time.Unix(0, 0), &out, &errOut))
	require.NoError(t, initUpdate(path, false, false, -1, time.Unix(0, 0), &out, &errOut))

	settings := readString(t, config.ProjectSettingsPath(path))
	assert.NotContains(t, settings, inline, "an inline secret never reaches the settings file")
	assert.NotContains(t, settings, resolved, "a $VAR is never resolved into the settings file")
	assert.Contains(t, settings, `"$ITERVOX_TEST_WEBHOOK_SECRET"`, "the reference moves as written")
	assert.Contains(t, settings, `"interval_ms": 45000`)
	assert.NotContains(t, out.String()+errOut.String(), inline, "the value is printed nowhere")
	assert.NotContains(t, out.String()+errOut.String(), resolved)
	assert.Contains(t, out.String(), "kept in "+path+": tracker.api_key: tracker.api_key holds an inline secret")
	assert.Contains(t, out.String(), "workspace.clone_url holds an inline secret")

	front, _ := splitWorkflowText(t, readString(t, path))
	assert.Equal(t, inline, front["tracker"].(map[string]any)["api_key"], "the inline secret stays in WORKFLOW.md")
	assert.NotContains(t, front["tracker"], "webhooks")
	after, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, before.Tracker.APIKey, after.Tracker.APIKey)
	assert.Equal(t, resolved, after.Tracker.Webhooks.Secret, "the reference still resolves from the environment")
	assert.Equal(t, before.Workspace.CloneURL, after.Workspace.CloneURL)
	assert.Equal(t, []string{"tracker.api_key", "workspace.clone_url"}, after.FrontMatter.Deprecated)
	assert.Contains(t, config.DeprecationMessage("tracker.api_key"), "$VAR reference")

	// The writer refuses an inline secret, and its error never quotes it.
	err = config.ValidateSettingValue("tracker.api_key", inline)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), inline)
	assert.NoError(t, config.ValidateSettingValue("tracker.api_key", "$LINEAR_API_KEY"))
	assert.NoError(t, config.ValidateSettingValue("tracker.webhooks.secret", ""))
}

func writeFileString(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}
