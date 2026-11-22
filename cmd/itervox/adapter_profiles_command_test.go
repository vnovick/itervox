package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

// A dashboard profile save whose command the next load would refuse (the
// agent CLI piped onward) is refused at save time; a brace group ending in
// the CLI is saved.
func TestProfileSaveRefusesAnAmbiguousCommand(t *testing.T) {
	a := &orchestratorAdapter{cfg: &config.Config{}}
	a.cfg.Agent.Command = "claude"
	err := a.checkProfileSetLoads(map[string]config.AgentProfile{"fast": {Command: "claude | tee log"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "profile save refused")
	assert.Contains(t, err.Error(), "pipeline")
	require.NoError(t, a.checkProfileSetLoads(map[string]config.AgentProfile{"nvm": {Command: "{ source ~/.nvm/nvm.sh; claude; }"}}))
}
