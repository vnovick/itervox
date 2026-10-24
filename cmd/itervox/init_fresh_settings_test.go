package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
)

// explainedValues maps every setting Explain lists to its value.
func explainValues(t *testing.T, path string) map[string]any {
	t.Helper()
	settings, err := config.Explain(path)
	require.NoError(t, err)
	out := map[string]any{}
	for _, s := range settings {
		out[s.Key] = s.Value
	}
	return out
}

// TestFreshInitHasNoDeprecatedFrontMatter (#239): `itervox init` — the
// standard scaffold, every --template, the cross-review preset, and the same
// scaffold `itervox quickstart` writes — puts the layered sections in
// .itervox/settings.jsonc and leaves WORKFLOW.md with the schema marker, the
// non-layered sections, the profiles and the prompt. The new project loads
// with no deprecated front-matter key and no conflict, its secrets are $VAR
// references, and its effective configuration is the one the old init (all
// in the front matter) produced.
func TestFreshInitHasNoDeprecatedFrontMatter(t *testing.T) {
	t.Setenv(config.UserSettingsEnv, "off")
	origClaude, origCodex := listClaudeModels, listCodexModels
	listClaudeModels = func() []agent.ModelOption { return []agent.ModelOption{{ID: "model-a", Label: "A"}} }
	listCodexModels = func() []agent.ModelOption { return nil }
	t.Cleanup(func() { listClaudeModels, listCodexModels = origClaude, origCodex })

	type variant struct {
		name string
		opts initOptions
	}
	variants := []variant{{"standard", initOptions{}}, {"cross-review", initOptions{Preset: presetCrossReview}}}
	for _, tmpl := range KnownTemplateNames() {
		variants = append(variants, variant{tmpl, initOptions{Template: tmpl}})
	}
	for _, tracker := range []string{"linear", "github", "local"} {
		for _, v := range variants {
			t.Run(tracker+"/"+v.name, func(t *testing.T) {
				dir := t.TempDir()
				path := filepath.Join(dir, "WORKFLOW.md")
				require.NoError(t, scaffoldWorkflow(path, dir, tracker, "claude", v.opts, io.Discard))

				cfg, err := config.Load(path)
				require.NoError(t, err)
				assert.Empty(t, cfg.FrontMatter.Deprecated, "a fresh project starts with nothing deprecated")
				assert.Empty(t, cfg.FrontMatter.Conflicts)
				front, body := splitWorkflowText(t, readString(t, path))
				for key := range front {
					assert.Contains(t, []string{"itervox_schema_version", "agent", "guardrails", "memory", "sessions", "pr_events"}, key, "WORKFLOW.md keeps no layered section but the profiles")
				}
				if a, ok := front["agent"].(map[string]any); ok {
					for key := range a {
						assert.True(t, config.KeptInFrontMatter("agent."+key), "agent.%s left the front matter", key)
					}
				}
				assert.NotEmpty(t, body, "the prompt stays the body")
				settings := readString(t, config.ProjectSettingsPath(path))
				assert.Contains(t, settings, `"tracker"`)
				if tracker != "local" {
					assert.Regexp(t, `"api_key":\s+"\$[A-Z_]+"`, settings, "the tracker key is a $VAR reference")
				}
				assert.False(t, settingsTextHasInlineSecret([]byte(settings)), "no inline secret in the settings file")
				_, err = os.Stat(path + ".bak")
				assert.ErrorIs(t, err, os.ErrNotExist, "a fresh init has nothing to back up")
				newValues := explainValues(t, path)

				// The old init: the same scaffold, all in the front matter.
				require.NoError(t, os.Remove(config.ProjectSettingsPath(path)))
				info := scanRepo(dir)
				info.ClaudeModels, info.CodexModels = listClaudeModels(), listCodexModels()
				require.NoError(t, os.WriteFile(path, []byte(generateWorkflowWith(tracker, "claude", info, path, v.opts)), 0o644))
				old, err := config.Load(path)
				require.NoError(t, err)
				assert.NotEmpty(t, old.FrontMatter.Deprecated, "the old layout is what #239 deprecates")
				assert.Equal(t, explainValues(t, path), newValues, "the same effective configuration")
				assert.True(t, strings.HasPrefix(readString(t, path), "---\n"))
			})
		}
	}
}
