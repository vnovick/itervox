package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

// configProject writes a minimal schema-2 workflow and points the user layer
// at a file in the test's temp dir.
func configProject(t *testing.T, workflowExtras, user string) (workflow, userPath string) {
	t.Helper()
	dir := t.TempDir()
	workflow = filepath.Join(dir, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(workflow, []byte("---\nitervox_schema_version: 2\ntracker:\n  kind: linear\n  api_key: lin_secret_value\n  project_slug: p\n"+
		workflowExtras+"---\n\nPrompt.\n"), 0o644))
	userPath = filepath.Join(dir, "user-settings.jsonc")
	t.Setenv(config.UserSettingsEnv, userPath)
	if user != "" {
		require.NoError(t, os.WriteFile(userPath, []byte(user), 0o644))
	}
	return workflow, userPath
}

func TestConfigShowPrintsEachValueWithItsLayer(t *testing.T) {
	wf, userPath := configProject(t, "agent:\n  max_turns: 2\n", `{"agent": {"max_concurrent_agents": 7}}`)
	var out, errOut bytes.Buffer
	require.Equal(t, 0, configCmd([]string{"show", "--workflow", wf}, &out, &errOut), errOut.String())
	text := out.String()
	assert.Regexp(t, `agent\.max_turns\s+2\s+workflow\n`, text)
	assert.Regexp(t, `agent\.max_concurrent_agents\s+7\s+user \(`+regexp.QuoteMeta(userPath)+`\)\n`, text)
	assert.Regexp(t, `agent\.max_retries\s+5\s+default\n`, text)
	assert.NotContains(t, text, "lin_secret_value", "the tracker key is never printed")

	out.Reset()
	require.Equal(t, 0, configCmd([]string{"show", "--json", "--workflow", wf}, &out, &errOut))
	var settings []config.Setting
	require.NoError(t, json.Unmarshal(out.Bytes(), &settings))
	assert.NotEmpty(t, settings)
	assert.NotContains(t, out.String(), "lin_secret_value")
}

func TestConfigSetWritesTheProjectFileAndWarnsWhenAHigherLayerWins(t *testing.T) {
	wf, userPath := configProject(t, "agent:\n  max_turns: 2\n", "")
	var out, errOut bytes.Buffer
	require.Equal(t, 0, configCmd([]string{"set", "agent.max_turns", "4", "--workflow", wf}, &out, &errOut), errOut.String())
	data, err := config.ReadSettingsFile(config.ProjectSettingsPath(wf))
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"agent": map[string]any{"max_turns": 4}}, data)
	// #239: the settings file wins over WORKFLOW.md now; the copy left there is named.
	assert.Contains(t, errOut.String(), "WORKFLOW.md's front matter also sets agent.max_turns; this file wins over it")
	cfg, err := config.Load(wf)
	require.NoError(t, err)
	assert.Equal(t, 4, cfg.Agent.MaxTurns)

	out.Reset()
	errOut.Reset()
	require.Equal(t, 0, configCmd([]string{"set", "--user", "polling.interval_ms", "9000", "--workflow", wf}, &out, &errOut), errOut.String())
	data, err = config.ReadSettingsFile(userPath)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"polling": map[string]any{"interval_ms": 9000}}, data)
	assert.Empty(t, errOut.String(), "nothing above the user layer sets it")
	cfg, err = config.Load(wf)
	require.NoError(t, err)
	assert.Equal(t, 9000, cfg.Polling.IntervalMs)
}

func TestConfigSetRefusesAWrongTypeAndChangesNothing(t *testing.T) {
	wf, _ := configProject(t, "", "")
	project := config.ProjectSettingsPath(wf)
	require.NoError(t, os.MkdirAll(filepath.Dir(project), 0o755))
	original := "// mine\n{\"agent\": {\"max_turns\": 3}}\n"
	require.NoError(t, os.WriteFile(project, []byte(original), 0o644))

	var out, errOut bytes.Buffer
	assert.Equal(t, 1, configCmd([]string{"set", "agent.max_turns", "lots", "--workflow", wf}, &out, &errOut))
	assert.Contains(t, errOut.String(), "agent.max_turns takes a number")
	got, err := os.ReadFile(project)
	require.NoError(t, err)
	assert.Equal(t, original, string(got), "the file is restored byte for byte")

	errOut.Reset()
	assert.Equal(t, 1, configCmd([]string{"set", "server.port", `"abc"`, "--workflow", wf}, &out, &errOut),
		"a key the parser reads by hand is type-checked too")
	assert.Contains(t, errOut.String(), "server.port takes a number")
	got, err = os.ReadFile(project)
	require.NoError(t, err)
	assert.Equal(t, original, string(got))

	errOut.Reset()
	// #239: tracker is layered now; guardrails is not.
	assert.Equal(t, 1, configCmd([]string{"set", "guardrails.enabled", "false", "--workflow", wf}, &out, &errOut))
	assert.Contains(t, errOut.String(), "cannot be set in a settings file")

	errOut.Reset()
	assert.Equal(t, 0, configCmd([]string{"set", "agent.max_turn", "4", "--workflow", wf}, &out, &errOut))
	assert.Contains(t, errOut.String(), "Itervox does not read agent.max_turn")
}

func TestConfigUnsetReportsTheValueNowInEffect(t *testing.T) {
	wf, _ := configProject(t, "", `{"agent": {"max_turns": 11}}`)
	var out, errOut bytes.Buffer
	require.Equal(t, 0, configCmd([]string{"set", "agent.max_turns", "4", "--workflow", wf}, &out, &errOut), errOut.String())
	out.Reset()
	require.Equal(t, 0, configCmd([]string{"unset", "agent.max_turns", "--workflow", wf}, &out, &errOut), errOut.String())
	assert.Contains(t, out.String(), "agent.max_turns is now 11 (user layer)")
}

func TestConfigUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	assert.Equal(t, 2, configCmd(nil, &out, &errOut))
	assert.Equal(t, 2, configCmd([]string{"set", "agent.max_turns"}, &out, &errOut))
	assert.Contains(t, errOut.String(), "usage: itervox config show")
}

// TestConfigSetLocalLayerWinsOverProject (#158): --local writes the
// gitignored .itervox/settings.local.jsonc, which wins over the project file.
func TestConfigSetLocalLayerWinsOverProject(t *testing.T) {
	wf, _ := configProject(t, "", "")
	var out, errOut bytes.Buffer
	require.Equal(t, 0, configCmd([]string{"set", "agent.max_turns", "4", "--workflow", wf}, &out, &errOut), errOut.String())
	require.Equal(t, 0, configCmd([]string{"set", "--local", "agent.max_turns", "6", "--workflow", wf}, &out, &errOut), errOut.String())
	data, err := config.ReadSettingsFile(config.LocalSettingsPath(wf))
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"agent": map[string]any{"max_turns": 6}}, data)
	cfg, err := config.Load(wf)
	require.NoError(t, err)
	assert.Equal(t, 6, cfg.Agent.MaxTurns, "the local layer wins over the project layer")
	ignore, err := os.ReadFile(filepath.Join(filepath.Dir(wf), ".itervox", ".gitignore"))
	require.NoError(t, err)
	assert.Contains(t, string(ignore), "/settings.local.jsonc")

	errOut.Reset()
	require.Equal(t, 0, configCmd([]string{"set", "agent.max_turns", "8", "--workflow", wf}, &out, &errOut))
	assert.Contains(t, errOut.String(), "the local layer also sets agent.max_turns (6), and it wins")
	assert.Equal(t, 2, configCmd([]string{"set", "--local", "--user", "agent.max_turns", "1", "--workflow", wf}, &out, &errOut))
}

// TestConfigSchemaPrintsPublishedSchema (#158): `itervox config schema`
// prints the generated settings schema, the bytes `make settings-schema`
// publishes.
func TestConfigSchemaPrintsPublishedSchema(t *testing.T) {
	var out, errOut bytes.Buffer
	require.Equal(t, 0, configCmd([]string{"schema"}, &out, &errOut), errOut.String())
	published, err := os.ReadFile(filepath.Join("..", "..", "site", "public", "schema", "settings.schema.json"))
	require.NoError(t, err)
	assert.Equal(t, string(published), out.String())
}
