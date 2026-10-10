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
	skipVerdict      bool
	mu               sync.Mutex
	commands         []string
	prompts          []string
}

func (r *reviewScriptRunner) RunTurn(_ context.Context, _ agent.Logger, _ func(agent.TurnResult), _ *string, prompt, ws, command, _, _ string, _, _ int, _ agent.PermissionMode) (agent.TurnResult, error) {
	r.mu.Lock()
	r.commands = append(r.commands, command)
	r.prompts = append(r.prompts, prompt)
	skip := r.skipVerdict
	r.mu.Unlock()
	if m := verdictPathRe.FindStringSubmatch(prompt); m != nil && !skip {
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
	assert.Contains(t, comments, "⚠️ This reviewer changed the branch (HEAD moved forward")

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

// TestReadOnlyReviewerRunCommitsNothing (#79): with a non-terminal
// completion_state the reviewer runs to completion like a normal worker,
// and Itervox still commits nothing on its behalf: the branch after the
// review is exactly the branch the implementer left.
func TestReadOnlyReviewerRunCommitsNothing(t *testing.T) {
	ws := gitInitRepo(t)
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 50
	cfg.Tracker.WorkingState = "In Progress"
	cfg.Tracker.CompletionState = "In Review"
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
	runner := &reviewScriptRunner{}
	orch := orchestrator.New(cfg, mt, runner, &recordingWorkspaceProvider{path: ws})
	orch.SetIssueProfile("ENG-1", "implementer")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	go orch.Run(ctx) //nolint:errcheck

	require.Eventually(t, func() bool {
		issues, err := mt.FetchIssueStatesByIDs(context.Background(), []string{"id1"})
		return strings.Contains(issueComments(t, mt), "Review by `reviewer` (claude): ✅ approve") &&
			err == nil && len(issues) == 1 && issues[0].State == "In Review"
	}, 6*time.Second, 25*time.Millisecond, "the reviewer completes and the issue returns to In Review")
	time.Sleep(200 * time.Millisecond)

	log, err := gitexec.Command(context.Background(), ws, "log", "--format=%s").Output()
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(log), "chore(itervox): record agent handoff"),
		"only the implementer's handoff is committed, never the reviewer's: %s", log)
	assert.NotContains(t, issueComments(t, mt), "changed the branch", "an untouched branch is not flagged")
	entries, err := os.ReadDir(filepath.Join(ws, ".itervox", "handoff"))
	require.NoError(t, err)
	assert.Len(t, entries, 2, "the reviewer's handoff is written but left uncommitted")
	_, err = os.Stat(filepath.Join(ws, ".itervox", "review", "ENG-1", "reviewer", "branch-before.json"))
	assert.True(t, os.IsNotExist(err), "the review's baseline is dropped once the review was checked")
}

// TestCompletionStateFollowsSettings (#79): the completion state that keeps
// a reviewer alive is re-read every tick, so a settings change applies.
func TestCompletionStateFollowsSettings(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Tracker.CompletionState = "In Review"
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := orchestrator.New(cfg, mt, &reviewScriptRunner{}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go orch.Run(ctx) //nolint:errcheck
	require.Eventually(t, func() bool { return orch.Snapshot().CompletionState == "In Review" }, 2*time.Second, 20*time.Millisecond)
	orch.SetTrackerStatesCfg(cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates, "Ready for Review")
	require.Eventually(t, func() bool { return orch.Snapshot().CompletionState == "Ready for Review" }, 2*time.Second, 20*time.Millisecond)
}

// TestReconcileKeepsReviewerOnlyInCompletionState (#79): a reviewer run is
// kept while its issue sits in completion_state, but an operator moving the
// issue elsewhere (backlog) still stops it, as it stops any worker.
func TestReconcileKeepsReviewerOnlyInCompletionState(t *testing.T) {
	for _, tc := range []struct {
		trackerState string
		kept         bool
	}{{"In Review", true}, {"Backlog", false}, {"In Progress", true}} {
		t.Run(tc.trackerState, func(t *testing.T) {
			cfg := baseConfig()
			cfg.Tracker.CompletionState = "In Review"
			mt := tracker.NewMemoryTracker([]domain.Issue{makeIssue("id1", "ENG-1", tc.trackerState, nil, nil)},
				cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
			state := orchestrator.NewState(cfg)
			state.Running["id1"] = &orchestrator.RunEntry{Issue: makeIssue("id1", "ENG-1", "In Review", nil, nil), Kind: "reviewer"}
			events := make(chan orchestrator.OrchestratorEvent, 4)
			state = orchestrator.ReconcileTrackerStates(context.Background(), state, mt, events, func(string) {})
			_, running := state.Running["id1"]
			assert.Equal(t, tc.kept, running)
		})
	}
}

// TestReviewerVerdictIsNotReusedAcrossRounds (#79): a reviewer that records
// no verdict in a later round is a block ("no verdict recorded"), not the
// earlier round's verdict posted again.
func TestReviewerVerdictIsNotReusedAcrossRounds(t *testing.T) {
	ws := gitInitRepo(t)
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 50
	cfg.Tracker.WorkingState = "In Progress"
	cfg.Tracker.CompletionState = "In Review"
	cfg.Agent.MaxTurns = 1
	cfg.Agent.ReviewerProfile = "reviewer"
	cfg.Agent.Profiles = map[string]config.AgentProfile{"reviewer": {Command: "claude", Instructions: "Review it."}}
	mt := tracker.NewMemoryTracker([]domain.Issue{makeIssue("id1", "ENG-1", "In Review", nil, nil)},
		cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	runner := &reviewScriptRunner{}
	orch := orchestrator.New(cfg, mt, runner, &recordingWorkspaceProvider{path: ws})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	go orch.Run(ctx) //nolint:errcheck

	require.Eventually(t, func() bool { return orch.DispatchReviewer("ENG-1") == nil }, 2*time.Second, 20*time.Millisecond)
	require.Eventually(t, func() bool {
		return strings.Contains(issueComments(t, mt), "Review by `reviewer` (claude): ✅ approve")
	}, 6*time.Second, 25*time.Millisecond)

	runner.mu.Lock()
	runner.skipVerdict = true // round 2: the reviewer writes nothing
	runner.mu.Unlock()
	require.Eventually(t, func() bool {
		_, running := orch.Snapshot().Running["id1"]
		return !running && orch.DispatchReviewer("ENG-1") == nil
	}, 3*time.Second, 25*time.Millisecond)
	require.Eventually(t, func() bool {
		return strings.Contains(issueComments(t, mt), "no verdict recorded")
	}, 6*time.Second, 25*time.Millisecond, "round 2 must not reuse round 1's approval")
	assert.Equal(t, 1, strings.Count(issueComments(t, mt), "✅ approve"))
}

// TestEveryChainReviewerGetsTheReviewerTemplate (#79): reviewers 2..n of a
// chain render reviewer_prompt like the first, not the implementer prompt.
func TestEveryChainReviewerGetsTheReviewerTemplate(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 50
	cfg.Tracker.CompletionState = "Done"
	cfg.Agent.MaxTurns = 1
	cfg.Agent.AutoReview = true
	cfg.PromptTemplate = "IMPLEMENTER TEMPLATE {{ issue.identifier }}"
	cfg.Agent.ReviewerPrompt = "REVIEWER TEMPLATE {{ issue.identifier }}"
	cfg.Agent.ReviewerProfile = "reviewer-codex"
	cfg.Agent.ReviewerProfiles = []string{"reviewer-codex", "reviewer"}
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"implementer":    {Command: "claude"},
		"reviewer":       {Command: "claude"},
		"reviewer-codex": {Command: "codex", Backend: "codex"},
	}
	mt := tracker.NewMemoryTracker([]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	runner := &reviewScriptRunner{}
	orch := orchestrator.New(cfg, mt, runner, &recordingWorkspaceProvider{path: t.TempDir()})
	orch.SetIssueProfile("ENG-1", "implementer")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	go orch.Run(ctx) //nolint:errcheck
	require.Eventually(t, func() bool { return strings.Contains(issueComments(t, mt), "Review result") }, 6*time.Second, 25*time.Millisecond)

	_, prompts := runner.snapshot()
	require.Len(t, prompts, 3)
	assert.True(t, strings.HasPrefix(prompts[0], "IMPLEMENTER TEMPLATE ENG-1"))
	assert.True(t, strings.HasPrefix(prompts[1], "REVIEWER TEMPLATE ENG-1"), "first reviewer")
	assert.True(t, strings.HasPrefix(prompts[2], "REVIEWER TEMPLATE ENG-1"), "second reviewer of the chain")
}

// TestOrchestratorNeverPushesForAReviewer (#79): on a PR-continuation run
// Itervox pushes the implementer's work, but never the branch after a
// reviewer run: a reviewer's local commit does not reach the remote through
// Itervox (and is flagged).
func TestOrchestratorNeverPushesForAReviewer(t *testing.T) {
	ws := gitInitRepo(t)
	origin := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "--bare", "-b", "main"}} {
		out, err := gitexec.Command(context.Background(), origin, args...).CombinedOutput()
		require.NoError(t, err, "%s", out)
	}
	for _, args := range [][]string{{"remote", "add", "origin", origin}, {"push", "-q", "origin", "itervox/eng-1"}} {
		out, err := gitexec.Command(context.Background(), ws, args...).CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	const prURL = "https://github.com/o/r/pull/9"
	ghDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(ghDir, "gh"), []byte(`#!/bin/sh
case "$*" in
  "pr view `+prURL+` --json state,headRefName,body,isDraft") echo '{"state":"OPEN","headRefName":"itervox/eng-1","body":"","isDraft":false}' ;;
esac
`), 0o755))
	t.Setenv("PATH", ghDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := baseConfig()
	cfg.Polling.IntervalMs = 50
	cfg.Tracker.WorkingState = "In Progress"
	cfg.Tracker.CompletionState = "In Review"
	cfg.Agent.MaxTurns = 1
	cfg.Agent.AutoReview = true
	cfg.Agent.ReviewerProfile = "reviewer"
	cfg.Workspace.BaseBranch = "main"
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"implementer": {Command: "claude", Instructions: "Implement it."},
		"reviewer":    {Command: "claude", Instructions: "Review it."},
	}
	issue := makeIssue("id1", "ENG-1", "In Progress", nil, nil)
	desc := "Follow-up on " + prURL
	issue.Description = &desc
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	runner := &reviewScriptRunner{commitAsReviewer: true}
	orch := orchestrator.New(cfg, mt, runner, &recordingWorkspaceProvider{path: ws})
	orch.SetIssueProfile("ENG-1", "implementer")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	go orch.Run(ctx) //nolint:errcheck
	require.Eventually(t, func() bool {
		_, running := orch.Snapshot().Running["id1"]
		issues, err := mt.FetchIssueStatesByIDs(context.Background(), []string{"id1"})
		return strings.Contains(issueComments(t, mt), "Review by `reviewer`") && !running &&
			err == nil && issues[0].State == "In Review"
	}, 6*time.Second, 25*time.Millisecond, "the reviewer run finishes completely (a push would come after its comment)")
	time.Sleep(200 * time.Millisecond)

	remote, err := gitexec.Command(context.Background(), origin, "log", "--format=%s", "itervox/eng-1").Output()
	require.NoError(t, err)
	assert.Contains(t, string(remote), "chore(itervox): record agent handoff", "the implementer's run was pushed")
	assert.NotContains(t, string(remote), "fix: reviewer corrections", "the reviewer's commit was not pushed by Itervox")
	assert.Contains(t, issueComments(t, mt), "⚠️ This reviewer changed the branch (HEAD moved forward")
}
