package workspace

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// RestackOutcome is the result of attempting to rebase a stacked worktree onto
// an updated base.
type RestackOutcome string

const (
	// RestackUpToDate means the branch already contained onto's tip; nothing
	// was rewritten.
	RestackUpToDate RestackOutcome = "up_to_date"
	// RestackRebased means the branch was successfully replayed onto the new
	// base.
	RestackRebased RestackOutcome = "rebased"
	// RestackConflict means the rebase hit a conflict and was ABORTED — the
	// branch is byte-identical to before the attempt. A conflict needs an
	// owner; guessing a resolution is worse than asking.
	RestackConflict RestackOutcome = "conflict"
	// RestackSkippedDirty means the worktree had uncommitted changes and was
	// left untouched.
	RestackSkippedDirty RestackOutcome = "skipped_dirty"
)

// RestackWorktree rebases identifier's worktree branch onto `onto`.
//
// This is the mechanical half of issue #60: when a blocker merges, every branch
// stacked on it is based on a commit that is no longer the tip of the base
// branch, and drifts further with every subsequent merge.
//
// Three refusals are deliberate, and each protects work that cannot be
// recovered if the operation gets it wrong:
//
//  1. **Dirty worktree -> skip.** Uncommitted changes are unpushed, unbacked-up
//     work. `git rebase` would refuse anyway, but failing explicitly here
//     means the caller reports "skipped, dirty" rather than surfacing a raw
//     git error an operator has to decode.
//
//  2. **Conflict -> abort, never resolve.** On any rebase failure the rebase is
//     aborted so the branch returns to exactly its pre-attempt state. The
//     caller escalates to a human. A conflict is a semantic disagreement
//     between two changes; an automated resolution is a guess with a plausible
//     shape, which is the worst kind.
//
//  3. **Caller decides liveness.** This function does NOT check whether an
//     agent is running against the worktree, because it cannot see the
//     orchestrator's state. Rebasing a worktree an agent has checked out and is
//     mid-turn on would move HEAD under a running process. The orchestrator
//     must only call this for idle issues — see restackDependentsForMergedBase.
func (m *Manager) RestackWorktree(ctx context.Context, identifier, branchName, onto string) (RestackOutcome, error) {
	onto = strings.TrimSpace(onto)
	if onto == "" {
		return "", fmt.Errorf("workspace: restack requires a base ref")
	}
	wtPath := m.ResolvePath(identifier)
	if wtPath == "" {
		return "", fmt.Errorf("workspace: no worktree path for %q", identifier)
	}

	dirty, err := worktreeIsDirty(ctx, wtPath)
	if err != nil {
		return "", err
	}
	if dirty {
		return RestackSkippedDirty, nil
	}

	// Already contains onto's tip: nothing to replay. Checking first keeps the
	// common no-op case from rewriting commit hashes, which would invalidate
	// an open PR's review state for no reason.
	contains, err := branchContains(ctx, wtPath, onto)
	if err != nil {
		return "", err
	}
	if contains {
		return RestackUpToDate, nil
	}

	rebase := exec.CommandContext(ctx, "git", "rebase", onto)
	rebase.Dir = wtPath
	if out, rerr := rebase.CombinedOutput(); rerr != nil {
		abort := exec.CommandContext(ctx, "git", "rebase", "--abort")
		abort.Dir = wtPath
		// An abort failure is worth surfacing: it means the worktree may be
		// left mid-rebase, which every later operation on it would trip over.
		if aout, aerr := abort.CombinedOutput(); aerr != nil {
			return RestackConflict, fmt.Errorf(
				"workspace: rebase of %s onto %s failed and could not be aborted: %v: %s",
				branchName, onto, aerr, strings.TrimSpace(string(aout)))
		}
		_ = out
		return RestackConflict, nil
	}
	return RestackRebased, nil
}

// worktreeIsDirty reports whether the worktree has uncommitted changes,
// including untracked files.
//
// Untracked files count: a rebase that leaves them in place can silently
// entangle them with a checkout, and they are exactly the "I was midway
// through something" signal that should stop an automated rewrite.
func worktreeIsDirty(ctx context.Context, wtPath string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	cmd.Dir = wtPath
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("workspace: git status in %s: %w", wtPath, err)
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// branchContains reports whether HEAD already contains ref's tip.
func branchContains(ctx context.Context, wtPath, ref string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "merge-base", "--is-ancestor", ref, "HEAD")
	cmd.Dir = wtPath
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if ok := asExitError(err, &exitErr); ok && exitErr.ExitCode() == 1 {
		return false, nil // valid answer: not an ancestor
	}
	return false, fmt.Errorf("workspace: merge-base --is-ancestor %s in %s: %w", ref, wtPath, err)
}

func asExitError(err error, target **exec.ExitError) bool {
	if e, ok := err.(*exec.ExitError); ok {
		*target = e
		return true
	}
	return false
}
