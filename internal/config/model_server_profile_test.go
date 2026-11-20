package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

// Local models: agent.profiles.<name>.model_server, its quota pool and the
// per-profile timeouts.

func loadProfiles(t *testing.T, profiles string) (*config.Config, error) {
	t.Helper()
	return config.Load(workflowWithContent(t, minimalV2WithProfileFiles(t, "agent:\n  profiles:\n"+profiles)))
}

// TestProfileModelServerRoundtrip: present, absent, $VAR (set and unset),
// a malformed URL, and a conflict with the command's own assignment.
func TestProfileModelServerRoundtrip(t *testing.T) {
	t.Run("present and absent", func(t *testing.T) {
		cfg, err := loadProfiles(t,
			"    local:\n      command: claude\n      model_server: http://127.0.0.1:8000\n"+
				"    coder:\n      command: claude\n")
		require.NoError(t, err)
		local := cfg.Agent.Profiles["local"]
		assert.Equal(t, "http://127.0.0.1:8000", local.ModelServer)
		assert.Equal(t, "http://127.0.0.1:8000", local.ModelServerURL)
		coder := cfg.Agent.Profiles["coder"]
		assert.Empty(t, coder.ModelServer)
		assert.Empty(t, coder.ModelServerURL)
		assert.Empty(t, coder.Pool)
		assert.False(t, coder.PoolAuto)
	})
	t.Run("$VAR resolves from the environment", func(t *testing.T) {
		t.Setenv("ITERVOX_TEST_MODEL_SERVER", "http://localhost:8080/")
		cfg, err := loadProfiles(t,
			"    local:\n      command: codex\n      model_server: $ITERVOX_TEST_MODEL_SERVER\n")
		require.NoError(t, err)
		local := cfg.Agent.Profiles["local"]
		assert.Equal(t, "$ITERVOX_TEST_MODEL_SERVER", local.ModelServer, "kept as written, for the save path")
		assert.Equal(t, "http://localhost:8080/", local.ModelServerURL)
	})
	t.Run("$VAR unset is a load error", func(t *testing.T) {
		t.Setenv("ITERVOX_TEST_MODEL_SERVER_UNSET", "")
		_, err := loadProfiles(t,
			"    local:\n      command: claude\n      model_server: $ITERVOX_TEST_MODEL_SERVER_UNSET\n")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "agent.profiles.local.model_server")
		assert.Contains(t, err.Error(), "is not set")
	})
	t.Run("malformed values fail load", func(t *testing.T) {
		for _, bad := range []string{"127.0.0.1:8000", "ftp://host", "http://", "http://$HOST:8000", "[x]"} {
			_, err := loadProfiles(t, "    local:\n      command: claude\n      model_server: \""+bad+"\"\n")
			require.Error(t, err, "model_server %s", bad)
			assert.Contains(t, err.Error(), "agent.profiles.local.model_server", "model_server %s", bad)
		}
	})
	t.Run("conflict with the command is a load error", func(t *testing.T) {
		_, err := loadProfiles(t,
			"    local:\n      command: \"ANTHROPIC_BASE_URL=http://127.0.0.1:9000 claude\"\n      model_server: http://127.0.0.1:8000\n")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "conflicts with ANTHROPIC_BASE_URL=http://127.0.0.1:9000")
	})
	t.Run("the same URL in both is not a conflict", func(t *testing.T) {
		cfg, err := loadProfiles(t,
			"    local:\n      command: \"ANTHROPIC_BASE_URL=http://127.0.0.1:8000 claude\"\n      model_server: http://127.0.0.1:8000\n")
		require.NoError(t, err)
		uses := config.ModelServerUses("(default)", cfg.Agent.Command, cfg.Agent.Profiles)
		require.Len(t, uses, 1)
		assert.Equal(t, []string{"local"}, uses[0].Profiles, "listed once")
		assert.True(t, uses[0].ModelServer)
	})
	t.Run("a backend without a model-server variable is a load error", func(t *testing.T) {
		_, err := loadProfiles(t, "    local:\n      command: opencode\n      model_server: http://127.0.0.1:8000\n")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "opencode backend reads its model server from its own config")
	})
}

// TestModelServerEnvVarPerBackend: the variable each backend's CLI reads.
func TestModelServerEnvVarPerBackend(t *testing.T) {
	assert.Equal(t, "ANTHROPIC_BASE_URL", config.ModelServerEnvVar("claude"))
	assert.Equal(t, "OPENAI_BASE_URL", config.ModelServerEnvVar("codex"))
	for _, b := range []string{"opencode", "cursor", "custom", ""} {
		assert.Empty(t, config.ModelServerEnvVar(b), b)
	}
	p := config.AgentProfile{Command: "claude", ModelServerURL: "http://127.0.0.1:8000"}
	assert.Equal(t, "ANTHROPIC_BASE_URL=http://127.0.0.1:8000", config.ProfileModelServerEnv(p, "claude", "claude"))
	assert.Equal(t, "ANTHROPIC_BASE_URL=http://127.0.0.1:8000", config.ProfileModelServerEnv(p, "", "claude"), "empty run backend is claude")
	assert.Empty(t, config.ProfileModelServerEnv(p, "codex", "claude"), "a run moved to another backend does not get it")
	assert.Empty(t, config.ProfileModelServerEnv(config.AgentProfile{Command: "claude"}, "claude", "claude"))
	codex := config.AgentProfile{Command: "codex", ModelServerURL: "http://127.0.0.1:8000/v1"}
	assert.Equal(t, "OPENAI_BASE_URL=http://127.0.0.1:8000/v1", config.ProfileModelServerEnv(codex, "codex", "claude"))
}

// TestModelServerAutoPool: two profiles on one server share one pool, an
// explicit pool wins, and the derived pool is marked so it is not saved.
func TestModelServerAutoPool(t *testing.T) {
	cfg, err := loadProfiles(t,
		"    local-a:\n      command: claude\n      model_server: http://127.0.0.1:8000\n"+
			"    local-b:\n      command: codex\n      model_server: http://localhost:8000/v1\n"+
			"    local-c:\n      command: claude\n      model_server: http://127.0.0.1:8001\n"+
			"    box:\n      command: claude\n      model_server: http://GPU-Box.lan:8000\n"+
			"    pinned:\n      command: claude\n      model_server: http://127.0.0.1:8000\n      pool: mine\n")
	require.NoError(t, err)
	p := cfg.Agent.Profiles
	assert.Equal(t, "local-8000", p["local-a"].Pool)
	assert.Equal(t, p["local-a"].Pool, p["local-b"].Pool, "one server, one pool")
	assert.True(t, p["local-a"].PoolAuto)
	assert.Equal(t, "local-8001", p["local-c"].Pool, "another port is another server")
	assert.Equal(t, "model-gpu-box-lan-8000", p["box"].Pool)
	assert.True(t, config.ValidPoolName(p["box"].Pool))
	assert.Equal(t, "mine", p["pinned"].Pool, "an explicit pool wins")
	assert.False(t, p["pinned"].PoolAuto)
	assert.Equal(t, "claude/local-8000", config.ProfileRunsLane(p["local-a"], cfg.Agent.Command),
		"the breaker lane: a claude limit never holds it")
	assert.Equal(t, "local-443", config.ModelServerPool("https://[::1]/"))
	assert.Equal(t, "local-80", config.ModelServerPool("http://127.0.0.2"))

	// Clearing model_server (the dashboard save) drops a derived pool and
	// keeps an explicit one.
	a := p["local-a"]
	a.ModelServer = ""
	require.NoError(t, config.ApplyProfileModelServer("local-a", &a, "claude"))
	assert.Empty(t, a.Pool)
	assert.False(t, a.PoolAuto)
	pinned := p["pinned"]
	pinned.ModelServer = ""
	require.NoError(t, config.ApplyProfileModelServer("pinned", &pinned, "claude"))
	assert.Equal(t, "mine", pinned.Pool)
}

// TestModelServerPoolInFallbackMap: a profile_map key names the derived
// lane, which validation accepts.
func TestModelServerPoolInFallbackMap(t *testing.T) {
	_, err := loadProfiles(t,
		"    coder:\n      command: claude\n"+
			"    coder-local:\n      command: claude\n      model_server: http://127.0.0.1:8000\n"+
			"  backend_fallback:\n    chain: [claude, claude/local-8000]\n"+
			"    profile_map:\n      coder:\n        \"claude/local-8000\": coder-local\n")
	require.NoError(t, err)
}

// TestProfileTimeouts: a profile's own values win; a model-server profile
// gets at least the model-server defaults, never less than the agent
// level, and never a timeout the agent level turned off.
func TestProfileTimeouts(t *testing.T) {
	agent := config.RunTimeouts{ReadMs: 30_000, TurnMs: 3_600_000, StallMs: 300_000}
	plain := config.AgentProfile{Command: "claude"}
	assert.Equal(t, agent, config.ProfileTimeouts(plain, "claude", agent), "no model server: the agent level")

	local := config.AgentProfile{Command: "claude", ModelServer: "http://127.0.0.1:8000", ModelServerURL: "http://127.0.0.1:8000"}
	assert.Equal(t, config.RunTimeouts{
		ReadMs: config.ModelServerReadTimeoutMs, TurnMs: config.ModelServerTurnTimeoutMs, StallMs: config.ModelServerStallTimeoutMs,
	}, config.ProfileTimeouts(local, "claude", agent))

	viaCommand := config.AgentProfile{Command: "ANTHROPIC_BASE_URL=http://127.0.0.1:8000 claude"}
	assert.Equal(t, config.ModelServerStallTimeoutMs, config.ProfileTimeouts(viaCommand, "claude", agent).StallMs,
		"the command form uses a model server too")
	assert.Equal(t, config.ModelServerReadTimeoutMs, config.ProfileTimeouts(config.AgentProfile{}, "ANTHROPIC_BASE_URL=http://x claude", agent).ReadMs,
		"a run without a profile follows agent.command")

	larger := config.RunTimeouts{ReadMs: 600_000, TurnMs: 20_000_000, StallMs: 1_800_000}
	assert.Equal(t, larger, config.ProfileTimeouts(local, "claude", larger), "a larger agent value is kept")

	off := config.RunTimeouts{ReadMs: 30_000, TurnMs: 0, StallMs: 0}
	got := config.ProfileTimeouts(local, "claude", off)
	assert.Zero(t, got.TurnMs, "a disabled turn timeout stays disabled")
	assert.Zero(t, got.StallMs, "a disabled stall timeout stays disabled")

	own := local
	own.ReadTimeoutMs, own.TurnTimeoutMs, own.StallTimeoutMs = 45_000, 60_000, 90_000
	assert.Equal(t, config.RunTimeouts{ReadMs: 45_000, TurnMs: 60_000, StallMs: 90_000}, config.ProfileTimeouts(own, "claude", agent),
		"the profile's own values win, even below the defaults")
}

// TestProfileTimeoutFieldsParse: the YAML keys, and non-positive values
// refused (a profile cannot turn a timeout off).
func TestProfileTimeoutFieldsParse(t *testing.T) {
	cfg, err := loadProfiles(t,
		"    slow:\n      command: claude\n      read_timeout_ms: 120000\n      turn_timeout_ms: 7200000\n      stall_timeout_ms: 600000\n"+
			"    plain:\n      command: claude\n")
	require.NoError(t, err)
	slow := cfg.Agent.Profiles["slow"]
	assert.Equal(t, 120000, slow.ReadTimeoutMs)
	assert.Equal(t, 7200000, slow.TurnTimeoutMs)
	assert.Equal(t, 600000, slow.StallTimeoutMs)
	assert.Zero(t, cfg.Agent.Profiles["plain"].StallTimeoutMs)
	for _, bad := range []string{"0", "-1", "soon"} {
		_, err := loadProfiles(t, "    slow:\n      command: claude\n      stall_timeout_ms: "+bad+"\n")
		require.Error(t, err, bad)
		assert.Contains(t, err.Error(), "agent.profiles.slow.stall_timeout_ms must be a positive number", bad)
	}
}

// TestCheckModelServerValue: the shape check the dashboard save uses.
func TestCheckModelServerValue(t *testing.T) {
	for _, ok := range []string{"", "http://127.0.0.1:8000", "https://box.lan/v1", "$LOCAL_MODEL_URL"} {
		assert.NoError(t, config.CheckModelServerValue(ok), ok)
	}
	for _, bad := range []string{"127.0.0.1:8000", "http://", "${X}", "http://$X"} {
		assert.Error(t, config.CheckModelServerValue(bad), bad)
	}
}
