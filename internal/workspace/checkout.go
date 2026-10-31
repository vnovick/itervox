package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/vnovick/itervox/internal/gitexec"
)

// CheckoutProvider is the workspace of `itervox run` (#252): an existing git
// checkout — in CI, the job's own — instead of directories or worktrees
// under workspace.root. The issue branch is created in the checkout itself,
// or, with Worktree set, in a git worktree under
// <checkout>/.itervox/run/worktrees/ (kept out of `git status` through the
// checkout's .git/info/exclude).
//
// A branch that already exists — locally, or only on origin, as when a run
// continues the branch a previous CI run pushed — is checked out as it is,
// never re-created from the base. Workspaces are never removed: the
// checkout belongs to the caller, and a worktree is left for inspection.
type CheckoutProvider struct {
	// Dir is the checkout's top-level directory.
	Dir string
	// Worktree puts the issue branch in a separate worktree instead of
	// switching the checkout's own branch.
	Worktree bool

	mu sync.Mutex
}

// RunDirRel is the checkout-relative directory of `itervox run`'s runtime
// files (the per-issue lock, worktrees); it is git-excluded locally.
const RunDirRel = ".itervox/run"

// EnsureWorkspace checks out branchName in the checkout (or its worktree)
// and returns its path. CreatedNow is true only for a worktree created now:
// the checkout itself was not created by itervox, so hooks.after_create
// does not run for it.
func (p *CheckoutProvider) EnsureWorkspace(ctx context.Context, identifier, branchName string) (Workspace, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if branchName == "" || IsDefaultBranch(branchName) {
		return Workspace{}, fmt.Errorf("workspace: refusing to work on branch %q", branchName)
	}
	fetchBranch(ctx, p.Dir, branchName)
	if p.Worktree {
		return p.ensureWorktree(ctx, identifier, branchName)
	}
	if GetCurrentBranch(ctx, p.Dir) == branchName {
		return Workspace{Path: p.Dir, Identifier: identifier}, nil
	}
	var args []string
	switch {
	case refResolves(ctx, p.Dir, "refs/heads/"+branchName):
		args = []string{"checkout", branchName}
	case refResolves(ctx, p.Dir, "refs/remotes/origin/"+branchName):
		args = []string{"checkout", "--no-track", "-b", branchName, "refs/remotes/origin/" + branchName}
	default:
		args = []string{"checkout", "--no-track", "-b", branchName}
	}
	if out, err := gitexec.Command(ctx, p.Dir, args...).CombinedOutput(); err != nil {
		return Workspace{}, fmt.Errorf("workspace: %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return Workspace{Path: p.Dir, Identifier: identifier}, nil
}

func (p *CheckoutProvider) ensureWorktree(ctx context.Context, identifier, branchName string) (Workspace, error) {
	path := p.ResolvePath(identifier)
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return Workspace{Path: path, Identifier: identifier}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Workspace{}, fmt.Errorf("workspace: create %s: %w", filepath.Dir(path), err)
	}
	var err error
	switch {
	case refResolves(ctx, p.Dir, "refs/heads/"+branchName):
		err = runGitWorktreeAdd(ctx, p.Dir, path, branchName, "", false)
	case refResolves(ctx, p.Dir, "refs/remotes/origin/"+branchName):
		err = runGitWorktreeAdd(ctx, p.Dir, path, branchName, "refs/remotes/origin/"+branchName, true)
	default:
		err = runGitWorktreeAdd(ctx, p.Dir, path, branchName, "", true)
	}
	if err != nil {
		return Workspace{}, fmt.Errorf("workspace: worktree add: %w", classifyWorktreeAddError(p.Dir, err))
	}
	return Workspace{Path: path, Identifier: identifier, CreatedNow: true}, nil
}

// RemoveWorkspace never removes anything: the checkout is the caller's and
// a run's worktree is kept for inspection.
func (p *CheckoutProvider) RemoveWorkspace(context.Context, string, string) error { return nil }

// ResolvePath is the checkout, or the issue's worktree under it.
func (p *CheckoutProvider) ResolvePath(identifier string) string {
	if !p.Worktree {
		return p.Dir
	}
	return filepath.Join(p.Dir, filepath.FromSlash(RunDirRel), "worktrees", SanitizeKey(identifier))
}

// fetchBranch refreshes origin's copy of branch, best effort: a branch a
// previous run pushed continues from there, and a branch origin does not
// have is simply new.
func fetchBranch(ctx context.Context, dir, branch string) {
	_ = gitexec.Command(ctx, dir, "fetch", "--quiet", "origin",
		"+refs/heads/"+branch+":refs/remotes/origin/"+branch).Run()
}

// CheckoutRoot is the top-level directory of the git checkout containing
// dir.
func CheckoutRoot(ctx context.Context, dir string) (string, error) {
	out, err := gitexec.Command(ctx, dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("workspace: %s is not inside a git checkout", dir)
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return "", errors.New("workspace: git reported no checkout root")
	}
	return root, nil
}

// ExcludeRunDir adds RunDirRel to the checkout's local exclude file
// (.git/info/exclude), so the run's lock and worktrees never show up in
// `git status` or get committed. Idempotent.
func ExcludeRunDir(ctx context.Context, dir string) error {
	out, err := gitexec.Command(ctx, dir, "rev-parse", "--git-path", "info/exclude").Output()
	if err != nil {
		return fmt.Errorf("workspace: locate info/exclude: %w", err)
	}
	path := strings.TrimSpace(string(out))
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	line := "/" + RunDirRel + "/"
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("workspace: read %s: %w", path, err)
	}
	for _, l := range strings.Split(string(existing), "\n") {
		if strings.TrimSpace(l) == line {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("workspace: create %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("workspace: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	prefix := ""
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		prefix = "\n"
	}
	_, err = f.WriteString(prefix + line + "\n")
	return err
}
