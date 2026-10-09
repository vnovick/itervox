package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/gitexec"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
)

var verdictPathRe = regexp.MustCompile("run\\.review_verdict_path: `([^`]+)`")

// reviewScriptRunner plays a worker and its reviewers: a reviewer (any run
// whose prompt names a verdict path) writes the verdict for its command —
// codex blocks with a reason and a line comment, claude approves — and, with
// commitAsReviewer, also commits a change, which reviewers must not do.
type reviewScriptRunner struct {
	commitAsReviewer bool
	mu               sync.Mutex
	commands         []string
	prompts          []string
}

func (r *reviewScriptRunner) RunTurn(_ context.Context, _ agent.Logger, _ func(agent.TurnResult), _ *string, prompt, ws, command, _, _ string, _, _ int, _ agent.PermissionMode) (agent.TurnResult, error) {
	r.mu.Lock()
	r.commands = append(r.commands, command)
	r.prompts = append(r.prompts, prompt)
	r.mu.Unlock()
	if m := verdictPathRe.FindStringSubmatch(prompt); m != nil {
		verdict := `{"verdict":"approve","reasons":["looks correct"]}`
		if strings.HasPrefix(command, "codex") {
			verdict = `{"verdict":"block","reasons":["missing test for the empty config"],` +
				`"comments":[{"path":"internal/foo.go","line":7,"body":"nil map write when cfg is empty"}]}`
		}
		p := filepath.Join(ws, m[1])
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return agent.TurnResult{}, err
		}
		if err := os.WriteFile(p, []byte(verdict), 0o644); err != nil {
			return agent.TurnResult{}, err
		}
		if r.commitAsReviewer {
			if err := os.WriteFile(filepath.Join(ws, "reviewer-fix.go"), []byte("package x\n"), 0o644); err != nil {
				return agent.TurnResult{}, err
			}
			for _, args := range [][]string{{"add", "reviewer-fix.go"}, {"commit", "-q", "-m", "fix: reviewer corrections"}} {
				if out, err := gitexec.Command(context.Background(), ws, args...).CombinedOutput(); err != nil {
					return agent.TurnResult{}, &gitErr{args: args, out: string(out)}
				}
			}
		}
	}
	return agent.TurnResult{SessionID: "s", InputTokens: 1, OutputTokens: 1, ResultText: "done"}, nil
}

type gitErr struct {
	args []string
	out  string
}

func (e *gitErr) Error() string { return "git " + strings.Join(e.args, " ") + ": " + e.out }

func (r *reviewScriptRunner) snapshot() ([]string, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.commands...), append([]string{}, r.prompts...)
}

func issueComments(t *testing.T, mt *tracker.MemoryTracker) string {
	t.Helper()
	d, err := mt.FetchIssueDetail(context.Background(), "id1")
	require.NoError(t, err)
	var all []string
	for _, c := range d.Comments {
		all = append(all, c.Body)
	}
	return strings.Join(all, "\n\n")
}

func gitInitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
		{"commit", "-q", "--allow-empty", "-m", "base"},
		{"checkout", "-q", "-b", "itervox/eng-1"},
	} {
		out, err := gitexec.Command(context.Background(), dir, args...).CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "feature.go"), []byte("package x\n\nfunc Feature() {}\n"), 0o644))
	for _, args := range [][]string{{"add", "feature.go"}, {"commit", "-q", "-m", "implement feature"}} {
		out, err := gitexec.Command(context.Background(), dir, args...).CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	return dir
}

// TestCrossReviewRunsWorkerAndTwoReviewersOnDifferentBackends (#79): the
// preset's shape — a Claude implementer reviewed by a Codex and a Claude
// reviewer — runs one worker and both reviewers on different backends, and
// each verdict (reasons, line comments) plus the quorum result is posted on
// the issue.
func TestCrossReviewRunsWorkerAndTwoReviewersOnDifferentBackends(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 50
	cfg.Tracker.CompletionState = "Done"
	cfg.Agent.MaxTurns = 1
	cfg.Agent.AutoReview = true
	cfg.Agent.ReviewerProfile = "reviewer-codex"
	cfg.Agent.ReviewerProfiles = []string{"reviewer-codex", "reviewer"}
	cfg.Agent.ReviewQuorum = config.ReviewQuorumAnyBlock
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"implementer":    {Command: "claude", Instructions: "Implement it."},
		"reviewer":       {Command: "claude", Instructions: "Review it."},
		"reviewer-codex": {Command: "codex", Backend: "codex", Instructions: "Review it."},
	}
	mt := tracker.NewMemoryTracker([]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	runner := &reviewScriptRunner{}
	orch := orchestrator.New(cfg, mt, runner, &recordingWorkspaceProvider{path: t.TempDir()})
	orch.SetIssueProfile("ENG-1", "implementer")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	go orch.Run(ctx) //nolint:errcheck

	require.Eventually(t, func() bool {
		return strings.Contains(issueComments(t, mt), "Review result")
	}, 6*time.Second, 25*time.Millisecond, "the quorum result must be posted on the issue")
	time.Sleep(300 * time.Millisecond) // no further runs may follow

	commands, _ := runner.snapshot()
	require.Len(t, commands, 3, "one worker and two reviewers: %v", commands)
	assert.True(t, strings.HasPrefix(commands[0], "claude"), "the worker runs claude: %v", commands)
	assert.ElementsMatch(t, []string{"codex", "claude"}, []string{firstWord(commands[1]), firstWord(commands[2])},
		"the two reviewers run on different backends: %v", commands)

	comments := issueComments(t, mt)
	assert.Contains(t, comments, "Review by `reviewer-codex` (codex): ❌ changes requested")
	assert.Contains(t, comments, "- missing test for the empty config")
	assert.Contains(t, comments, "- `internal/foo.go:7` nil map write when cfg is empty")
	assert.Contains(t, comments, "Review by `reviewer` (claude): ✅ approve")
	assert.Contains(t, comments, "Review result: ❌ changes requested")
}

func firstWord(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return ""
}

// TestReadOnlyReviewerThatCommitsIsFlagged (#79): a reviewer that commits to
// the branch (which a push needs) is flagged on the issue and its approval
// counts as a block; the orchestrator itself never commits a reviewer's
// handoff. The reviewer gets the diff against the base branch and the
// latest handoff, not the whole handoff history.
func TestReadOnlyReviewerThatCommitsIsFlagged(t *testing.T) {
	ws := gitInitRepo(t)
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 50
	cfg.Tracker.CompletionState = "Done"
	cfg.Agent.MaxTurns = 1
	cfg.Agent.AutoReview = true
	cfg.Agent.ReviewerProfile = "reviewer"
	cfg.Workspace.BaseBranch = "main"
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"implementer": {Command: "claude", Instructions: "Implement it."},
		"reviewer":    {Command: "claude", Instructions: "Review it."},
	}
	mt := tracker.NewMemoryTracker([]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	runner := &reviewScriptRunner{commitAsReviewer: true}
	orch := orchestrator.New(cfg, mt, runner, &recordingWorkspaceProvider{path: ws})
	orch.SetIssueProfile("ENG-1", "implementer")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	go orch.Run(ctx) //nolint:errcheck

	require.Eventually(t, func() bool {
		return strings.Contains(issueComments(t, mt), "Review by `reviewer`")
	}, 6*time.Second, 25*time.Millisecond)

	comments := issueComments(t, mt)
	assert.Contains(t, comments, "Review by `reviewer` (claude): ❌ changes requested",
		"an approval from a reviewer that changed the branch counts as a block")
	assert.Contains(t, comments, "⚠️ This reviewer changed the branch (committed")

	log, err := gitexec.Command(context.Background(), ws, "log", "--format=%s").Output()
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(log), "chore(itervox): record agent handoff"),
		"only the implementer's handoff is committed, never the reviewer's: %s", log)

	_, prompts := runner.snapshot()
	require.Len(t, prompts, 2)
	reviewerPrompt := prompts[1]
	assert.Contains(t, reviewerPrompt, "## Changes to Review")
	assert.Contains(t, reviewerPrompt, "+func Feature() {}", "the diff against main is inlined")
	assert.Contains(t, reviewerPrompt, "## Latest Handoff")
	assert.NotContains(t, reviewerPrompt, "## Prior Agent Handoffs")
	assert.Contains(t, reviewerPrompt, "You are read-only")
}
