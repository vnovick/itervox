package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/skills"
)

// refsAdapter builds an adapter over a project with one Claude subagent.
func refsAdapter(t *testing.T, cfg *config.Config) *orchestratorAdapter {
	t.Helper()
	isolateHome(t)
	proj := t.TempDir()
	agents := filepath.Join(proj, ".claude", "agents")
	require.NoError(t, os.MkdirAll(agents, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(agents, "code-reviewer.md"),
		[]byte("---\nname: code-reviewer\ndescription: Reviews diffs\n---\nReview.\n"), 0o644))
	a := &orchestratorAdapter{cfg: cfg, workflowPath: filepath.Join(proj, "WORKFLOW.md")}
	a.initSkillsCache()
	return a
}

// TestAdapterCheckPromptRefsResolvesDraftBackend: the draft's command and
// backend pick the backend the way the runner does, so a Codex draft gets
// MISSING_SUBAGENT_REF for a Claude subagent and a Claude draft does not.
func TestAdapterCheckPromptRefsResolvesDraftBackend(t *testing.T) {
	a := refsAdapter(t, &config.Config{})
	text := "Ask @agent-code-reviewer."

	claude, ok := a.CheckPromptRefs(server.PromptRefCheckRequest{Command: "claude", Text: text})
	require.True(t, ok)
	assert.Equal(t, "claude", claude.Backend)
	require.Len(t, claude.Findings, 1)
	assert.Equal(t, skills.RefStatusOK, claude.Findings[0].Status)

	// A wrapper command with a codex backend request runs on Codex.
	codex, ok := a.CheckPromptRefs(server.PromptRefCheckRequest{Profile: "fast", Command: "my-wrapper", Backend: "codex", Text: text})
	require.True(t, ok)
	assert.Equal(t, "codex", codex.Backend)
	require.Len(t, codex.Findings, 1)
	assert.Equal(t, skills.RefStatusMissing, codex.Findings[0].Status)
	assert.Equal(t, "MISSING_SUBAGENT_REF", codex.Findings[0].Issue.ID)
	assert.Contains(t, codex.Findings[0].Issue.Title, `"fast"`)

	// A recognised binary wins over a conflicting backend request.
	pinned, _ := a.CheckPromptRefs(server.PromptRefCheckRequest{Command: "claude", Backend: "codex", Text: text})
	assert.Equal(t, "claude", pinned.Backend)
}

func TestAdapterCheckPromptRefsNeedsInventory(t *testing.T) {
	a := &orchestratorAdapter{cfg: &config.Config{}}
	_, ok := a.CheckPromptRefs(server.PromptRefCheckRequest{Text: "x"})
	assert.False(t, ok)
}

// TestAdapterProfileBackends maps each profile (and the default) to the
// backend skills.ProfileBackend resolves.
func TestAdapterProfileBackends(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.Command = "codex"
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"inherits": {},
		"claude":   {Command: "claude --model x"},
		"wrapped":  {Command: "run-agent.sh", Backend: "claude"},
	}
	a := &orchestratorAdapter{cfg: cfg}
	assert.Equal(t, map[string]string{"": "codex", "inherits": "codex", "claude": "claude", "wrapped": "claude"}, a.ProfileBackends())
}
