package workspace

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/vnovick/itervox/internal/gitexec"
)

// ErrBranchInUse is matched (errors.Is) by every BranchInUseError.
var ErrBranchInUse = errors.New("workspace: branch is checked out in another worktree")

// BranchInUseError reports that a worktree could not be created on Branch
// because git already has it checked out at Path (#193). Git refuses to check
// a branch out twice, so retrying cannot help until that worktree lets go of
// the branch: the orchestrator holds the issue with the branch_in_use reason
// instead of scheduling a retry.
type BranchInUseError struct {
	Branch string
	// Path is the worktree that holds the branch, as git reports it.
	Path string
	// Holder is the identifier of the itervox worktree at Path
	// (<root>/worktrees/<identifier>), or "" when Path is not one of them
	// (the repository's own checkout, or a worktree someone added by hand).
	Holder string
}

func (e *BranchInUseError) Error() string {
	who := e.Path
	if e.Holder != "" {
		who = fmt.Sprintf("%s (issue %s)", e.Path, e.Holder)
	}
	return fmt.Sprintf("worktree: branch %q is already checked out in another worktree at %s", e.Branch, who)
}

// Is makes errors.Is(err, ErrBranchInUse) true for a *BranchInUseError.
func (e *BranchInUseError) Is(target error) bool { return target == ErrBranchInUse }

// branchInUseRe matches git's refusal to check a branch out a second time:
// "is already used by worktree at" (git 2.42+) and "is already checked out
// at" (older git). LANG=C is forced on the worktree add, so the text is
// English.
var branchInUseRe = regexp.MustCompile(`'([^']*)' is already (?:used by worktree|checked out) at '([^']*)'`)

// classifyWorktreeAddError turns git's "branch already checked out" refusal
// into a *BranchInUseError; any other error is returned unchanged. It covers
// the window between the pre-check and `git worktree add`, and a git whose
// worktree list could not be read.
func classifyWorktreeAddError(root string, err error) error {
	if err == nil {
		return nil
	}
	m := branchInUseRe.FindStringSubmatch(err.Error())
	if m == nil {
		return err
	}
	return &BranchInUseError{Branch: m[1], Path: m[2], Holder: holderIdentifier(root, m[2])}
}

// worktreeEntry is one record of `git worktree list --porcelain`.
type worktreeEntry struct {
	Path     string
	Branch   string // short name; "" for a detached HEAD or the bare entry
	Bare     bool
	Prunable bool
}

// listWorktrees runs `git worktree list --porcelain` in gitDir.
func listWorktrees(ctx context.Context, gitDir string) ([]worktreeEntry, error) {
	cmd := gitexec.Command(ctx, gitDir, "-C", gitDir, "worktree", "list", "--porcelain")
	cmd.Env = append(cmd.Env, "LANG=C", "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("worktree: list: %w", err)
	}
	return parseWorktreeList(out), nil
}

func parseWorktreeList(out []byte) []worktreeEntry {
	var entries []worktreeEntry
	var cur *worktreeEntry
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		key, val, _ := strings.Cut(line, " ")
		switch key {
		case "worktree":
			entries = append(entries, worktreeEntry{Path: val})
			cur = &entries[len(entries)-1]
		case "branch":
			if cur != nil {
				cur.Branch = strings.TrimPrefix(val, "refs/heads/")
			}
		case "bare":
			if cur != nil {
				cur.Bare = true
			}
		case "prunable":
			if cur != nil {
				cur.Prunable = true
			}
		}
	}
	return entries
}

// checkBranchFree makes sure `git worktree add` can check branch out at
// wtPath. A registration of the branch at wtPath itself, or at a worktree
// whose directory is gone, is stale metadata: it is pruned so the issue's own
// worktree is re-created rather than held against itself. A live worktree
// elsewhere holding the branch is reported as *BranchInUseError. An
// unreadable worktree list is not an error here: `git worktree add` decides,
// and classifyWorktreeAddError types its refusal.
func checkBranchFree(ctx context.Context, gitDir, root, wtPath, branch string) error {
	if branch == "" {
		return nil
	}
	entries, err := listWorktrees(ctx, gitDir)
	if err != nil {
		return nil //nolint:nilerr // best-effort pre-check; see above
	}
	pruned := false
	for _, e := range entries {
		if e.Bare || e.Branch != branch {
			continue
		}
		if e.Prunable || samePath(e.Path, wtPath) || !dirExists(e.Path) {
			if !pruned {
				_ = gitexec.Command(ctx, gitDir, "-C", gitDir, "worktree", "prune").Run()
				pruned = true
			}
			continue
		}
		return &BranchInUseError{Branch: branch, Path: e.Path, Holder: holderIdentifier(root, e.Path)}
	}
	return nil
}

// holderIdentifier returns the issue identifier of the itervox worktree at
// path (<root>/worktrees/<identifier>), or "".
func holderIdentifier(root, path string) string {
	if root == "" || path == "" {
		return ""
	}
	if samePath(filepath.Dir(path), filepath.Join(root, "worktrees")) {
		return filepath.Base(path)
	}
	return ""
}

// samePath compares two paths after resolving symlinks, so a root under a
// symlinked directory (macOS's /var → /private/var) matches the real path
// git reports.
func samePath(a, b string) bool {
	return canonicalPath(a) == canonicalPath(b)
}

func canonicalPath(p string) string {
	p = filepath.Clean(p)
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	// A path that no longer exists: resolve its parent so the comparison
	// still sees through a symlinked root.
	if real, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
		return filepath.Join(real, filepath.Base(p))
	}
	return p
}

// BranchCheckedOutAt reports whether the worktree at path still has branch
// checked out. It reads the worktree's HEAD from disk instead of running git,
// so the orchestrator's event loop can re-check a branch_in_use hold every
// tick cheaply. Any read error (the worktree was removed) reports false: the
// branch is free as far as that worktree is concerned.
func BranchCheckedOutAt(path, branch string) bool {
	if path == "" || branch == "" {
		return false
	}
	dotGit := filepath.Join(path, ".git")
	info, err := os.Stat(dotGit)
	if err != nil {
		return false
	}
	gitDir := dotGit
	if !info.IsDir() {
		// A linked worktree: .git is a file "gitdir: <repo>/worktrees/<name>".
		raw, err := os.ReadFile(dotGit)
		if err != nil {
			return false
		}
		ref, ok := strings.CutPrefix(strings.TrimSpace(string(raw)), "gitdir:")
		if !ok {
			return false
		}
		gitDir = strings.TrimSpace(ref)
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(path, gitDir)
		}
	}
	head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(head)) == "ref: refs/heads/"+branch
}
