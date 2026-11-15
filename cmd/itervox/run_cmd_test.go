package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	"github.com/vnovick/itervox/internal/gitexec"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
)

// `itervox run` tests (#252): the real orchestrator against a memory
// tracker, a scripted agent runner, a local bare git remote and a fake PR
// opener. A fake `gh` first on PATH fails every call, so nothing reaches
// GitHub; no agent CLI runs.

const runTestPRURL = "https://github.com/acme/widgets/pull/7"

// runTestWorkflow is the rig's WORKFLOW.md; %[1]s is extra agent keys,
// %[2]s extra top-level sections.
const runTestWorkflow = `---
itervox_schema_version: 2
tracker:
  kind: memory
  active_states: ["Todo", "In Progress"]
  working_state: "In Progress"
  completion_state: "In Review"
  terminal_states: ["Done"]
polling:
  interval_ms: 50
agent:
  command: fake-agent
  max_turns: 1
  max_retries: 1
%[1]s%[2]s---
Work on {{ issue.identifier }}: {{ issue.title }}.
`

// runTurn is one scripted agent turn.
type runTurn struct {
	ctx        context.Context
	log        agent.Logger
	onProgress func(agent.TurnResult)
	prompt     string
	ws         string
	command    string
	call       int
	turnMs     int // the turn timeout the orchestrator passed
}

type fakeRunRunner struct {
	fn    func(runTurn) (agent.TurnResult, error)
	calls atomic.Int32
}

func (f *fakeRunRunner) RunTurn(ctx context.Context, log agent.Logger, onProgress func(agent.TurnResult), _ *string,
	prompt, workspacePath, command, _, _ string, _, turnTimeoutMs int, _ agent.PermissionMode) (agent.TurnResult, error) {
	n := int(f.calls.Add(1))
	return f.fn(runTurn{ctx: ctx, log: log, onProgress: onProgress, prompt: prompt, ws: workspacePath, command: command, call: n, turnMs: turnTimeoutMs})
}

type runPRCall struct{ dir, head, base, title, body string }

type runRig struct {
	t      *testing.T
	root   string
	remote string
	repo   string
	mt     *tracker.MemoryTracker
	runner *fakeRunRunner
	ghLog  string

	mu  sync.Mutex
	prs []runPRCall
}

func rigGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitexec.Command(context.Background(), dir, args...).CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

// newRunRig writes a repository with WORKFLOW.md (agentExtra and topExtra
// added), pushes it to a bare remote, and points `itervox run` at a memory
// tracker holding ENG-1 and at turn.
func newRunRig(t *testing.T, agentExtra, topExtra string, turn func(runTurn) (agent.TurnResult, error)) *runRig {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir()) // macOS: /var -> /private/var
	require.NoError(t, err)
	r := &runRig{t: t, root: root, remote: filepath.Join(root, "remote.git"), repo: filepath.Join(root, "repo"),
		ghLog: filepath.Join(root, "gh.log")}
	home := filepath.Join(root, "home")
	bin := filepath.Join(root, "bin")
	require.NoError(t, os.MkdirAll(home, 0o755))
	require.NoError(t, os.MkdirAll(bin, 0o755))
	t.Setenv("HOME", home)
	t.Setenv("GITHUB_STEP_SUMMARY", "")
	t.Setenv("ITERVOX_LOG_FORMAT", "")
	require.NoError(t, os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\necho \"$*\" >> "+r.ghLog+"\nexit 1\n"), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	rigGit(t, root, "init", "--quiet", "--bare", "-b", "main", r.remote)
	rigGit(t, root, "init", "--quiet", "-b", "main", r.repo)
	r.configureGit(r.repo)
	require.NoError(t, os.WriteFile(filepath.Join(r.repo, "WORKFLOW.md"),
		[]byte(strings.ReplaceAll(strings.ReplaceAll(runTestWorkflow, "%[1]s", agentExtra), "%[2]s", topExtra)), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(r.repo, "README.md"), []byte("# widgets\n"), 0o644))
	for _, f := range []string{"SOUL.md", "INSTRUCTIONS.md"} {
		p := filepath.Join(r.repo, ".itervox", "agents", "reviewer", f)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("You review.\n"), 0o644))
	}
	rigGit(t, r.repo, "add", "-A")
	rigGit(t, r.repo, "commit", "--quiet", "-m", "initial")
	rigGit(t, r.repo, "remote", "add", "origin", r.remote)
	rigGit(t, r.repo, "push", "--quiet", "origin", "main")

	r.mt = tracker.NewMemoryTracker([]domain.Issue{{ID: "1", Identifier: "ENG-1", Title: "Add the feature", State: "Todo"}},
		[]string{"Todo", "In Progress"}, []string{"Done"})
	r.runner = &fakeRunRunner{fn: turn}
	prev := newIssueRunDeps
	newIssueRunDeps = func() issueRunDeps {
		return issueRunDeps{
			tracker: func(*config.Config) (tracker.Tracker, error) { return r.mt, nil },
			runner:  func(*config.Config) agent.Runner { return r.runner },
			prCreate: func(_ context.Context, dir, head, base, title, body string) (string, error) {
				r.mu.Lock()
				defer r.mu.Unlock()
				r.prs = append(r.prs, runPRCall{dir: dir, head: head, base: base, title: title, body: body})
				return runTestPRURL, nil
			},
		}
	}
	t.Cleanup(func() { newIssueRunDeps = prev })
	return r
}

func (r *runRig) configureGit(dir string) {
	rigGit(r.t, dir, "config", "user.name", "Itervox Test")
	rigGit(r.t, dir, "config", "user.email", "test@example.invalid")
	rigGit(r.t, dir, "config", "commit.gpgsign", "false")
}

// clone is a fresh checkout of the remote, as a new CI job has.
func (r *runRig) clone(name string) string {
	dir := filepath.Join(r.root, name)
	rigGit(r.t, r.root, "clone", "--quiet", r.remote, dir)
	r.configureGit(dir)
	return dir
}

// run runs `itervox run` against the checkout dir ("" = the rig's repo).
func (r *runRig) run(dir string, args ...string) (int, string, string) {
	r.t.Helper()
	if dir == "" {
		dir = r.repo
	}
	var out, errOut bytes.Buffer
	code := runIssueCommand(context.Background(), append([]string{"--workflow", filepath.Join(dir, "WORKFLOW.md")}, args...), &out, &errOut)
	return code, out.String(), errOut.String()
}

func (r *runRig) prCalls() []runPRCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]runPRCall(nil), r.prs...)
}

func (r *runRig) issue() *domain.Issue {
	r.t.Helper()
	is, err := r.mt.FetchIssueDetail(context.Background(), "1")
	require.NoError(r.t, err)
	return is
}

func (r *runRig) comments() string {
	var b strings.Builder
	for _, c := range r.issue().Comments {
		b.WriteString(c.Body + "\n")
	}
	return b.String()
}

// remoteFiles lists the files on branch in the bare remote.
func (r *runRig) remoteFiles(branch string) string {
	return rigGit(r.t, r.remote, "ls-tree", "-r", "--name-only", "refs/heads/"+branch)
}

// commitFeature is the implementer's work: one committed file. It runs on
// the worker goroutine, so it reports errors instead of failing the test.
func commitFeature(ws, name string) error {
	if err := os.WriteFile(filepath.Join(ws, name), []byte("feature\n"), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{{"add", name}, {"commit", "--quiet", "-m", "feat: " + name}} {
		if out, err := gitexec.Command(context.Background(), ws, args...).CombinedOutput(); err != nil {
			return fmt.Errorf("git %v: %w: %s", args, err, out)
		}
	}
	return nil
}

func implementerTurn() func(runTurn) (agent.TurnResult, error) {
	return func(rt runTurn) (agent.TurnResult, error) {
		if err := commitFeature(rt.ws, "feature.txt"); err != nil {
			return agent.TurnResult{Failed: true, FailureText: err.Error()}, err
		}
		cost := 0.25
		return agent.TurnResult{InputTokens: 120, OutputTokens: 30, ResultText: "Implemented the feature.",
			AllTextBlocks: []string{"Implemented the feature."}, SessionID: "sess-impl", CostUSD: &cost}, nil
	}
}

const runTestQuestion = "Which store should the cache use: Redis or Postgres?"

func questionTurn() (agent.TurnResult, error) {
	return agent.TurnResult{InputRequired: true, ResultText: runTestQuestion, InputTokens: 40, OutputTokens: 10, SessionID: "sess-q"}, nil
}

// #252: one issue, then exit — the agent's commit is pushed to the remote,
// the PR is opened against the checkout's branch, its link commented on the
// issue, the issue moved to completion_state, and the run exits 0 with the
// PR in its summary.
func TestRunOneIssueOpensPRAndExitsZero(t *testing.T) {
	r := newRunRig(t, "", "", implementerTurn())

	code, out, errOut := r.run("", "--issue", "ENG-1")
	require.Equal(t, runExitOK, code, "stdout:\n%s\nstderr:\n%s", out, errOut)

	assert.Contains(t, out, "Pull request opened")
	assert.Contains(t, out, runTestPRURL)
	prs := r.prCalls()
	require.Len(t, prs, 1)
	assert.Equal(t, "itervox/eng-1", prs[0].head)
	assert.Equal(t, "main", prs[0].base)
	assert.Equal(t, "ENG-1: Add the feature", prs[0].title)
	assert.Contains(t, prs[0].body, "Resolves ENG-1")

	assert.Equal(t, "In Review", r.issue().State)
	assert.Contains(t, r.comments(), "Pull request created: "+runTestPRURL)

	files := r.remoteFiles("itervox/eng-1")
	assert.Contains(t, files, "feature.txt", "the agent's commit reached the remote")
	assert.Contains(t, files, ".itervox/handoff/", "the run's committed handoff reached the remote")
	assert.Equal(t, 1, int(r.runner.calls.Load()), "exactly one agent turn")
}

// #252: an agent question is posted on the issue, committed as a handoff on
// the pushed issue branch, and the run exits 3 with the question and how to
// answer it.
func TestRunNeedsInputPostsQuestionAndExits3(t *testing.T) {
	r := newRunRig(t, "", "", func(runTurn) (agent.TurnResult, error) { return questionTurn() })

	code, out, errOut := r.run("", "--issue", "ENG-1")
	require.Equal(t, runExitNeedsInput, code, "stdout:\n%s\nstderr:\n%s", out, errOut)

	assert.Contains(t, out, "Needs input")
	assert.Contains(t, out, runTestQuestion)
	assert.Contains(t, out, "/itervox <answer>")
	comments := r.comments()
	assert.Contains(t, comments, runTestQuestion, "the question is posted on the issue")
	assert.Contains(t, comments, "/itervox <your answer>", "with the CI reply hint")
	assert.Equal(t, "In Progress", r.issue().State, "the issue stays in its working state")
	assert.Empty(t, r.prCalls())

	files := r.remoteFiles("itervox/eng-1")
	require.Contains(t, files, "_question.md")
	var question string
	for _, f := range strings.Fields(files) {
		if strings.HasSuffix(f, "_question.md") {
			question = rigGit(t, r.remote, "show", "refs/heads/itervox/eng-1:"+f)
		}
	}
	assert.Contains(t, question, runQuestionStart+"\n"+runTestQuestion+"\n"+runQuestionEnd)
}

// #252: `--answer` in a fresh checkout continues from the question handoff
// committed on the issue branch plus the question and the answer, and then
// completes normally.
func TestRunAnswerResumesFromHandoff(t *testing.T) {
	var resumed runTurn
	r := newRunRig(t, "", "", func(rt runTurn) (agent.TurnResult, error) {
		if rt.call == 1 {
			return questionTurn()
		}
		resumed = rt
		if err := commitFeature(rt.ws, "feature.txt"); err != nil {
			return agent.TurnResult{Failed: true, FailureText: err.Error()}, err
		}
		return agent.TurnResult{InputTokens: 90, OutputTokens: 20, ResultText: "Done with Postgres.", AllTextBlocks: []string{"Done."}}, nil
	})
	code, out, errOut := r.run("", "--issue", "ENG-1")
	require.Equal(t, runExitNeedsInput, code, "stdout:\n%s\nstderr:\n%s", out, errOut)

	fresh := r.clone("ci-job-2")
	code, out, errOut = r.run(fresh, "--issue", "ENG-1", "--answer", "Use Postgres.")
	require.Equal(t, runExitOK, code, "stdout:\n%s\nstderr:\n%s", out, errOut)

	assert.Equal(t, fresh, resumed.ws, "the new run works in its own checkout")
	handoffs, err := filepath.Glob(filepath.Join(fresh, ".itervox", "handoff", "*_question.md"))
	require.NoError(t, err)
	require.Len(t, handoffs, 1, "the issue branch was checked out from the remote with its handoff")
	assert.Contains(t, resumed.prompt, "Question for the operator", "the committed handoff is in the prompt")
	assert.Contains(t, resumed.prompt, "Agent request:\n"+runTestQuestion)
	assert.Contains(t, resumed.prompt, "User reply:\nUse Postgres.")

	prs := r.prCalls()
	require.Len(t, prs, 1)
	assert.Equal(t, "itervox/eng-1", prs[0].head)
	files := r.remoteFiles("itervox/eng-1")
	assert.Contains(t, files, "feature.txt")
	assert.Contains(t, files, "_question.md")
	assert.Equal(t, "In Review", r.issue().State)
}

// #252: with no approval channel, a guardrail approval command fails closed:
// nothing runs, the run exits 3 naming the command (on the issue and in the
// summary); `--approve <command>` re-runs with exactly that command allowed.
func TestRunApprovalFailsClosedThenApproveFlag(t *testing.T) {
	r := newRunRig(t, "", "guardrails:\n  enabled: true\n  approval_commands: [\"deploy prod\"]\n",
		func(rt runTurn) (agent.TurnResult, error) {
			cmd := exec.CommandContext(rt.ctx, "/bin/sh", "-c", "deploy prod")
			cmd.Dir = rt.ws
			env := agent.RunEnvFrom(rt.ctx) // what the real runners apply
			cmd.Env = append(os.Environ(), env.Vars...)
			if env.PathPrepend != "" {
				cmd.Env = append(cmd.Env, "PATH="+env.PathPrepend+string(os.PathListSeparator)+os.Getenv("PATH"))
			}
			out, err := cmd.CombinedOutput()
			rt.log.Info("agent: deploy", "output", string(out), "error", err)
			if rt.call > 1 {
				if err := commitFeature(rt.ws, "feature.txt"); err != nil {
					return agent.TurnResult{Failed: true, FailureText: err.Error()}, err
				}
			}
			return agent.TurnResult{InputTokens: 60, OutputTokens: 15, ResultText: "Deploy attempted.", AllTextBlocks: []string{"Deploy attempted."}}, nil
		})
	deployLog := filepath.Join(r.root, "deploy.log")
	bin := filepath.Join(r.root, "bin")
	require.NoError(t, os.WriteFile(filepath.Join(bin, "deploy"), []byte("#!/bin/sh\necho \"$*\" >> "+deployLog+"\n"), 0o755))

	code, out, errOut := r.run("", "--issue", "ENG-1")
	require.Equal(t, runExitNeedsInput, code, "stdout:\n%s\nstderr:\n%s", out, errOut)
	assert.NoFileExists(t, deployLog, "fail closed: the command never ran")
	assert.Contains(t, out, "approval needed for `deploy prod`")
	assert.Contains(t, out, "/itervox approve deploy prod")
	assert.Contains(t, r.comments(), "`deploy prod`", "the question names the command on the issue")
	assert.Empty(t, r.prCalls())

	code, out, errOut = r.run("", "--issue", "ENG-1", "--approve", "deploy  prod")
	require.Equal(t, runExitOK, code, "stdout:\n%s\nstderr:\n%s", out, errOut)
	data, err := os.ReadFile(deployLog)
	require.NoError(t, err, "the approved command ran")
	assert.Equal(t, "prod\n", string(data), "exactly once")
	assert.Len(t, r.prCalls(), 1)
}

// #252: a run that reaches agent.budget (#136) is stopped, the issue paused
// with budget_exceeded, and the run exits 4.
func TestRunBudgetExceededExits4(t *testing.T) {
	r := newRunRig(t, "  budget:\n    tokens: 100\n", "", func(rt runTurn) (agent.TurnResult, error) {
		rt.onProgress(agent.TurnResult{InputTokens: 400, OutputTokens: 100, TotalTokens: 500})
		select {
		case <-rt.ctx.Done():
			return agent.TurnResult{InputTokens: 400, OutputTokens: 100}, rt.ctx.Err()
		case <-time.After(20 * time.Second):
			return agent.TurnResult{}, errors.New("the budget never stopped the run")
		}
	})

	code, out, errOut := r.run("", "--issue", "ENG-1")
	require.Equal(t, runExitBudget, code, "stdout:\n%s\nstderr:\n%s", out, errOut)
	assert.Contains(t, out, "Budget exceeded")
	assert.Contains(t, out, "400 in / 100 out")
	assert.Empty(t, r.prCalls())
	assert.NotEqual(t, "In Review", r.issue().State)
}

// #252: --json prints one object (status, PR, turns, tokens, cost, reviewer
// verdicts, the Markdown summary); the Markdown goes to $GITHUB_STEP_SUMMARY;
// --log-dir gets redacted logs.
func TestRunWritesStepSummaryAndJSON(t *testing.T) {
	secret := "ghp_" + strings.Repeat("Zq9X", 9)
	r := newRunRig(t, `  reviewer_profile: reviewer
  auto_review: true
  profiles:
    reviewer:
      command: fake-reviewer
      soul_file: .itervox/agents/reviewer/SOUL.md
      instructions_file: .itervox/agents/reviewer/INSTRUCTIONS.md
`, "", func(rt runTurn) (agent.TurnResult, error) {
		rt.log.Info("agent: output", "text", "token "+secret)
		if strings.Contains(rt.command, "fake-reviewer") {
			dir := orchestrator.ReviewDir(rt.ws, "ENG-1", "reviewer")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return agent.TurnResult{}, err
			}
			if err := os.WriteFile(filepath.Join(dir, orchestrator.ReviewVerdictFileName),
				[]byte(`{"verdict":"approve","reasons":["tests cover it"]}`), 0o644); err != nil {
				return agent.TurnResult{}, err
			}
			return agent.TurnResult{InputTokens: 10, OutputTokens: 5, ResultText: "Approved.", AllTextBlocks: []string{"Approved."}}, nil
		}
		return implementerTurn()(rt)
	})
	stepSummary := filepath.Join(r.root, "step-summary.md")
	t.Setenv("GITHUB_STEP_SUMMARY", stepSummary)
	logDir := filepath.Join(r.root, "logs")

	code, out, errOut := r.run("", "--issue", "ENG-1", "--json", "--log-dir", logDir)
	require.Equal(t, runExitOK, code, "stdout:\n%s\nstderr:\n%s", out, errOut)

	dec := json.NewDecoder(strings.NewReader(out))
	var got runSummary
	require.NoError(t, dec.Decode(&got))
	assert.False(t, dec.More(), "exactly one JSON object on stdout")
	assert.Equal(t, "pr_opened", got.Status)
	assert.Equal(t, 0, got.ExitCode)
	assert.Equal(t, "ENG-1", got.Issue)
	assert.Equal(t, runTestPRURL, got.PRURL)
	assert.Equal(t, "itervox/eng-1", got.Branch)
	assert.Equal(t, 1, got.Turns)
	assert.Equal(t, 130, got.InputTokens, "implementer and reviewer")
	assert.Equal(t, 35, got.OutputTokens)
	require.NotNil(t, got.CostUSD)
	assert.InDelta(t, 0.25, *got.CostUSD, 1e-9)
	require.Len(t, got.Reviewers, 1)
	assert.Equal(t, runReviewer{Profile: "reviewer", Verdict: "approve", Reasons: []string{"tests cover it"}}, got.Reviewers[0])
	assert.Equal(t, "passed", got.ReviewOutcome)
	assert.Contains(t, got.Summary, "### Reviewer verdicts")

	md, err := os.ReadFile(stepSummary)
	require.NoError(t, err)
	assert.Equal(t, got.Summary+"\n", string(md), "the same Markdown goes to the step summary")
	assert.Contains(t, string(md), "## itervox run: ENG-1")
	assert.Contains(t, string(md), runTestPRURL)
	assert.Contains(t, string(md), "**reviewer**: approve")

	logText, err := os.ReadFile(filepath.Join(logDir, "itervox.log"))
	require.NoError(t, err)
	assert.Contains(t, string(logText), "run: started")
	issueLogs, err := filepath.Glob(filepath.Join(logDir, "issues", "*"))
	require.NoError(t, err)
	assert.NotEmpty(t, issueLogs, "per-issue logs are kept")
	require.NoError(t, filepath.WalkDir(logDir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, rerr := os.ReadFile(p)
		require.NoError(t, rerr)
		assert.NotContains(t, string(data), secret, "%s is redacted", p)
		return nil
	}))
	assert.NotContains(t, errOut, secret)
}

// #252: a run opens no listener (no HTTP server, hub or MCP socket), joins
// no hub and writes none of the daemon's runtime files.
func TestRunHasNoServerOrHub(t *testing.T) {
	before := listeningSockets(t)
	var during map[string]bool
	r := newRunRig(t, "", "", func(rt runTurn) (agent.TurnResult, error) {
		during = listeningSockets(t)
		return implementerTurn()(rt)
	})

	code, out, errOut := r.run("", "--issue", "ENG-1")
	require.Equal(t, runExitOK, code, "stdout:\n%s\nstderr:\n%s", out, errOut)

	require.NotNil(t, during, "sampled while the agent ran")
	for inode := range during {
		assert.True(t, before[inode], "a listening socket (inode %s) was opened during the run", inode)
	}
	assert.False(t, hubMembership.Load(), "the run never joins the hub")
	workflow := filepath.Join(r.repo, "WORKFLOW.md")
	pid, err := pidFilePath(workflow)
	require.NoError(t, err)
	assert.NoFileExists(t, pid)
	assert.NoFileExists(t, heartbeatPath(workflow))
	assert.NoFileExists(t, dashboardURLFilePath(workflow))
	reg, err := daemonRegistryEntryPath()
	require.NoError(t, err)
	assert.NoFileExists(t, reg)
}

// listeningSockets is the inodes of this process's listening TCP and Unix
// sockets (Linux /proc).
func listeningSockets(t *testing.T) map[string]bool {
	t.Helper()
	fds, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skip("needs /proc")
	}
	mine := map[string]bool{}
	for _, fd := range fds {
		link, err := os.Readlink(filepath.Join("/proc/self/fd", fd.Name()))
		if err == nil && strings.HasPrefix(link, "socket:[") {
			mine[strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")] = true
		}
	}
	out := map[string]bool{}
	for _, table := range []string{"tcp", "tcp6"} {
		data, err := os.ReadFile("/proc/self/net/" + table)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n")[1:] {
			f := strings.Fields(line)
			if len(f) > 9 && f[3] == "0A" && mine[f[9]] { // 0A = LISTEN
				out[f[9]] = true
			}
		}
	}
	if data, err := os.ReadFile("/proc/self/net/unix"); err == nil {
		for _, line := range strings.Split(string(data), "\n")[1:] {
			f := strings.Fields(line)
			if len(f) > 6 && f[3] == "00010000" && mine[f[6]] { // __SO_ACCEPTCON
				out[f[6]] = true
			}
		}
	}
	return out
}

// #252: an issue outside tracker.active_states is nothing to do (exit 0,
// no agent turn); an invalid workflow or a missing --issue is a
// configuration error (exit 5).
func TestRunNothingToDoAndConfigErrorExitCodes(t *testing.T) {
	r := newRunRig(t, "", "", implementerTurn())
	r.mt.SetIssueState("1", "Done")
	code, out, errOut := r.run("", "--issue", "ENG-1")
	require.Equal(t, runExitOK, code, "stdout:\n%s\nstderr:\n%s", out, errOut)
	assert.Contains(t, out, "Nothing to do")
	assert.Zero(t, r.runner.calls.Load())

	code, _, errOut = r.run("")
	assert.Equal(t, runExitConfig, code)
	assert.Contains(t, errOut, "--issue is required")

	require.NoError(t, os.WriteFile(filepath.Join(r.repo, "WORKFLOW.md"), []byte("---\ntracker:\n  kind: memory\n---\nx\n"), 0o644))
	code, out, _ = r.run("", "--issue", "ENG-1", "--json")
	assert.Equal(t, runExitConfig, code)
	assert.Contains(t, out, `"status":"config_error"`)
}

// #252: one run per issue — a second run of the issue in the same checkout
// is refused while the first holds .itervox/run/issue-<id>.lock.
func TestRunRefusesASecondRunOfTheSameIssue(t *testing.T) {
	r := newRunRig(t, "", "", implementerTurn())
	release, err := lockRunIssue(r.repo, "ENG-1")
	require.NoError(t, err)
	code, out, errOut := r.run("", "--issue", "ENG-1")
	assert.Equal(t, runExitFailed, code, "stdout:\n%s\nstderr:\n%s", out, errOut)
	assert.Contains(t, out, "another itervox run is working on ENG-1")
	assert.Zero(t, r.runner.calls.Load())
	release()

	code, out, errOut = r.run("", "--issue", "ENG-1")
	assert.Equal(t, runExitOK, code, "stdout:\n%s\nstderr:\n%s", out, errOut)
	assert.Empty(t, strings.TrimSpace(rigGit(t, r.repo, "status", "--porcelain", "--ignored=no")),
		"the lock and run files stay out of git status")
}

// #252: --worktree works in .itervox/run/worktrees/<issue>, and the checkout
// keeps its own branch.
func TestRunWorktreeKeepsTheCheckoutOnItsBranch(t *testing.T) {
	var ws string
	r := newRunRig(t, "", "", func(rt runTurn) (agent.TurnResult, error) {
		ws = rt.ws
		return implementerTurn()(rt)
	})
	code, out, errOut := r.run("", "--issue", "ENG-1", "--worktree")
	require.Equal(t, runExitOK, code, "stdout:\n%s\nstderr:\n%s", out, errOut)
	assert.Equal(t, filepath.Join(r.repo, ".itervox", "run", "worktrees", "ENG-1"), ws)
	assert.Equal(t, "main", rigGit(t, r.repo, "rev-parse", "--abbrev-ref", "HEAD"))
	assert.Contains(t, r.remoteFiles("itervox/eng-1"), "feature.txt")
	require.Len(t, r.prCalls(), 1)
	assert.Equal(t, "main", r.prCalls()[0].base)
}

// #252: --timeout caps the whole run (exit 2, "timed out") and the agent
// turn timeout is lowered below it.
func TestRunTimeoutCapsTheRun(t *testing.T) {
	var turnMs atomic.Int64
	r := newRunRig(t, "", "", func(rt runTurn) (agent.TurnResult, error) {
		turnMs.Store(int64(rt.turnMs))
		<-rt.ctx.Done() // a turn that never ends on its own
		return agent.TurnResult{}, rt.ctx.Err()
	})
	start := time.Now()
	code, out, errOut := r.run("", "--issue", "ENG-1", "--timeout", "2s")
	require.Equal(t, runExitFailed, code, "stdout:\n%s\nstderr:\n%s", out, errOut)
	assert.Contains(t, out, "timed out after 2s")
	assert.Less(t, time.Since(start), 40*time.Second)
	assert.Equal(t, int64(1800), turnMs.Load(), "turn timeout = 90% of --timeout")
	assert.Empty(t, r.prCalls())
}
