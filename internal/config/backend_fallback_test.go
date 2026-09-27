package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/config"
)

// CORE-054 — agent.backend_fallback parse + validation.

func TestBackendFallback_ParseRoundtripAndDefaults(t *testing.T) {
	t.Run("absent block keeps fallback off with defaults", func(t *testing.T) {
		cfg, err := config.Load(workflowWithContent(t, minimal("")))
		require.NoError(t, err)
		fb := cfg.Agent.BackendFallback
		assert.False(t, fb.Enabled)
		assert.Equal(t, []string{"claude", "codex"}, fb.Chain)
		assert.Equal(t, 15, fb.DefaultCooldownMinutes)
		assert.Equal(t, 30, fb.MinDwellMinutes)
		assert.Equal(t, config.BackendFallbackSwitchBackAtReset, fb.SwitchBack)
		assert.Equal(t, config.BackendFallbackUnmappedHold, fb.OnUnmapped)
	})
	t.Run("present block parses every key", func(t *testing.T) {
		cfg, err := config.Load(workflowWithContent(t, minimal(
			"agent:\n"+
				"  backend_fallback:\n"+
				"    chain: [codex, claude]\n"+
				"    profile_map:\n"+
				"      coder:\n"+
				"        codex: coder-codex\n"+
				"      default:\n"+
				"        codex: coder-codex\n"+
				"    on_unmapped: backend_hint\n"+
				"    default_cooldown_minutes: 20\n"+
				"    min_dwell_minutes: 45\n"+
				"    switch_back: on_success\n")))
		require.NoError(t, err)
		fb := cfg.Agent.BackendFallback
		assert.True(t, fb.Enabled, "a present block is enabled unless enabled: false")
		assert.Equal(t, []string{"codex", "claude"}, fb.Chain)
		assert.Equal(t, "coder-codex", fb.Lookup("coder", "codex"))
		assert.Equal(t, "coder-codex", fb.Lookup("", "codex"), "the default key maps agent.command runs")
		assert.Equal(t, config.BackendFallbackUnmappedBackendHint, fb.OnUnmapped)
		assert.Equal(t, 20, fb.DefaultCooldownMinutes)
		assert.Equal(t, 45, fb.MinDwellMinutes)
		assert.Equal(t, config.BackendFallbackSwitchBackOnSuccess, fb.SwitchBack)
	})
	t.Run("enabled false", func(t *testing.T) {
		cfg, err := config.Load(workflowWithContent(t, minimal(
			"agent:\n  backend_fallback:\n    enabled: false\n")))
		require.NoError(t, err)
		assert.False(t, cfg.Agent.BackendFallback.Enabled)
	})
}

func TestValidate_BackendFallbackProfileMap(t *testing.T) {
	disabled := false
	profiles := map[string]config.AgentProfile{
		"coder":        {Command: "claude --model opus"},
		"coder-codex":  {Command: "codex --model gpt-5"},
		"coder-off":    {Command: "codex", Enabled: &disabled},
		"wrapper":      {Command: "./run.sh"},
		"wrapper-hint": {Command: "./run.sh", Backend: "codex"},
		"inherits":     {},
	}
	base := func(mut func(*config.BackendFallbackConfig)) config.BackendFallbackConfig {
		fb := config.BackendFallbackConfig{
			Enabled:                true,
			Chain:                  []string{"claude", "codex"},
			ProfileMap:             map[string]map[string]string{"coder": {"codex": "coder-codex"}},
			OnUnmapped:             config.BackendFallbackUnmappedHold,
			DefaultCooldownMinutes: 15,
			MinDwellMinutes:        30,
			SwitchBack:             config.BackendFallbackSwitchBackAtReset,
		}
		if mut != nil {
			mut(&fb)
		}
		return fb
	}
	tests := []struct {
		name    string
		fb      config.BackendFallbackConfig
		defCmd  string
		wantErr string
	}{
		{"valid", base(nil), "claude", ""},
		{"default key and wrapper with backend", base(func(fb *config.BackendFallbackConfig) {
			fb.ProfileMap["default"] = map[string]string{"codex": "wrapper-hint"}
		}), "claude", ""},
		{"inherited command matches", base(func(fb *config.BackendFallbackConfig) {
			fb.ProfileMap["coder"] = map[string]string{"codex": "inherits"}
		}), "codex", ""},
		{"disabled block is not validated", base(func(fb *config.BackendFallbackConfig) {
			fb.Enabled = false
			fb.ProfileMap["coder"] = map[string]string{"codex": "nope"}
		}), "claude", ""},
		{"unknown target", base(func(fb *config.BackendFallbackConfig) {
			fb.ProfileMap["coder"] = map[string]string{"codex": "nope"}
		}), "claude", "unknown profile \"nope\""},
		{"disabled target", base(func(fb *config.BackendFallbackConfig) {
			fb.ProfileMap["coder"] = map[string]string{"codex": "coder-off"}
		}), "claude", "disabled profile \"coder-off\""},
		{"wrong-backend target", base(func(fb *config.BackendFallbackConfig) {
			fb.ProfileMap["coder"] = map[string]string{"codex": "coder"}
		}), "claude", "runs \"claude\", not \"codex\""},
		{"inherited wrong backend", base(func(fb *config.BackendFallbackConfig) {
			fb.ProfileMap["coder"] = map[string]string{"codex": "inherits"}
		}), "claude", "runs \"claude\", not \"codex\""},
		{"unrecognisable wrapper without backend", base(func(fb *config.BackendFallbackConfig) {
			fb.ProfileMap["coder"] = map[string]string{"codex": "wrapper"}
		}), "claude", "set backend"},
		{"unknown source profile", base(func(fb *config.BackendFallbackConfig) {
			fb.ProfileMap["ghost"] = map[string]string{"codex": "coder-codex"}
		}), "claude", "unknown source profile \"ghost\""},
		{"unknown backend key", base(func(fb *config.BackendFallbackConfig) {
			fb.ProfileMap["coder"] = map[string]string{"gemini": "coder-codex"}
		}), "claude", "backend key \"gemini\""},
		{"unknown chain backend", base(func(fb *config.BackendFallbackConfig) {
			fb.Chain = []string{"claude", "gemini"}
		}), "claude", "chain"},
		{"duplicate chain backend", base(func(fb *config.BackendFallbackConfig) {
			fb.Chain = []string{"claude", "claude"}
		}), "claude", "chain"},
		{"bad switch_back", base(func(fb *config.BackendFallbackConfig) {
			fb.SwitchBack = "later"
		}), "claude", "switch_back"},
		{"bad on_unmapped", base(func(fb *config.BackendFallbackConfig) {
			fb.OnUnmapped = "pause"
		}), "claude", "on_unmapped"},
		{"negative dwell", base(func(fb *config.BackendFallbackConfig) {
			fb.MinDwellMinutes = -1
		}), "claude", "min_dwell_minutes"},
		{"non-positive cooldown", base(func(fb *config.BackendFallbackConfig) {
			fb.DefaultCooldownMinutes = 0
		}), "claude", "default_cooldown_minutes"},
		{"cooldown longer than a day", base(func(fb *config.BackendFallbackConfig) {
			fb.DefaultCooldownMinutes = 1441
		}), "claude", "default_cooldown_minutes"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := config.ValidateBackendFallback(tc.fb, profiles, tc.defCmd)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), "backend_fallback")
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestProfileRunsBackend_BinaryWinsOverBackendField(t *testing.T) {
	assert.Equal(t, "claude", config.ProfileRunsBackend(config.AgentProfile{Command: "claude", Backend: "codex"}, ""))
	assert.Equal(t, "codex", config.ProfileRunsBackend(config.AgentProfile{Command: "./w.sh", Backend: "codex"}, ""))
	assert.Equal(t, "codex", config.ProfileRunsBackend(config.AgentProfile{}, "codex --m x"))
	assert.Equal(t, "", config.ProfileRunsBackend(config.AgentProfile{Command: "./w.sh"}, "claude"))
}

// M3-close BH-M3-1: a scalar or mistyped agent.backend_fallback used to
// silently ENABLE fallback with the default chain.
func TestBackendFallback_NonMapShapesThroughLoad(t *testing.T) {
	for _, tc := range []struct {
		name, yaml  string
		wantEnabled bool
		wantErr     string
	}{
		{"scalar false disables", "  backend_fallback: false\n", false, ""},
		{"scalar true enables defaults", "  backend_fallback: true\n", true, ""},
		{"null keeps it off", "  backend_fallback:\n", false, ""},
		{"string off is rejected", "  backend_fallback: \"off\"\n", false, "must be a map or a boolean"},
		{"number is rejected", "  backend_fallback: 0\n", false, "must be a map or a boolean"},
		{"list is rejected", "  backend_fallback: [claude, codex]\n", false, "must be a map or a boolean"},
		{"string enabled is rejected", "  backend_fallback:\n    enabled: \"false\"\n", false, "enabled must be a boolean"},
		{"map enabled false", "  backend_fallback:\n    enabled: false\n", false, ""},
		{"string chain is rejected", "  backend_fallback:\n    chain: claude\n", false, "chain must be a list"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Load(workflowWithContent(t, minimal("agent:\n"+tc.yaml)))
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "agent.backend_fallback")
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantEnabled, cfg.Agent.BackendFallback.Enabled)
		})
	}
}
