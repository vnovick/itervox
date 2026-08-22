package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeWorkflowAt(t *testing.T, dir, extra string) string {
	t.Helper()
	path := filepath.Join(dir, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(path, []byte(
		"---\nitervox_schema_version: 2\ntracker:\n  kind: github\n"+extra+"---\n# p\n"), 0o644))
	return path
}

// TestDefaultWorkspaceRootIsolatesProjects pins the fix for the most severe
// collision surface found in this audit.
//
// A workspace directory is keyed by issue IDENTIFIER alone, and GitHub
// identifiers are the repo-local issue number — "#1", "#2". Every GitHub repo
// has a #1. With a shared ~/.itervox/workspaces root, two daemons on two repos
// used the SAME directory for their respective issue #1: two agents checking
// out different code over each other, and one project's workspace.auto_clear
// deleting another project's live worktree mid-run.
func TestDefaultWorkspaceRootIsolatesProjects(t *testing.T) {
	a, err := Load(writeWorkflowAt(t, t.TempDir(), ""))
	require.NoError(t, err)
	b, err := Load(writeWorkflowAt(t, t.TempDir(), ""))
	require.NoError(t, err)

	assert.NotEqual(t, a.Workspace.Root, b.Workspace.Root,
		"two projects must not share a workspace root — directories are keyed by issue identifier, "+
			"and every GitHub repo has a #1")

	home, hErr := os.UserHomeDir()
	require.NoError(t, hErr)
	shared := filepath.Join(home, ".itervox", "workspaces")
	assert.NotEqual(t, shared, a.Workspace.Root, "must not be the bare shared root")
	assert.True(t, strings.HasPrefix(a.Workspace.Root, shared),
		"but must stay under it so existing tooling still finds workspaces: %s", a.Workspace.Root)
}

// TestDefaultWorkspaceRootIsStable — the path must not move between runs, or
// every restart would abandon the previous run's worktrees.
func TestDefaultWorkspaceRootIsStable(t *testing.T) {
	path := writeWorkflowAt(t, t.TempDir(), "")
	a, err := Load(path)
	require.NoError(t, err)
	b, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, a.Workspace.Root, b.Workspace.Root)
}

// TestExplicitWorkspaceRootIsUntouched — an operator who set workspace.root
// keeps exactly what they asked for.
func TestExplicitWorkspaceRootIsUntouched(t *testing.T) {
	dir := t.TempDir()
	explicit := filepath.Join(dir, "my-workspaces")
	cfg, err := Load(writeWorkflowAt(t, dir, "workspace:\n  root: "+explicit+"\n"))
	require.NoError(t, err)
	assert.Equal(t, explicit, cfg.Workspace.Root)
}
