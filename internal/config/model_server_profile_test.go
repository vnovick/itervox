package config_test

import (
	"strconv"
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
		for _, bad := range []string{"127.0.0.1:8000", "ftp://host", "http://", "http://$HOST:8000", "[x]", "http://127.0.0.1", "https://box.lan/v1"} {
			_, err := loadProfiles(t, "    local:\n      command: claude\n      model_server: \""+bad+"\"\n")
			require.Error(t, err, "model_server %s", bad)
			assert.Contains(t, err.Error(), "agent.profiles.local.model_server", "model_server %s", bad)
		}
	})
	t.Run("a URL without an explicit port is refused", func(t *testing.T) {
		t.Setenv("ITERVOX_TEST_MODEL_SERVER_NOPORT", "http://127.0.0.1/")
		for _, v := range []string{"http://127.0.0.1", "https://box.lan/v1", "$ITERVOX_TEST_MODEL_SERVER_NOPORT"} {
			_, err := loadProfiles(t, "    local:\n      command: claude\n      model_server: \""+v+"\"\n")
			require.Error(t, err, v)
			assert.Contains(t, err.Error(), "has no port; write the port the server listens on", v)
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

// TestModelServerAutoPool: profiles on one server (host and port; the path
// and the loopback spelling do not matter) share one pool named after the
// first of them, with no port in the name; another port or host is another
// pool; an explicit pool wins; the derived pool is marked so it is not
// saved.
func TestModelServerAutoPool(t *testing.T) {
	cfg, err := loadProfiles(t,
		"    local-b:\n      command: codex\n      model_server: http://localhost:11434/v1\n"+
			"    local-a:\n      command: claude\n      model_server: http://127.0.0.1:11434\n"+
			"    local-c:\n      command: claude\n      model_server: http://127.0.0.1:1234\n"+
			"    box:\n      command: claude\n      model_server: http://GPU-Box.lan:8080\n"+
			"    pinned:\n      command: claude\n      model_server: http://127.0.0.1:11434\n      pool: mine\n")
	require.NoError(t, err)
	p := cfg.Agent.Profiles
	assert.Equal(t, "model-local-a", p["local-a"].Pool, "named after the first profile on the server by name")
	assert.Equal(t, p["local-a"].Pool, p["local-b"].Pool, "one server, one pool")
	assert.True(t, p["local-a"].PoolAuto)
	assert.True(t, p["local-b"].PoolAuto)
	assert.Equal(t, "model-local-c", p["local-c"].Pool, "another port is another server")
	assert.Equal(t, "model-box", p["box"].Pool, "another host is another server")
	assert.Equal(t, "mine", p["pinned"].Pool, "an explicit pool wins")
	assert.False(t, p["pinned"].PoolAuto)
	for name, profile := range p {
		assert.True(t, config.ValidPoolName(profile.Pool), name)
		assert.NotRegexp(t, `[0-9]{2,}`, profile.Pool, "%s: no port in a pool name", name)
	}
	assert.Equal(t, "claude/model-local-a", config.ProfileRunsLane(p["local-a"], cfg.Agent.Command),
		"the breaker lane: a claude limit never holds it")
	assert.Equal(t, "loopback:11434", config.ModelServerKey("http://[::1]:11434/v1"))
	assert.Equal(t, "gpu-box.lan:8080", config.ModelServerKey("https://GPU-Box.lan:8080"))
	assert.Empty(t, config.ModelServerKey("http://127.0.0.1"), "no explicit port, no key")

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

	// Over the whole set, the server's pool now follows its remaining
	// first profile.
	p["local-a"] = a
	require.NoError(t, config.AssignModelServerPools(p))
	assert.Empty(t, p["local-a"].Pool)
	assert.Equal(t, "model-local-b", p["local-b"].Pool)
}

// TestModelServerNamePool: model_server_name names the server's pool for
// every profile on it (pool: still wins), is saved as its own key, and is
// refused without a model_server, with a bad name, when two profiles on one
// server disagree, and when one name would cover two servers.
func TestModelServerNamePool(t *testing.T) {
	cfg, err := loadProfiles(t,
		"    alpha:\n      command: claude\n      model_server: http://127.0.0.1:1234\n"+
			"    beta:\n      command: codex\n      model_server: http://127.0.0.1:1234/v1\n      model_server_name: studio\n"+
			"    gamma:\n      command: claude\n      model_server: http://127.0.0.1:1234\n      pool: own\n")
	require.NoError(t, err)
	p := cfg.Agent.Profiles
	assert.Equal(t, "studio", p["beta"].ModelServerName)
	assert.Empty(t, p["alpha"].ModelServerName)
	assert.Equal(t, "studio", p["alpha"].Pool, "the name covers every profile on the server")
	assert.Equal(t, "studio", p["beta"].Pool)
	assert.True(t, p["alpha"].PoolAuto)
	assert.Equal(t, "own", p["gamma"].Pool)
	assert.False(t, p["gamma"].PoolAuto)

	// An explicit pool shared by profiles of one server, or by hosted
	// profiles only, is one quota and loads.
	cfg, err = loadProfiles(t,
		"    hosted-a:\n      command: claude\n      pool: team\n"+
			"    hosted-b:\n      command: codex\n      pool: team\n"+
			"    box:\n      command: claude\n      model_server: http://127.0.0.1:1234\n      model_server_name: box\n"+
			"    box-pinned:\n      command: claude\n      model_server: http://localhost:1234\n      pool: box\n")
	require.NoError(t, err)
	assert.Equal(t, "box", cfg.Agent.Profiles["box-pinned"].Pool)
	assert.Equal(t, "team", cfg.Agent.Profiles["hosted-b"].Pool)

	for name, tc := range map[string]struct{ profiles, want string }{
		"without model_server": {
			"    alpha:\n      command: claude\n      model_server_name: studio\n",
			"model_server_name is set without a model_server",
		},
		"bad name": {
			"    alpha:\n      command: claude\n      model_server: http://127.0.0.1:1234\n      model_server_name: Studio_1\n",
			"must match [a-z0-9-]+",
		},
		"two names, one server": {
			"    alpha:\n      command: claude\n      model_server: http://127.0.0.1:1234\n      model_server_name: one\n" +
				"    beta:\n      command: claude\n      model_server: http://localhost:1234\n      model_server_name: two\n",
			"name the same model server",
		},
		"one name, two servers": {
			"    alpha:\n      command: claude\n      model_server: http://127.0.0.1:1234\n      model_server_name: shared\n" +
				"    beta:\n      command: claude\n      model_server: http://127.0.0.1:11434\n      model_server_name: shared\n",
			"names two servers",
		},
		"a derived name taken by another server": {
			"    alpha:\n      command: claude\n      model_server: http://127.0.0.1:1234\n" +
				"    beta:\n      command: claude\n      model_server: http://127.0.0.1:11434\n      model_server_name: model-alpha\n",
			"names two servers",
		},
		"a hosted pool named like a server": {
			"    hosted:\n      command: claude\n      pool: local\n" +
				"    box:\n      command: claude\n      model_server: http://127.0.0.1:1234\n      model_server_name: local\n",
			`the pool "local" names two servers (agent.profiles.hosted, no model server; agent.profiles.box, model server loopback:1234)`,
		},
		"a hosted pool named like a derived pool": {
			"    hosted:\n      command: claude\n      pool: model-box\n" +
				"    box:\n      command: claude\n      model_server: http://127.0.0.1:1234\n",
			`the pool "model-box" names two servers (agent.profiles.hosted, no model server; agent.profiles.box, model server loopback:1234)`,
		},
		"one explicit pool on two servers": {
			"    alpha:\n      command: claude\n      model_server: http://127.0.0.1:1234\n      pool: shared\n" +
				"    beta:\n      command: claude\n      model_server: http://10.0.0.5:1234\n      pool: shared\n",
			`the pool "shared" names two servers`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadProfiles(t, tc.profiles)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// TestModelServerPoolInFallbackMap: a chain step and a profile_map key name
// the derived lane (no port in it), which validation accepts.
func TestModelServerPoolInFallbackMap(t *testing.T) {
	_, err := loadProfiles(t,
		"    coder:\n      command: claude\n"+
			"    coder-local:\n      command: claude\n      model_server: http://127.0.0.1:11434\n      model_server_name: workstation\n"+
			"  backend_fallback:\n    chain: [claude, codex, claude/workstation]\n"+
			"    profile_map:\n      coder:\n        \"claude/workstation\": coder-local\n")
	require.NoError(t, err)
}

// TestProfileTimeouts: a profile's own values win; a model-server profile
// gets at least the model-server defaults, never less than the agent
// level, and never a timeout the agent level turned off.
func TestProfileTimeouts(t *testing.T) {
	agent := config.RunTimeouts{ReadMs: 30_000, TurnMs: 3_600_000, StallMs: 300_000}
	plain := config.AgentProfile{Command: "claude"}
	assert.Equal(t, agent, config.ProfileTimeouts(plain, "claude", "claude", agent), "no model server: the agent level")

	local := config.AgentProfile{Command: "claude", ModelServer: "http://127.0.0.1:8000", ModelServerURL: "http://127.0.0.1:8000"}
	assert.Equal(t, config.RunTimeouts{
		ReadMs: config.ModelServerReadTimeoutMs, TurnMs: config.ModelServerTurnTimeoutMs, StallMs: config.ModelServerStallTimeoutMs,
	}, config.ProfileTimeouts(local, "claude", "claude", agent))
	assert.Equal(t, config.ModelServerStallTimeoutMs, config.ProfileTimeouts(local, "", "claude", agent).StallMs, "an empty run backend is claude")

	larger := config.RunTimeouts{ReadMs: 600_000, TurnMs: 20_000_000, StallMs: 1_800_000}
	assert.Equal(t, larger, config.ProfileTimeouts(local, "claude", "claude", larger), "a larger agent value is kept")

	off := config.RunTimeouts{ReadMs: 30_000, TurnMs: 0, StallMs: 0}
	got := config.ProfileTimeouts(local, "claude", "claude", off)
	assert.Zero(t, got.TurnMs, "a disabled turn timeout stays disabled")
	assert.Zero(t, got.StallMs, "a disabled stall timeout stays disabled")

	own := local
	own.ReadTimeoutMs, own.TurnTimeoutMs, own.StallTimeoutMs = 45_000, 60_000, 90_000
	assert.Equal(t, config.RunTimeouts{ReadMs: 45_000, TurnMs: 60_000, StallMs: 90_000}, config.ProfileTimeouts(own, "claude", "claude", agent),
		"the profile's own values win, even below the defaults")
	assert.Equal(t, config.RunTimeouts{ReadMs: 45_000, TurnMs: 60_000, StallMs: 90_000}, config.ProfileTimeouts(own, "codex", "claude", agent),
		"the profile's own values hold on any backend")
}

// TestProfileTimeoutsOnlyForModelServerOnItsOwnBackend: the model-server
// defaults apply only to a profile with model_server, and only while the
// run uses that profile's own backend. A command that writes the variable
// itself (`ANTHROPIC_BASE_URL=… claude`) keeps the agent level, and so does
// a run a per-issue or recovery switch moved to another backend.
func TestProfileTimeoutsOnlyForModelServerOnItsOwnBackend(t *testing.T) {
	agent := config.RunTimeouts{ReadMs: 30_000, TurnMs: 3_600_000, StallMs: 300_000}
	viaCommand := config.AgentProfile{Command: "ANTHROPIC_BASE_URL=http://127.0.0.1:8000 claude"}
	assert.Equal(t, agent, config.ProfileTimeouts(viaCommand, "claude", "claude", agent), "the command form keeps the agent level")
	assert.Equal(t, agent, config.ProfileTimeouts(config.AgentProfile{}, "claude", "ANTHROPIC_BASE_URL=http://x:1 claude", agent),
		"a run without a profile keeps the agent level")
	assert.False(t, config.ProfileUsesModelServer(viaCommand, "claude", "claude"))

	local := config.AgentProfile{Command: "claude", ModelServer: "http://127.0.0.1:8000", ModelServerURL: "http://127.0.0.1:8000"}
	assert.True(t, config.ProfileUsesModelServer(local, "claude", "claude"))
	assert.False(t, config.ProfileUsesModelServer(local, "codex", "claude"), "switched to another backend")
	assert.Equal(t, agent, config.ProfileTimeouts(local, "codex", "claude", agent), "a switched run gets the agent level")

	codex := config.AgentProfile{Command: "codex", ModelServer: "http://127.0.0.1:8000/v1", ModelServerURL: "http://127.0.0.1:8000/v1"}
	assert.Equal(t, config.ModelServerReadTimeoutMs, config.ProfileTimeouts(codex, "codex", "claude", agent).ReadMs)
	assert.Equal(t, agent, config.ProfileTimeouts(codex, "claude", "claude", agent))
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

// TestTimeoutsAtMostThirtyDays: a per-profile or agent-level timeout above
// config.MaxTimeoutMs is refused at load with the key in the message; a
// larger value would overflow time.Duration (a skipped stall check, a
// negative read deadline). 30 days itself loads.
func TestTimeoutsAtMostThirtyDays(t *testing.T) {
	month := strconv.Itoa(config.MaxTimeoutMs)
	cfg, err := loadProfiles(t, "    slow:\n      command: claude\n      stall_timeout_ms: "+month+"\n      turn_timeout_ms: "+month+"\n")
	require.NoError(t, err)
	assert.Equal(t, config.MaxTimeoutMs, cfg.Agent.Profiles["slow"].StallTimeoutMs)
	for _, key := range []string{"read_timeout_ms", "turn_timeout_ms", "stall_timeout_ms"} {
		for _, bad := range []string{strconv.Itoa(config.MaxTimeoutMs + 1), "9300000000000", "1e30"} {
			_, err := loadProfiles(t, "    slow:\n      command: claude\n      "+key+": "+bad+"\n")
			require.Error(t, err, key+"="+bad)
			assert.Contains(t, err.Error(), "agent.profiles.slow."+key+" must be at most 2592000000 milliseconds (30 days)")

			_, err = config.Load(workflowWithContent(t, minimalV2WithProfileFiles(t, "agent:\n  "+key+": "+bad+"\n")))
			require.Error(t, err, "agent."+key+"="+bad)
			assert.Contains(t, err.Error(), "agent."+key+" must be at most 2592000000 milliseconds (30 days)")
		}
	}
	_, err = config.Load(workflowWithContent(t, minimalV2WithProfileFiles(t, "agent:\n  turn_timeout_ms: "+month+"\n  stall_timeout_ms: 0\n")))
	require.NoError(t, err, "30 days, and 0 (off), load at the agent level")
}

// TestCheckModelServerValue: the shape check the dashboard save uses.
func TestCheckModelServerValue(t *testing.T) {
	for _, ok := range []string{"", "http://127.0.0.1:11434", "https://box.lan:443/v1", "http://[::1]:1234", "$LOCAL_MODEL_URL", "http://127.0.0.1:1", "http://127.0.0.1:65535"} {
		assert.NoError(t, config.CheckModelServerValue(ok), ok)
	}
	for _, bad := range []string{"127.0.0.1:8000", "http://", "${X}", "http://$X", "http://127.0.0.1", "https://box.lan/v1", "http://[::1]/",
		"http://127.0.0.1:0", "http://127.0.0.1:65536", "http://127.0.0.1:99999", "http://127.0.0.1:00"} {
		assert.Error(t, config.CheckModelServerValue(bad), bad)
	}
}
