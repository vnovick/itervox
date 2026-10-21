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

// TestRemoteHeadSHA (#173): the head a run left on the remote is the pushed
// commit, not an unpushed local one; an unknown branch reads as "".
func TestRemoteHeadSHA(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	ws := filepath.Join(root, "ws")
	require.NoError(t, os.MkdirAll(ws, 0o755))
	gitIn(t, root, "init", "--bare", "-q", remote)
	gitIn(t, ws, "init", "-q", "-b", "feat")
	gitIn(t, ws, "remote", "add", "origin", remote)
	gitIn(t, ws, "config", "user.email", "test@test.com")
	gitIn(t, ws, "config", "user.name", "test")
	gitIn(t, ws, "commit", "-q", "--allow-empty", "-m", "one")
	gitIn(t, ws, "push", "-q", "origin", "feat")
	pushed := headOf(t, ws)

	assert.Equal(t, pushed, workspace.RemoteHeadSHA(context.Background(), ws, "feat"))
	gitIn(t, ws, "commit", "-q", "--allow-empty", "-m", "unpushed")
	assert.Equal(t, pushed, workspace.RemoteHeadSHA(context.Background(), ws, "feat"), "unpushed commits are not the PR head")
	assert.Empty(t, workspace.RemoteHeadSHA(context.Background(), ws, "nope"))
	assert.Empty(t, workspace.RemoteHeadSHA(context.Background(), "", "feat"))
}

func headOf(t *testing.T, dir string) string {
	t.Helper()
	out, err := gitexec.Command(context.Background(), dir, "rev-parse", "HEAD").Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}
