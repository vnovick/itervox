package workspace

import (
	"context"
	"log/slog"
	"os"

	"github.com/vnovick/itervox/internal/config"
)

// Provider defines the workspace operations needed by the orchestrator.
type Provider interface {
	EnsureWorkspace(ctx context.Context, identifier, branchName string) (Workspace, error)
	RemoveWorkspace(ctx context.Context, identifier, branchName string) error
	ResolvePath(identifier string) string
}

// Workspace represents a resolved per-issue workspace directory.
type Workspace struct {
	Path       string
	Identifier string
	CreatedNow bool
}

// Manager handles creation, reuse, and removal of per-issue workspace directories.
type Manager struct {
	cfg *config.Config
}

// NewManager constructs a Manager using the given config.
func NewManager(cfg *config.Config) *Manager {
	return &Manager{cfg: cfg}
}

// EnsureWorkspace creates or reuses the workspace for the given identifier.
// When cfg.Workspace.Worktree is true, a git worktree is used (branchName is
// the desired branch). Otherwise the legacy directory-based path is used and
// both ctx and branchName are ignored.
func (m *Manager) EnsureWorkspace(ctx context.Context, identifier, branchName string) (Workspace, error) {
	if m.cfg.Workspace.Worktree {
		return m.ensureWorktree(ctx, identifier, branchName)
	}
	return m.ensureDirectory(identifier)
}

// ensureDirectory is the legacy implementation of EnsureWorkspace: it creates
// or reuses a plain directory under workspace.root.
func (m *Manager) ensureDirectory(identifier string) (Workspace, error) {
	root := m.cfg.Workspace.Root
	path := WorkspacePath(root, identifier)

	info, err := os.Stat(path)
	if err == nil {
		if !info.IsDir() {
			// A non-directory exists at the path — remove it and create fresh.
			if err := os.Remove(path); err != nil {
				return Workspace{}, err
			}
		}
	}

	if err := os.MkdirAll(path, 0o755); err != nil {
		return Workspace{}, err
	}

	if err := AssertContained(root, path); err != nil {
		_ = os.RemoveAll(path)
		return Workspace{}, err
	}

	createdNow := info == nil || !info.IsDir()
	return Workspace{Path: path, Identifier: identifier, CreatedNow: createdNow}, nil
}

// ResolvePath returns the workspace directory path for the given identifier
// without creating it. This mirrors the path resolution used by
// EnsureWorkspace and RemoveWorkspace and is intended for callers that
// need to read files inside an already-provisioned workspace (e.g. agent
// handoff bookkeeping after a worker has exited).
func (m *Manager) ResolvePath(identifier string) string {
	root := m.cfg.Workspace.Root
	if m.cfg.Workspace.Worktree {
		return worktreePath(root, identifier)
	}
	return WorkspacePath(root, identifier)
}

// RemoveWorkspace deletes the workspace for identifier.
// When cfg.Workspace.Worktree is true, the git worktree is removed (branchName
// is required). Otherwise the legacy directory is removed and branchName is
// ignored. Safe to call when the workspace does not exist.
// If cfg.Hooks.BeforeRemove is set, the hook is run inside the workspace
// directory before deletion; a hook failure is logged but removal proceeds.
func (m *Manager) RemoveWorkspace(ctx context.Context, identifier, branchName string) error {
	root := m.cfg.Workspace.Root
	var hookPath string
	if m.cfg.Workspace.Worktree {
		hookPath = worktreePath(root, identifier)
	} else {
		hookPath = WorkspacePath(root, identifier)
	}

	if m.cfg.Hooks.BeforeRemove != "" {
		// logFn is intentionally omitted: at cleanup time the per-issue log buffer
		// entry may already be removed, so there is no reliable destination for
		// hook output. Hook failures are still surfaced via slog.Warn below.
		if err := RunHook(ctx, m.cfg.Hooks.BeforeRemove, hookPath, m.cfg.Hooks.TimeoutMs); err != nil {
			// Hook failure is non-fatal: log and proceed with removal so a broken
			// hook cannot permanently prevent workspace cleanup.
			slog.Warn("workspace: before_remove hook failed, proceeding with removal",
				"identifier", identifier, "error", err)
		}
	}

	if m.cfg.Workspace.Worktree {
		return m.removeWorktree(ctx, identifier, branchName)
	}
	return os.RemoveAll(hookPath)
}
