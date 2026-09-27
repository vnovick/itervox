package workspace_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/gitexec"
	"github.com/vnovick/itervox/internal/workspace"
)

// setupUpstreamRepo creates a bare "upstream" repo with one commit,
// simulating a GitHub remote on local disk.
func setupUpstreamRepo(t *testing.T) string {
	t.Helper()
	upstream := filepath.Join(t.TempDir(), "upstream.git")
	work := t.TempDir()

	cmds := [][]string{
		{"init", "-b", "main", work},
		{"-C", work, "config", "user.email", "test@test.com"},
		{"-C", work, "config", "user.name", "Test"},
		{"-C", work, "commit", "--allow-empty", "-m", "init"},
		{"clone", "--bare", work, upstream},
	}
	for _, args := range cmds {
		out, err := gitexec.Command(context.Background(), work, args...).CombinedOutput()
		require.NoError(t, err, "setup: %s: %s", args, out)
	}
	return upstream
}

func TestEnsureBareClone_ClonesFromRemote(t *testing.T) {
	upstream := setupUpstreamRepo(t)
	root := t.TempDir()

	barePath, err := workspace.EnsureBareClone(context.Background(), root, upstream)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, ".bare"), barePath)
	assert.DirExists(t, barePath)

	// Verify it's a bare repo
	cmd := gitexec.Command(context.Background(), barePath, "rev-parse", "--is-bare-repository")
	out, err := cmd.Output()
	require.NoError(t, err)
	assert.Equal(t, "true", strings.TrimSpace(string(out)))
}

func TestEnsureBareClone_ReusesExisting(t *testing.T) {
	upstream := setupUpstreamRepo(t)
	root := t.TempDir()

	path1, err := workspace.EnsureBareClone(context.Background(), root, upstream)
	require.NoError(t, err)

	// Write sentinel to prove we don't re-clone
	sentinel := filepath.Join(path1, "sentinel")
	require.NoError(t, os.WriteFile(sentinel, []byte("x"), 0o644))

	path2, err := workspace.EnsureBareClone(context.Background(), root, upstream)
	require.NoError(t, err)
	assert.Equal(t, path1, path2)
	assert.FileExists(t, sentinel)
}

func TestEnsureBareClone_EmptyURLNoExistingBareErrors(t *testing.T) {
	root := t.TempDir()
	_, err := workspace.EnsureBareClone(context.Background(), root, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no clone_url configured")
}

func TestFetchBare(t *testing.T) {
	upstream := setupUpstreamRepo(t)
	root := t.TempDir()

	barePath, err := workspace.EnsureBareClone(context.Background(), root, upstream)
	require.NoError(t, err)

	// Add a new commit to upstream
	work := t.TempDir()
	cmds := [][]string{
		{"clone", upstream, work},
		{"-C", work, "config", "user.email", "test@test.com"},
		{"-C", work, "config", "user.name", "Test"},
		{"-C", work, "commit", "--allow-empty", "-m", "second"},
		{"-C", work, "push", "origin", "main"},
	}
	for _, args := range cmds {
		out, err := gitexec.Command(context.Background(), root, args...).CombinedOutput()
		require.NoError(t, err, "push: %s: %s", args, out)
	}

	err = workspace.FetchBare(context.Background(), barePath)
	require.NoError(t, err)

	// Verify we have the new commit
	cmd := gitexec.Command(context.Background(), barePath, "log", "--oneline", "--all")
	out, err := cmd.Output()
	require.NoError(t, err)
	assert.Contains(t, string(out), "second")
}

func TestBarePath(t *testing.T) {
	assert.Equal(t, "/tmp/ws/.bare", workspace.BarePath("/tmp/ws"))
}
