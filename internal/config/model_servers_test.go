package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

// TestModelServerURLsReadsCommandAssignments: only assignments before the
// program count, through an optional `env` prefix, quotes stripped.
func TestModelServerURLsReadsCommandAssignments(t *testing.T) {
	assert.Equal(t, map[string]string{"ANTHROPIC_BASE_URL": "http://127.0.0.1:8000"},
		config.ModelServerURLs("ANTHROPIC_BASE_URL=http://127.0.0.1:8000 claude --model glm"))
	assert.Equal(t, map[string]string{"OPENAI_BASE_URL": "http://localhost:8000/v1"},
		config.ModelServerURLs(`env -i OPENAI_BASE_URL="http://localhost:8000/v1" opencode`))
	assert.Empty(t, config.ModelServerURLs("claude ANTHROPIC_BASE_URL=http://x"), "after the program it is an argument")
	assert.Empty(t, config.ModelServerURLs("FOO=1 claude"))
}

// TestModelServerUsesGroupsProfilesAndPools: one entry per variable and
// URL, with the run modes using it and their pools; disabled profiles are
// skipped, an empty profile command runs agent.command, and a $VAR URL is
// unchecked.
func TestModelServerUsesGroupsProfilesAndPools(t *testing.T) {
	local := "ANTHROPIC_BASE_URL=http://127.0.0.1:8000 claude"
	uses := config.ModelServerUses("(default)", local, map[string]config.AgentProfile{
		"coder-local": {Command: local + " --model glm", Pool: "local"},
		"inherits":    {Pool: "local"},
		"other-pool":  {Command: local, Pool: "box"},
		"from-var":    {Command: "OPENAI_BASE_URL=$LOCAL_URL opencode"},
		"vendor":      {Command: "claude"},
		"off":         {Command: local, Enabled: new(false)},
	})
	require.Len(t, uses, 2)
	assert.Equal(t, config.ModelServerUse{
		Var: "OPENAI_BASE_URL", URL: "$LOCAL_URL", Profiles: []string{"from-var"},
		Pools: nil, Unchecked: true,
	}, uses[0])
	assert.Equal(t, config.ModelServerUse{
		Var: "ANTHROPIC_BASE_URL", URL: "http://127.0.0.1:8000",
		Profiles: []string{"(default)", "coder-local", "inherits", "other-pool"},
		Pools:    []string{"box", "local"},
	}, uses[1])
	assert.Empty(t, config.ModelServerUses("(default)", "claude", nil))
}
