package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
)

// A New session started from a profile whose command is a brace group gets
// the interactive flags and the prompt inside the group, after the CLI, as
// the runners do (config.SpliceAgentArgs). Nothing runs.
func TestInteractiveCommandSplicesIntoACompoundCommand(t *testing.T) {
	look := func(string) (string, error) { return "/usr/bin/claude", nil }
	got, err := interactiveCommand("claude", "{ source ~/.nvm/nvm.sh; claude --model m; }", agent.PermissionBypass, "fix it", look)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(got, "{ source ~/.nvm/nvm.sh; claude --model m '"), got)
	assert.True(t, strings.HasSuffix(got, " 'fix it'; }"), got)
}
