package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/atomicfs"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/gitexec"
	"github.com/vnovick/itervox/internal/tracker"
)

// Read-only reviewers (#79). A reviewer judges the change and records a
// verdict; it never commits, pushes or moves the issue. This file holds the
// pieces around that contract:
//
//   - the reviewer's context: the diff against the base branch plus the
//     latest handoff, instead of the whole handoff history;
//   - detection of a reviewer that changed the branch anyway (committed,
//     which a push requires, or edited tracked files), which is flagged on
//     the issue and turns its verdict into a block;
//   - posting each verdict (reasons and line comments) and the closed
//     quorum on the issue, so the worker and the operator see them.

// reviewDiffBudgetBytes bounds the diff inlined into a reviewer's prompt.
const reviewDiffBudgetBytes = 40 * 1024

// isReviewerProfile reports whether name is a configured reviewer: the
// singular reviewer_profile or any entry of reviewer_profiles. Callers hold
// cfgMu (ReviewerProfile and the chain are runtime-mutable).
func isReviewerProfile(cfg *config.Config, name string) bool {
	if cfg == nil || name == "" {
		return false
	}
	return name == cfg.Agent.ReviewerProfile || slices.Contains(ReviewerProfileChain(cfg), name)
}

// reviewBaseCandidates lists the refs to diff a reviewer's branch against,
// best first.
func reviewBaseCandidates(prBase, agentBase string) []string {
	var out []string
	if prBase != "" {
		out = append(out, "origin/"+prBase, prBase)
	}
	if agentBase != "" {
		out = append(out, agentBase)
	}
	return append(out, "origin/HEAD", "origin/main", "main")
}

// buildReviewDiffBlock renders "## Changes to Review": the stat and the diff
// of HEAD against its merge base with the first resolvable base ref, the
// diff truncated to reviewDiffBudgetBytes. Empty when wsPath is not a git
// work tree.
func buildReviewDiffBlock(ctx context.Context, wsPath string, bases []string) string {
	if wsPath == "" {
		return ""
	}
	git := func(args ...string) (string, error) {
		out, err := gitexec.Command(ctx, wsPath, args...).Output()
		return string(out), err
	}
	if _, err := git("rev-parse", "--is-inside-work-tree"); err != nil {
		return ""
	}
	base, mergeBase := "", ""
	for _, b := range bases {
		out, err := git("merge-base", b, "HEAD")
		if err == nil && strings.TrimSpace(out) != "" {
			base, mergeBase = b, strings.TrimSpace(out)
			break
		}
	}
	if mergeBase == "" {
		return "## Changes to Review\n\nItervox could not find the base branch to diff against; review the branch with `git log` and `git diff` yourself."
	}
	stat, _ := git("diff", "--stat", mergeBase, "HEAD")
	diff, _ := git("diff", mergeBase, "HEAD")
	truncated := ""
	if len(diff) > reviewDiffBudgetBytes {
		diff = diff[:reviewDiffBudgetBytes]
		truncated = fmt.Sprintf("\n… diff truncated at %d KB; run `git diff %s HEAD` for the rest.", reviewDiffBudgetBytes/1024, mergeBase[:min(12, len(mergeBase))])
	}
	if strings.TrimSpace(stat) == "" {
		return fmt.Sprintf("## Changes to Review\n\nThe branch has no commits beyond `%s`.", base)
	}
	return fmt.Sprintf("## Changes to Review\n\nDiff of this branch against `%s` (merge base `%s`):\n\n```\n%s```\n\n```diff\n%s```%s",
		base, mergeBase[:min(12, len(mergeBase))], stat, diff, truncated)
}

// buildLatestHandoffBlock renders only the most recent handoff: a reviewer
// needs what the implementer just did, not the whole history.
func buildLatestHandoffBlock(wsPath string) string {
	if wsPath == "" {
		return ""
	}
	entries, err := os.ReadDir(filepath.Join(wsPath, HandoffDirRelPath))
	if err != nil {
		return ""
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names) // ISO8601 prefix: last is newest
	latest := names[len(names)-1]
	data, err := os.ReadFile(filepath.Join(wsPath, HandoffDirRelPath, latest))
	if err != nil {
		return ""
	}
	body := strings.TrimSpace(string(data))
	if len(body) > DefaultHandoffBudgetBytes {
		body = body[:DefaultHandoffBudgetBytes] + "\n… (truncated)"
	}
	return fmt.Sprintf("## Latest Handoff\n\n### %s\n\n%s", latest, body)
}

// reviewerBranchState is what a read-only reviewer must leave unchanged:
// HEAD, the content of every changed file, and the refs on the workspace's
// remotes as the remotes themselves report them (git ls-remote), so a push
// is seen whether it went through a remote name or its URL.
type reviewerBranchState struct {
	Head        string            `json:"head"`
	Dirty       map[string]string `json:"dirty"`        // path -> content hash ("deleted" when gone)
	RemoteRefs  map[string]string `json:"remote_refs"`  // "<remote> <ref>" -> commit
	ReflogCount int               `json:"reflog_count"` // entries in this worktree's HEAD reflog
}

// reviewerBookkeeping are the paths a reviewer is expected to write.
var reviewerBookkeeping = []string{HandoffDirRelPath + "/", ".itervox/review/", EvidenceDirRelPath + "/"}

func captureReviewerBranchState(ctx context.Context, wsPath string) (reviewerBranchState, bool) {
	if wsPath == "" {
		return reviewerBranchState{}, false
	}
	git := func(args ...string) (string, error) {
		out, err := gitexec.Command(ctx, wsPath, args...).Output()
		return string(out), err
	}
	head, err := git("rev-parse", "HEAD")
	if err != nil {
		return reviewerBranchState{}, false
	}
	st := reviewerBranchState{Head: strings.TrimSpace(head), Dirty: map[string]string{}, RemoteRefs: map[string]string{}}
	if out, err := git("status", "--porcelain", "-z", "--untracked-files=all"); err == nil {
		entries := strings.Split(out, "\x00")
		for i := 0; i < len(entries); i++ {
			e := entries[i]
			if len(e) < 4 {
				continue
			}
			path := e[3:]
			if e[0] == 'R' || e[0] == 'C' {
				i++ // a rename's source path follows; the destination is what changed
			}
			if slices.ContainsFunc(reviewerBookkeeping, func(p string) bool { return strings.HasPrefix(path, p) }) {
				continue
			}
			hash, err := git("hash-object", "--", path)
			if err != nil {
				hash = "deleted"
			}
			st.Dirty[path] = strings.TrimSpace(hash)
		}
	}
	if out, err := git("remote"); err == nil {
		for _, remote := range strings.Fields(out) {
			lsCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			refs, err := gitexec.Command(lsCtx, wsPath, "ls-remote", "--heads", "--tags", remote).Output()
			cancel()
			if err != nil {
				continue // unreachable remote: its pushes cannot be checked
			}
			for _, line := range strings.Split(strings.TrimSpace(string(refs)), "\n") {
				if sha, ref, ok := strings.Cut(line, "\t"); ok {
					st.RemoteRefs[remote+" "+ref] = sha
				}
			}
		}
	}
	if out, err := git("reflog", "show", "--format=%H", "HEAD"); err == nil {
		st.ReflogCount = len(strings.Fields(out))
	}
	return st, true
}

// reviewerBaselinePath keeps the "before" state of a reviewer run.
func reviewerBaselinePath(wsPath, identifier, profile string) string {
	return filepath.Join(ReviewDir(wsPath, identifier, profile), "branch-before.json")
}

// reviewerBaseline returns the branch state the reviewer first found. A new
// review (fresh) records it; a retry of the same review (attempt > 0)
// reuses the recorded one, so a change made before a failed turn is still
// caught. A file left by an abandoned or crashed review is overwritten by
// the next fresh review, never reused.
func reviewerBaseline(ctx context.Context, wsPath, identifier, profile string, fresh bool) (reviewerBranchState, bool) {
	path := reviewerBaselinePath(wsPath, identifier, profile)
	if !fresh {
		if raw, err := os.ReadFile(path); err == nil { //nolint:gosec // daemon-controlled path
			var st reviewerBranchState
			if json.Unmarshal(raw, &st) == nil && st.Head != "" {
				return st, true
			}
		}
	}
	st, ok := captureReviewerBranchState(ctx, wsPath)
	if !ok {
		return st, false
	}
	if data, err := json.Marshal(st); err == nil {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
			_ = atomicfs.WriteFile(path, data, 0o644)
		}
	}
	return st, true
}

// clearReviewerBaseline drops the baseline once the review was checked.
func clearReviewerBaseline(wsPath, identifier, profile string) {
	_ = os.Remove(reviewerBaselinePath(wsPath, identifier, profile))
}

// visitedCommits are the commits this worktree's HEAD pointed at during
// the review: the start, the end and every reflog entry added since.
func visitedCommits(ctx context.Context, wsPath string, before, after reviewerBranchState) map[string]bool {
	visited := map[string]bool{before.Head: true, after.Head: true}
	if n := after.ReflogCount - before.ReflogCount; n > 0 {
		out, err := gitexec.Command(ctx, wsPath, "reflog", "show", "--format=%H", "-n", strconv.Itoa(n), "HEAD").Output()
		if err == nil {
			for _, sha := range strings.Fields(string(out)) {
				visited[sha] = true
			}
		}
	}
	return visited
}

// reviewerChanges describes how the branch changed between before and after;
// empty when the reviewer left it alone.
func reviewerChanges(ctx context.Context, wsPath string, before, after reviewerBranchState) []string {
	var out []string
	if after.Head != before.Head {
		if gitexec.Command(ctx, wsPath, "merge-base", "--is-ancestor", before.Head, after.Head).Run() == nil {
			out = append(out, fmt.Sprintf("HEAD moved forward %s → %s (a commit or pull)", short(before.Head), short(after.Head)))
		} else {
			out = append(out, fmt.Sprintf("HEAD moved %s → %s (a reset or checkout)", short(before.Head), short(after.Head)))
		}
	}
	// A remote ref that moved to a commit this worktree's HEAD visited was
	// pushed from here. Other workers' pushes and fetches do not match.
	visited := visitedCommits(ctx, wsPath, before, after)
	var pushed []string
	for key, sha := range after.RemoteRefs {
		if before.RemoteRefs[key] == sha || !visited[sha] {
			continue
		}
		remote, ref, _ := strings.Cut(key, " ")
		pushed = append(pushed, remote+"/"+strings.TrimPrefix(strings.TrimPrefix(ref, "refs/heads/"), "refs/tags/"))
	}
	if len(pushed) > 0 {
		sort.Strings(pushed)
		out = append(out, "pushed "+strings.Join(pushed, ", "))
	}
	var edited []string
	for p, hash := range after.Dirty {
		if before.Dirty[p] != hash {
			edited = append(edited, p)
		}
	}
	for p := range before.Dirty {
		if _, still := after.Dirty[p]; !still && after.Head == before.Head {
			edited = append(edited, p) // reverted or committed away
		}
	}
	if len(edited) > 0 {
		sort.Strings(edited)
		if len(edited) > 5 {
			edited = append(edited[:5], fmt.Sprintf("and %d more", len(edited)-5))
		}
		out = append(out, "edited "+strings.Join(edited, ", "))
	}
	return out
}

func short(sha string) string { return sha[:min(12, len(sha))] }

// flagReviewerVerdict rewrites the reviewer's verdict file as a block that
// records how it changed the branch, so the quorum (and the posted comment)
// count it as one. A reviewer that changed the code has reviewed its own
// change, not the implementer's.
func flagReviewerVerdict(wsPath, identifier, profile string, changes []string) {
	reason := "reviewer changed the branch, which reviewers must not do: " + strings.Join(changes, "; ")
	v := ReviewVerdict{Verdict: ReviewVerdictBlock}
	path := filepath.Join(ReviewDir(wsPath, identifier, profile), ReviewVerdictFileName)
	if raw, err := os.ReadFile(path); err == nil { //nolint:gosec // daemon-controlled path
		_ = json.Unmarshal(raw, &v)
	}
	v.Verdict = ReviewVerdictBlock
	v.Reasons = append([]string{reason}, v.Reasons...)
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		slog.Warn("review: cannot record flagged verdict", "identifier", identifier, "error", err)
		return
	}
	if err := atomicfs.WriteFile(path, data, 0o644); err != nil {
		slog.Warn("review: cannot record flagged verdict", "identifier", identifier, "error", err)
	}
}

// formatReviewVerdictComment is the issue comment for one reviewer's verdict.
func formatReviewVerdictComment(profile, backend string, v ReviewVerdict, readErr error, changes []string) string {
	var b strings.Builder
	who := "`" + profile + "`"
	if backend != "" {
		who += " (" + backend + ")"
	}
	switch {
	case readErr != nil:
		fmt.Fprintf(&b, "**Review by %s: ❌ no verdict recorded** (counts as a block)\n", who)
	case v.Verdict == ReviewVerdictApprove:
		fmt.Fprintf(&b, "**Review by %s: ✅ approve**\n", who)
	default:
		fmt.Fprintf(&b, "**Review by %s: ❌ changes requested**\n", who)
	}
	if len(changes) > 0 {
		fmt.Fprintf(&b, "\n⚠️ This reviewer changed the branch (%s). Reviewers are read-only, so its verdict counts as a block. Check those changes before merging.\n", strings.Join(changes, "; "))
	}
	if len(v.Reasons) > 0 {
		b.WriteString("\n")
		for _, r := range v.Reasons {
			fmt.Fprintf(&b, "- %s\n", strings.TrimSpace(r))
		}
	}
	if len(v.Comments) > 0 {
		b.WriteString("\n**Line comments**\n\n")
		for _, c := range v.Comments {
			loc := "`" + c.Path
			if c.Line > 0 {
				loc += fmt.Sprintf(":%d", c.Line)
			}
			loc += "`"
			fmt.Fprintf(&b, "- %s %s\n", loc, strings.TrimSpace(c.Body))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// formatReviewOutcomeComment is the issue comment when the quorum closes.
func formatReviewOutcomeComment(outcome ReviewOutcome, verdicts []ReviewVerdict) string {
	var b strings.Builder
	if outcome.Blocked {
		fmt.Fprintf(&b, "**Review result: ❌ changes requested** (%d approve / %d block, quorum `%s`)\n", outcome.Approvals, outcome.Blocks, outcome.Quorum)
	} else {
		fmt.Fprintf(&b, "**Review result: ✅ approved** (%d approve / %d block, quorum `%s`)\n", outcome.Approvals, outcome.Blocks, outcome.Quorum)
	}
	for _, v := range verdicts {
		fmt.Fprintf(&b, "- `%s`: %s\n", v.Profile, v.Verdict)
	}
	if outcome.Blocked {
		b.WriteString("\nThe reasons and line comments are in the reviewer comments above. Move the issue back to an active state to have the implementer address them.")
	}
	return strings.TrimRight(b.String(), "\n")
}

// postReviewComment posts body on the issue off the event loop.
func (o *Orchestrator) postReviewComment(issueID, identifier, body string) {
	goSafe(&o.commentWg, "review-comment", identifier, func() {
		ctx, cancel := context.WithTimeout(context.Background(), postRunTimeout)
		defer cancel()
		if err := o.writeSink().CreateComment(ctx, issueID, identifier, tracker.MarkManagedComment(body)); err != nil {
			slog.Warn("review: posting review comment failed", "identifier", identifier, "error", err)
		}
	}, o.withPanicFailure("review-comment", identifier, nil))
}
