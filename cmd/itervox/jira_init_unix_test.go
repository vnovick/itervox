//go:build unix

package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
)

// TestInitAcceptsJiraTracker: the real `itervox init` accepts --tracker jira
// (it gets past the tracker check to the runner check), and its tracker
// errors and flag help name jira.
func TestInitAcceptsJiraTracker(t *testing.T) {
	dir := t.TempDir()
	code, _, stderr := runItervoxCLI(t, dir, "init", "--tracker", "jira", "--runner", "nope")
	assert.Equal(t, 1, code, stderr)
	assert.Contains(t, stderr, `unknown runner "nope"`, "jira passed the tracker check")

	code, _, stderr = runItervoxCLI(t, dir, "init", "--tracker", "trello")
	assert.Equal(t, 1, code, stderr)
	assert.Contains(t, stderr, `unknown tracker "trello" (supported: linear, github, jira, local)`)

	code, _, stderr = runItervoxCLI(t, dir, "init")
	assert.Equal(t, 1, code, stderr)
	assert.Contains(t, stderr, "--tracker is required (linear, github, jira or local)")
	assert.Contains(t, stderr, "tracker kind: linear, github, jira or local (required)")

	_, _, stderr = runItervoxCLI(t, dir, "help")
	assert.Contains(t, stderr, "--tracker  github|linear|jira|local  (default: detected)")
	assert.Contains(t, stderr, "--tracker  linear|github|jira|local  (required for new workflows)")
}

// TestQuickstartWritesJiraWorkflow: `itervox quickstart --tracker jira`
// scaffolds the Jira project and stops at the credential step, naming the
// variable and the two settings to fill in before the next run.
func TestQuickstartWritesJiraWorkflow(t *testing.T) {
	dir := repoWithRemote(t, "")
	t.Setenv("HOME", filepath.Join(dir, "home"))
	t.Setenv("LINEAR_API_KEY", "")
	t.Setenv("JIRA_API_KEY", "")
	stubLookPath(t, "claude")
	origClaude, origCodex := listClaudeModels, listCodexModels
	listClaudeModels = func() []agent.ModelOption { return nil }
	listCodexModels = func() []agent.ModelOption { return nil }
	t.Cleanup(func() { listClaudeModels, listCodexModels = origClaude, origCodex })

	var out strings.Builder
	code := quickstart(quickstartOptions{Dir: dir, Tracker: "jira", NoStart: true}, strings.NewReader(""), &out)
	assert.Equal(t, 1, code)
	assert.Contains(t, out.String(), "tracker jira (--tracker), agent claude")
	assert.Contains(t, out.String(), "set JIRA_API_KEY=you@example.com:your_api_token in "+filepath.Join(dir, ".itervox", ".env"))
	assert.Contains(t, out.String(), "set tracker.endpoint and tracker.project_slug in "+filepath.Join(dir, ".itervox", "settings.jsonc"))
	front, err := readQuickstartFrontMatter(filepath.Join(dir, "WORKFLOW.md"))
	require.NoError(t, err)
	assert.Equal(t, "jira", front.Tracker.Kind)
	assert.FileExists(t, filepath.Join(dir, ".itervox", ".env"))

	// The fallback to the local tracker names every service it could use.
	det, err := detectQuickstart(repoWithRemote(t, ""), "", "")
	require.NoError(t, err)
	assert.Contains(t, det.TrackerReason, "pass --tracker github, linear or jira to use a service")
}
