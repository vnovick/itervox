package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

// portTestManager is a directory-mode Manager over a fresh root with the
// port range lo-hi.
func portTestManager(t *testing.T, root string, lo, hi int) *Manager {
	t.Helper()
	cfg := &config.Config{}
	cfg.Workspace.Root = root
	cfg.Workspace.PortMin, cfg.Workspace.PortMax = lo, hi
	return NewManager(cfg)
}

// stubPortFree makes the bind probe report exactly the ports in busy as in
// use, for the rest of the test.
func stubPortFree(t *testing.T, busy ...int) {
	t.Helper()
	prev := portFree
	t.Cleanup(func() { portFree = prev })
	set := map[int]bool{}
	for _, p := range busy {
		set[p] = true
	}
	portFree = func(p int) bool { return !set[p] }
}

// TestWorkspacePortIsStableAndReleased (#250): each workspace gets the
// lowest free port of workspace.port_range that no other workspace holds;
// it stays the same on every later run while the workspace exists (even
// while something is listening on it — the issue's own dev server), the
// before_remove hook still sees it as ITERVOX_PORT, and removing the
// workspace releases it for the next issue.
func TestWorkspacePortIsStableAndReleased(t *testing.T) {
	root := t.TempDir()
	mgr := portTestManager(t, root, 41000, 41003)
	stubPortFree(t, 41000) // something else already listens on 41000

	ctx := context.Background()
	ws1, err := mgr.EnsureWorkspace(ctx, "ENG-1", "")
	require.NoError(t, err)
	assert.Equal(t, 41001, ws1.Port, "the lowest free port")
	ws2, err := mgr.EnsureWorkspace(ctx, "ENG-2", "")
	require.NoError(t, err)
	assert.Equal(t, 41002, ws2.Port, "a port another workspace holds is never handed out twice")

	stubPortFree(t, 41000, 41001, 41002, 41003) // ENG-1's dev server is up now
	again, err := mgr.EnsureWorkspace(ctx, "ENG-1", "")
	require.NoError(t, err)
	assert.Equal(t, 41001, again.Port, "stable for the issue while its workspace exists")
	assert.Equal(t, 41001, mgr.WorkspacePort("ENG-1"))

	out := filepath.Join(t.TempDir(), "port.txt")
	mgr.cfg.Hooks.BeforeRemove = `printf '%s' "$ITERVOX_PORT" > ` + "'" + out + "'"
	require.NoError(t, mgr.RemoveWorkspace(ctx, "ENG-1", ""))
	got, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, "41001", string(got), "before_remove runs with the workspace's port")
	assert.Equal(t, 0, mgr.WorkspacePort("ENG-1"), "released when the workspace is cleared")
	ledger, err := os.ReadFile(filepath.Join(root, PortsFile))
	require.NoError(t, err)
	assert.NotContains(t, string(ledger), "ENG-1")

	stubPortFree(t, 41000)
	ws3, err := mgr.EnsureWorkspace(ctx, "ENG-3", "")
	require.NoError(t, err)
	assert.Equal(t, 41001, ws3.Port, "a released port is handed out again")

	// A workspace removed by another path (itervox clear, a manual rm)
	// releases its port too.
	require.NoError(t, os.RemoveAll(ws2.Path))
	assert.Equal(t, 0, mgr.WorkspacePort("ENG-2"))
	ws4, err := mgr.EnsureWorkspace(ctx, "ENG-4", "")
	require.NoError(t, err)
	assert.Equal(t, 41002, ws4.Port)

	stubPortFree(t, 41000, 41003)
	_, err = mgr.AssignPort("ENG-5")
	require.Error(t, err, "an exhausted range is an error")
	ws5, err := mgr.EnsureWorkspace(ctx, "ENG-5", "")
	require.NoError(t, err, "an exhausted range never fails the dispatch")
	assert.Zero(t, ws5.Port)
}

// TestWorkspacePortSurvivesRestart (#250): the port is persisted with the
// workspace, so a new Manager (a restarted daemon) hands each existing
// workspace the port it had, not the lowest free one, and keeps it in a
// worktree-mode layout too.
func TestWorkspacePortSurvivesRestart(t *testing.T) {
	root := t.TempDir()
	stubPortFree(t)
	ctx := context.Background()
	before := portTestManager(t, root, 41000, 41999)
	_, err := before.EnsureWorkspace(ctx, "ENG-1", "")
	require.NoError(t, err)
	ws2, err := before.EnsureWorkspace(ctx, "ENG-2", "")
	require.NoError(t, err)
	require.Equal(t, 41001, ws2.Port)
	require.NoError(t, before.RemoveWorkspace(ctx, "ENG-1", "")) // 41000 is free again

	after := portTestManager(t, root, 41000, 41999) // the restarted daemon
	assert.Equal(t, 41001, after.WorkspacePort("ENG-2"))
	stubPortFree(t, 41001) // ENG-2's dev server survived the restart
	ws2b, err := after.EnsureWorkspace(ctx, "ENG-2", "")
	require.NoError(t, err)
	assert.Equal(t, 41001, ws2b.Port, "the recorded port, not the lowest free one")

	data, err := os.ReadFile(filepath.Join(root, PortsFile))
	require.NoError(t, err)
	assert.True(t, strings.Contains(string(data), `"ENG-2": 41001`), "persisted: %s", data)

	// A changed range re-assigns a port that is no longer in it.
	narrowed := portTestManager(t, root, 42000, 42010)
	ws2c, err := narrowed.EnsureWorkspace(ctx, "ENG-2", "")
	require.NoError(t, err)
	assert.Equal(t, 42000, ws2c.Port)

	// Worktree mode keeps the ledger beside the worktrees, not in the root.
	wtRoot := t.TempDir()
	cfg := &config.Config{}
	cfg.Workspace.Root, cfg.Workspace.Worktree = wtRoot, true
	cfg.Workspace.PortMin, cfg.Workspace.PortMax = 41000, 41999
	require.NoError(t, os.MkdirAll(filepath.Join(wtRoot, "worktrees", "ENG-9"), 0o755))
	p, err := NewManager(cfg).AssignPort("ENG-9")
	require.NoError(t, err)
	assert.Equal(t, p, NewManager(cfg).WorkspacePort("ENG-9"))
	assert.FileExists(t, filepath.Join(wtRoot, "worktrees", PortsFile))
	assert.NoFileExists(t, filepath.Join(wtRoot, PortsFile))
}
