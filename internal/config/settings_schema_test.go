package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// schemaProbeWorkflow is a minimal workflow: every layered key the loader
// reads is then read at its default, so Explain lists it.
func schemaProbeWorkflow(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "WORKFLOW.md")
	require.NoError(t, os.WriteFile(path, []byte("---\nitervox_schema_version: 2\ntracker:\n  kind: memory\n---\nDo {{ issue.title }}\n"), 0o644))
	return path
}

func explainedLayered(t *testing.T) []Setting {
	t.Helper()
	settings, err := Explain(schemaProbeWorkflow(t))
	require.NoError(t, err)
	var out []Setting
	for _, s := range settings {
		if inLayeredSection(s.Key) {
			out = append(out, s)
		}
	}
	require.NotEmpty(t, out)
	return out
}

// TestSettingsSchemaCoversEveryLayeredField (#158): every key the loader reads
// in a layered section (agent, polling, server, workspace) has a SettingFields
// entry, so it is in the published schema and on the Settings page. A key
// the loader lists as a map whose keys are themselves registered
// (agent.sort, agent.budget) is covered by them.
func TestSettingsSchemaCoversEveryLayeredField(t *testing.T) {
	read := map[string]bool{}
	for _, s := range explainedLayered(t) {
		read[s.Key] = true
	}
	// Read by hand, so Explain lists them only when a layer sets them.
	for _, k := range readByHand {
		if inLayeredSection(k) {
			read[k] = true
		}
	}
	for k := range effectiveValues {
		if inLayeredSection(k) {
			read[k] = true
		}
	}
	var missing []string
	for key := range read {
		if _, ok := SettingFieldFor(key); ok {
			continue
		}
		if slices.ContainsFunc(SettingFields, func(f SettingField) bool { return strings.HasPrefix(f.Key, key+".") }) {
			continue
		}
		missing = append(missing, key)
	}
	slices.Sort(missing)
	assert.Empty(t, missing, "layered settings with no schema entry: add them to SettingFields (internal/config/settings_fields.go), then run `make settings-schema`")
}

// TestSettingsSchemaHasNoUnreadEntries (#158): every SettingFields entry is a
// key the loader reads, so the schema never offers a setting that does
// nothing.
func TestSettingsSchemaHasNoUnreadEntries(t *testing.T) {
	values := map[string]any{}
	for _, s := range explainedLayered(t) {
		values[s.Key] = s.Value
	}
	for _, f := range SettingFields {
		assert.True(t, coveredByRead(values, f.Key), "%s is in SettingFields but the loader does not read it", f.Key)
		assert.True(t, inLayeredSection(f.Key), "%s is not in a layered section", f.Key)
	}
}

// TestSettingsSchemaDefaultsMatchLoader (#158): a listed default is the value
// the loader uses when no layer sets the key.
func TestSettingsSchemaDefaultsMatchLoader(t *testing.T) {
	values := map[string]any{}
	for _, s := range explainedLayered(t) {
		values[s.Key] = s.Value
	}
	for _, f := range SettingFields {
		if f.Default == nil {
			continue
		}
		got, ok := values[f.Key]
		if !assert.True(t, ok, "%s has a default but the loader did not list it", f.Key) {
			continue
		}
		want, err := json.Marshal(f.Default)
		require.NoError(t, err)
		have, err := json.Marshal(got)
		require.NoError(t, err)
		assert.JSONEq(t, string(want), string(have), "default of %s", f.Key)
		assert.NoError(t, ValidateSettingValue(f.Key, normalizeJSON(mustRoundTrip(t, f.Default))), "default of %s fails its own type", f.Key)
	}
}

func mustRoundTrip(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	out, err := ParseJSONC(b)
	require.NoError(t, err)
	return out
}

// TestSettingsSchemaPublishedFileIsCurrent (#158): the published schema is
// what SettingsSchemaJSON generates now. Regenerate with `make settings-schema`.
func TestSettingsSchemaPublishedFileIsCurrent(t *testing.T) {
	published, err := os.ReadFile(filepath.Join("..", "..", "site", "public", "schema", "settings.schema.json"))
	require.NoError(t, err, "run `make settings-schema`")
	want, err := SettingsSchemaJSON()
	require.NoError(t, err)
	assert.Equal(t, string(want), string(published), "site/public/schema/settings.schema.json is stale: run `make settings-schema`")
}

// TestSettingsSchemaShape (#158): the schema nests every field under its
// section and carries the Settings page's metadata.
func TestSettingsSchemaShape(t *testing.T) {
	s := SettingsSchema()
	assert.Equal(t, SettingsSchemaURL, s["$id"])
	props := s["properties"].(map[string]any)
	for _, section := range LayeredSections {
		require.Contains(t, props, section)
	}
	agent := props["agent"].(map[string]any)["properties"].(map[string]any)
	turns := agent["max_concurrent_agents"].(map[string]any)
	assert.Equal(t, TypeInteger, turns["type"])
	assert.Equal(t, true, turns["x-itervox-live"])
	assert.Equal(t, "Max concurrent agents", turns["title"])
	budget := agent["budget"].(map[string]any)["properties"].(map[string]any)
	assert.Contains(t, budget, "tokens")
	assert.Equal(t, []string{"round-robin", "least-loaded"}, agent["dispatch_strategy"].(map[string]any)["enum"])
}

func TestValidateSettingValue(t *testing.T) {
	assert.NoError(t, ValidateSettingValue("agent.max_turns", 4))
	assert.NoError(t, ValidateSettingValue("agent.max_turns", nil))
	assert.Error(t, ValidateSettingValue("agent.max_turns", "four"))
	assert.Error(t, ValidateSettingValue("agent.max_turns", 0), "below the minimum")
	assert.Error(t, ValidateSettingValue("agent.max_turns", 1.5))
	assert.Error(t, ValidateSettingValue("agent.nope", 1))
	assert.Error(t, ValidateSettingValue("agent.dispatch_strategy", "fastest"))
	assert.NoError(t, ValidateSettingValue("agent.ssh_hosts", []any{"a", "b"}))
	assert.Error(t, ValidateSettingValue("agent.ssh_hosts", []any{"a", 2}))
	assert.NoError(t, ValidateSettingValue("agent.max_concurrent_agents_by_state", map[string]any{"todo": 2}))
	assert.Error(t, ValidateSettingValue("agent.max_concurrent_agents_by_state", map[string]any{"todo": "x"}))
}
