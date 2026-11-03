package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/atomicfs"
	"github.com/vnovick/itervox/internal/gitexec"
)

// #195 — the shared bare clone's fetch layout.
//
// The bare clone used to fetch with `+refs/heads/*:refs/heads/*`, mapping
// origin's branches straight onto its local branches. git refuses to update a
// branch some worktree has checked out, so once any worktree held a branch
// that also exists on origin, every fetch exited 128 and every base branch
// silently stopped moving. The same mapping made local branches look like
// remote-tracking ones, so `git worktree add -b <new> <path> <start>` set
// <start> as the new branch's upstream (#193's root cause).
//
// Origin's branches now land in refs/remotes/origin/*. Local branches are
// never written by a fetch: an agent's unpushed commits are safe, and a
// checked-out branch can no longer abort it.

// BareFetchRefspec is the fetch refspec of the shared bare clone.
const BareFetchRefspec = "+refs/heads/*:refs/remotes/origin/*"

// LegacyBareFetchRefspec is the refspec bare clones made before #195 carry.
const LegacyBareFetchRefspec = "+refs/heads/*:refs/heads/*"

// legacyRefspecPattern matches LegacyBareFetchRefspec as a `git config`
// value-pattern (a regular expression).
const legacyRefspecPattern = `^\+refs/heads/\*:refs/heads/\*$`

// repairPendingKey marks a bare clone whose branch upstreams still need the
// one-time repair: set when the legacy refspec is rewritten, cleared after
// the first successful fetch has run the repair (the repair needs
// refs/remotes/origin/* populated to tell which branches origin has).
const repairPendingKey = "itervox.upstreamRepairPending"

// BareFetchStatusFile is written inside the bare clone after every fetch so
// `itervox doctor` (a separate process) can report a failing or stale fetch.
// git ignores unknown files in its directory.
const BareFetchStatusFile = "itervox-fetch-status.json"

// BareFetchStatus is the content of BareFetchStatusFile.
type BareFetchStatus struct {
	LastSuccessAt time.Time `json:"last_success_at,omitzero"`
	LastErrorAt   time.Time `json:"last_error_at,omitzero"`
	LastError     string    `json:"last_error,omitempty"`
}

// Failing reports whether the most recent fetch failed.
func (s BareFetchStatus) Failing() bool {
	return !s.LastErrorAt.IsZero() && s.LastErrorAt.After(s.LastSuccessAt)
}

// ReadBareFetchStatus reads the fetch status recorded in the bare clone at
// barePath. A missing file is reported as an error (os.ErrNotExist).
func ReadBareFetchStatus(barePath string) (BareFetchStatus, error) {
	var s BareFetchStatus
	data, err := os.ReadFile(filepath.Join(barePath, BareFetchStatusFile))
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("workspace: parse %s: %w", BareFetchStatusFile, err)
	}
	return s, nil
}

// writeBareFetchStatus records the outcome of one fetch. Best-effort: the
// status file is a diagnostic, never a reason to fail a dispatch.
func writeBareFetchStatus(barePath string, fetchErr error, now time.Time) {
	s, _ := ReadBareFetchStatus(barePath)
	if fetchErr == nil {
		s.LastSuccessAt = now
	} else {
		s.LastErrorAt = now
		s.LastError = summarizeGitError(fetchErr.Error())
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return
	}
	if err := atomicfs.WriteFile(filepath.Join(barePath, BareFetchStatusFile), append(data, '\n'), 0o644); err != nil {
		slog.Debug("bare: write fetch status failed", "path", barePath, "error", err)
	}
}

// summarizeGitError keeps the line of git's output that says what went wrong
// (the last "fatal:"/"error:" line, else the last line), so a status entry
// or failure record stays one readable line.
func summarizeGitError(msg string) string {
	lines := strings.Split(strings.TrimSpace(msg), "\n")
	pick := ""
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if strings.Contains(l, "fatal:") || strings.Contains(l, "error:") {
			pick = l
		}
	}
	if pick == "" && len(lines) > 0 {
		pick = strings.TrimSpace(lines[len(lines)-1])
	}
	return pick
}

// fetchFailuresReported remembers, per bare clone, the failure last handed
// to the caller for recording, so a fetch that keeps failing the same way is
// recorded once rather than on every dispatch. A success forgets it.
var fetchFailuresReported sync.Map // canonical bare path → string

// bareFetchFailureToReport returns the message to record for a failed fetch,
// or "" when the fetch succeeded or the same failure was already reported
// since the last success.
func bareFetchFailureToReport(barePath string, fetchErr error) string {
	key := canonicalPath(barePath)
	if fetchErr == nil {
		fetchFailuresReported.Delete(key)
		return ""
	}
	msg := "shared bare clone fetch failed, so new worktrees branch from a stale base: " + summarizeGitError(fetchErr.Error())
	if prev, ok := fetchFailuresReported.Load(key); ok && prev.(string) == msg {
		return ""
	}
	fetchFailuresReported.Store(key, msg)
	return msg
}

// forgetBareFetchFailureReport undoes bareFetchFailureToReport for a report
// that never reached the caller, so the next attempt reports it again.
func forgetBareFetchFailureReport(barePath, msg string) {
	fetchFailuresReported.CompareAndDelete(canonicalPath(barePath), msg)
}

// gitConfigValues returns every value of key in the repository at gitDir.
func gitConfigValues(ctx context.Context, gitDir, key string) []string {
	out, err := gitexec.Command(ctx, gitDir, "-C", gitDir, "config", "--get-all", key).Output()
	if err != nil {
		return nil
	}
	var vals []string
	for _, v := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if v = strings.TrimSpace(v); v != "" {
			vals = append(vals, v)
		}
	}
	return vals
}

// runGit runs git in gitDir and returns its combined output on failure.
func runGit(ctx context.Context, gitDir string, args ...string) error {
	cmd := gitexec.Command(ctx, gitDir, append([]string{"-C", gitDir}, args...)...)
	cmd.Env = append(cmd.Env, "LANG=C", "LC_ALL=C")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// BareRefspecNeedsMigration reports whether the bare clone at barePath still
// fetches with the legacy refspec (or has none at all).
func BareRefspecNeedsMigration(ctx context.Context, barePath string) bool {
	vals := gitConfigValues(ctx, barePath, "remote.origin.fetch")
	if len(vals) == 0 {
		return true
	}
	for _, v := range vals {
		if v == LegacyBareFetchRefspec {
			return true
		}
	}
	return false
}

// migrateBareRefspec moves a bare clone onto BareFetchRefspec: the legacy
// value is replaced (any refspec an operator added is left alone), and the
// upstream repair is marked pending for the first successful fetch.
// Idempotent; it never touches a ref. Caller holds the repository lock.
func migrateBareRefspec(ctx context.Context, barePath string) error {
	vals := gitConfigValues(ctx, barePath, "remote.origin.fetch")
	hasLegacy := false
	for _, v := range vals {
		hasLegacy = hasLegacy || v == LegacyBareFetchRefspec
	}
	switch {
	case hasLegacy:
		if err := runGit(ctx, barePath, "config", "--replace-all", "remote.origin.fetch", BareFetchRefspec, legacyRefspecPattern); err != nil {
			return fmt.Errorf("bare: migrate fetch refspec: %w", err)
		}
		if err := runGit(ctx, barePath, "config", repairPendingKey, "true"); err != nil {
			return fmt.Errorf("bare: mark upstream repair: %w", err)
		}
		slog.Info("bare: migrated fetch refspec to remote-tracking refs (#195)",
			"path", barePath, "refspec", BareFetchRefspec)
	case len(vals) == 0:
		if err := runGit(ctx, barePath, "config", "remote.origin.fetch", BareFetchRefspec); err != nil {
			return fmt.Errorf("bare: set fetch refspec: %w", err)
		}
	}
	return nil
}

// repairUpstreamsIfPending runs the one-time upstream repair after a
// successful fetch of a migrated clone. Caller holds the repository lock.
func repairUpstreamsIfPending(ctx context.Context, barePath string) {
	if vals := gitConfigValues(ctx, barePath, repairPendingKey); len(vals) == 0 {
		return
	}
	if err := repairCrossBranchUpstreams(ctx, barePath); err != nil {
		slog.Warn("bare: upstream repair incomplete; will retry after the next fetch", "path", barePath, "error", err)
		return
	}
	_ = runGit(ctx, barePath, "config", "--unset-all", repairPendingKey)
}

// repairCrossBranchUpstreams fixes branches whose upstream is ANOTHER branch
// (#193's layout: with the legacy refspec git took a local start point for a
// remote-tracking branch and recorded it as branch.<new>.merge). Each such
// branch is pointed at its own name on origin when origin has it, and has
// its upstream unset otherwise. Only branch.<b>.remote/merge config is
// changed: no ref, branch or commit is touched. Upstreams on any remote other
// than origin (or ".") are an operator's own and are left alone.
func repairCrossBranchUpstreams(ctx context.Context, gitDir string) error {
	out, err := gitexec.Command(ctx, gitDir, "-C", gitDir, "config", "--get-regexp", `^branch\..*\.merge$`).Output()
	if err != nil {
		return nil //nolint:nilerr // exit 1: no branch has an upstream
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		key, merge, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(key, "branch."), ".merge")
		remote := ""
		if vals := gitConfigValues(ctx, gitDir, "branch."+name+".remote"); len(vals) > 0 {
			remote = vals[0]
		}
		own := "refs/heads/" + name
		if (remote != "origin" && remote != ".") || (remote == "origin" && merge == own) {
			continue
		}
		if refResolves(ctx, gitDir, "refs/remotes/origin/"+name) {
			if err := setOriginUpstream(ctx, gitDir, name); err != nil {
				return err
			}
			slog.Info("bare: repaired branch upstream to its own branch on origin", "branch", name, "was", merge)
			continue
		}
		_ = runGit(ctx, gitDir, "config", "--unset-all", "branch."+name+".merge")
		_ = runGit(ctx, gitDir, "config", "--unset-all", "branch."+name+".remote")
		slog.Info("bare: removed branch upstream that pointed at another branch", "branch", name, "was", merge)
	}
	return nil
}

// setOriginUpstream makes origin/<branch> the upstream of branch.
func setOriginUpstream(ctx context.Context, gitDir, branch string) error {
	if err := runGit(ctx, gitDir, "config", "branch."+branch+".remote", "origin"); err != nil {
		return err
	}
	return runGit(ctx, gitDir, "config", "branch."+branch+".merge", "refs/heads/"+branch)
}

// refResolves reports whether ref names a commit in gitDir.
func refResolves(ctx context.Context, gitDir, ref string) bool {
	return revParse(ctx, gitDir, ref) != ""
}

// revParse returns the commit ref names in gitDir, or "".
func revParse(ctx context.Context, gitDir, ref string) string {
	out, err := gitexec.Command(ctx, gitDir, "-C", gitDir, "rev-parse", "--verify", "--quiet", ref+"^{commit}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// fastForwardFromOrigin moves the local branch to origin/<branch> when that
// is a fast-forward and no worktree has the branch checked out; a missing
// local branch is created there. It never rewinds or rewrites a branch: a
// local branch with commits origin lacks (an agent's unpushed work) is left
// as it is. The update is compare-and-swap, so a concurrent writer wins.
// Returns whether the branch moved.
func fastForwardFromOrigin(ctx context.Context, gitDir, branch string) bool {
	unlock := lockRepo(gitDir)
	defer unlock()
	remote := revParse(ctx, gitDir, "refs/remotes/origin/"+branch)
	if remote == "" {
		return false
	}
	local := revParse(ctx, gitDir, "refs/heads/"+branch)
	if local == remote {
		return false
	}
	if local != "" {
		if gitexec.Command(ctx, gitDir, "-C", gitDir, "merge-base", "--is-ancestor", local, remote).Run() != nil {
			return false // local has commits origin lacks, or diverged
		}
		entries, err := listWorktrees(ctx, gitDir)
		if err != nil {
			return false
		}
		for _, e := range entries {
			if !e.Bare && e.Branch == branch {
				return false // never move a branch under a worktree
			}
		}
	}
	// An empty old value makes update-ref create-only.
	if err := runGit(ctx, gitDir, "update-ref", "refs/heads/"+branch, remote, local); err != nil {
		slog.Debug("bare: fast-forward skipped", "branch", branch, "error", err)
		return false
	}
	return true
}
