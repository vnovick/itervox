package workspace

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/vnovick/itervox/internal/gitexec"
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
//
// fetchFailure is non-empty when the bare fetch failed and that failure has
// not been reported since the last success (#195): the caller records it once
// in recent failures. The fetch stays best-effort: a stale base is better
// than no worktree.
func (m *Manager) resolveGitDir(ctx context.Context, root, branchName string) (gitDir, fetchFailure string, err error) {
	if m.cfg.Workspace.CloneURL != "" {
		barePath, err := EnsureBareClone(ctx, root, m.cfg.Workspace.CloneURL)
		if err != nil {
			return "", "", fmt.Errorf("worktree: bare clone: %w", err)
		}
		fetchErr := FetchBare(ctx, barePath)
		if fetchErr != nil {
			slog.Warn("worktree: bare fetch failed (best-effort)", "error", fetchErr)
		} else {
			m.syncLocalBase(ctx, barePath)
		}
		return barePath, bareFetchFailureToReport(barePath, fetchErr), nil
	}
	// Legacy: fetch in the root repo directly. Serialized like FetchBare:
	// every worktree shares this repository's refs (#193). root is the
	// operator's own clone with its own refspec (normally the standard
	// refs/remotes/origin/* one), so #195 does not apply here.
	unlock := lockRepo(root)
	fetchCmd := gitexec.Command(ctx, root, "-C", root, "fetch", "origin", branchName)
	ferr := fetchCmd.Run()
	unlock()
	if ferr != nil {
		slog.Debug("worktree: fetch failed (best-effort)", "branch", branchName, "error", ferr)
	}
	return root, "", nil
}

// baseBranch is workspace.base_branch, "main" when unset.
func (m *Manager) baseBranch() string {
	if b := m.cfg.Workspace.BaseBranch; b != "" {
		return b
	}
	return "main"
}

// syncLocalBase fast-forwards the bare clone's local base branch (and the
// branch its HEAD names) to origin's after a fetch (#195). Fetches no longer
// write local branches, but HEAD, `stackedOn`'s ancestry check and restack
// all name the base branch locally, so it is kept current the safe way:
// fast-forward only, never under a worktree.
func (m *Manager) syncLocalBase(ctx context.Context, barePath string) {
	branches := []string{m.baseBranch()}
	if out, err := gitexec.Command(ctx, barePath, "-C", barePath, "symbolic-ref", "--quiet", "--short", "HEAD").Output(); err == nil {
		if head := strings.TrimSpace(string(out)); head != "" && head != branches[0] {
			branches = append(branches, head)
		}
	}
	for _, b := range branches {
		fastForwardFromOrigin(ctx, barePath, b)
	}
}

// ensureWorktree creates a git worktree at worktreePath(root, identifier) checked
// out on branchName. If the worktree directory already exists it is reused (retry
// case). If the branch already exists in the base repo but has no worktree, the
// branch is checked out without -b.
//
// When cfg.Workspace.CloneURL is set, a bare clone at <root>/.bare/ is used as
// the git directory for all operations, avoiding lock contention with the user's
// working copy.
func (m *Manager) ensureWorktree(ctx context.Context, identifier, branchName, requestedStart string) (_ Workspace, retErr error) {
	root := m.cfg.Workspace.Root
	wtPath := worktreePath(root, identifier)

	// Reuse existing worktree (retry case).
	if info, err := os.Stat(wtPath); err == nil && info.IsDir() {
		if err := AssertContained(root, wtPath); err != nil {
			return Workspace{}, err
		}
		return Workspace{Path: wtPath, Identifier: identifier, CreatedNow: false,
			StackedOn: m.stackedOn(ctx, wtPath, requestedStart)}, nil
	}

	// Ensure the worktrees/ parent directory exists.
	worktreesDir := filepath.Join(root, "worktrees")
	if err := os.MkdirAll(worktreesDir, 0o755); err != nil {
		return Workspace{}, fmt.Errorf("worktree: create worktrees dir: %w", err)
	}

	gitDir, fetchFailure, err := m.resolveGitDir(ctx, root, branchName)
	if err != nil {
		return Workspace{}, err
	}
	if fetchFailure != "" {
		// The failure travels on the returned Workspace; a dispatch that
		// fails later never delivers it, so let the next attempt report it.
		defer func() {
			if retErr != nil {
				forgetBareFetchFailureReport(gitDir, fetchFailure)
			}
		}()
	}
	bareMode := m.cfg.Workspace.CloneURL != ""

	// For bare clones, start from origin's base branch (#195): fetches update
	// refs/remotes/origin/*, so that is the latest state. A clone whose fetch
	// never populated it falls back to the local base branch.
	startPoint := ""
	if bareMode {
		startPoint = m.baseBranch()
		if refResolves(ctx, gitDir, "refs/remotes/origin/"+startPoint) {
			startPoint = "refs/remotes/origin/" + startPoint
		}
	}
	// A stacked start point wins when it resolves. Verified rather than
	// trusted: the blocker's branch may not exist here (not yet dispatched,
	// worked on another machine, worktree cleared), and a `git worktree add`
	// against a missing ref would fail the whole dispatch. Stacking is a
	// review-ergonomics improvement, so it degrades to the normal base rather
	// than blocking work.
	// In a bare clone the blocker's branch is refreshed from origin first,
	// as the legacy refspec's fetch used to: created when only origin has it
	// (its worktree was cleared), fast-forwarded when behind and not checked
	// out anywhere.
	if requestedStart != "" && bareMode && !strings.HasPrefix(requestedStart, "refs/") {
		fastForwardFromOrigin(ctx, gitDir, requestedStart)
	}
	if requestedStart != "" && m.refExists(ctx, gitDir, requestedStart) {
		startPoint = requestedStart
		slog.Info("workspace: stacking worktree on blocker branch",
			"identifier", identifier, "branch", branchName, "base", startPoint)
	}

	// #193: git checks a branch out in one worktree only. When another
	// worktree holds it, `worktree add` fails the same way on every retry,
	// so report it as a typed BranchInUseError (the orchestrator holds the
	// issue with branch_in_use) instead of a generic failure. Stale
	// registrations of this issue's own path are pruned first.
	if err := checkBranchFree(ctx, gitDir, root, wtPath, branchName); err != nil {
		return Workspace{}, err
	}

	// A branch origin already has (an open PR's branch, or this issue's
	// branch pushed before its worktree was cleared) continues from origin
	// (#195). The fetch no longer writes local branches, so: a missing local
	// branch is created from origin/<branch>, and an existing one is
	// fast-forwarded to it — never rewound, and never while a worktree has
	// it checked out (checkBranchFree above, re-checked under the lock).
	onOrigin := bareMode && refResolves(ctx, gitDir, "refs/remotes/origin/"+branchName)
	if onOrigin {
		if refResolves(ctx, gitDir, "refs/heads/"+branchName) {
			if fastForwardFromOrigin(ctx, gitDir, branchName) {
				slog.Info("workspace: fast-forwarded branch to origin before checkout",
					"identifier", identifier, "branch", branchName)
			}
		} else {
			startPoint = "refs/remotes/origin/" + branchName
		}
	}

	// git -C <gitDir> worktree add --no-track <wtPath> -b <branchName> [startPoint]
	created := true
	if err := runGitWorktreeAdd(ctx, gitDir, wtPath, branchName, startPoint, true); err != nil {
		// Branch already exists: retry without -b to check it out into a new worktree.
		if strings.Contains(err.Error(), "already exists") {
			created = false
			if err2 := runGitWorktreeAdd(ctx, gitDir, wtPath, branchName, startPoint, false); err2 != nil {
				return Workspace{}, fmt.Errorf("worktree: add (existing branch): %w", classifyWorktreeAddError(root, err2))
			}
		} else {
			return Workspace{}, fmt.Errorf("worktree: add: %w", classifyWorktreeAddError(root, err))
		}
	}
	// A new branch's only upstream is its own name on origin, set when origin
	// has it (#195). `--no-track` above stops git recording the start point —
	// base, or a blocker's branch — as upstream, which made `gh pr view` and
	// `git push` act on another issue's branch (#193). Without one, the
	// agent's `git push -u origin HEAD` sets it on first push.
	if created && onOrigin {
		if err := setOriginUpstream(ctx, gitDir, branchName); err != nil {
			slog.Warn("workspace: set branch upstream failed", "branch", branchName, "error", err)
		}
	}

	// Safety: assert the resulting path is still under root.
	if err := AssertContained(root, wtPath); err != nil {
		_ = gitexec.Command(context.Background(), gitDir, "-C", gitDir, "worktree", "remove", "--force", wtPath).Run()
		return Workspace{}, err
	}

	// Decided from the resulting history, not from the requested start point:
	// when the branch already existed, `worktree add` checked it out as it is
	// and the start point was never used.
	return Workspace{Path: wtPath, Identifier: identifier, CreatedNow: true,
		StackedOn: m.stackedOn(ctx, wtPath, requestedStart), BareFetchFailure: fetchFailure}, nil
}

// stackedOn reports blocker as the branch the worktree at wtPath is stacked
// on (#73) when the worktree's history contains it AND it carries commits
// that workspace.base_branch does not. A blocker branch with nothing of its
// own (or one that is behind base) is not a meaningful base for a pull
// request, so it reports "". `merge-base --is-ancestor` fails for a missing
// ref, so a missing blocker branch is never reported; a missing base branch
// skips the second check.
func (m *Manager) stackedOn(ctx context.Context, wtPath, blocker string) string {
	if blocker == "" {
		return ""
	}
	if gitexec.Command(ctx, wtPath, "merge-base", "--is-ancestor", blocker, "HEAD").Run() != nil {
		return ""
	}
	base := m.cfg.Workspace.BaseBranch
	if base == "" {
		base = "main"
	}
	if gitexec.Command(ctx, wtPath, "merge-base", "--is-ancestor", blocker, base).Run() == nil {
		return "" // everything on the blocker branch is already in base
	}
	return blocker
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
		// --no-track: the start point is never the new branch's upstream
		// (#195); ensureWorktree sets origin/<branchName> when it exists.
		args = []string{"-C", root, "worktree", "add", "--no-track", wtPath, "-b", branchName}
		if startPoint != "" {
			args = append(args, startPoint)
		}
	} else {
		// git worktree add <path> <branch>  (no startpoint allowed for existing branches)
		args = []string{"-C", root, "worktree", "add", wtPath, branchName}
	}
	cmd := gitexec.Command(ctx, root, args...)
	cmd.Env = append(cmd.Env, "LANG=C", "LC_ALL=C")
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
	cmd := gitexec.Command(ctx, gitDir, "-C", gitDir, "worktree", "remove", "--force", wtPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		output := strings.TrimSpace(string(out))
		// These messages mean the worktree is already gone — treat as success.
		if !strings.Contains(output, "is not a working tree") &&
			!strings.Contains(output, "No such file or directory") {
			return fmt.Errorf("worktree: remove: %w: %s", err, output)
		}
	}

	// Prune stale metadata from .git/worktrees/
	_ = gitexec.Command(ctx, gitDir, "-C", gitDir, "worktree", "prune").Run()

	// Delete branch (best-effort, only when caller provides a name).
	if branchName != "" {
		_ = gitexec.Command(ctx, gitDir, "-C", gitDir, "branch", "-D", branchName).Run()
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
	cmd := gitexec.Command(ctx, gitDir, "-C", gitDir, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	return cmd.Run() == nil
}
