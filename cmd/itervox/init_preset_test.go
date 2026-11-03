package main

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
)

// writePresetWorkflow scaffolds the cross-review preset for runner the way
// `itervox init --preset cross-review` does, without the network-backed
// model discovery.
func writePresetWorkflow(t *testing.T, runner string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "WORKFLOW.md")
	content := generateWorkflow("github", runner, repoInfo{ProjectName: "demo", Owner: "acme", Repo: "demo", DefaultBranch: "main"}, path)
	content, err := applyCrossReviewPreset(content, runner)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	require.NoError(t, writeInitAgentFiles(path, runner))
	require.NoError(t, writeCrossReviewerFiles(path, runner))
	return path
}

// TestCrossReviewPresetConfig (#79): the preset loads and validates, runs one
// implementer on the chosen runner and two reviewers on different backends
// with any_block and auto_review — "Claude writes, Codex reviews" and the
// reverse.
func TestCrossReviewPresetConfig(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	for _, tc := range []struct{ runner, cross string }{{"claude", "reviewer-codex"}, {"codex", "reviewer-claude"}} {
		t.Run(tc.runner, func(t *testing.T) {
			cfg, err := config.Load(writePresetWorkflow(t, tc.runner))
			require.NoError(t, err)
			require.NoError(t, config.ValidateDispatch(cfg), "the preset must pass startup validation")

			assert.Equal(t, []string{tc.cross, "reviewer"}, cfg.Agent.ReviewerProfiles)
			assert.Equal(t, tc.cross, cfg.Agent.ReviewerProfile)
			assert.Equal(t, config.ReviewQuorumAnyBlock, cfg.Agent.ReviewQuorum)
			assert.True(t, cfg.Agent.AutoReview)

			backendOf := func(profile string) string {
				return agent.BackendFromCommand(cfg.Agent.Profiles[profile].Command)
			}
			assert.Equal(t, tc.runner, backendOf("implementer"))
			assert.Equal(t, tc.runner, backendOf("reviewer"))
			assert.Equal(t, otherRunner(tc.runner), backendOf(tc.cross))
			assert.NotEqual(t, backendOf("reviewer"), backendOf(tc.cross), "the two reviewers run on different backends")
			assert.Contains(t, cfg.Agent.Profiles[tc.cross].Instructions, "Do not edit files, commit, push or move the issue")
		})
	}
}

// TestReviewerScaffoldsAreReadOnly (#79): no reviewer prompt Itervox writes
// tells a reviewer to commit or push.
func TestReviewerScaffoldsAreReadOnly(t *testing.T) {
	content := generateWorkflow("github", "claude", repoInfo{ProjectName: "demo", Owner: "acme", Repo: "demo"}, filepath.Join(t.TempDir(), "WORKFLOW.md"))
	for name, text := range map[string]string{
		"init reviewer_prompt":          reviewerPromptBlock(content),
		"DefaultReviewerPrompt":         config.DefaultReviewerPrompt,
		"reviewer INSTRUCTIONS":         initInstructionsContent("reviewer", "claude"),
		"reviewer-codex INSTRUCTIONS":   initInstructionsContent("reviewer-codex", "codex"),
		"reviewer SOUL":                 initSoulContent("reviewer"),
		"reviewer-claude SOUL (shared)": initSoulContent("reviewer-claude"),
	} {
		assert.NotContains(t, text, "git push", name)
		assert.NotContains(t, text, "git commit", name)
		assert.NotContains(t, text, "Fix them directly", name)
		assert.Regexp(t, `(?i)read-only|do not edit files, commit, push`, text, name)
	}
}

// TestOfferCrossReviewPreset (#79): init offers the preset only on a
// terminal with both agent CLIs installed; anything but y is no.
func TestOfferCrossReviewPreset(t *testing.T) {
	both := func(string) (string, error) { return "/bin/x", nil }
	noCodex := func(name string) (string, error) {
		if name == "codex" {
			return "", errors.New("not found")
		}
		return "/bin/claude", nil
	}
	for _, tc := range []struct {
		name     string
		terminal bool
		look     func(string) (string, error)
		answer   string
		want     string
		asked    bool
	}{
		{"yes", true, both, "y\n", presetCrossReview, true},
		{"no", true, both, "n\n", "", true},
		{"eof", true, both, "", "", true},
		{"no terminal", false, both, "y\n", "", false},
		{"codex missing", true, noCodex, "y\n", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			orig := lookPath
			lookPath = tc.look
			t.Cleanup(func() { lookPath = orig })
			var out bytes.Buffer
			got := offerCrossReviewPreset("claude", tc.terminal, bufio.NewReader(strings.NewReader(tc.answer)), &out)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.asked, strings.Contains(out.String(), "Use the cross-review preset (Claude writes; Codex and Claude review every run"), out.String())
		})
	}
}

// reviewerPromptBlock is the agent.reviewer_prompt block of a generated
// workflow (it ends where the next top-level key starts).
func reviewerPromptBlock(content string) string {
	start := strings.Index(content, "  reviewer_prompt: |")
	end := strings.Index(content[start:], "\nworkspace:")
	return content[start : start+end]
}
