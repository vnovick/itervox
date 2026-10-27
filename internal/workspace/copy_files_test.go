package workspace_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/logging"
	"github.com/vnovick/itervox/internal/workspace"
)

func writeModeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), mode))
	require.NoError(t, os.Chmod(path, mode))
}

// copyFixture builds the main checkout (src: holds WORKFLOW.md and a
// committed .gitignore) and a clone of it as the worktree root, and returns
// a worktree-mode Manager over them with copy_files set to patterns.
func copyFixture(t *testing.T, gitignore string, patterns ...string) (mgr *workspace.Manager, src string) {
	t.Helper()
	src = t.TempDir()
	git(t, src, "init", "-b", "main")
	git(t, src, "config", "user.email", "test@test.com")
	git(t, src, "config", "user.name", "Test")
	writeModeFile(t, filepath.Join(src, ".gitignore"), gitignore, 0o644)
	writeModeFile(t, filepath.Join(src, "tracked.txt"), "committed\n", 0o644)
	writeModeFile(t, filepath.Join(src, "WORKFLOW.md"), "---\n---\n", 0o644)
	git(t, src, "add", ".gitignore", "tracked.txt", "WORKFLOW.md")
	git(t, src, "commit", "-m", "init")

	root := filepath.Join(t.TempDir(), "root")
	git(t, filepath.Dir(root), "clone", "-q", src, root)
	git(t, root, "config", "user.email", "test@test.com")
	git(t, root, "config", "user.name", "Test")

	cfg := &config.Config{WorkflowPath: filepath.Join(src, "WORKFLOW.md")}
	cfg.Workspace.Root = root
	cfg.Workspace.Worktree = true
	cfg.Workspace.CopyFiles = patterns
	cfg.Workspace.CopyFilesMaxBytes = config.DefaultCopyFilesMaxBytes
	return workspace.NewManager(cfg), src
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

// TestCopyFilesCopiesOnlyIgnoredFiles (#250): copy_files copies the matches
// git ignores — in the main checkout AND in the worktree — into a new
// worktree before after_create, skips a tracked or unignored match with a
// reason, keeps modes, never leaves anything committable behind, and on a
// later run refreshes only what the main checkout has newer.
func TestCopyFilesCopiesOnlyIgnoredFiles(t *testing.T) {
	mgr, src := copyFixture(t, ".env\n.env.local\nconfig/local/\n",
		".env", ".env.local", "tracked.txt", "notignored.txt", "config/local", "local-only.cfg", "missing.txt")
	writeModeFile(t, filepath.Join(src, ".env"), "APP_MODE=local\n", 0o600)
	writeModeFile(t, filepath.Join(src, ".env.local"), "APP_FLAG=on\n", 0o640)
	writeModeFile(t, filepath.Join(src, "config", "local", "settings.json"), "{}\n", 0o644)
	writeModeFile(t, filepath.Join(src, "tracked.txt"), "edited in the main checkout\n", 0o644)
	writeModeFile(t, filepath.Join(src, "notignored.txt"), "untracked, not ignored\n", 0o644)
	// Ignored only by the main checkout's own exclude file: the worktree's
	// repository does not ignore it, so it must not be copied there.
	writeModeFile(t, filepath.Join(src, ".git", "info", "exclude"), "local-only.cfg\n", 0o644)
	writeModeFile(t, filepath.Join(src, "local-only.cfg"), "x\n", 0o644)

	ws, err := mgr.EnsureWorkspace(context.Background(), "ENG-1", "itervox/eng-1")
	require.NoError(t, err)
	require.True(t, ws.CreatedNow)
	require.NoError(t, ws.CopyFilesErr)
	require.NotNil(t, ws.CopyFiles)
	rep := ws.CopyFiles
	assert.Equal(t, []string{".env", ".env.local", "config/local/settings.json"}, rep.Copied)
	assert.Equal(t, []string{"missing.txt"}, rep.Unmatched)
	skipped := strings.Join(rep.Skipped, "\n")
	assert.Contains(t, skipped, "tracked.txt: not gitignored (tracked or unignored) in the main checkout")
	assert.Contains(t, skipped, "notignored.txt: not gitignored (tracked or unignored) in the main checkout")
	assert.Contains(t, skipped, "local-only.cfg: not gitignored (tracked or unignored) in the worktree")

	assert.Equal(t, "APP_MODE=local\n", readFile(t, filepath.Join(ws.Path, ".env")))
	assert.Equal(t, "committed\n", readFile(t, filepath.Join(ws.Path, "tracked.txt")), "a tracked file is never overwritten")
	assert.NoFileExists(t, filepath.Join(ws.Path, "notignored.txt"))
	assert.NoFileExists(t, filepath.Join(ws.Path, "local-only.cfg"))
	for rel, mode := range map[string]os.FileMode{".env": 0o600, ".env.local": 0o640} {
		fi, err := os.Stat(filepath.Join(ws.Path, rel))
		require.NoError(t, err)
		assert.Equal(t, mode, fi.Mode().Perm(), "mode of %s", rel)
	}
	assert.Empty(t, git(t, ws.Path, "status", "--porcelain"), "nothing copied can be committed")

	// A later run: the worktree's own edit of .env is newer and is kept;
	// .env.local changed in the main checkout and is refreshed.
	writeModeFile(t, filepath.Join(ws.Path, ".env"), "APP_MODE=edited-in-worktree\n", 0o600)
	future := time.Now().Add(time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(ws.Path, ".env"), future, future))
	writeModeFile(t, filepath.Join(src, ".env.local"), "APP_FLAG=off\n", 0o640)
	later := time.Now().Add(2 * time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(src, ".env.local"), later, later))

	ws2, err := mgr.EnsureWorkspace(context.Background(), "ENG-1", "itervox/eng-1")
	require.NoError(t, err)
	require.False(t, ws2.CreatedNow)
	require.NotNil(t, ws2.CopyFiles)
	assert.Equal(t, []string{".env.local"}, ws2.CopyFiles.Copied)
	assert.Equal(t, []string{".env", "config/local/settings.json"}, ws2.CopyFiles.Kept)
	assert.Equal(t, "APP_MODE=edited-in-worktree\n", readFile(t, filepath.Join(ws.Path, ".env")))
	assert.Equal(t, "APP_FLAG=off\n", readFile(t, filepath.Join(ws.Path, ".env.local")))
}

// TestCopyFilesRefusesPathsOutsideRepo (#250): an entry that is absolute or
// climbs out of the repository root fails the config load, and the copy
// itself still refuses one (and never reads through a symlinked directory
// that points outside the repository).
func TestCopyFilesRefusesPathsOutsideRepo(t *testing.T) {
	t.Setenv(config.UserSettingsEnv, "off")
	for _, entry := range []string{"../outside.env", "/etc/hosts", "config/../../outside.env", ".."} {
		dir := t.TempDir()
		path := filepath.Join(dir, "WORKFLOW.md")
		writeModeFile(t, path, "---\nitervox_schema_version: 2\ntracker:\n  kind: memory\nworkspace:\n  copy_files: ["+
			`"`+entry+`"`+"]\n---\nprompt\n", 0o644)
		_, err := config.Load(path)
		require.Error(t, err, "entry %q must be refused at load", entry)
		assert.Contains(t, err.Error(), "workspace.copy_files[0]", entry)
	}

	outside := t.TempDir()
	writeModeFile(t, filepath.Join(outside, "outside.env"), "APP_OUTSIDE=1\n", 0o644)
	writeModeFile(t, filepath.Join(outside, "secret.env"), "APP_OUTSIDE=2\n", 0o644)
	mgr, src := copyFixture(t, "*.env\nlinked/\n")
	require.NoError(t, os.Symlink(outside, filepath.Join(src, "linked")))
	ws, err := mgr.EnsureWorkspace(context.Background(), "ENG-2", "itervox/eng-2")
	require.NoError(t, err)

	rep, err := workspace.CopyFiles(context.Background(), src, ws.Path,
		[]string{"../outside.env", "linked/secret.env", "linked"}, 0)
	require.NoError(t, err)
	assert.Empty(t, rep.Copied)
	skipped := strings.Join(rep.Skipped, "\n")
	assert.Contains(t, skipped, "../outside.env: leaves the repository root")
	assert.Contains(t, skipped, "linked/secret.env: reached through a symlink (not followed)")
	assert.Contains(t, skipped, "linked: symlink (not followed)")
	assert.NoFileExists(t, filepath.Join(ws.Path, "linked", "secret.env"))
	assert.NoFileExists(t, filepath.Join(filepath.Dir(ws.Path), "outside.env"))
}

// TestCopyFilesSizeCapAndSymlinks (#250): directories are copied
// recursively up to the size cap, a cap hit is reported, symlinks (a match
// that is one, and one inside a copied directory) are never followed, and
// file modes are kept.
func TestCopyFilesSizeCapAndSymlinks(t *testing.T) {
	mgr, src := copyFixture(t, "big/\nlinks/\n.env*\n")
	ws, err := mgr.EnsureWorkspace(context.Background(), "ENG-3", "itervox/eng-3")
	require.NoError(t, err)

	block := strings.Repeat("x", 600)
	writeModeFile(t, filepath.Join(src, "big", "a.bin"), block, 0o644)
	writeModeFile(t, filepath.Join(src, "big", "b.bin"), block, 0o644)
	writeModeFile(t, filepath.Join(src, "big", "nested", "c.bin"), block, 0o644)
	rep, err := workspace.CopyFiles(context.Background(), src, ws.Path, []string{"big"}, 1000)
	require.NoError(t, err)
	assert.True(t, rep.CapHit, "the cap hit is reported")
	assert.Equal(t, []string{"big/a.bin"}, rep.Copied)
	assert.Equal(t, int64(600), rep.Bytes)
	skipped := strings.Join(rep.Skipped, "\n")
	assert.Contains(t, skipped, "big/b.bin: size cap reached")
	assert.Contains(t, skipped, "big/nested/c.bin: size cap reached")
	assert.FileExists(t, filepath.Join(ws.Path, "big", "a.bin"))
	assert.NoFileExists(t, filepath.Join(ws.Path, "big", "b.bin"))

	outside := t.TempDir()
	writeModeFile(t, filepath.Join(outside, "target.txt"), "outside the repository\n", 0o644)
	writeModeFile(t, filepath.Join(src, "links", "run.sh"), "#!/bin/sh\n", 0o755)
	writeModeFile(t, filepath.Join(src, "links", "private.txt"), "p\n", 0o600)
	require.NoError(t, os.Symlink(filepath.Join(outside, "target.txt"), filepath.Join(src, "links", "escape.txt")))
	writeModeFile(t, filepath.Join(src, ".env"), "APP_MODE=local\n", 0o644)
	require.NoError(t, os.Symlink(filepath.Join(src, ".env"), filepath.Join(src, ".env.link")))

	rep, err = workspace.CopyFiles(context.Background(), src, ws.Path, []string{"links", ".env.link"}, 0)
	require.NoError(t, err)
	assert.False(t, rep.CapHit)
	assert.Equal(t, []string{"links/private.txt", "links/run.sh"}, rep.Copied)
	skipped = strings.Join(rep.Skipped, "\n")
	assert.Contains(t, skipped, "links/escape.txt: symlink (not followed)")
	assert.Contains(t, skipped, ".env.link: symlink (not followed)")
	_, err = os.Lstat(filepath.Join(ws.Path, "links", "escape.txt"))
	assert.True(t, os.IsNotExist(err), "a symlink is never copied or followed")
	_, err = os.Lstat(filepath.Join(ws.Path, ".env.link"))
	assert.True(t, os.IsNotExist(err))
	for rel, mode := range map[string]os.FileMode{"links/run.sh": 0o755, "links/private.txt": 0o600} {
		fi, err := os.Stat(filepath.Join(ws.Path, filepath.FromSlash(rel)))
		require.NoError(t, err)
		assert.Equal(t, mode, fi.Mode().Perm(), "mode of %s", rel)
	}
}

// TestCopiedDotenvValuesAreRedacted (#250): the values of a copied dotenv
// file are registered with the log redactor; a copied file that is not a
// dotenv file registers nothing. The fixture values are plain words, which
// no redaction pattern matches on its own.
func TestCopiedDotenvValuesAreRedacted(t *testing.T) {
	mgr, src := copyFixture(t, ".env.local\nnotes.txt\n")
	ws, err := mgr.EnsureWorkspace(context.Background(), "ENG-4", "itervox/eng-4")
	require.NoError(t, err)

	quoted := "quiet" + "-harbor-" + "lantern"
	plain := "wiggly" + "-otter-" + "fixture"
	notDotenv := "sleepy" + "-badger-" + "notes"
	for _, v := range []string{quoted, plain, notDotenv} {
		require.Equal(t, "x "+v, logging.RedactString("x "+v), "fixture %q must not be redacted by a pattern alone", v)
	}
	writeModeFile(t, filepath.Join(src, ".env.local"), "# local\nAPP_NAME=\""+quoted+"\"\nexport APP_OTHER="+plain+"\nAPP_SHORT=ab\n", 0o600)
	writeModeFile(t, filepath.Join(src, "notes.txt"), "APP_NOTE="+notDotenv+"\n", 0o644)

	rep, err := workspace.CopyFiles(context.Background(), src, ws.Path, []string{".env.local", "notes.txt"}, 0)
	require.NoError(t, err)
	assert.Equal(t, []string{".env.local", "notes.txt"}, rep.Copied)
	assert.Equal(t, 3, rep.Secrets)

	for _, v := range []string{quoted, plain} {
		out := logging.RedactString("agent printed " + v + " to the log")
		assert.NotContains(t, out, v)
		assert.Contains(t, out, "***")
	}
	assert.Equal(t, "agent printed "+notDotenv, logging.RedactString("agent printed "+notDotenv),
		"only dotenv files are registered")
}
