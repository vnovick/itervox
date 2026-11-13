package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/gitexec"
)

// TestInitSuggestsIgnoredDotenvCopyFiles (#250): `itervox init` suggests
// .env and .env.local for workspace.copy_files only when they exist and git
// ignores them; a tracked .env, a symlink, or a directory outside git is
// never suggested, and nothing is written when none qualifies.
func TestInitSuggestsIgnoredDotenvCopyFiles(t *testing.T) {
	dir := t.TempDir()
	assert.Empty(t, detectCopyFiles(dir), "outside a git checkout nothing is suggested")

	run := func(args ...string) {
		out, err := gitexec.Command(context.Background(), dir, args...).CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	run("init", "-q", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".env\n.env.local\n"), 0o644))
	assert.Empty(t, detectCopyFiles(dir), "missing files are not suggested")

	require.NoError(t, os.WriteFile(filepath.Join(dir, ".env"), []byte("APP_MODE=local\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".env.local"), []byte("APP_FLAG=on\n"), 0o600))
	assert.Equal(t, []string{".env", ".env.local"}, detectCopyFiles(dir))

	// The whole scaffold: the suggestion lands in the project settings file
	// (#239) and loads as workspace.copy_files.
	t.Setenv(config.UserSettingsEnv, "off")
	origClaude, origCodex := listClaudeModels, listCodexModels
	listClaudeModels = func() []agent.ModelOption { return nil }
	listCodexModels = func() []agent.ModelOption { return nil }
	t.Cleanup(func() { listClaudeModels, listCodexModels = origClaude, origCodex })
	wf := filepath.Join(dir, "WORKFLOW.md")
	var out strings.Builder
	require.NoError(t, scaffoldWorkflow(wf, dir, "local", "claude", initOptions{}, &out))
	assert.Contains(t, out.String(), "copy_files : .env, .env.local")
	cfg, err := config.Load(wf)
	require.NoError(t, err)
	assert.Equal(t, []string{".env", ".env.local"}, cfg.Workspace.CopyFiles)
	assert.Contains(t, readString(t, config.ProjectSettingsPath(wf)), `"copy_files"`)

	run("-c", "user.email=t@example.com", "-c", "user.name=T", "add", "-f", ".env")
	assert.Equal(t, []string{".env.local"}, detectCopyFiles(dir), "a tracked .env is not suggested")

	require.NoError(t, os.Remove(filepath.Join(dir, ".env.local")))
	require.NoError(t, os.Symlink(filepath.Join(dir, ".env"), filepath.Join(dir, ".env.local")))
	assert.Empty(t, detectCopyFiles(dir), "a symlink is not suggested")

	path := filepath.Join(t.TempDir(), "WORKFLOW.md")
	with := generateWorkflow("github", "claude", repoInfo{ProjectName: "demo", Owner: "acme", Repo: "demo",
		DefaultBranch: "main", CopyFiles: []string{".env", ".env.local"}}, path)
	assert.Contains(t, with, "  copy_files: [\".env\", \".env.local\"]\n")
	assert.Contains(t, with, "the agent can read them")
	without := generateWorkflow("github", "claude", repoInfo{ProjectName: "demo", Owner: "acme", Repo: "demo", DefaultBranch: "main"}, path)
	assert.False(t, strings.Contains(without, "copy_files"), "empty by default")
}
