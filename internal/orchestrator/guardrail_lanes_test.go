package orchestrator_test

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agentactions"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/gitexec"
	"github.com/vnovick/itervox/internal/guardrails"
	"github.com/vnovick/itervox/internal/metrics"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
)

func laneGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitexec.Command(context.Background(), dir, args...).CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

// laneRig is a real orchestrator and server whose agent works in a real
// git clone with a bare remote: its script commits pushScript's change on
// a new branch and pushes it through the guardrail hook.
type laneRig struct {
	t      *testing.T
	orch   *orchestrator.Orchestrator
	remote string
	out    string
}

func newLaneRig(t *testing.T, file string) *laneRig {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	for _, kv := range [][2]string{{"GIT_AUTHOR_NAME", "t"}, {"GIT_AUTHOR_EMAIL", "t@example.com"},
		{"GIT_COMMITTER_NAME", "t"}, {"GIT_COMMITTER_EMAIL", "t@example.com"}, {"GIT_CONFIG_GLOBAL", "/dev/null"}} {
		t.Setenv(kv[0], kv[1])
	}
	r := &laneRig{t: t, remote: filepath.Join(root, "remote.git"), out: filepath.Join(root, "agent.out")}
	ws := filepath.Join(root, "ws")
	laneGit(t, root, "init", "-q", "--bare", "-b", "main", r.remote)
	laneGit(t, root, "clone", "-q", r.remote, ws)
	require.NoError(t, os.WriteFile(filepath.Join(ws, "README.md"), []byte("x\n"), 0o644))
	laneGit(t, ws, "add", "README.md")
	laneGit(t, ws, "commit", "-q", "-m", "base")
	laneGit(t, ws, "push", "-q", "origin", "HEAD:main")
	laneGit(t, ws, "fetch", "-q", "origin")

	script := filepath.Join(root, "agent.sh")
	body := "set -e\ngit checkout -q -b eng-1-fix\nmkdir -p " + filepath.Dir(file) + "\necho 'continue-on-error: true' > " + file +
		"\ngit add -A\ngit commit -q -m change\nset +e\ngit push -u origin HEAD > " + r.out + " 2>&1\necho \"exit=$?\" >> " + r.out + "\n"
	require.NoError(t, os.WriteFile(script, []byte(body), 0o755))

	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 1
	cfg.Agent.MaxConcurrentAgents = 1
	cfg.Agent.Command = "/bin/sh " + script
	cfg.Agent.TurnTimeoutMs = 60_000
	cfg.Tracker.CompletionState = "Done"
	cfg.Guardrails.Enabled = true
	cfg.Guardrails.Lanes.Human = []string{".github/**"}
	cfg.Guardrails.Lanes.SecondOpinion = []string{"auth/**"}
	mt := tracker.NewMemoryTracker([]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	r.orch = orchestrator.New(cfg, mt, shellRunner{}, &recordingWorkspaceProvider{path: ws})
	r.orch.SetPushLanesFile(filepath.Join(root, ".itervox", "push_lanes.json"))

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

func (r *laneRig) agentOutput() string {
	r.t.Helper()
	var out string
	require.Eventually(r.t, func() bool {
		data, _ := os.ReadFile(r.out)
		out = string(data)
		return strings.Contains(out, "exit=")
	}, 30*time.Second, 20*time.Millisecond, "the agent's push never finished")
	return out
}

func (r *laneRig) remoteRef(ref string) string {
	out, err := gitexec.Command(context.Background(), r.remote, "rev-parse", "-q", "--verify", ref).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// guardrails.lanes end to end: an agent's push that changes
// .github/workflows/ci.yml is held as a "pipeline change" input request
// naming the file; an agent cannot approve it; a human approval lets the
// push land once; and merge_pr is then refused for the issue (lane_human)
// — merging a pipeline change is a human's job. A human who rejects
// instead keeps the push off the remote and counts lane_rejected.
func TestMergeRefusedForHumanLaneRun(t *testing.T) {
	r := newLaneRig(t, ".github/workflows/ci.yml")
	require.Eventually(t, func() bool { // once the loop runs; before, the check fails closed
		return r.orch.MergeLaneRefusal(context.Background(), "ENG-1") == ""
	}, 5*time.Second, 5*time.Millisecond, "nothing pushed yet: no lane")

	var entry *orchestrator.InputRequiredEntry
	require.Eventually(t, func() bool {
		entry = r.orch.Snapshot().InputRequiredIssues["ENG-1"]
		return entry != nil && entry.GuardrailApprovalID != ""
	}, 30*time.Second, 20*time.Millisecond, "the pipeline push never raised an input request")
	assert.Equal(t, []string{".github/workflows/ci.yml"}, entry.GuardrailLanePaths)
	assert.Contains(t, entry.Context, "Pipeline change: the agent wants to push changes to `.github/workflows/ci.yml`")
	assert.Contains(t, entry.Context, "Itervox will not auto-merge this pull request")
	assert.Empty(t, r.remoteRef("refs/heads/eng-1-fix"), "held: nothing on the remote yet")

	require.NoError(t, r.orch.ProvideAgentInput("ENG-1", "approve"))
	time.Sleep(300 * time.Millisecond)
	assert.NotNil(t, r.orch.Snapshot().InputRequiredIssues["ENG-1"], "an agent cannot approve its own push")

	require.NoError(t, r.orch.ProvideInput("ENG-1", "approve"))
	out := r.agentOutput()
	assert.Contains(t, out, "exit=0", out)
	assert.NotEmpty(t, r.remoteRef("refs/heads/eng-1-fix"), "the approved push landed")

	var reason string
	require.Eventually(t, func() bool {
		reason = r.orch.MergeLaneRefusal(context.Background(), "ENG-1")
		return reason != ""
	}, 5*time.Second, 20*time.Millisecond)
	assert.True(t, strings.HasPrefix(reason, orchestrator.MergeRefusalLaneHuman+":"), reason)
	assert.Contains(t, reason, "`.github/workflows/ci.yml`")
	assert.Contains(t, reason, "Merging it is a human's job")
	rec := r.orch.Snapshot().PushLanes["ENG-1"]
	assert.Equal(t, guardrails.LaneHuman, rec.Lane)

	// Rejected: the push stays off the remote, counted as lane_rejected.
	before := metrics.GuardrailBlockCount(guardrails.KindLaneRejected)
	rej := newLaneRig(t, ".github/workflows/ci.yml")
	require.Eventually(t, func() bool {
		e := rej.orch.Snapshot().InputRequiredIssues["ENG-1"]
		return e != nil && e.GuardrailApprovalID != ""
	}, 30*time.Second, 20*time.Millisecond)
	require.NoError(t, rej.orch.ProvideInput("ENG-1", "no: keep CI strict and fix the test"))
	out = rej.agentOutput()
	assert.Contains(t, out, "exit=1", out)
	assert.Contains(t, out, "the operator rejected `git push origin refs/heads/eng-1-fix`: no: keep CI strict and fix the test")
	assert.Empty(t, rej.remoteRef("refs/heads/eng-1-fix"))
	assert.Equal(t, before+1, metrics.GuardrailBlockCount(guardrails.KindLaneRejected))
	assert.Empty(t, rej.orch.MergeLaneRefusal(context.Background(), "ENG-1"), "a rejected push leaves no lane")
}

// guardrails.lanes: a held push says WHY, with file and line — the audit
// finding on the pushed workflow and the lane pattern it matched — on the
// input-required entry (the dashboard card), in its question (the tracker
// comment, `itervox watch`) and in the run context Slack reads.
func TestHeldPushQuestionSaysWhy(t *testing.T) {
	r := newLaneRig(t, ".github/workflows/ci.yml")
	var entry *orchestrator.InputRequiredEntry
	require.Eventually(t, func() bool {
		entry = r.orch.Snapshot().InputRequiredIssues["ENG-1"]
		return entry != nil && entry.GuardrailApprovalID != ""
	}, 30*time.Second, 20*time.Millisecond, "the pipeline push never raised an input request")
	want := []string{
		".github/workflows/ci.yml:1 adds `continue-on-error: true` (failure ignored in a CI workflow (continue-on-error / allow_failure: true))",
		".github/workflows/ci.yml matched .github/** → human",
	}
	assert.Equal(t, want, guardrails.LaneReasonLines(entry.GuardrailLaneReasons, entry.GuardrailLaneReasonsTotal, guardrails.MaxLaneReasonsShown))
	assert.Contains(t, entry.Context, "Why this needs a person:\n- "+want[0]+"\n- "+want[1]+"\n")
	rc, ok := r.orch.RunContext("ENG-1")
	require.True(t, ok)
	assert.True(t, rc.PendingApproval)
	assert.Equal(t, want, rc.PendingReasons)

	require.NoError(t, r.orch.ProvideInput("ENG-1", "no"))
	assert.Contains(t, r.agentOutput(), "exit=1")
}

// guardrails.lanes end to end: a second-opinion push is not held; it lands
// and is recorded for merge policy with the writer's backend.
func TestSecondOpinionPushRecordedWithWriterBackend(t *testing.T) {
	r := newLaneRig(t, "auth/token.go")
	out := r.agentOutput()
	assert.Contains(t, out, "exit=0", out)
	assert.NotEmpty(t, r.remoteRef("refs/heads/eng-1-fix"))
	var reason string
	require.Eventually(t, func() bool {
		reason = r.orch.MergeLaneRefusal(context.Background(), "ENG-1")
		return reason != ""
	}, 5*time.Second, 20*time.Millisecond)
	assert.True(t, strings.HasPrefix(reason, orchestrator.MergeRefusalLaneSecondOpinion+":"), reason)
	assert.Contains(t, reason, "(claude)", "the writer's backend is named")
	assert.Equal(t, []string{"claude"}, r.orch.Snapshot().PushLanes["ENG-1"].WriterBackends)
}
