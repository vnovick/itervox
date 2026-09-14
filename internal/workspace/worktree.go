package workspace

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"
)

// defaultBranches is the set of branch names treated as "no feature branch".
// If issue.BranchName is one of these, ResolveWorktreeBranch falls back to
// the itervox/<slug> convention.
var defaultBranches = map[string]bool{
	"":        true,
	"main":    true,
	"master":  true,
	"develop": true,
	"HEAD":    true,
}

// IsDefaultBranch reports whether branch is a well-known default branch name
// (empty string, "main", "master", "develop", or "HEAD"). These names are
// treated as "no feature branch" and should never be used as worktree branch names.
func IsDefaultBranch(branch string) bool {
	return defaultBranches[branch]
}

// SlugifyIdentifier lowercases the identifier and replaces any non-alphanumeric
// character with a hyphen, trimming leading/trailing hyphens and deduplicating consecutive hyphens.
// Examples: "ENG-123" → "eng-123", "My Issue #7" → "my-issue-7".
func SlugifyIdentifier(id string) string {
	id = strings.TrimSpace(strings.ToLower(id))
	var b strings.Builder
	for _, r := range id {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	result := strings.Trim(b.String(), "-")
	// Deduplicate consecutive hyphens
	for strings.Contains(result, "--") {
		result = strings.ReplaceAll(result, "--", "-")
	}
	return result
}

// ResolveWorktreeBranch returns the git branch name to use for a worktree.
// Priority:
//  1. branchName if non-nil, non-empty, and not a default branch (main/master/develop)
//  2. "itervox/" + SlugifyIdentifier(identifier)
func ResolveWorktreeBranch(branchName *string, identifier string) string {
	if branchName != nil && !IsDefaultBranch(*branchName) {
		return *branchName
	}
	return "itervox/" + SlugifyIdentifier(identifier)
}

// worktreePath returns the absolute path for the worktree for the given
// identifier: <root>/worktrees/<identifier>.
// This is unexported — callers within this package use it; the orchestrator
// gets paths via Manager.EnsureWorkspace return value.
func worktreePath(root, identifier string) string {
	return filepath.Join(root, "worktrees", identifier)
}

// resolveGitDir returns the git directory to use for worktree operations.
// When cfg.Workspace.CloneURL is set, it ensures a bare clone exists at
// <root>/.bare/ and fetches latest refs, returning the bare path. Otherwise
// it returns root (the legacy behavior where root is itself a git repo).
func (m *Manager) resolveGitDir(ctx context.Context, root, branchName string) (string, error) {
	if m.cfg.Workspace.CloneURL != "" {
		barePath, err := EnsureBareClone(ctx, root, m.cfg.Workspace.CloneURL)
		if err != nil {
			return "", fmt.Errorf("worktree: bare clone: %w", err)
		}
		if err := FetchBare(ctx, barePath); err != nil {
			slog.Warn("worktree: bare fetch failed (best-effort)", "error", err)
		}
		return barePath, nil
	}
	// Legacy: fetch in the root repo directly.
	fetchCmd := exec.CommandContext(ctx, "git", "-C", root, "fetch", "origin", branchName)
	if err := fetchCmd.Run(); err != nil {
		slog.Debug("worktree: fetch failed (best-effort)", "branch", branchName, "error", err)
	}
	return root, nil
}

// ensureWorktree creates a git worktree at worktreePath(root, identifier) checked
// out on branchName. If the worktree directory already exists it is reused (retry
// case). If the branch already exists in the base repo but has no worktree, the
// branch is checked out without -b.
//
// When cfg.Workspace.CloneURL is set, a bare clone at <root>/.bare/ is used as
// the git directory for all operations, avoiding lock contention with the user's
// working copy.
func (m *Manager) ensureWorktree(ctx context.Context, identifier, branchName, requestedStart string) (Workspace, error) {
	root := m.cfg.Workspace.Root
	wtPath := worktreePath(root, identifier)

	// Reuse existing worktree (retry case).
	if info, err := os.Stat(wtPath); err == nil && info.IsDir() {
		if err := AssertContained(root, wtPath); err != nil {
			return Workspace{}, err
		}
		return Workspace{Path: wtPath, Identifier: identifier, CreatedNow: false}, nil
	}

	// Ensure the worktrees/ parent directory exists.
	worktreesDir := filepath.Join(root, "worktrees")
	if err := os.MkdirAll(worktreesDir, 0o755); err != nil {
		return Workspace{}, fmt.Errorf("worktree: create worktrees dir: %w", err)
	}

	gitDir, err := m.resolveGitDir(ctx, root, branchName)
	if err != nil {
		return Workspace{}, err
	}

	// For bare clones, specify the base branch as start point so worktrees
	// are created from the latest fetched state of that branch.
	startPoint := ""
	if m.cfg.Workspace.CloneURL != "" {
		startPoint = m.cfg.Workspace.BaseBranch
		if startPoint == "" {
			startPoint = "main"
		}
	}
	// A stacked start point wins when it resolves. Verified rather than
	// trusted: the blocker's branch may not exist here (not yet dispatched,
	// worked on another machine, worktree cleared), and a `git worktree add`
	// against a missing ref would fail the whole dispatch. Stacking is a
	// review-ergonomics improvement, so it degrades to the normal base rather
	// than blocking work.
	if requestedStart != "" && m.refExists(ctx, gitDir, requestedStart) {
		startPoint = requestedStart
		slog.Info("workspace: stacking worktree on blocker branch",
			"identifier", identifier, "branch", branchName, "base", startPoint)
	}

	// git -C <gitDir> worktree add <wtPath> -b <branchName> [startPoint]
	if err := runGitWorktreeAdd(ctx, gitDir, wtPath, branchName, startPoint, true); err != nil {
		// Branch already exists: retry without -b to check it out into a new worktree.
		if strings.Contains(err.Error(), "already exists") {
			if err2 := runGitWorktreeAdd(ctx, gitDir, wtPath, branchName, startPoint, false); err2 != nil {
				return Workspace{}, fmt.Errorf("worktree: add (existing branch): %w", err2)
			}
		} else {
			return Workspace{}, fmt.Errorf("worktree: add: %w", err)
		}
	}

	// Safety: assert the resulting path is still under root.
	if err := AssertContained(root, wtPath); err != nil {
		_ = exec.Command("git", "-C", gitDir, "worktree", "remove", "--force", wtPath).Run()
		return Workspace{}, err
	}

	return Workspace{Path: wtPath, Identifier: identifier, CreatedNow: true}, nil
}

// runGitWorktreeAdd runs git worktree add. If createBranch is true it passes
// -b <branchName>; otherwise checks out the existing branch directly.
// startPoint, when non-empty, specifies the commit/ref to start from (e.g.
// "origin/main") instead of HEAD — required for bare clones where HEAD may
// point to an invalid ref.
// LANG=C and LC_ALL=C are forced so that the English error text ("already exists")
// is reliably produced on non-English systems and can be matched by ensureWorktree.
func runGitWorktreeAdd(ctx context.Context, root, wtPath, branchName, startPoint string, createBranch bool) error {
	var args []string
	if createBranch {
		// git worktree add <path> -b <branch> [<startpoint>]
		args = []string{"-C", root, "worktree", "add", wtPath, "-b", branchName}
		if startPoint != "" {
			args = append(args, startPoint)
		}
	} else {
		// git worktree add <path> <branch>  (no startpoint allowed for existing branches)
		args = []string{"-C", root, "worktree", "add", wtPath, branchName}
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "LANG=C", "LC_ALL=C")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// removeWorktree removes the git worktree for identifier and, if branchName is
// non-empty, deletes the branch from the base repo.
// Safe to call when the worktree does not exist (idempotent).
func (m *Manager) removeWorktree(ctx context.Context, identifier, branchName string) error {
	root := m.cfg.Workspace.Root
	wtPath := worktreePath(root, identifier)

	// Resolve git dir: bare clone → root (if it's a git repo) → plain directory removal.
	gitDir := ""
	if bp := BarePath(root); dirExists(bp) {
		gitDir = bp
	} else if dirExists(filepath.Join(root, ".git")) {
		gitDir = root
	}

	// No git repo found — the workspace is a plain directory (legacy mode or stale).
	// Just remove the directory if it exists.
	if gitDir == "" {
		if dirExists(wtPath) {
			return os.RemoveAll(wtPath)
		}
		return nil
	}

	// git -C <gitDir> worktree remove --force <wtPath>
	cmd := exec.CommandContext(ctx, "git", "-C", gitDir, "worktree", "remove", "--force", wtPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		output := strings.TrimSpace(string(out))
		// These messages mean the worktree is already gone — treat as success.
		if !strings.Contains(output, "is not a working tree") &&
			!strings.Contains(output, "No such file or directory") {
			return fmt.Errorf("worktree: remove: %w: %s", err, output)
		}
	}

	// Prune stale metadata from .git/worktrees/
	_ = exec.CommandContext(ctx, "git", "-C", gitDir, "worktree", "prune").Run()

	// Delete branch (best-effort, only when caller provides a name).
	if branchName != "" {
		_ = exec.CommandContext(ctx, "git", "-C", gitDir, "branch", "-D", branchName).Run()
	}

	return nil
}

// refExists reports whether ref resolves in the repository at gitDir.
//
// `git rev-parse --verify <ref>^{commit}` is the cheap, side-effect-free way
// to ask: it exits non-zero for an unknown ref rather than creating anything.
func (m *Manager) refExists(ctx context.Context, gitDir, ref string) bool {
	if ref == "" {
		return false
	}
	cmd := exec.CommandContext(ctx, "git", "-C", gitDir, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	return cmd.Run() == nil
}
