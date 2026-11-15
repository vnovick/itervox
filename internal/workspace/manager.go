package workspace

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/gitexec"
)

// Provider defines the workspace operations needed by the orchestrator.
type Provider interface {
	EnsureWorkspace(ctx context.Context, identifier, branchName string) (Workspace, error)
	RemoveWorkspace(ctx context.Context, identifier, branchName string) error
	ResolvePath(identifier string) string
}

// StackedProvider is an OPTIONAL interface for providers that can base a new
// worktree on a branch other than workspace.base_branch — the mechanism behind
// stacked PRs, where an issue's work sits on top of its blocker's branch so
// the diff shows only the increment.
//
// Optional, and type-asserted rather than added to Provider, for the same
// reason tracker.RateLimiter is: widening a small interface with several
// implementations (including every test fake) forces unrelated churn on all of
// them. A provider that does not implement this simply never stacks, which is
// the correct fallback.
type StackedProvider interface {
	// EnsureWorkspaceFrom is EnsureWorkspace with an explicit start point.
	// An empty or non-existent startPoint must behave exactly like
	// EnsureWorkspace — stacking is best-effort and must never be the reason
	// a dispatch fails.
	EnsureWorkspaceFrom(ctx context.Context, identifier, branchName, startPoint string) (Workspace, error)
}

// Workspace represents a resolved per-issue workspace directory.
type Workspace struct {
	Path       string
	Identifier string
	CreatedNow bool
	// StackedOn is the branch this worktree is stacked on (#73): the start
	// point EnsureWorkspaceFrom was asked for, when that branch is in the
	// worktree's history (new or reused) and has commits that
	// workspace.base_branch lacks. Empty otherwise, in directory mode, and
	// for EnsureWorkspace.
	StackedOn string
	// BareFetchFailure is set when creating this worktree's fetch of the
	// shared bare clone failed and that failure was not yet reported since
	// the last successful fetch (#195). The caller records it once in recent
	// failures; the worktree itself was created from the last fetched base.
	BareFetchFailure string
	// Port is the workspace's TCP port from workspace.port_range (#250),
	// exported to agents and hooks as ITERVOX_PORT; 0 when none could be
	// assigned.
	Port int
	// CopyFiles is what workspace.copy_files did for this run (#250); nil
	// when copy_files is empty or this is not a worktree. CopyFilesErr is set
	// when the pass could not run at all.
	CopyFiles    *CopyFilesReport
	CopyFilesErr error
}

// PortProvider is an OPTIONAL interface for providers that assign each
// workspace a port (#250). Type-asserted for the same reason as
// StackedProvider.
type PortProvider interface {
	// WorkspacePort returns identifier's workspace port, or 0.
	WorkspacePort(identifier string) int
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
	return m.EnsureWorkspaceFrom(ctx, identifier, branchName, "")
}

// EnsureWorkspaceFrom implements StackedProvider: it is EnsureWorkspace with
// an explicit start point for the new branch.
//
// startPoint is ADVISORY. It is used only when it names a ref that actually
// exists, because the branch it points at may legitimately be absent — the
// blocker may not have been dispatched yet, may have been worked on a
// different machine, or may have had its worktree cleared. Stacking is a
// review-ergonomics improvement; it must never be the reason a dispatch
// fails, so an unresolvable start point silently falls back to base_branch.
func (m *Manager) EnsureWorkspaceFrom(ctx context.Context, identifier, branchName, startPoint string) (Workspace, error) {
	var ws Workspace
	var err error
	if m.cfg.Workspace.Worktree {
		ws, err = m.ensureWorktree(ctx, identifier, branchName, startPoint)
		if err == nil {
			m.copyFilesInto(ctx, &ws)
		}
	} else {
		// Directory mode has no branches, so there is nothing to stack on.
		ws, err = m.ensureDirectory(identifier)
	}
	if err != nil {
		return ws, err
	}
	// #250: a port problem never fails the dispatch; the run goes without
	// ITERVOX_PORT and the warning says why.
	if port, perr := m.AssignPort(identifier); perr != nil {
		slog.Warn("workspace: no port assigned", "identifier", identifier, "error", perr)
	} else {
		ws.Port = port
	}
	return ws, nil
}

// copyFilesInto runs workspace.copy_files for the worktree ws (#250), from
// the main checkout: the git top level of the directory holding WORKFLOW.md.
func (m *Manager) copyFilesInto(ctx context.Context, ws *Workspace) {
	patterns := m.cfg.Workspace.CopyFiles
	if len(patterns) == 0 {
		return
	}
	src, err := m.copySource(ctx)
	if err != nil {
		ws.CopyFilesErr = err
		return
	}
	dst, err := filepath.Abs(ws.Path)
	if err != nil {
		ws.CopyFilesErr = err
		return
	}
	rep, err := CopyFiles(ctx, src, dst, patterns, int64(m.cfg.Workspace.CopyFilesMaxBytes))
	ws.CopyFiles, ws.CopyFilesErr = &rep, err
}

// copySource is the main checkout copy_files reads from.
func (m *Manager) copySource(ctx context.Context) (string, error) {
	if m.cfg.WorkflowPath == "" {
		return "", errors.New("workspace: copy_files: no WORKFLOW.md path to find the main checkout from")
	}
	dir, err := filepath.Abs(filepath.Dir(m.cfg.WorkflowPath))
	if err != nil {
		return "", err
	}
	out, err := gitexec.Command(ctx, dir, "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("workspace: copy_files: %s is not in a git checkout: %w", dir, err)
	}
	return filepath.Clean(strings.TrimSpace(string(out))), nil
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

	// #250: before_remove still sees the workspace's port; it is released
	// once the workspace is gone.
	hookCtx := WithHookEnv(ctx, PortEnv(m.WorkspacePort(identifier))...)
	defer func() {
		if _, err := os.Stat(hookPath); errors.Is(err, os.ErrNotExist) {
			m.releasePort(identifier)
		}
	}()

	if m.cfg.Hooks.BeforeRemove != "" {
		// logFn is intentionally omitted: at cleanup time the per-issue log buffer
		// entry may already be removed, so there is no reliable destination for
		// hook output. Hook failures are still surfaced via slog.Warn below.
		if err := RunHook(hookCtx, m.cfg.Hooks.BeforeRemove, hookPath, m.cfg.Hooks.TimeoutMs); err != nil {
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
