package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

// Quota pools: agent.profiles.<name>.pool and backend/pool lanes in
// agent.backend_fallback.

// TestProfilePoolParses is the roundtrip for pool: set on one profile, absent
// on another, through both profile parse paths (schema 2 and legacy).
func TestProfilePoolParses(t *testing.T) {
	t.Run("schema 2", func(t *testing.T) {
		cfg, err := config.Load(workflowWithContent(t, minimalV2WithProfileFiles(t,
			"agent:\n"+
				"  profiles:\n"+
				"    coder-local:\n"+
				"      command: \"ANTHROPIC_BASE_URL=http://127.0.0.1:8000 claude\"\n"+
				"      pool: local\n"+
				"    coder:\n"+
				"      command: claude\n")))
		require.NoError(t, err)
		local := cfg.Agent.Profiles["coder-local"]
		assert.Equal(t, "local", local.Pool, "an explicit pool reaches the profile")
		assert.Empty(t, cfg.Agent.Profiles["coder"].Pool, "an absent pool stays empty: the backend's own quota")
		assert.Equal(t, "claude", config.ProfileRunsBackend(local, cfg.Agent.Command), "a pooled profile still runs the claude backend")
		assert.Equal(t, "claude/local", config.ProfileRunsLane(local, cfg.Agent.Command))
		assert.Equal(t, "claude", config.ProfileRunsLane(cfg.Agent.Profiles["coder"], cfg.Agent.Command))
	})
	t.Run("legacy schema", func(t *testing.T) {
		cfg, err := config.Load(workflowWithContent(t, minimal(
			"agent:\n"+
				"  profiles:\n"+
				"    coder-local:\n"+
				"      command: claude\n"+
				"      pool: local-2\n"+
				"    coder:\n"+
				"      command: claude\n")))
		require.NoError(t, err)
		assert.Equal(t, "local-2", cfg.Agent.Profiles["coder-local"].Pool)
		assert.Empty(t, cfg.Agent.Profiles["coder"].Pool)
	})
	t.Run("invalid pool fails load", func(t *testing.T) {
		for _, bad := range []string{`"Local"`, `"local/x"`, `"a b"`, `"claude@x"`, `[local]`} {
			_, err := config.Load(workflowWithContent(t, minimal(
				"agent:\n"+
					"  profiles:\n"+
					"    coder-local:\n"+
					"      command: claude\n"+
					"      pool: "+bad+"\n")))
			require.Error(t, err, "pool %s", bad)
			assert.Contains(t, err.Error(), "agent.profiles.coder-local.pool", "pool %s", bad)
		}
	})
	t.Run("pooled chain and profile_map through load", func(t *testing.T) {
		cfg, err := config.Load(workflowWithContent(t, minimal(
			"agent:\n"+
				"  profiles:\n"+
				"    coder:\n"+
				"      command: claude\n"+
				"    coder-codex:\n"+
				"      command: codex\n"+
				"    coder-local:\n"+
				"      command: \"ANTHROPIC_BASE_URL=http://127.0.0.1:8000 claude\"\n"+
				"      pool: local\n"+
				"  backend_fallback:\n"+
				"    chain: [claude, codex, claude/local]\n"+
				"    profile_map:\n"+
				"      coder:\n"+
				"        codex: coder-codex\n"+
				"        \"claude/local\": coder-local\n")))
		require.NoError(t, err)
		fb := cfg.Agent.BackendFallback
		assert.Equal(t, []string{"claude", "codex", "claude/local"}, fb.Chain)
		assert.Equal(t, "coder-local", fb.Lookup("coder", "claude/local"))
	})
}

func TestBackendLaneHelpers(t *testing.T) {
	assert.Equal(t, "claude", config.BackendLane("claude", ""))
	assert.Equal(t, "claude/local", config.BackendLane("claude", "local"))
	b, p := config.SplitBackendLane("claude/local")
	assert.Equal(t, [2]string{"claude", "local"}, [2]string{b, p})
	b, p = config.SplitBackendLane("codex")
	assert.Equal(t, [2]string{"codex", ""}, [2]string{b, p})
	require.NoError(t, config.ValidateBackendLane("claude"))
	require.NoError(t, config.ValidateBackendLane("claude/local-1"))
	for _, bad := range []string{"", "gemini", "gemini/local", "claude/", "claude/Local", "claude/a/b", "claude/a_b"} {
		assert.Error(t, config.ValidateBackendLane(bad), bad)
	}
}

// TestBackendFallbackValidatesPoolEntries: chain and profile_map entries
// may name backend/pool lanes; an unknown backend, a bad pool name, and a
// map target that does not run the key's pool are rejected.
func TestBackendFallbackValidatesPoolEntries(t *testing.T) {
	profiles := map[string]config.AgentProfile{
		"coder":       {Command: "claude"},
		"coder-codex": {Command: "codex"},
		"coder-local": {Command: "ANTHROPIC_BASE_URL=http://127.0.0.1:8000 claude", Pool: "local"},
		"coder-other": {Command: "claude", Pool: "other"},
		"codex-local": {Command: "codex", Pool: "local"},
	}
	base := func(mut func(*config.BackendFallbackConfig)) config.BackendFallbackConfig {
		fb := config.BackendFallbackConfig{
			Enabled: true,
			Chain:   []string{"claude", "codex", "claude/local"},
			ProfileMap: map[string]map[string]string{"coder": {
				"codex": "coder-codex", "claude/local": "coder-local",
			}},
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
		wantErr string
	}{
		{"valid pooled chain and map", base(nil), ""},
		{"pooled source maps back to the plain backend", base(func(fb *config.BackendFallbackConfig) {
			fb.ProfileMap["coder-local"] = map[string]string{"claude": "coder"}
		}), ""},
		{"same backend in two pools is not a duplicate", base(func(fb *config.BackendFallbackConfig) {
			fb.Chain = []string{"claude", "claude/local", "claude/other"}
		}), ""},
		{"unknown backend in chain lane", base(func(fb *config.BackendFallbackConfig) {
			fb.Chain = []string{"claude", "gemini/local"}
		}), "unknown backend \"gemini\""},
		{"bad pool in chain", base(func(fb *config.BackendFallbackConfig) {
			fb.Chain = []string{"claude", "claude/Local"}
		}), "must match [a-z0-9-]+"},
		{"empty pool in chain", base(func(fb *config.BackendFallbackConfig) {
			fb.Chain = []string{"claude", "claude/"}
		}), "must match [a-z0-9-]+"},
		{"duplicate pooled chain step", base(func(fb *config.BackendFallbackConfig) {
			fb.Chain = []string{"claude/local", "claude/local"}
		}), "listed twice"},
		{"unknown backend in map key", base(func(fb *config.BackendFallbackConfig) {
			fb.ProfileMap["coder"] = map[string]string{"gemini/local": "coder-local"}
		}), "backend key \"gemini/local\""},
		{"bad pool in map key", base(func(fb *config.BackendFallbackConfig) {
			fb.ProfileMap["coder"] = map[string]string{"claude/lo cal": "coder-local"}
		}), "backend key \"claude/lo cal\""},
		{"pooled key, target without that pool", base(func(fb *config.BackendFallbackConfig) {
			fb.ProfileMap["coder-codex"] = map[string]string{"claude/local": "coder"}
		}), "profile \"coder\" runs \"claude\", not \"claude/local\""},
		{"pooled key, target in another pool", base(func(fb *config.BackendFallbackConfig) {
			fb.ProfileMap["coder"] = map[string]string{"claude/local": "coder-other"}
		}), "profile \"coder-other\" runs \"claude/other\", not \"claude/local\""},
		{"plain key, pooled target", base(func(fb *config.BackendFallbackConfig) {
			fb.ProfileMap["coder-codex"] = map[string]string{"claude": "coder-local"}
		}), "profile \"coder-local\" runs \"claude/local\", not \"claude\""},
		{"pooled key, target on another backend", base(func(fb *config.BackendFallbackConfig) {
			fb.ProfileMap["coder"] = map[string]string{"claude/local": "codex-local"}
		}), "profile \"codex-local\" runs \"codex\", not \"claude\""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := config.ValidateBackendFallback(tc.fb, profiles, "claude")
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
