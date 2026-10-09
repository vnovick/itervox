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
	"strings"

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

// reviewerBranchState is what a read-only reviewer must leave unchanged.
type reviewerBranchState struct {
	Head  string
	Dirty map[string]bool // tracked or untracked paths outside .itervox/
}

func captureReviewerBranchState(ctx context.Context, wsPath string) (reviewerBranchState, bool) {
	if wsPath == "" {
		return reviewerBranchState{}, false
	}
	head, err := gitexec.Command(ctx, wsPath, "rev-parse", "HEAD").Output()
	if err != nil {
		return reviewerBranchState{}, false
	}
	st := reviewerBranchState{Head: strings.TrimSpace(string(head)), Dirty: map[string]bool{}}
	out, err := gitexec.Command(ctx, wsPath, "status", "--porcelain", "--untracked-files=all").Output()
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if len(line) < 4 {
				continue
			}
			p := strings.TrimSpace(line[3:])
			if strings.HasPrefix(p, ".itervox/") {
				continue // handoff, verdict and evidence files are expected
			}
			st.Dirty[p] = true
		}
	}
	return st, true
}

// reviewerChanges describes how the branch changed between before and after;
// empty when the reviewer left it alone.
func reviewerChanges(ctx context.Context, wsPath string, before, after reviewerBranchState) []string {
	var out []string
	if after.Head != before.Head {
		msg := fmt.Sprintf("committed (HEAD moved %s → %s)", short(before.Head), short(after.Head))
		if pushedTo := remoteRefsAt(ctx, wsPath, after.Head); len(pushedTo) > 0 {
			msg += "; pushed to " + strings.Join(pushedTo, ", ")
		}
		out = append(out, msg)
	}
	var edited []string
	for p := range after.Dirty {
		if !before.Dirty[p] {
			edited = append(edited, p)
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

// remoteRefsAt lists remote-tracking refs pointing at commit: a reviewer's
// own commit there means it was pushed from this worktree.
func remoteRefsAt(ctx context.Context, wsPath, commit string) []string {
	out, err := gitexec.Command(ctx, wsPath, "for-each-ref", "--points-at", commit, "--format=%(refname:short)", "refs/remotes").Output()
	if err != nil {
		return nil
	}
	return strings.Fields(string(out))
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
