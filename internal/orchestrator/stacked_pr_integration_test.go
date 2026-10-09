package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/workspace"
)

// stackedWorkspaceProvider returns a workspace that reports it is stacked on
// stackedOn, the way workspace.Manager does for a worktree created from (or
// containing) its blocker's branch.
type stackedWorkspaceProvider struct {
	path, stackedOn string
}

func (p *stackedWorkspaceProvider) EnsureWorkspace(_ context.Context, identifier, _ string) (workspace.Workspace, error) {
	if err := os.MkdirAll(p.path, 0o755); err != nil {
		return workspace.Workspace{}, err
	}
	return workspace.Workspace{Path: p.path, Identifier: identifier, StackedOn: p.stackedOn}, nil
}
func (p *stackedWorkspaceProvider) RemoveWorkspace(context.Context, string, string) error { return nil }
func (p *stackedWorkspaceProvider) ResolvePath(string) string                             { return p.path }

// installFakeGHForPR puts a `gh` on PATH that reports prURL as the open PR of
// the current branch, serves its base from a file, applies `pr edit --base`,
// and logs every call.
func installFakeGHForPR(t *testing.T, prURL string) (calls func() []string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	base := filepath.Join(dir, "base")
	require.NoError(t, os.WriteFile(base, []byte("main\n"), 0o644))
	script := `#!/bin/sh
echo "$*" >> "` + log + `"
case "$*" in
  "pr view --json url,state "*) echo "` + prURL + `" ;;
  "pr view ` + prURL + ` --json baseRefName"*) cat "` + base + `" ;;
  "pr edit ` + prURL + ` --base "*) printf '%s\n' "$5" > "` + base + `" ;;
esac
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() []string {
		b, err := os.ReadFile(log)
		if err != nil {
			return nil
		}
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
}

// runStackedWorker dispatches ENG-2 once and waits for it to complete.
func runStackedWorker(t *testing.T, stackedOn string) (prompt string, ghCalls []string) {
	t.Helper()
	const prURL = "https://github.com/o/r/pull/12"
	calls := installFakeGHForPR(t, prURL)

	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 1
	cfg.Tracker.CompletionState = "Done"
	cfg.Workspace.BaseBranch = "main"
	cfg.Dependencies.StackedPRs = true
	cfg.PromptTemplate = "Implement {{ issue.identifier }}; open the PR against {{ run.pr_base_branch }}."

	mt := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id2", "ENG-2", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates,
	)
	runner := &promptCaptureRunner{done: make(chan struct{}, 1)}
	orch := orchestrator.New(cfg, mt, runner, &stackedWorkspaceProvider{path: t.TempDir(), stackedOn: stackedOn})

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	go orch.Run(ctx) //nolint:errcheck
	require.Eventually(t, func() bool {
		issues, err := mt.FetchIssueStatesByIDs(ctx, []string{"id2"})
		return err == nil && len(issues) == 1 && issues[0].State == "Done"
	}, 6*time.Second, 20*time.Millisecond, "the run must complete")
	cancel()

	_, prompts := runner.snapshot()
	require.NotEmpty(t, prompts)
	return prompts[0], calls()
}

// TestStackedRunTargetsBlockerBranch (#73): a run whose worktree is stacked on
// its blocker's branch tells the agent that base (run.pr_base_branch and a
// "Stacked Branch" prompt block) and sets it on the pull request the run
// produced with `gh pr edit --base`.
func TestStackedRunTargetsBlockerBranch(t *testing.T) {
	prompt, calls := runStackedWorker(t, "itervox/eng-1")

	assert.Contains(t, prompt, "open the PR against itervox/eng-1.")
	assert.Contains(t, prompt, "## Stacked Branch")
	assert.Contains(t, prompt, "gh pr create --base itervox/eng-1")
	assert.Contains(t, calls, "pr edit https://github.com/o/r/pull/12 --base itervox/eng-1",
		"the PR from a stacked worktree must be pointed at the blocker's branch; gh calls: %v", calls)
}

// TestUnstackedRunKeepsDefaultBase: without a stacked worktree the prompt
// names workspace.base_branch, carries no stacked block, and the PR base is
// not touched.
func TestUnstackedRunKeepsDefaultBase(t *testing.T) {
	prompt, calls := runStackedWorker(t, "")

	assert.Contains(t, prompt, "open the PR against main.")
	assert.NotContains(t, prompt, "## Stacked Branch")
	for _, c := range calls {
		assert.NotContains(t, c, "pr edit", "an unstacked run must not edit the PR base")
	}
}
