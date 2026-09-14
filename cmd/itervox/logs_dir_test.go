package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDefaultLogsDirIsolatesProjectsWithoutSlug pins the fix for a
// cross-project state collision.
//
// This directory holds automation_queue.json, which carries the DEPENDENCY
// AUDIT. When a workflow set no tracker.project_slug, defaultLogsDir fell back
// to the bare shared ~/.itervox/logs — so every such project read and wrote
// one another's audit rows. Observed live: a Linear project inherited ten
// `demo-id-*` rows written by an unrelated `kind: memory` run, then asked
// Linear about issues that had never existed there on every refresh cycle,
// failing the whole batch because those ids are not valid Linear references.
func TestDefaultLogsDirIsolatesProjectsWithoutSlug(t *testing.T) {
	writeWorkflow := func(t *testing.T, dir string) string {
		t.Helper()
		path := filepath.Join(dir, "WORKFLOW.md")
		require.NoError(t, os.WriteFile(path, []byte(
			"---\nitervox_schema_version: 2\ntracker:\n  kind: linear\n---\n# p\n"), 0o644))
		return path
	}

	a := defaultLogsDir(writeWorkflow(t, t.TempDir()))
	b := defaultLogsDir(writeWorkflow(t, t.TempDir()))

	assert.NotEqual(t, a, b,
		"two slugless projects must not share a logs dir — that dir holds the dependency audit")

	home, err := os.UserHomeDir()
	require.NoError(t, err)
	shared := filepath.Join(home, ".itervox", "logs")
	assert.NotEqual(t, shared, a, "the fallback must not be the bare shared base")
	assert.True(t, strings.HasPrefix(a, shared), "but it must stay under it: %s", a)
}

// TestDefaultLogsDirIsStableForOneWorkflow — the path must not move between
// runs, or each restart would abandon the previous run's queue and audit.
func TestDefaultLogsDirIsStableForOneWorkflow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(path, []byte(
		"---\nitervox_schema_version: 2\ntracker:\n  kind: linear\n---\n# p\n"), 0o644))

	assert.Equal(t, defaultLogsDir(path), defaultLogsDir(path))
}

// TestDefaultLogsDirPrefersProjectSlug — the existing per-project layout is
// unchanged for anyone who sets a slug.
func TestDefaultLogsDirPrefersProjectSlug(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(path, []byte(
		"---\nitervox_schema_version: 2\ntracker:\n  kind: linear\n  project_slug: my-proj\n---\n# p\n"), 0o644))

	got := defaultLogsDir(path)
	assert.Contains(t, got, filepath.Join("logs", "linear", "my-proj"),
		"the slug stays in the path so the directory is recognisable: %s", got)

	// The slug alone is NOT a project identity: two checkouts of one repo, or
	// several repos driven by one Linear project, share a slug — and this
	// directory holds the dependency audit. The PID guard does not stop it
	// (it keys on the workflow's directory, so both checkouts pass).
	other := filepath.Join(t.TempDir(), "WORKFLOW.md")
	require.NoError(t, os.WriteFile(other, []byte(
		"---\nitervox_schema_version: 2\ntracker:\n  kind: linear\n  project_slug: my-proj\n---\n# p\n"), 0o644))
	assert.NotEqual(t, got, defaultLogsDir(other),
		"two checkouts sharing a project_slug must not share a logs dir")
}

// TestGeneratedWorkflowRootIsNamespaced pins that `itervox init` does not
// scaffold a colliding workspace root.
//
// It wrote `root: ~/.itervox/workspaces/<repo-basename>`, and because that is
// EXPLICIT, resolvePathValue never consults defaultWorkspaceRoot — so the
// per-project protection added for the default was bypassed on the very path
// itervox generates. Two checkouts named "api" under different owners shared
// a root, and every non-git directory shared "my-project" (scanRepo's
// fallback). Workspaces are keyed by issue identifier alone, so sharing a root
// means two agents in one directory and one project's auto_clear deleting the
// other's live worktree.
func TestGeneratedWorkflowRootIsNamespaced(t *testing.T) {
	sameName := repoInfo{ProjectName: "api", Owner: "orgA", Repo: "api", DefaultBranch: "main"}

	a := generateWorkflow("github", "claude", sameName, filepath.Join(t.TempDir(), "api", "WORKFLOW.md"))
	b := generateWorkflow("github", "claude", sameName, filepath.Join(t.TempDir(), "api", "WORKFLOW.md"))

	rootOf := func(s string) string {
		for _, line := range strings.Split(s, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "root:") {
				return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "root:"))
			}
		}
		return ""
	}

	ra, rb := rootOf(a), rootOf(b)
	require.NotEmpty(t, ra, "the scaffold must still set a workspace root")
	assert.NotEqual(t, ra, rb,
		"two same-named repos must not scaffold the same workspace root")
	assert.NotEqual(t, "~/.itervox/workspaces/api", ra,
		"the bare project name is exactly the colliding value this replaced")
	assert.True(t, strings.HasPrefix(ra, "~/.itervox/workspaces/"),
		"but it must stay under the conventional base: %s", ra)
}
