package orchestrator_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agentactions"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/guardrails"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
)

// patchOutRig is a real orchestrator and server whose patch-out (push:
// daemon) agent works in a real git clone with a bare remote. The agent's
// script commits file on a new branch, then tries `git push` and
// `gh pr create` itself (both must be refused). The daemon's PR opener is
// a fake that records its calls.
type patchOutRig struct {
	t      *testing.T
	orch   *orchestrator.Orchestrator
	mt     *tracker.MemoryTracker
	remote string
	ws     string
	out    string

	mu  sync.Mutex
	prs []string // "head->base" per PR the daemon opened
}

type patchOutOpts struct {
	file   string               // the file the agent changes and commits
	dirty  bool                 // leave a further uncommitted change after the commit
	verify []config.VerifyCheck // the profile's signed checks
	human  []string             // guardrails.lanes.human
	// completion is tracker.completion_state ("Done" when empty). A
	// non-terminal one keeps the issue's push-lane record.
	completion string
	// extra are further files the agent changes in the same commit.
	extra []string
	// split is guardrails.lanes.split.
	split bool
}

func newPatchOutRig(t *testing.T, o patchOutOpts) *patchOutRig {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake agent is a POSIX shell script")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	for _, kv := range [][2]string{{"GIT_AUTHOR_NAME", "t"}, {"GIT_AUTHOR_EMAIL", "t@example.com"},
		{"GIT_COMMITTER_NAME", "t"}, {"GIT_COMMITTER_EMAIL", "t@example.com"}, {"GIT_CONFIG_GLOBAL", "/dev/null"}} {
		t.Setenv(kv[0], kv[1])
	}
	r := &patchOutRig{t: t, remote: filepath.Join(root, "remote.git"), ws: filepath.Join(root, "ws"), out: filepath.Join(root, "agent.out")}
	laneGit(t, root, "init", "-q", "--bare", "-b", "main", r.remote)
	laneGit(t, root, "clone", "-q", r.remote, r.ws)
	require.NoError(t, os.WriteFile(filepath.Join(r.ws, "README.md"), []byte("x\n"), 0o644))
	laneGit(t, r.ws, "add", "README.md")
	laneGit(t, r.ws, "commit", "-q", "-m", "base")
	laneGit(t, r.ws, "push", "-q", "origin", "HEAD:main")
	laneGit(t, r.ws, "fetch", "-q", "origin")

	script := filepath.Join(root, "agent.sh")
	body := "set -e\ngit checkout -q -b eng-1-fix\n"
	for _, f := range append([]string{o.file}, o.extra...) {
		body += "mkdir -p " + filepath.Dir(f) + "\necho 'change' > " + f + "\n"
	}
	body += "git add -A\ngit commit -q -m change\n"
	if o.dirty {
		body += "echo 'uncommitted' >> " + o.file + "\n"
	}
	body += "set +e\ngit push -u origin HEAD > " + r.out + " 2>&1\necho \"pushexit=$?\" >> " + r.out +
		"\ngh pr create --title t --body b >> " + r.out + " 2>&1\necho \"ghexit=$?\" >> " + r.out + "\n"
	require.NoError(t, os.WriteFile(script, []byte(body), 0o755))

	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 1
	cfg.Agent.MaxConcurrentAgents = 1
	cfg.Agent.TurnTimeoutMs = 60_000
	cfg.Tracker.CompletionState = "Done"
	if o.completion != "" {
		cfg.Tracker.CompletionState = o.completion
	}
	cfg.Guardrails.Enabled = true
	cfg.Guardrails.Lanes.Human = o.human
	cfg.Guardrails.Lanes.Split = o.split
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"impl": {Command: "/bin/sh " + script, Instructions: "Implement it.", Push: config.PushDaemon, Verify: o.verify},
	}
	r.mt = tracker.NewMemoryTracker([]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	r.orch = orchestrator.New(cfg, r.mt, shellRunner{}, &recordingWorkspaceProvider{path: r.ws})
	r.orch.SetPushLanesFile(filepath.Join(root, ".itervox", "push_lanes.json"))
	r.orch.SetIssueProfile("ENG-1", "impl")
	r.orch.SetPRCreator(func(_ context.Context, _, head, base, _, _ string) (string, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.prs = append(r.prs, head+"->"+base)
		return fmt.Sprintf("https://example.invalid/o/r/pull/%d", len(r.prs)), nil
	})

	store := agentactions.NewStore()
	srv := httptest.NewServer(server.New(server.Config{
		Snapshot:         func() server.StateSnapshot { return server.StateSnapshot{} },
		RefreshChan:      make(chan struct{}, 1),
		ActionTokenStore: store,
		Guardrails:       r.orch,
	}))
	t.Cleanup(srv.Close)
	r.orch.SetAgentActionBaseURL(srv.URL)
	r.orch.SetAgentActionTokens(store)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = r.orch.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return r
}

func (r *patchOutRig) agentOutput() string {
	r.t.Helper()
	var out string
	require.Eventually(r.t, func() bool {
		data, _ := os.ReadFile(r.out)
		out = string(data)
		return strings.Contains(out, "ghexit=")
	}, 30*time.Second, 20*time.Millisecond, "the agent never finished")
	return out
}

func (r *patchOutRig) remoteRef(ref string) string {
	return (&laneRig{remote: r.remote}).remoteRef(ref)
}

func (r *patchOutRig) openedPRs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.prs...)
}

// waitSettled waits until the run moved the issue to Done or held it.
func (r *patchOutRig) waitSettled() *orchestrator.InputRequiredEntry {
	r.t.Helper()
	var entry *orchestrator.InputRequiredEntry
	require.Eventually(r.t, func() bool {
		if entry = r.orch.Snapshot().InputRequiredIssues["ENG-1"]; entry != nil {
			return true
		}
		return issueState(r.t, r.mt) == "Done"
	}, 30*time.Second, 20*time.Millisecond, "the run never settled")
	return entry
}

// push: daemon end to end: the agent's own `git push` and `gh pr create`
// are refused by its shims with the patch-out message; after the run and
// its signed checks pass, the daemon pushes the agent's commit to the
// remote itself and opens the pull request, and the issue moves on.
func TestPatchOutDaemonPushesAfterChecks(t *testing.T) {
	r := newPatchOutRig(t, patchOutOpts{file: "pkg/fix.go", verify: []config.VerifyCheck{
		{Name: "test", Run: "test -f pkg/fix.go", TimeoutMs: 60_000},
	}})
	out := r.agentOutput()
	assert.Contains(t, out, guardrails.PatchOutMessage, out)
	assert.Contains(t, out, "`git push` is refused", out)
	assert.Contains(t, out, "`gh pr create` is refused", out)
	assert.Contains(t, out, "pushexit=1", out)
	assert.Contains(t, out, "ghexit=1", out)

	assert.Nil(t, r.waitSettled(), "the run must succeed, not hold")
	assert.Equal(t, "Done", issueState(t, r.mt))
	require.NotEmpty(t, r.remoteRef("refs/heads/eng-1-fix"), "the daemon pushed the branch")
	assert.Equal(t, "change", laneGit(t, r.remote, "log", "-1", "--format=%s", "refs/heads/eng-1-fix"),
		"the remote has the agent's commit (the handoff commit after it stays local, as before)")
	assert.Equal(t, []string{"eng-1-fix->"}, r.openedPRs(), "the daemon opened the PR for the branch")
}

// push: daemon: a failing signed check holds the run before any push, so
// nothing reaches the remote.
func TestPatchOutNoPushWhenChecksFail(t *testing.T) {
	r := newPatchOutRig(t, patchOutOpts{file: "pkg/fix.go", verify: []config.VerifyCheck{
		{Name: "test", Run: "exit 1", TimeoutMs: 60_000},
	}})
	entry := r.waitSettled()
	require.NotNil(t, entry, "a failed check holds the run")
	assert.Contains(t, entry.Context, "Signed checks failed (verify)")
	assert.Empty(t, r.remoteRef("refs/heads/eng-1-fix"), "nothing pushed")
	assert.Empty(t, r.openedPRs())
}

// push: daemon: the checks ran on a working tree with an uncommitted change,
// so the commit the daemon would push is not the tree the checks passed on:
// the push is refused ("the tree changed after the checks") and the run is
// held.
func TestPatchOutRefusesTreeChangedAfterVerify(t *testing.T) {
	r := newPatchOutRig(t, patchOutOpts{file: "pkg/fix.go", dirty: true, verify: []config.VerifyCheck{
		{Name: "test", Run: "true", TimeoutMs: 60_000},
	}})
	entry := r.waitSettled()
	require.NotNil(t, entry, "the push is refused and the run held")
	assert.Contains(t, entry.Context, "Patches only (push: daemon): Itervox did not push this run's branch.")
	assert.Contains(t, entry.Context, "the tree changed after the checks")
	assert.Empty(t, r.remoteRef("refs/heads/eng-1-fix"), "nothing pushed")
	assert.Empty(t, r.openedPRs())
	assert.Equal(t, "In Progress", issueState(t, r.mt))
}

// push: daemon: a daemon push that changes a guardrails.lanes.human file is
// held for a human exactly as an agent's pre-push hold is: an input-required
// pipeline-change approval naming the file, nothing on the remote until a
// human approves; then the push lands, the PR opens, and the issue is in the
// human lane for merge policy.
func TestPatchOutHumanLaneHolds(t *testing.T) {
	r := newPatchOutRig(t, patchOutOpts{file: ".github/workflows/ci.yml", human: []string{".github/**"}, completion: "In Review"})
	var entry *orchestrator.InputRequiredEntry
	require.Eventually(t, func() bool {
		entry = r.orch.Snapshot().InputRequiredIssues["ENG-1"]
		return entry != nil && entry.GuardrailApprovalID != ""
	}, 30*time.Second, 20*time.Millisecond, "the daemon's pipeline push never raised an approval")
	assert.Equal(t, []string{".github/workflows/ci.yml"}, entry.GuardrailLanePaths)
	assert.Contains(t, entry.Context, "Pipeline change:")
	assert.Contains(t, entry.Context, "by Itervox for a patches-only run")
	assert.Empty(t, r.remoteRef("refs/heads/eng-1-fix"), "held: nothing on the remote yet")
	assert.Empty(t, r.openedPRs())

	require.NoError(t, r.orch.ProvideAgentInput("ENG-1", "approve"))
	time.Sleep(300 * time.Millisecond)
	assert.Empty(t, r.remoteRef("refs/heads/eng-1-fix"), "an agent cannot approve the push")

	require.NoError(t, r.orch.ProvideInput("ENG-1", "approve"))
	require.Eventually(t, func() bool { return issueState(t, r.mt) == "In Review" }, 30*time.Second, 20*time.Millisecond,
		"the approved run never moved on")
	assert.Nil(t, r.orch.Snapshot().InputRequiredIssues["ENG-1"])
	assert.Equal(t, "change", laneGit(t, r.remote, "log", "-1", "--format=%s", "refs/heads/eng-1-fix"), "the approved push landed")
	assert.Equal(t, []string{"eng-1-fix->"}, r.openedPRs())
	assert.Equal(t, guardrails.LaneHuman, r.orch.Snapshot().PushLanes["ENG-1"].Lane)
}

// No push mode: a run is exactly as before push: daemon existed: the
// prompt carries no "Publishing (patches only)" block, and the existing
// verify path moves the issue without any daemon push being attempted
// (the worktree has no remote; a daemon push would hold the run).
func TestPatchOutDefaultWorkerUnchanged(t *testing.T) {
	orch, mt, _, _ := runVerifyWorker(t, passingSelfEvidence, []config.VerifyCheck{{Name: "test", Run: "true", TimeoutMs: 60_000}})
	assert.Nil(t, orch.Snapshot().InputRequiredIssues["ENG-1"])
	assert.Equal(t, "Done", issueState(t, mt))
}

// push: daemon: the agent's prompt tells it publishing is the daemon's job.
func TestPatchOutPromptTellsAgent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX git fixture")
	}
	ws := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"},
		{"commit", "-q", "--allow-empty", "-m", "base"}} {
		laneGit(t, ws, args...)
	}
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 1
	cfg.Tracker.CompletionState = "Done"
	cfg.Guardrails.Enabled = true
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"impl": {Command: "claude", Instructions: "Implement it.", Push: config.PushDaemon},
	}
	mt := tracker.NewMemoryTracker([]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	runner := &evidenceRunner{}
	orch := orchestrator.New(cfg, mt, runner, &recordingWorkspaceProvider{path: ws})
	orch.SetIssueProfile("ENG-1", "impl")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = orch.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	require.Eventually(t, func() bool {
		runner.mu.Lock()
		defer runner.mu.Unlock()
		return len(runner.prompts) > 0
	}, 15*time.Second, 20*time.Millisecond)
	runner.mu.Lock()
	prompt := runner.prompts[0]
	runner.mu.Unlock()
	assert.Contains(t, prompt, "## Publishing (patches only)")
	assert.Contains(t, prompt, guardrails.PatchOutMessage)
}
