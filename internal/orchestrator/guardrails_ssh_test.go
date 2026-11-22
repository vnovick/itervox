package orchestrator_test

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/agentactions"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/gitexec"
	"github.com/vnovick/itervox/internal/guardrails"
	"github.com/vnovick/itervox/internal/guardrails/sshtest"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
)

func sshRigGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitexec.Command(context.Background(), dir, args...).CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

// 0.3.4, the acceptance test for guarding SSH worker hosts: a real
// orchestrator, a real server for the guardrail routes and the real Claude
// runner's SSH transport, with a fake ssh client (sshtest) whose "worker"
// runs the remote command in its own HOME. The fake agent CLI runs on the
// worker and tries every guarded operation:
//   - a push to another branch (git by absolute path: only the worker's
//     pre-push hook can stop it) and a tag push are refused by the hook;
//   - `gh pr merge` is refused by the worker's gh shim;
//   - `vercel --prod` is held: the approval reaches the daemon over the
//     turn's reverse-forwarded socket and is shown as input required; the
//     agent cannot answer it; a human approval runs it exactly once;
//   - its own issue branch is pushed.
//
// Every refusal reaches RecentFailures through the callback; the run's
// token is in the agent's environment on the worker but in no ssh argument;
// the worker's runtime directory is gone when the run ends.
//
// Before 0.3.4 the same run was unguarded (prepareRunGuardrails returned
// nothing for an SSH worker): the push to other-branch and the tag landed.
func TestSSHWorkerRunIsGuardedOnTheWorker(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	require.NoError(t, err)
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com",
		"GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com", "GIT_CONFIG_GLOBAL": "/dev/null"} {
		t.Setenv(k, v)
	}
	origin, work := filepath.Join(root, "origin.git"), filepath.Join(root, "ws")
	sshRigGit(t, root, "init", "-q", "--bare", "-b", "main", origin)
	sshRigGit(t, root, "init", "-q", "-b", "main", work)
	sshRigGit(t, work, "remote", "add", "origin", origin)
	require.NoError(t, os.WriteFile(filepath.Join(work, "f.txt"), []byte("base\n"), 0o644))
	sshRigGit(t, work, "add", "f.txt")
	sshRigGit(t, work, "commit", "-q", "-m", "base")
	sshRigGit(t, work, "push", "-q", "origin", "main")
	sshRigGit(t, work, "checkout", "-q", "-b", "itervox/eng-1")
	require.NoError(t, os.WriteFile(filepath.Join(work, "f.txt"), []byte("work\n"), 0o644))
	sshRigGit(t, work, "commit", "-q", "-am", "work")

	fakes := filepath.Join(root, "fakebin")
	require.NoError(t, os.MkdirAll(fakes, 0o755))
	ghLog, vercelLog, out := filepath.Join(root, "gh.log"), filepath.Join(root, "vercel.log"), filepath.Join(root, "agent.out")
	// The daemon itself runs `gh pr view` (PR detection): only a merge is recorded.
	require.NoError(t, os.WriteFile(filepath.Join(fakes, "gh"), []byte("#!/bin/sh\n[ \"$1 $2\" = \"pr merge\" ] && echo \"$*\" >> "+ghLog+"\nexit 0\n"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(fakes, "vercel"), []byte("#!/bin/sh\necho \"$*\" >> "+vercelLog+"\n"), 0o755))
	t.Setenv("PATH", fakes+string(os.PathListSeparator)+os.Getenv("PATH"))
	_, argvLog := sshtest.Install(t)

	step := func(tag, cmd string) string {
		return cmd + " >> " + out + " 2>&1; echo \"" + tag + "=$?\" >> " + out + "\n"
	}
	script := filepath.Join(root, "agent.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\n"+
		"printf 'token=%s\\nsocket=%s\\n' \"$"+guardrails.TokenEnv+"\" \"$"+guardrails.SocketEnv+"\" >> "+out+"\n"+
		step("hook_other", gitBin+" push origin HEAD:refs/heads/other-branch")+
		step("tag", gitBin+" tag v9 && "+gitBin+" push origin refs/tags/v9")+
		step("merge", "gh pr merge 1 --squash")+
		step("deploy", "vercel --prod")+
		step("own", gitBin+" push origin HEAD:refs/heads/itervox/eng-1")+
		"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"done\",\"session_id\":\"s1\"}'\n"), 0o755))

	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 1
	cfg.Agent.MaxConcurrentAgents = 1
	cfg.Agent.Command = "/bin/sh " + script
	cfg.Agent.SSHHosts = []string{"worker-1"}
	cfg.Agent.TurnTimeoutMs = 120_000
	cfg.Agent.ReadTimeoutMs = 120_000
	cfg.Tracker.CompletionState = "Done"
	cfg.Guardrails.Enabled = true
	cfg.Guardrails.ApprovalCommands = []string{"vercel --prod"}
	branch := "itervox/eng-1"
	issue := makeIssue("id1", "ENG-1", "In Progress", nil, nil)
	issue.BranchName = &branch
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := orchestrator.New(cfg, mt, agent.NewClaudeRunner(), &recordingWorkspaceProvider{path: work})
	store := agentactions.NewStore()
	srv := httptest.NewServer(server.New(server.Config{
		Snapshot:         func() server.StateSnapshot { return server.StateSnapshot{} },
		RefreshChan:      make(chan struct{}, 1),
		ActionTokenStore: store,
		Guardrails:       orch,
	}))
	t.Cleanup(srv.Close)
	orch.SetAgentActionBaseURL(srv.URL)
	orch.SetAgentActionTokens(store)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = orch.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	read := func() string { data, _ := os.ReadFile(out); return string(data) }
	var entry *orchestrator.InputRequiredEntry
	require.Eventually(t, func() bool {
		entry = orch.Snapshot().InputRequiredIssues["ENG-1"]
		return entry != nil && entry.GuardrailApprovalID != ""
	}, 60*time.Second, 50*time.Millisecond, "the deploy never raised an input request")
	assert.Contains(t, entry.Context, "Agent wants to run: `vercel --prod`")
	assert.Equal(t, "worker-1", entry.WorkerHost)
	before := read()
	assert.Contains(t, before, "hook_other=1", before)
	assert.Contains(t, before, "tag=1", before)
	assert.Contains(t, before, "merge=1", before)
	assert.Empty(t, readFields(vercelLog), "nothing runs before the approval")

	require.NoError(t, orch.ProvideAgentInput("ENG-1", "approve"))
	time.Sleep(300 * time.Millisecond)
	assert.NotNil(t, orch.Snapshot().InputRequiredIssues["ENG-1"], "an agent cannot approve its own command")
	require.NoError(t, orch.ProvideInput("ENG-1", "approve"))

	require.Eventually(t, func() bool { return strings.Contains(read(), "own=") }, 60*time.Second, 50*time.Millisecond, "the agent never finished")
	final := read()
	assert.Contains(t, final, "deploy=0", final)
	assert.Contains(t, final, "approved; running `vercel --prod` once")
	assert.Contains(t, final, "own=0", final)
	assert.Contains(t, final, "push to refs/heads/other-branch is refused")
	assert.Contains(t, final, "gh pr merge is refused")
	assert.Equal(t, []string{"--prod"}, readFields(vercelLog), "the approved command ran exactly once")
	assert.Empty(t, readFields(ghLog), "gh pr merge never ran")
	assert.Empty(t, remoteRef(origin, "refs/heads/other-branch"), "the worker's hook refused the push")
	assert.Empty(t, remoteRef(origin, "refs/tags/v9"))
	assert.NotEmpty(t, remoteRef(origin, "refs/heads/itervox/eng-1"), "the issue branch is pushed")

	kinds := map[string]bool{}
	require.Eventually(t, func() bool {
		for _, f := range orch.Snapshot().RecentFailures {
			if f.Kind == orchestrator.FailureKindGuardrail && f.Identifier == "ENG-1" {
				kinds[f.Source] = true
			}
		}
		return kinds[guardrails.KindBranch] && kinds[guardrails.KindTag] && kinds[guardrails.KindSelfMerge]
	}, 10*time.Second, 50*time.Millisecond, "refusals reported through the callback: %v", kinds)

	token, socket := lineValue(final, "token"), lineValue(final, "socket")
	require.NotEmpty(t, token, "the shims got the run's token")
	require.NotEmpty(t, socket)
	argv, err := os.ReadFile(argvLog)
	require.NoError(t, err)
	assert.False(t, strings.Contains(string(argv), token), "the token is never in an ssh argument")
	assert.Contains(t, string(argv), socket+":"+strings.TrimPrefix(srv.URL, "http://"), "the turn reverse-forwards its socket")
	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Dir(socket))
		return os.IsNotExist(err)
	}, 30*time.Second, 50*time.Millisecond, "the worker's runtime directory is removed at run end")
}

func readFields(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return strings.Fields(strings.TrimSpace(string(data)))
}

func remoteRef(repo, ref string) string {
	out, err := gitexec.Command(context.Background(), repo, "rev-parse", "-q", "--verify", ref).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func lineValue(out, key string) string {
	for _, l := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(l, key+"="); ok {
			return v
		}
	}
	return ""
}
