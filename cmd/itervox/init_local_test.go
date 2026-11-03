package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/tracker/local"
)

// TestInitLocalTrackerWorkflow (#85): `itervox init --tracker local` writes
// a workflow that loads and validates with no credentials, builds the local
// tracker over .itervox/issues/, and seeds one example issue without ever
// overwriting it.
func TestInitLocalTrackerWorkflow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	content := generateWorkflow("local", "claude", repoInfo{ProjectName: "demo", DefaultBranch: "main"}, path)
	assert.Contains(t, content, "kind: local")
	assert.NotContains(t, content, "api_key")
	assert.NotContains(t, content, "gh issue comment", "no GitHub step for a local tracker")
	assert.NotContains(t, content, "issue.url", "local issues have no URL")
	assert.Contains(t, content, "Issue file: .itervox/issues/{{ issue.identifier }}.md")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	require.NoError(t, writeInitAgentFiles(path, "claude"))

	t.Setenv("LINEAR_API_KEY", "")
	t.Setenv("GITHUB_TOKEN", "")
	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.NoError(t, config.ValidateDispatch(cfg), "no api_key is needed")
	assert.Equal(t, []string{"Backlog"}, cfg.Tracker.BacklogStates)

	var out bytes.Buffer
	issuesDir := filepath.Join(dir, ".itervox", "issues")
	seedLocalIssues(issuesDir, &out)
	assert.Contains(t, out.String(), "ITX-1.md")

	tr, err := buildTracker(cfg)
	require.NoError(t, err)
	lt, ok := tr.(*local.Tracker)
	require.True(t, ok, "kind local builds the local tracker")
	assert.Equal(t, issuesDir, lt.Dir())
	is, err := tr.FetchIssueDetail(context.Background(), "ITX-1")
	require.NoError(t, err)
	assert.Equal(t, "Backlog", is.State, "the example is not dispatched")

	require.NoError(t, tr.UpdateIssueState(context.Background(), "ITX-1", "Todo"))
	out.Reset()
	seedLocalIssues(issuesDir, &out)
	assert.Contains(t, out.String(), "not written")
	is, err = tr.FetchIssueDetail(context.Background(), "ITX-1")
	require.NoError(t, err)
	assert.Equal(t, "Todo", is.State, "a second init never overwrites the issue")
}

// TestLocalTrackerConfigValidation (#85): local needs no api_key, and a set
// project_slug must be a usable identifier prefix.
func TestLocalTrackerConfigValidation(t *testing.T) {
	assert.False(t, config.TrackerNeedsAPIKey("local"))
	assert.False(t, config.TrackerNeedsAPIKey("memory"))
	assert.True(t, config.TrackerNeedsAPIKey("github"))
	assert.True(t, config.TrackerNeedsAPIKey("linear"))

	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	require.NoError(t, writeInitAgentFiles(path, "claude"))
	write := func(slug string) error {
		content := generateWorkflow("local", "claude", repoInfo{ProjectName: "demo"}, path)
		if slug != "" {
			content = replaceOnce(t, content, "  # project_slug: ITX", "  project_slug: "+slug+" #")
		}
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
		cfg, err := config.Load(path)
		require.NoError(t, err)
		return config.ValidateDispatch(cfg)
	}
	require.NoError(t, write("APP"))
	err := write("acme/widgets")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a valid issue prefix")
}

func replaceOnce(t *testing.T, s, old, repl string) string {
	t.Helper()
	i := bytes.Index([]byte(s), []byte(old))
	require.GreaterOrEqual(t, i, 0, "%q not found", old)
	return s[:i] + repl + s[i+len(old):]
}

// TestLocalIssuesAreCommittable (#85): a root .gitignore that hides
// .itervox/ gets a carve-out so the issue files can be committed.
func TestLocalIssuesAreCommittable(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".itervox/\n"), 0o644))
	seedLocalIssues(filepath.Join(dir, ".itervox", "issues"), &bytes.Buffer{})
	require.NoError(t, finalizeItervoxGitignore(filepath.Join(dir, ".itervox")))
	assert.False(t, gitPathIgnored(t, dir, ".itervox/issues/ITX-1.md"))
	assert.True(t, gitPathIgnored(t, dir, ".itervox/.env"), "runtime files stay ignored")
}

// TestDoctorReportsMalformedLocalIssues (#85): doctor warns about an issue
// file that does not parse, without failing.
func TestDoctorReportsMalformedLocalIssues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(path, []byte(generateWorkflow("local", "claude", repoInfo{ProjectName: "demo"}, path)), 0o644))
	require.NoError(t, writeInitAgentFiles(path, "claude"))
	issues := filepath.Join(dir, ".itervox", "issues")
	seedLocalIssues(issues, &bytes.Buffer{})
	require.NoError(t, os.WriteFile(filepath.Join(issues, "ITX-2.md"), []byte("title: no front matter\n"), 0o644))

	report, _ := collectDoctorReport(path)
	require.Len(t, report.LocalIssueProblems, 1)
	assert.Contains(t, report.LocalIssueProblems[0], "ITX-2.md")
	assert.Contains(t, renderDoctorReport(report), "WARNING: issue file does not parse")
}
