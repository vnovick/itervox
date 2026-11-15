package main

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/gitexec"
	"github.com/vnovick/itervox/internal/logging"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/workspace"
)

// Question markers delimit the question in a run's question handoff, so
// the next run (--answer / --approve) can quote it to the agent.
const (
	runQuestionStart = "<!-- itervox:question -->"
	runQuestionEnd   = "<!-- /itervox:question -->"
	// runQuestionRole names the question handoff files
	// (.itervox/handoff/<timestamp>_question.md).
	runQuestionRole = "question"
)

// lockRunIssue takes the per-issue lock (.itervox/run/issue-<id>.lock in
// the checkout): one `itervox run` per issue at a time.
func lockRunIssue(dir, issue string) (func(), error) {
	lockDir := filepath.Join(dir, filepath.FromSlash(workspace.RunDirRel))
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", lockDir, err)
	}
	path := filepath.Join(lockDir, "issue-"+workspace.SanitizeKey(issue)+".lock")
	release, acquired, err := tryLockPIDFile(path)
	if err != nil {
		return nil, err
	}
	if !acquired {
		return nil, fmt.Errorf("another itervox run is working on %s in this checkout (%s)", issue, path)
	}
	return release, nil
}

// runBaseBranch is the branch the issue's PR targets: the branch the
// checkout was on when the run started (in CI, the default branch), else
// workspace.base_branch.
func runBaseBranch(ctx context.Context, cfg *config.Config, dir, issueBranch string) string {
	if cur := workspace.GetCurrentBranch(ctx, dir); cur != "" && cur != issueBranch {
		return cur
	}
	if cfg.Workspace.BaseBranch != "" {
		return cfg.Workspace.BaseBranch
	}
	return "main"
}

func gitRefResolves(ctx context.Context, dir, ref string) bool {
	return gitexec.Command(ctx, dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}").Run() == nil
}

// pushBranch pushes branch from dir to origin.
func pushBranch(ctx context.Context, dir, branch string) error {
	ref := "refs/heads/" + branch
	if out, err := gitexec.Command(ctx, dir, "push", "origin", ref+":"+ref).CombinedOutput(); err != nil {
		return fmt.Errorf("git push origin %s: %w: %s", branch, err, logging.RedactString(strings.TrimSpace(string(out))))
	}
	return nil
}

// openPR is OneShot.OpenPR: when the branch has commits beyond the base, it
// pushes the branch and opens the PR (deps.prCreate; gh in production).
func (s *runSession) openPR(ctx context.Context, wsPath, branch string, issue domain.Issue) (string, error) {
	if branch == "" || workspace.IsDefaultBranch(branch) || branch == s.base {
		return "", nil
	}
	baseRef := s.base
	if gitRefResolves(ctx, wsPath, "refs/remotes/origin/"+s.base) {
		baseRef = "refs/remotes/origin/" + s.base
	}
	out, err := gitexec.Command(ctx, wsPath, "rev-list", "--count", baseRef+"..refs/heads/"+branch).Output()
	if err != nil {
		return "", fmt.Errorf("count commits on %s: %w", branch, err)
	}
	if strings.TrimSpace(string(out)) == "0" {
		slog.Info("run: the branch has no commits beyond the base; no pull request", "branch", branch, "base", s.base)
		return "", nil
	}
	if err := pushBranch(ctx, wsPath, branch); err != nil {
		return "", err
	}
	s.mu.Lock()
	s.prPushed = true
	s.mu.Unlock()
	title := issue.Identifier + ": " + issue.Title
	return s.deps.prCreate(ctx, wsPath, branch, s.base, title, runPRBody(issue, s.cfg.Tracker.Kind))
}

// runPRBody links the PR to its issue (GitHub closes it on merge).
func runPRBody(issue domain.Issue, trackerKind string) string {
	var b strings.Builder
	if trackerKind == "github" && strings.HasPrefix(issue.Identifier, "#") {
		b.WriteString("Closes " + issue.Identifier + "\n\n")
	} else {
		b.WriteString("Resolves " + issue.Identifier + "\n\n")
	}
	if issue.URL != nil && *issue.URL != "" {
		b.WriteString(*issue.URL + "\n\n")
	}
	b.WriteString("Opened by `itervox run`.\n")
	return b.String()
}

// ghCreatePR opens a PR with `gh pr create` and returns its URL (the last
// line gh prints).
func ghCreatePR(ctx context.Context, dir, head, base, title, body string) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", "pr", "create", "--head", head, "--base", base, "--title", title, "--body", body)
	cmd.Dir = dir
	cmd.Env = gitexec.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("gh pr create: %w: %s", err, logging.RedactString(strings.TrimSpace(string(out))))
	}
	lines := strings.Fields(string(out))
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], "https://") {
			return lines[i], nil
		}
	}
	return "", fmt.Errorf("gh pr create printed no PR URL")
}

// resumeContext continues the previous run when --answer or --approve is
// given: the question it asked (from its handoff on the issue branch) and
// the operator's reply.
func (s *runSession) resumeContext(ctx context.Context) *orchestrator.ResumeContext {
	if s.opts.answer == "" && s.opts.approve == "" {
		return nil
	}
	var reply []string
	if s.opts.answer != "" {
		reply = append(reply, s.opts.answer)
	}
	if s.opts.approve != "" {
		reply = append(reply, "The operator approved `"+s.opts.approve+"` for this run: run it again and it will be allowed.")
	}
	branch := workspace.ResolveWorktreeBranch(s.issue.BranchName, s.issue.Identifier)
	return &orchestrator.ResumeContext{
		InputContext: previousQuestion(ctx, s.dir, branch),
		UserMessage:  strings.Join(reply, "\n\n"),
	}
}

// previousQuestion reads the question of the latest question handoff
// committed on branch (origin's copy when there is one); "" when none.
func previousQuestion(ctx context.Context, dir, branch string) string {
	_ = gitexec.Command(ctx, dir, "fetch", "--quiet", "origin", "+refs/heads/"+branch+":refs/remotes/origin/"+branch).Run()
	ref := "refs/remotes/origin/" + branch
	if !gitRefResolves(ctx, dir, ref) {
		ref = "refs/heads/" + branch
		if !gitRefResolves(ctx, dir, ref) {
			return ""
		}
	}
	out, err := gitexec.Command(ctx, dir, "ls-tree", "--name-only", ref, orchestrator.HandoffDirRelPath+"/").Output()
	if err != nil {
		return ""
	}
	var names []string
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		if name := strings.TrimSpace(sc.Text()); strings.HasSuffix(name, "_"+runQuestionRole+".md") {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	slices.Sort(names)
	body, err := gitexec.Command(ctx, dir, "show", ref+":"+names[len(names)-1]).Output()
	if err != nil {
		return ""
	}
	text := string(body)
	if _, after, ok := strings.Cut(text, runQuestionStart); ok {
		text, _, _ = strings.Cut(after, runQuestionEnd)
	}
	return strings.TrimSpace(text)
}

// finalize publishes what the next run needs: on exit 3 the question as a
// committed handoff, pushed with the branch; on success the branch again,
// so the handoff committed after the PR was opened reaches origin.
func (s *runSession) finalize(ctx context.Context, sum runSummary) runSummary {
	if s.ws == nil || sum.Branch == "" || workspace.IsDefaultBranch(sum.Branch) || sum.Branch == s.base {
		return sum
	}
	wsPath := s.ws.ResolvePath(s.issue.Identifier)
	s.mu.Lock()
	pushed := s.prPushed
	s.mu.Unlock()
	switch {
	case sum.ExitCode == runExitNeedsInput && sum.asked:
		rel := orchestrator.HandoffPathFor(time.Now(), runQuestionRole)
		if err := os.MkdirAll(filepath.Join(wsPath, filepath.Dir(rel)), 0o755); err != nil {
			sum.Warnings = append(sum.Warnings, "question handoff not written: "+err.Error())
			return sum
		}
		if err := os.WriteFile(filepath.Join(wsPath, rel), []byte(questionHandoff(sum, s.issue.Identifier)), 0o644); err != nil {
			sum.Warnings = append(sum.Warnings, "question handoff not written: "+err.Error())
			return sum
		}
		if _, err := workspace.CommitPathOnly(ctx, wsPath, orchestrator.HandoffDirRelPath, "chore(itervox): record the question for the operator"); err != nil {
			sum.Warnings = append(sum.Warnings, "question handoff not committed: "+err.Error())
		}
	case sum.ExitCode == runExitOK && pushed:
	default:
		return sum
	}
	if err := pushBranch(ctx, wsPath, sum.Branch); err != nil {
		sum.Warnings = append(sum.Warnings, err.Error())
	}
	return sum
}

// questionHandoff is the handoff a run that exits 3 commits: the question
// between markers (read back by the next run) and how to answer it.
func questionHandoff(sum runSummary, identifier string) string {
	var b strings.Builder
	b.WriteString("# Question for the operator\n\n")
	b.WriteString("`itervox run` stopped on " + identifier + " (exit 3) and continues once it is answered.\n\n")
	b.WriteString(runQuestionStart + "\n" + strings.TrimSpace(sum.Question) + "\n" + runQuestionEnd + "\n\n")
	b.WriteString("Answer with an issue comment `/itervox <answer>`, or run `itervox run --issue " + identifier + " --answer \"…\"`.\n")
	for _, c := range sum.ApprovalCommands {
		b.WriteString("Approve with `/itervox approve " + c + "`, or run `itervox run --issue " + identifier + " --approve \"" + c + "\"`.\n")
	}
	return b.String()
}

// copyRedactedIssueLogs copies the staged per-issue logs into dst with every
// line redacted.
func copyRedactedIssueLogs(staging, dst string) {
	_ = filepath.WalkDir(staging, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(staging, path)
		if err != nil {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		out := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return nil
		}
		lines := strings.Split(string(data), "\n")
		for i := range lines {
			lines[i] = logging.RedactString(lines[i])
		}
		if err := os.WriteFile(out, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
			slog.Warn("run: issue log not copied", "path", out, "error", err)
		}
		return nil
	})
}
