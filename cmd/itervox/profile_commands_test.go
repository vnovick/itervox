package main

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/vnovick/itervox/internal/config"
)

func boolPtr(b bool) *bool { return &b }

// TestUnresolvableProfileCommands pins the diagnostic that turns a runtime
// "command not found" into a startup-time answer.
//
// The failure this catches is otherwise invisible until dispatch: a tool
// installed under a version manager (nvm, asdf) lives in a versioned bin
// directory that is on PATH in an interactive shell but frequently not in the
// environment the daemon was started from — and that path changes on every
// version switch. Observed live as `zsh:1: command not found: codex` while
// `command -v codex` succeeded in the operator's own shell.
func TestUnresolvableProfileCommands(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"works":    {Command: "sh"}, // always on PATH
		"missing":  {Command: "itervox-definitely-not-a-real-binary"},
		"withArgs": {Command: "itervox-also-not-real --model gpt-5"}, // only the binary is checked
		"disabled": {Command: "itervox-nope", Enabled: boolPtr(false)},
	}

	got := unresolvableProfileCommands(cfg)

	assert.Contains(t, got, "missing: itervox-definitely-not-a-real-binary")
	assert.Contains(t, got, "withArgs: itervox-also-not-real",
		"a command line is not a path — only its first field is the binary")
	assert.NotContains(t, got, "works: sh", "a resolvable command must not be reported")
	for _, pair := range got {
		assert.NotContains(t, pair, "disabled",
			"a disabled profile cannot be dispatched, so its binary is irrelevant")
	}
	assert.Equal(t, []string{"missing: itervox-definitely-not-a-real-binary", "withArgs: itervox-also-not-real"}, got,
		"output must be sorted for stable doctor reports")
}

func TestUnresolvableProfileCommandsEdgeCases(t *testing.T) {
	assert.Nil(t, unresolvableProfileCommands(nil))

	cfg := &config.Config{}
	cfg.Agent.Profiles = map[string]config.AgentProfile{"blank": {Command: "   "}}
	assert.Empty(t, unresolvableProfileCommands(cfg),
		"an empty command is a config gap other validation owns, not a PATH failure")
}

func TestFirstField(t *testing.T) {
	assert.Equal(t, "claude", firstField("claude --model claude-sonnet-4-6"))
	assert.Equal(t, "codex", firstField("  codex  "))
	assert.Equal(t, "", firstField(""))
}
