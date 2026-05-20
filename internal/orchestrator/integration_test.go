package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/workspace"
)

// succeedOnceRunner returns a successful result on every call (non-zero tokens,
// Failed=false). After the first call it returns zero tokens so the worker
// treats the session as concluded and exits cleanly.
type succeedOnceRunner struct {
	calls atomic.Int32
}

func (r *succeedOnceRunner) RunTurn(_ context.Context, _ agent.Logger, _ func(agent.TurnResult), _ *string, _, _, _, _, _ string, _, _ int) (agent.TurnResult, error) {
	n := r.calls.Add(1)
	if n == 1 {
		return agent.TurnResult{
			SessionID:    "test-session-1",
			InputTokens:  100,
			OutputTokens: 50,
			TotalTokens:  150,
			ResultText:   "task completed",
		}, nil
	}
	// Subsequent turns: zero tokens signals "session concluded".
	return agent.TurnResult{SessionID: "test-session-1"}, nil
}

type stableWorkspaceProvider struct {
	mu               sync.Mutex
	path             string
	createdNowByCall map[int]bool
	ensureCalls      int
	branchByCall     []string
}

func (p *stableWorkspaceProvider) EnsureWorkspace(_ context.Context, identifier, branchName string) (workspace.Workspace, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ensureCalls++
	p.branchByCall = append(p.branchByCall, branchName)
	if err := os.MkdirAll(p.path, 0o755); err != nil {
		return workspace.Workspace{}, err
	}
	createdNow := p.ensureCalls == 1
	if override, ok := p.createdNowByCall[p.ensureCalls]; ok {
		createdNow = override
	}
	return workspace.Workspace{
		Path:       p.path,
		Identifier: identifier,
		CreatedNow: createdNow,
	}, nil
}

func (p *stableWorkspaceProvider) RemoveWorkspace(_ context.Context, _, _ string) error {
	return nil
}

func (p *stableWorkspaceProvider) snapshot() (ensureCalls int, branchByCall []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ensureCalls, append([]string{}, p.branchByCall...)
}

type commentTracker struct {
	base *tracker.MemoryTracker

	mu       sync.Mutex
	comments map[string][]domain.Comment
	nextID   int
}

func newCommentTracker(issues []domain.Issue, activeStates, terminalStates []string) *commentTracker {
	comments := make(map[string][]domain.Comment, len(issues))
	for _, issue := range issues {
		comments[issue.ID] = append([]domain.Comment{}, issue.Comments...)
	}
	return &commentTracker{
		base:     tracker.NewMemoryTracker(issues, activeStates, terminalStates),
		comments: comments,
	}
}

func (t *commentTracker) FetchCandidateIssues(ctx context.Context) ([]domain.Issue, error) {
	return t.base.FetchCandidateIssues(ctx)
}

func (t *commentTracker) FetchIssuesByStates(ctx context.Context, stateNames []string) ([]domain.Issue, error) {
	return t.base.FetchIssuesByStates(ctx, stateNames)
}

func (t *commentTracker) FetchIssueStatesByIDs(ctx context.Context, issueIDs []string) ([]domain.Issue, error) {
	return t.base.FetchIssueStatesByIDs(ctx, issueIDs)
}

func (t *commentTracker) CreateComment(_ context.Context, issueID, body string) (*domain.Comment, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.nextID++
	comment := domain.Comment{
		ID:         "itervox-comment-" + issueID + "-" + strconv.Itoa(t.nextID),
		Body:       body,
		AuthorID:   "itervox-bot",
		AuthorName: "Itervox",
	}
	t.comments[issueID] = append(t.comments[issueID], comment)
	return &comment, nil
}

func (t *commentTracker) CreateIssue(ctx context.Context, sourceIssueID, title, body, stateName string) (*domain.Issue, error) {
	return t.base.CreateIssue(ctx, sourceIssueID, title, body, stateName)
}

func (t *commentTracker) UpdateIssueState(ctx context.Context, issueID, stateName string) error {
	return t.base.UpdateIssueState(ctx, issueID, stateName)
}

func (t *commentTracker) FetchIssueDetail(ctx context.Context, issueID string) (*domain.Issue, error) {
	issue, err := t.base.FetchIssueDetail(ctx, issueID)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	issue.Comments = append([]domain.Comment{}, t.comments[issueID]...)
	return issue, nil
}

func (t *commentTracker) FetchIssueByIdentifier(ctx context.Context, identifier string) (*domain.Issue, error) {
	issue, err := t.base.FetchIssueByIdentifier(ctx, identifier)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	issue.Comments = append([]domain.Comment{}, t.comments[issue.ID]...)
	return issue, nil
}

func (t *commentTracker) SetIssueBranch(ctx context.Context, issueID, branchName string) error {
	return t.base.SetIssueBranch(ctx, issueID, branchName)
}

func (t *commentTracker) addComment(issueID, body string, authorParts ...string) domain.Comment {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.nextID++
	authorID := "human-user"
	authorName := "Human"
	if len(authorParts) > 0 && authorParts[0] != "" {
		authorID = authorParts[0]
	}
	if len(authorParts) > 1 && authorParts[1] != "" {
		authorName = authorParts[1]
	}
	comment := domain.Comment{
		ID:         "comment-" + issueID + "-" + strconv.Itoa(t.nextID),
		Body:       body,
		AuthorID:   authorID,
		AuthorName: authorName,
	}
	t.comments[issueID] = append(t.comments[issueID], comment)
	return comment
}

func (t *commentTracker) commentsFor(issueID string) []domain.Comment {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]domain.Comment{}, t.comments[issueID]...)
}

type inputRequiredResumeRunner struct {
	mu             sync.Mutex
	calls          int
	sessionIDs     []string
	prompts        []string
	workspacePaths []string
	commands       []string
	workerHosts    []string
}

type inputRequiredResumeStallRunner struct {
	mu             sync.Mutex
	calls          int
	sessionIDs     []string
	prompts        []string
	workspacePaths []string
	commands       []string
	workerHosts    []string
}

func (r *inputRequiredResumeStallRunner) RunTurn(ctx context.Context, _ agent.Logger, _ func(agent.TurnResult), sessionID *string, prompt, workspacePath, command, workerHost, logDir string, readTimeoutMs, turnTimeoutMs int) (agent.TurnResult, error) {
	r.mu.Lock()
	r.calls++
	sid := ""
	if sessionID != nil {
		sid = *sessionID
	}
	r.sessionIDs = append(r.sessionIDs, sid)
	r.prompts = append(r.prompts, prompt)
	r.workspacePaths = append(r.workspacePaths, workspacePath)
	r.commands = append(r.commands, command)
	r.workerHosts = append(r.workerHosts, workerHost)
	r.mu.Unlock()

	if sid == "" {
		return agent.TurnResult{
			SessionID:     "resume-session-1",
			InputTokens:   7,
			OutputTokens:  3,
			TotalTokens:   10,
			InputRequired: true,
			FailureText:   "Need approval for deploy step",
		}, nil
	}

	<-ctx.Done()
	return agent.TurnResult{Failed: true}, ctx.Err()
}

func (r *inputRequiredResumeRunner) RunTurn(_ context.Context, _ agent.Logger, _ func(agent.TurnResult), sessionID *string, prompt, workspacePath, command, workerHost, logDir string, readTimeoutMs, turnTimeoutMs int) (agent.TurnResult, error) {
	r.mu.Lock()
	r.calls++
	sid := ""
	if sessionID != nil {
		sid = *sessionID
	}
	r.sessionIDs = append(r.sessionIDs, sid)
	r.prompts = append(r.prompts, prompt)
	r.workspacePaths = append(r.workspacePaths, workspacePath)
	r.commands = append(r.commands, command)
	r.workerHosts = append(r.workerHosts, workerHost)
	r.mu.Unlock()

	if sid == "" {
		return agent.TurnResult{
			SessionID:     "resume-session-1",
			InputTokens:   7,
			OutputTokens:  3,
			TotalTokens:   10,
			InputRequired: true,
			FailureText:   "Need approval for deploy step",
		}, nil
	}

	return agent.TurnResult{
		SessionID:    "resume-session-1",
		InputTokens:  11,
		OutputTokens: 5,
		TotalTokens:  16,
		ResultText:   "approved, continuing",
	}, nil
}

func (r *inputRequiredResumeRunner) snapshot() (calls int, sessionIDs, prompts, workspacePaths, commands, workerHosts []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls,
		append([]string{}, r.sessionIDs...),
		append([]string{}, r.prompts...),
		append([]string{}, r.workspacePaths...),
		append([]string{}, r.commands...),
		append([]string{}, r.workerHosts...)
}

// TestOrchestratorLifecycle exercises the full dispatch cycle:
// create orchestrator → add issue in active state → run a few ticks →
// verify dispatch, runner invocation, worker exit, and state transitions.
func TestOrchestratorLifecycle(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 3

	mt := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)

	runner := &succeedOnceRunner{}
	dispatched := make(chan string, 1)

	orch := orchestrator.New(cfg, mt, runner, nil)
	orch.OnDispatch = func(issueID string) {
		select {
		case dispatched <- issueID:
		default:
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go orch.Run(ctx) //nolint:errcheck

	// 1. Verify the issue gets dispatched.
	select {
	case id := <-dispatched:
		assert.Equal(t, "id1", id, "dispatched issue ID should match")
	case <-time.After(3 * time.Second):
		t.Fatal("issue was not dispatched within timeout")
	}

	// 2. Wait for the runner to be called at least once.
	deadline := time.After(3 * time.Second)
	for runner.calls.Load() < 1 {
		select {
		case <-deadline:
			t.Fatal("runner was not called within timeout")
		case <-time.After(20 * time.Millisecond):
		}
	}

	// 3. Wait for the worker to exit — Running map should become empty.
	deadline = time.After(10 * time.Second)
	for {
		snap := orch.Snapshot()
		if len(snap.Running) == 0 {
			// Worker exited. Verify the issue is no longer claimed
			// (successful completion releases the claim).
			_, claimed := snap.Claimed["id1"]
			assert.False(t, claimed, "issue should not be claimed after successful completion")
			cancel()
			return
		}
		select {
		case <-deadline:
			t.Fatalf("worker did not exit within timeout; Running=%d", len(orch.Snapshot().Running))
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// TestOrchestratorLifecycleWithCompletionState verifies that when a
// completion_state is configured, the tracker issue is transitioned after a
// successful worker run.
func TestOrchestratorLifecycleWithCompletionState(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 3
	cfg.Tracker.CompletionState = "Done"

	mt := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)

	runner := &succeedOnceRunner{}
	orch := orchestrator.New(cfg, mt, runner, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go orch.Run(ctx) //nolint:errcheck

	// Wait for the issue to be transitioned to "Done" by the worker's
	// post-run hook.
	deadline := time.After(4 * time.Second)
	for {
		issues, _ := mt.FetchIssueStatesByIDs(ctx, []string{"id1"})
		if len(issues) > 0 && issues[0].State == "Done" {
			cancel()
			return
		}
		select {
		case <-deadline:
			snap := orch.Snapshot()
			t.Fatalf("issue was not transitioned to Done within timeout; Running=%d, Claimed=%d",
				len(snap.Running), len(snap.Claimed))
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// TestOrchestratorLifecycleFailAndRetry verifies the fail → retry → fail →
// pause cycle using alwaysFailRunner with MaxRetries=1.
func TestOrchestratorLifecycleFailAndRetry(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxRetries = 1
	cfg.Agent.MaxRetryBackoffMs = 10

	mt := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)

	runner := &alwaysFailRunner{}
	orch := orchestrator.New(cfg, mt, runner, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go orch.Run(ctx) //nolint:errcheck

	// MaxRetries=1: initial attempt fails → retry → second attempt fails → paused.
	deadline := time.After(4 * time.Second)
	for {
		snap := orch.Snapshot()
		if _, paused := snap.PausedIdentifiers["ENG-1"]; paused {
			require.Greater(t, int(runner.callCount.Load()), 1,
				"runner should have been called more than once (initial + retry)")
			cancel()
			return
		}
		select {
		case <-deadline:
			t.Fatal("issue was not paused after max retries within timeout")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func TestInputRequiredResumeReusesWorkspaceWithoutRerunningBeforeRun(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 3
	cfg.Tracker.CompletionState = "Done"
	cfg.PromptTemplate = "Work on {{ issue.identifier }} from {{ issue.branch_name }}"
	cfg.Hooks.BeforeRun = "printf x >> .before-run-count"

	branch := "feature/eng-1"
	issue := makeIssue("id1", "ENG-1", "In Progress", nil, nil)
	issue.BranchName = &branch

	mt := tracker.NewMemoryTracker(
		[]domain.Issue{issue},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)

	wsPath := filepath.Join(t.TempDir(), "workspace")
	wm := &stableWorkspaceProvider{path: wsPath}
	runner := &inputRequiredResumeRunner{}
	orch := orchestrator.New(cfg, mt, runner, wm)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go orch.Run(ctx) //nolint:errcheck

	deadline := time.After(3 * time.Second)
	for {
		snap := orch.Snapshot()
		if _, ok := snap.InputRequiredIssues["ENG-1"]; ok {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("issue did not enter input_required within timeout; snap=%+v", orch.Snapshot())
		case <-time.After(20 * time.Millisecond):
		}
	}

	require.True(t, orch.ProvideInput("ENG-1", "Approved. Continue with the existing branch."))

	deadline = time.After(3 * time.Second)
	for {
		calls, sessionIDs, prompts, workspacePaths, commands, _ := runner.snapshot()
		issues, err := mt.FetchIssueStatesByIDs(ctx, []string{"id1"})
		require.NoError(t, err)
		if calls >= 2 && len(issues) > 0 && issues[0].State == "Done" {
			require.Len(t, sessionIDs, 2)
			require.Len(t, prompts, 2)
			require.Len(t, workspacePaths, 2)
			require.Len(t, commands, 2)
			assert.Equal(t, "", sessionIDs[0], "initial dispatch should start a fresh session")
			assert.Equal(t, "resume-session-1", sessionIDs[1], "resume must use the saved session ID")
			assert.Equal(t, wsPath, workspacePaths[0], "initial dispatch should use the workspace path")
			assert.Equal(t, wsPath, workspacePaths[1], "input-required resume must reuse the same workspace path")
			assert.Equal(t, "Approved. Continue with the existing branch.", prompts[1], "resume must pass the user reply, not a re-rendered workflow prompt")

			counterBytes, readErr := os.ReadFile(filepath.Join(wsPath, ".before-run-count"))
			require.NoError(t, readErr)
			assert.Equal(t, "x", string(counterBytes), "before_run must not run again on input-required resume")
			history := orch.RunHistory()
			require.Len(t, history, 2)
			assert.Equal(t, "input_required", history[0].Status)
			assert.Equal(t, 7, history[0].InputTokens)
			assert.Equal(t, 3, history[0].OutputTokens)
			assert.Equal(t, 10, history[0].TotalTokens)
			assert.Equal(t, "succeeded", history[1].Status)
			return
		}
		select {
		case <-deadline:
			calls, sessionIDs, prompts, workspacePaths, commands, _ := runner.snapshot()
			counterBytes, _ := os.ReadFile(filepath.Join(wsPath, ".before-run-count"))
			t.Fatalf("input-required resume did not complete as expected; calls=%d sessionIDs=%v prompts=%v workspacePaths=%v commands=%v counter=%q snap=%+v",
				calls, sessionIDs, prompts, workspacePaths, commands, string(counterBytes), orch.Snapshot())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func TestInputRequiredCommentsAreMarkedManaged(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 3
	cfg.Tracker.CompletionState = "Done"
	cfg.PromptTemplate = "Handle {{ issue.identifier }}"

	issue := makeIssue("id1", "ENG-1", "In Progress", nil, nil)
	ct := newCommentTracker(
		[]domain.Issue{issue},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)
	runner := &inputRequiredResumeRunner{}
	orch := orchestrator.New(cfg, ct, runner, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go orch.Run(ctx) //nolint:errcheck

	deadline := time.After(3 * time.Second)
	for {
		comments := ct.commentsFor("id1")
		if _, ok := orch.Snapshot().InputRequiredIssues["ENG-1"]; ok && len(comments) > 0 {
			assert.Contains(t, comments[0].Body, "<!-- itervox:managed -->")
			assert.Contains(t, comments[0].Body, "Agent needs your input")
			break
		}
		select {
		case <-deadline:
			t.Fatalf("input-required question comment was not posted; snap=%+v comments=%+v", orch.Snapshot(), comments)
		case <-time.After(20 * time.Millisecond):
		}
	}

	require.True(t, orch.ProvideInput("ENG-1", "Approved. Continue with the existing branch."))

	deadline = time.After(3 * time.Second)
	for {
		comments := ct.commentsFor("id1")
		issues, err := ct.FetchIssueStatesByIDs(ctx, []string{"id1"})
		require.NoError(t, err)
		if len(comments) >= 2 && len(issues) > 0 && issues[0].State == "Done" {
			reply := comments[len(comments)-1]
			assert.Contains(t, reply.Body, "Approved. Continue with the existing branch.")
			assert.Contains(t, reply.Body, "<!-- itervox:managed -->")
			return
		}
		select {
		case <-deadline:
			t.Fatalf("managed provide-input comment was not posted; snap=%+v comments=%+v", orch.Snapshot(), comments)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func TestInputRequiredResumeUsesCodexSessionAndUserReply(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 3
	cfg.Tracker.CompletionState = "Done"
	cfg.Agent.Command = "codex"
	cfg.Agent.Backend = "codex"
	cfg.PromptTemplate = "Handle {{ issue.identifier }}"
	cfg.Hooks.BeforeRun = "printf x >> .before-run-count"

	branch := "feature/eng-1"
	issue := makeIssue("id1", "ENG-1", "In Progress", nil, nil)
	issue.BranchName = &branch

	mt := tracker.NewMemoryTracker(
		[]domain.Issue{issue},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)

	wsPath := filepath.Join(t.TempDir(), "workspace")
	wm := &stableWorkspaceProvider{path: wsPath}
	runner := &inputRequiredResumeRunner{}
	orch := orchestrator.New(cfg, mt, runner, wm)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go orch.Run(ctx) //nolint:errcheck

	deadline := time.After(3 * time.Second)
	for {
		snap := orch.Snapshot()
		if _, ok := snap.InputRequiredIssues["ENG-1"]; ok {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("codex issue did not enter input_required within timeout; snap=%+v", orch.Snapshot())
		case <-time.After(20 * time.Millisecond):
		}
	}

	require.True(t, orch.ProvideInput("ENG-1", "Use Codex resume and continue."))

	deadline = time.After(3 * time.Second)
	for {
		calls, sessionIDs, prompts, workspacePaths, commands, _ := runner.snapshot()
		issues, err := mt.FetchIssueStatesByIDs(ctx, []string{"id1"})
		require.NoError(t, err)
		if calls >= 2 && len(issues) > 0 && issues[0].State == "Done" {
			require.Len(t, sessionIDs, 2)
			require.Len(t, prompts, 2)
			require.Len(t, workspacePaths, 2)
			require.Len(t, commands, 2)
			assert.Equal(t, "resume-session-1", sessionIDs[1], "codex resume must use the saved session ID")
			assert.Equal(t, "Use Codex resume and continue.", prompts[1], "codex resume must pass the exact user reply")
			assert.Equal(t, wsPath, workspacePaths[1], "codex resume must reuse the existing workspace")
			assert.Equal(t, "codex", commands[1], "codex resume must preserve the configured codex command")

			counterBytes, readErr := os.ReadFile(filepath.Join(wsPath, ".before-run-count"))
			require.NoError(t, readErr)
			assert.Equal(t, "x", string(counterBytes), "before_run must not run again on codex input-required resume")
			return
		}
		select {
		case <-deadline:
			calls, sessionIDs, prompts, workspacePaths, commands, _ := runner.snapshot()
			counterBytes, _ := os.ReadFile(filepath.Join(wsPath, ".before-run-count"))
			t.Fatalf("codex input-required resume did not complete as expected; calls=%d sessionIDs=%v prompts=%v workspacePaths=%v commands=%v counter=%q snap=%+v",
				calls, sessionIDs, prompts, workspacePaths, commands, string(counterBytes), orch.Snapshot())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func TestInputRequiredResumeRerunsSetupWhenWorkspaceIsRecreated(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 3
	cfg.Tracker.CompletionState = "Done"
	cfg.PromptTemplate = "Work on {{ issue.identifier }} from {{ issue.branch_name }}"
	cfg.Hooks.AfterCreate = "printf y >> .after-create-count"
	cfg.Hooks.BeforeRun = "printf x >> .before-run-count"

	branch := "feature/eng-1"
	issue := makeIssue("id1", "ENG-1", "In Progress", nil, nil)
	issue.BranchName = &branch

	mt := tracker.NewMemoryTracker(
		[]domain.Issue{issue},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)

	wsPath := filepath.Join(t.TempDir(), "workspace")
	wm := &stableWorkspaceProvider{
		path:             wsPath,
		createdNowByCall: map[int]bool{2: true},
	}
	runner := &inputRequiredResumeRunner{}
	orch := orchestrator.New(cfg, mt, runner, wm)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go orch.Run(ctx) //nolint:errcheck

	deadline := time.After(3 * time.Second)
	for {
		snap := orch.Snapshot()
		if _, ok := snap.InputRequiredIssues["ENG-1"]; ok {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("issue did not enter input_required within timeout; snap=%+v", orch.Snapshot())
		case <-time.After(20 * time.Millisecond):
		}
	}

	require.NoError(t, os.RemoveAll(wsPath))
	require.True(t, orch.ProvideInput("ENG-1", "Approved after restart. Recreate the workspace and continue."))

	deadline = time.After(3 * time.Second)
	for {
		calls, sessionIDs, prompts, workspacePaths, _, _ := runner.snapshot()
		issues, err := mt.FetchIssueStatesByIDs(ctx, []string{"id1"})
		require.NoError(t, err)
		if calls >= 2 && len(issues) > 0 && issues[0].State == "Done" {
			require.Len(t, sessionIDs, 2)
			require.Len(t, prompts, 2)
			require.Len(t, workspacePaths, 2)
			assert.Equal(t, "resume-session-1", sessionIDs[1], "resume must still use the saved session ID")
			assert.Equal(t, "Approved after restart. Recreate the workspace and continue.", prompts[1], "resume must still pass the exact user reply")
			assert.Equal(t, wsPath, workspacePaths[1], "resume should continue in the recreated workspace path")
			_, branches := wm.snapshot()
			require.Len(t, branches, 2)
			assert.Equal(t, branch, branches[1], "recreated input-required resume must preserve the tracked branch")

			afterCreateBytes, afterErr := os.ReadFile(filepath.Join(wsPath, ".after-create-count"))
			require.NoError(t, afterErr)
			assert.Equal(t, "y", string(afterCreateBytes), "after_create must rerun when the workspace is recreated")

			beforeRunBytes, beforeErr := os.ReadFile(filepath.Join(wsPath, ".before-run-count"))
			require.NoError(t, beforeErr)
			assert.Equal(t, "x", string(beforeRunBytes), "before_run must rerun when the workspace is recreated")
			return
		}
		select {
		case <-deadline:
			calls, sessionIDs, prompts, workspacePaths, commands, _ := runner.snapshot()
			afterCreateBytes, _ := os.ReadFile(filepath.Join(wsPath, ".after-create-count"))
			beforeRunBytes, _ := os.ReadFile(filepath.Join(wsPath, ".before-run-count"))
			t.Fatalf("input-required resume did not rerun setup after workspace recreation; calls=%d sessionIDs=%v prompts=%v workspacePaths=%v commands=%v after_create=%q before_run=%q snap=%+v",
				calls, sessionIDs, prompts, workspacePaths, commands, string(afterCreateBytes), string(beforeRunBytes), orch.Snapshot())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func TestInputRequiredPersistenceResumesAfterTrackerReplyWithoutTrackerMetadata(t *testing.T) {
	cfg1 := baseConfig()
	cfg1.Polling.IntervalMs = 20
	cfg1.Agent.MaxTurns = 3
	cfg1.Agent.Command = "codex"
	cfg1.Agent.Backend = "codex"
	cfg1.Agent.SSHHosts = []string{"ssh-recover"}
	cfg1.Tracker.CompletionState = "Done"
	cfg1.PromptTemplate = "Handle {{ issue.identifier }}"

	branch := "feature/eng-1"
	issue := makeIssue("id1", "ENG-1", "In Progress", nil, nil)
	issue.BranchName = &branch

	ct := newCommentTracker(
		[]domain.Issue{issue},
		cfg1.Tracker.ActiveStates,
		cfg1.Tracker.TerminalStates,
	)
	irFile := filepath.Join(t.TempDir(), "input_required.json")
	wsPath := filepath.Join(t.TempDir(), "workspace")
	wm := &stableWorkspaceProvider{path: wsPath}
	runner1 := &inputRequiredResumeRunner{}

	orch1 := orchestrator.New(cfg1, ct, runner1, wm)
	orch1.SetInputRequiredFile(irFile)
	ctx1, cancel1 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel1()

	// Wait for the orchestrator goroutine to fully exit before t.TempDir's
	// cleanup runs os.RemoveAll — otherwise Run may still be writing
	// input_required.json into TempDir when cleanup fires, producing
	// "directory not empty" flakes (same class as TestReanalyzeIssueRace).
	run1Done := make(chan struct{})
	go func() {
		_ = orch1.Run(ctx1)
		close(run1Done)
	}()

	deadline := time.After(3 * time.Second)
	for {
		snap := orch1.Snapshot()
		comments := ct.commentsFor("id1")
		if _, ok := snap.InputRequiredIssues["ENG-1"]; ok && len(comments) > 0 {
			data, readErr := os.ReadFile(irFile)
			if readErr == nil && strings.Contains(string(data), "resume-session-1") {
				require.Contains(t, comments[0].Body, "🤖 **Agent needs your input**")
				require.Contains(t, comments[0].Body, "Reply in the tracker or via the Itervox dashboard to continue.")
				require.NotContains(t, comments[0].Body, "<!-- itervox:input-required-meta ")
				break
			}
		}
		select {
		case <-deadline:
			t.Fatalf("first orchestrator did not persist local input-required context; snap=%+v comments=%+v", orch1.Snapshot(), comments)
		case <-time.After(20 * time.Millisecond):
		}
	}
	cancel1()
	select {
	case <-run1Done:
	case <-time.After(2 * time.Second):
		t.Fatal("orch1 did not exit within 2s of cancel")
	}

	ct.addComment("id1", "Approved via tracker comment before restart.")

	cfg2 := baseConfig()
	cfg2.Polling.IntervalMs = 20
	cfg2.Agent.MaxTurns = 3
	cfg2.Agent.Command = "claude"
	cfg2.Agent.Backend = "claude"
	cfg2.Tracker.CompletionState = "Done"
	cfg2.PromptTemplate = "Handle {{ issue.identifier }}"

	runner2 := &inputRequiredResumeRunner{}
	orch2 := orchestrator.New(cfg2, ct, runner2, wm)
	orch2.SetInputRequiredFile(irFile)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()

	run2Done := make(chan struct{})
	go func() {
		_ = orch2.Run(ctx2)
		close(run2Done)
	}()
	defer func() {
		cancel2()
		select {
		case <-run2Done:
		case <-time.After(2 * time.Second):
			t.Log("orch2 did not exit within 2s of cancel — TempDir flake possible")
		}
	}()

	deadline = time.After(3 * time.Second)
	for {
		calls, sessionIDs, prompts, workspacePaths, commands, workerHosts := runner2.snapshot()
		issues, err := ct.FetchIssueStatesByIDs(ctx2, []string{"id1"})
		require.NoError(t, err)
		if calls >= 1 && len(issues) > 0 && issues[0].State == "Done" {
			require.Len(t, sessionIDs, 1)
			require.Len(t, prompts, 1)
			require.Len(t, workspacePaths, 1)
			require.Len(t, commands, 1)
			require.Len(t, workerHosts, 1)
			assert.Equal(t, "resume-session-1", sessionIDs[0], "recovered tracker comment resume must reuse the saved agent session")
			assert.Equal(t, "Approved via tracker comment before restart.", prompts[0], "restart-after-reply resume must pass the saved tracker reply")
			assert.Equal(t, "codex", commands[0], "recovered tracker comment resume must preserve the saved command")
			assert.Equal(t, "ssh-recover", workerHosts[0], "recovered tracker comment resume must preserve the saved SSH host")
			assert.Equal(t, wsPath, workspacePaths[0], "recovered tracker comment resume must reuse the workspace")
			return
		}
		select {
		case <-deadline:
			calls, sessionIDs, prompts, workspacePaths, commands, workerHosts := runner2.snapshot()
			t.Fatalf("recovered tracker comment resume did not complete as expected; calls=%d sessionIDs=%v prompts=%v workspacePaths=%v commands=%v workerHosts=%v snap=%+v comments=%+v",
				calls, sessionIDs, prompts, workspacePaths, commands, workerHosts, orch2.Snapshot(), ct.commentsFor("id1"))
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func TestRecoveredTrackerReplySkipsSameAuthorCommentsAndUsesExactQuestionCommentID(t *testing.T) {
	cfg1 := baseConfig()
	cfg1.Polling.IntervalMs = 20
	cfg1.Agent.MaxTurns = 3
	cfg1.Agent.Command = "codex"
	cfg1.Agent.Backend = "codex"
	cfg1.Agent.SSHHosts = []string{"ssh-recover"}
	cfg1.Tracker.CompletionState = "Done"
	cfg1.PromptTemplate = "Handle {{ issue.identifier }}"

	branch := "feature/eng-1"
	issue := makeIssue("id1", "ENG-1", "In Progress", nil, nil)
	issue.BranchName = &branch

	ct := newCommentTracker(
		[]domain.Issue{issue},
		cfg1.Tracker.ActiveStates,
		cfg1.Tracker.TerminalStates,
	)
	irFile := filepath.Join(t.TempDir(), "input_required.json")
	wsPath := filepath.Join(t.TempDir(), "workspace")
	wm := &stableWorkspaceProvider{path: wsPath}
	runner1 := &inputRequiredResumeRunner{}

	orch1 := orchestrator.New(cfg1, ct, runner1, wm)
	orch1.SetInputRequiredFile(irFile)
	ctx1, cancel1 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel1()

	// Same TempDir-cleanup race guard as the sibling tests.
	run1Done := make(chan struct{})
	go func() {
		_ = orch1.Run(ctx1)
		close(run1Done)
	}()

	var questionComment domain.Comment
	deadline := time.After(3 * time.Second)
	for {
		snap := orch1.Snapshot()
		comments := ct.commentsFor("id1")
		if _, ok := snap.InputRequiredIssues["ENG-1"]; ok && len(comments) > 0 {
			data, readErr := os.ReadFile(irFile)
			if readErr == nil && strings.Contains(string(data), `"question_comment_id":"`) {
				questionComment = comments[0]
				require.NotEmpty(t, questionComment.ID)
				require.NotEmpty(t, questionComment.AuthorID)
				break
			}
		}
		select {
		case <-deadline:
			t.Fatalf("question comment metadata was not persisted; snap=%+v comments=%+v", orch1.Snapshot(), comments)
		case <-time.After(20 * time.Millisecond):
		}
	}
	cancel1()
	select {
	case <-run1Done:
	case <-time.After(2 * time.Second):
		t.Fatal("orch1 did not exit within 2s of cancel")
	}

	ct.addComment("id1", "Follow-up from the same bot author.", questionComment.AuthorID, questionComment.AuthorName)
	ct.addComment("id1", "Approved via tracker comment from a human.", "human-user-1", "Alice")

	cfg2 := baseConfig()
	cfg2.Polling.IntervalMs = 20
	cfg2.Agent.MaxTurns = 3
	cfg2.Agent.Command = "claude"
	cfg2.Agent.Backend = "claude"
	cfg2.Tracker.CompletionState = "Done"
	cfg2.PromptTemplate = "Handle {{ issue.identifier }}"

	runner2 := &inputRequiredResumeRunner{}
	orch2 := orchestrator.New(cfg2, ct, runner2, wm)
	orch2.SetInputRequiredFile(irFile)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()

	run2Done := make(chan struct{})
	go func() {
		_ = orch2.Run(ctx2)
		close(run2Done)
	}()
	defer func() {
		cancel2()
		select {
		case <-run2Done:
		case <-time.After(2 * time.Second):
			t.Log("orch2 did not exit within 2s of cancel — TempDir flake possible")
		}
	}()

	deadline = time.After(3 * time.Second)
	for {
		calls, sessionIDs, prompts, workspacePaths, commands, workerHosts := runner2.snapshot()
		issues, err := ct.FetchIssueStatesByIDs(ctx2, []string{"id1"})
		require.NoError(t, err)
		if calls >= 1 && len(issues) > 0 && issues[0].State == "Done" {
			require.Len(t, sessionIDs, 1)
			assert.Equal(t, "resume-session-1", sessionIDs[0])
			assert.Equal(t, "Approved via tracker comment from a human.", prompts[0], "same-author follow-up must be ignored")
			assert.Equal(t, "codex", commands[0])
			assert.Equal(t, "ssh-recover", workerHosts[0])
			assert.Equal(t, wsPath, workspacePaths[0])
			return
		}
		select {
		case <-deadline:
			calls, sessionIDs, prompts, workspacePaths, commands, workerHosts := runner2.snapshot()
			t.Fatalf("exact-comment-id recovery did not resume correctly; calls=%d sessionIDs=%v prompts=%v workspacePaths=%v commands=%v workerHosts=%v snap=%+v comments=%+v",
				calls, sessionIDs, prompts, workspacePaths, commands, workerHosts, orch2.Snapshot(), ct.commentsFor("id1"))
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func TestProvideInputPendingResumeSurvivesRestartBeforeResumedTurnCompletes(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 3
	cfg.Agent.Command = "codex"
	cfg.Agent.Backend = "codex"
	cfg.Tracker.CompletionState = "Done"
	cfg.PromptTemplate = "Handle {{ issue.identifier }}"

	branch := "feature/eng-1"
	issue := makeIssue("id1", "ENG-1", "In Progress", nil, nil)
	issue.BranchName = &branch

	ct := newCommentTracker(
		[]domain.Issue{issue},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)
	irFile := filepath.Join(t.TempDir(), "input_required.json")
	wsPath := filepath.Join(t.TempDir(), "workspace")
	wm := &stableWorkspaceProvider{path: wsPath}

	runner1 := &inputRequiredResumeStallRunner{}
	orch1 := orchestrator.New(cfg, ct, runner1, wm)
	orch1.SetInputRequiredFile(irFile)
	ctx1, cancel1 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel1()

	// Track the orchestrator goroutine so we can wait for it to fully exit
	// before t.TempDir's cleanup runs os.RemoveAll — otherwise Run may still
	// be writing input_required.json into TempDir when cleanup fires, producing
	// "directory not empty" flakes (same class as TestReanalyzeIssueRace).
	run1Done := make(chan struct{})
	go func() {
		_ = orch1.Run(ctx1)
		close(run1Done)
	}()

	deadline := time.After(3 * time.Second)
	for {
		snap := orch1.Snapshot()
		if _, ok := snap.InputRequiredIssues["ENG-1"]; ok {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("issue did not enter input_required within timeout; snap=%+v", orch1.Snapshot())
		case <-time.After(20 * time.Millisecond):
		}
	}

	require.True(t, orch1.ProvideInput("ENG-1", "Approved from dashboard before restart."))

	deadline = time.After(3 * time.Second)
	for {
		data, readErr := os.ReadFile(irFile)
		if readErr == nil && strings.Contains(string(data), "Approved from dashboard before restart.") {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("pending resume reply was not persisted before restart; file=%q", mustReadFile(t, irFile))
		case <-time.After(20 * time.Millisecond):
		}
	}

	cancel1()
	select {
	case <-run1Done:
	case <-time.After(2 * time.Second):
		t.Fatal("orch1 did not exit within 2s of cancel")
	}

	runner2 := &inputRequiredResumeRunner{}
	orch2 := orchestrator.New(cfg, ct, runner2, wm)
	orch2.SetInputRequiredFile(irFile)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()

	run2Done := make(chan struct{})
	go func() {
		_ = orch2.Run(ctx2)
		close(run2Done)
	}()

	// Ensure orch2 is stopped and fully exited before TempDir cleanup runs
	// on any exit path (success return, fatal, or scope unwind).
	defer func() {
		cancel2()
		select {
		case <-run2Done:
		case <-time.After(2 * time.Second):
			t.Log("orch2 did not exit within 2s of cancel — TempDir flake possible")
		}
	}()

	deadline = time.After(3 * time.Second)
	for {
		calls, sessionIDs, prompts, workspacePaths, commands, workerHosts := runner2.snapshot()
		issues, err := ct.FetchIssueStatesByIDs(ctx2, []string{"id1"})
		require.NoError(t, err)
		if calls >= 1 && len(issues) > 0 && issues[0].State == "Done" {
			require.Len(t, sessionIDs, 1)
			assert.Equal(t, "resume-session-1", sessionIDs[0], "restart must reuse the saved agent session")
			assert.Equal(t, "Approved from dashboard before restart.", prompts[0], "restart must resume with the persisted dashboard reply")
			assert.Equal(t, "codex", commands[0])
			assert.Equal(t, wsPath, workspacePaths[0])
			assert.Empty(t, workerHosts[0])
			return
		}
		select {
		case <-deadline:
			calls, sessionIDs, prompts, workspacePaths, commands, workerHosts := runner2.snapshot()
			t.Fatalf("pending provide-input resume did not complete after restart; calls=%d sessionIDs=%v prompts=%v workspacePaths=%v commands=%v workerHosts=%v snap=%+v comments=%+v file=%q",
				calls, sessionIDs, prompts, workspacePaths, commands, workerHosts, orch2.Snapshot(), ct.commentsFor("id1"), mustReadFile(t, irFile))
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

// MED-2 regression guard: end-to-end test for the automation dispatch pipeline.
// Proves the full path: DispatchAutomation (any goroutine) → events channel →
// event loop → EventDispatchAutomation handler → ineligibleReasonForAutomation
// (which must skip the isActiveState gate per CRIT-3) → startAutomationRun →
// worker spawn. Uses a backlog-state issue to exercise CRIT-3 in one shot.
func TestOrchestratorAutomationDispatchPipeline(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 3
	cfg.Tracker.BacklogStates = []string{"Backlog"}
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"reviewer": {Command: "claude", Backend: "claude"},
	}

	backlogIssue := makeIssue("id1", "ENG-1", "Backlog", nil, nil)
	mt := tracker.NewMemoryTracker(
		[]domain.Issue{backlogIssue},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)

	runner := &succeedOnceRunner{}
	orch := orchestrator.New(cfg, mt, runner, nil)

	// ctx must outlast both internal poll deadlines (3s + 2s) plus the 20ms
	// startup sleep; a 5s ctx was tight enough that under load the orchestrator
	// exited mid-poll and the worker's exit event was dropped before the
	// AutomationID-tagged history row could land.
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		_ = orch.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	time.Sleep(20 * time.Millisecond)

	// Fire an automation dispatch for a BACKLOG issue — the reconcile loop
	// would reject this via isActiveState, but automation dispatch must
	// accept it (CRIT-3 invariant).
	ok := orch.DispatchAutomation(backlogIssue, orchestrator.AutomationDispatch{
		AutomationID: "test-backlog-review",
		ProfileName:  "reviewer",
		Instructions: "Review the backlog issue.",
		Trigger: orchestrator.AutomationTriggerContext{
			Type:         "issue_moved_to_backlog",
			FiredAt:      time.Now(),
			AutomationID: "test-backlog-review",
			CurrentState: "Backlog",
		},
	})
	require.True(t, ok, "DispatchAutomation should enqueue for a backlog-state issue")

	// The runner should be invoked — proves the full event-loop path reached
	// the worker despite the issue being in a non-active state (CRIT-3 guard).
	// startAutomationRun does NOT fire OnDispatch, so we observe via runner.
	deadline := time.After(3 * time.Second)
	for runner.calls.Load() < 1 {
		select {
		case <-deadline:
			t.Fatal("runner was not called for backlog-state automation dispatch within timeout — CRIT-3 regression")
		case <-time.After(20 * time.Millisecond):
		}
	}

	// F-1 acceptance: once the worker has been observed (runner.calls >= 1) the
	// orchestrator must already have written a Running entry tagged with the
	// automation ID + trigger type. We poll the snapshot briefly because the
	// event loop may store it on a slightly different goroutine boundary than
	// the runner invocation.
	var captured *orchestrator.RunEntry
	pollDeadline := time.After(2 * time.Second)
PollSnap:
	for {
		snap := orch.Snapshot()
		for _, entry := range snap.Running {
			if entry.AutomationID != "" {
				captured = entry
				break PollSnap
			}
		}
		// Also accept history entries — by the time we observe the runner,
		// the worker may have already finished and moved to history.
		hist := orch.RunHistory()
		for i := range hist {
			if hist[i].AutomationID == "test-backlog-review" {
				assert.Equal(t, "issue_moved_to_backlog", hist[i].TriggerType,
					"history row must propagate TriggerType from RunEntry")
				return
			}
		}
		select {
		case <-pollDeadline:
			t.Fatal("automation dispatch never produced a snapshot row tagged with AutomationID")
		case <-time.After(20 * time.Millisecond):
		}
	}
	assert.Equal(t, "test-backlog-review", captured.AutomationID,
		"snapshot Running row must carry AutomationID set by startAutomationRun")
	assert.Equal(t, "issue_moved_to_backlog", captured.TriggerType,
		"snapshot Running row must carry TriggerType set by startAutomationRun")
	assert.Equal(t, "automation", captured.Kind,
		"automation-dispatched runs must be tagged Kind=automation")
}

func TestRecoveredInputRequiredDispatchesMatchingAutomations(t *testing.T) {
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 3
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"responder": {Command: "claude", Backend: "claude"},
	}

	issue := makeIssue("id1", "ENG-1", "Todo", nil, nil)
	issue.Comments = []domain.Comment{
		{
			ID:         "q1",
			AuthorID:   "itervox",
			AuthorName: "Itervox",
			Body:       "🤖 **Agent needs your input**\n\nContinue with the existing branch\n\n---\n_Reply in the tracker or via the Itervox dashboard to continue._",
		},
	}
	mt := tracker.NewMemoryTracker(
		[]domain.Issue{issue},
		cfg.Tracker.ActiveStates,
		cfg.Tracker.TerminalStates,
	)

	runner := &succeedOnceRunner{}
	orch := orchestrator.New(cfg, mt, runner, nil)
	orch.SetInputRequiredAutomations([]orchestrator.InputRequiredAutomation{
		{
			ID:                "input-responder",
			ProfileName:       "responder",
			States:            []string{"Todo"},
			InputContextRegex: regexp.MustCompile(`continue|branch`),
		},
	})

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	go orch.Run(ctx) //nolint:errcheck

	deadline := time.After(3 * time.Second)
	for runner.calls.Load() < 1 {
		select {
		case <-deadline:
			t.Fatalf("recovered input-required automation did not dispatch; snap=%+v", orch.Snapshot())
		case <-time.After(20 * time.Millisecond):
		}
	}
}
