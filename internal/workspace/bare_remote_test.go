package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func initUpstream(t *testing.T, dir, marker string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(cmd.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	run("init", "-q", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "marker"), []byte(marker), 0o644))
	run("add", "-A")
	run("commit", "-qm", marker)
	return dir
}

// TestEnsureBareCloneRefusesDifferentRepository pins the fix for a severe
// cross-project collision.
//
// EnsureBareClone reused <root>/.bare whenever a HEAD file existed, without
// ever comparing remote.origin.url to the configured clone_url. Two projects
// sharing a workspace root therefore had the SECOND daemon silently operate on
// the FIRST one's repository: agents branched from, committed to and pushed
// the wrong repo, and worktree metadata collided for two repos' issue #1.
func TestEnsureBareCloneRefusesDifferentRepository(t *testing.T) {
	base := t.TempDir()
	upA := initUpstream(t, filepath.Join(base, "upA"), "REPO-A")
	upB := initUpstream(t, filepath.Join(base, "upB"), "REPO-B")
	root := filepath.Join(base, "shared-root")

	// Daemon A clones first.
	pathA, err := EnsureBareClone(context.Background(), root, upA)
	require.NoError(t, err)
	require.DirExists(t, pathA)

	// Daemon B, configured for a DIFFERENT repo, must not be handed A's clone.
	_, err = EnsureBareClone(context.Background(), root, upB)
	require.Error(t, err, "reusing another repository's clone must be refused, not silent")
	assert.Contains(t, err.Error(), "different repository")
	assert.Contains(t, err.Error(), "workspace.root",
		"the error must name the actual cause so the operator can fix it")
}

// TestEnsureBareCloneReusesSameRepository — the refusal must not break the
// normal path, or every restart would re-clone.
func TestEnsureBareCloneReusesSameRepository(t *testing.T) {
	base := t.TempDir()
	up := initUpstream(t, filepath.Join(base, "up"), "REPO")
	root := filepath.Join(base, "root")

	first, err := EnsureBareClone(context.Background(), root, up)
	require.NoError(t, err)
	second, err := EnsureBareClone(context.Background(), root, up)
	require.NoError(t, err, "the same repository must be reused, not re-cloned")
	assert.Equal(t, first, second)
}

// TestNormalizeRemoteURL — the same repository is spelled several ways, and
// treating those as different would force a needless re-clone or a spurious
// refusal.
func TestNormalizeRemoteURL(t *testing.T) {
	same := []string{
		"git@github.com:acme/api.git",
		"https://github.com/acme/api.git",
		"https://github.com/acme/api",
		"ssh://git@github.com/acme/api.git",
	}
	want := normalizeRemoteURL(same[0])
	for _, u := range same[1:] {
		assert.Equal(t, want, normalizeRemoteURL(u), "spelling %q must compare equal", u)
	}
	assert.NotEqual(t, want, normalizeRemoteURL("git@github.com:acme/other.git"))
	assert.NotEqual(t, want, normalizeRemoteURL("git@github.com:someone-else/api.git"))

	// A path segment must not be able to impersonate a host. Searching the
	// whole string for "@" made these compare EQUAL, so a bare clone of one
	// repository would be reused for a different one — the exact substitution
	// EnsureBareClone's check exists to prevent.
	assert.NotEqual(t,
		normalizeRemoteURL("https://gitlab.com/org/repo"),
		normalizeRemoteURL("https://evil.example.com/x@gitlab.com/org/repo"),
		"a path segment containing @ must not impersonate a host")

	// A port is part of the identity: two different endpoints on one host.
	assert.NotEqual(t,
		normalizeRemoteURL("https://host:8443/a/b"),
		normalizeRemoteURL("https://host/8443/a/b"),
		"a port must not collapse into the path")

	// scp-style host:path still normalises to the https spelling.
	assert.Equal(t,
		normalizeRemoteURL("git@github.com:acme/api.git"),
		normalizeRemoteURL("https://github.com/acme/api"))
}
