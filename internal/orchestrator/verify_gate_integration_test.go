package orchestrator_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/attest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/gitexec"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
)

// passingSelfEvidence is what a confident agent writes: test passed, on
// its own commit ("HEAD" is replaced with the commit by evidenceRunner).
const passingSelfEvidence = `{"commit":"HEAD","checks":[{"name":"test","command":"go test ./...","output":"ok  pkg 0.1s","passed":true}]}`

// runVerifyWorker dispatches ENG-1 once under an implementer profile with
// require_evidence [test] and the given verify checks, in a git worktree,
// with a real signing key and store. selfEvidence is what the agent writes
// to its evidence file ("" writes none). It returns after the run moved the
// issue or held it.
func runVerifyWorker(t *testing.T, selfEvidence string, verify []config.VerifyCheck) (*orchestrator.Orchestrator, *tracker.MemoryTracker, attest.Store, ed25519.PublicKey) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("verify commands are bash command lines")
	}
	ws := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
		{"commit", "-q", "--allow-empty", "-m", "base"},
	} {
		out, err := gitexec.Command(context.Background(), ws, args...).CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 1
	cfg.Tracker.CompletionState = "Done"
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"implementer": {Command: "claude", Instructions: "Implement it.", RequireEvidence: []string{"test"}, Verify: verify},
	}
	mt := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates,
	)
	runner := &evidenceRunner{evidence: selfEvidence, gitCommit: true}
	orch := orchestrator.New(cfg, mt, runner, &recordingWorkspaceProvider{path: ws})
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer := attest.NewSigner(priv)
	store := attest.Store{Dir: t.TempDir()}
	orch.SetAttestation(signer, store)
	orch.SetIssueProfile("ENG-1", "implementer")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	go orch.Run(ctx) //nolint:errcheck
	require.Eventually(t, func() bool {
		if _, held := orch.Snapshot().InputRequiredIssues["ENG-1"]; held {
			return true
		}
		issues, err := mt.FetchIssueStatesByIDs(ctx, []string{"id1"})
		return err == nil && len(issues) == 1 && issues[0].State == "Done"
	}, 12*time.Second, 20*time.Millisecond, "the run must either move the issue or hold it")
	require.NotEmpty(t, runner.prompts)
	assert.Contains(t, runner.prompts[0], "- signed checks: `test`", "the agent is told Itervox runs the check")
	assert.NotContains(t, runner.prompts[0], "## Publishing (patches only)", "no push mode: no patches-only block")
	return orch, mt, store, signer.PublicKey()
}

// TestDaemonRunsVerifyAfterAgent (signed checks): the agent writes a
// passing self-evidence file for `test`, but the daemon's own run of the
// verify command exits 1, so the run does not succeed: the issue is held
// input-required naming the check and the exit code, it is not moved, and
// the signed record of the failing run is in the store.
func TestDaemonRunsVerifyAfterAgent(t *testing.T) {
	orch, mt, store, pub := runVerifyWorker(t, passingSelfEvidence, []config.VerifyCheck{
		{Name: "test", Run: "echo 'FAIL pkg' && exit 1", TimeoutMs: 60_000},
	})
	entry := orch.Snapshot().InputRequiredIssues["ENG-1"]
	require.NotNil(t, entry, "the run must be held, not succeed")
	assert.Contains(t, entry.Context, "Signed checks failed (verify)")
	assert.Contains(t, entry.Context, "`test` exited with code 1")
	assert.Contains(t, entry.Context, "FAIL pkg")
	assert.Equal(t, "In Progress", issueState(t, mt), "the agent's passing evidence file does not move the issue")

	runs, err := store.Runs("ENG-1")
	require.NoError(t, err)
	require.Len(t, runs, 1)
	recs := runs[0].Records
	require.Len(t, recs, 2, "one check record and the seal")
	assert.Equal(t, attest.KindCheck, recs[0].Kind)
	assert.Equal(t, "test", recs[0].Name)
	assert.Equal(t, 1, recs[0].ExitCode)
	assert.Equal(t, "ENG-1", recs[0].Issue)
	assert.Equal(t, "implementer", recs[0].Profile)
	assert.NotEmpty(t, recs[0].TreeHash)
	assert.NotEmpty(t, recs[0].Head)
	assert.Equal(t, attest.KindSeal, recs[1].Kind)
	for _, res := range attest.VerifyRun(runs[0], pub) {
		assert.True(t, res.Verified, "a daemon record verifies: %s", res.Reason)
	}
}

// TestDaemonVerifyPassMovesIssue (signed checks): when every verify
// command exits 0 the run succeeds and moves the issue, with signed,
// verifiable records whose tree is the run's final tree.
func TestDaemonVerifyPassMovesIssue(t *testing.T) {
	orch, mt, store, pub := runVerifyWorker(t, passingSelfEvidence, []config.VerifyCheck{
		{Name: "test", Run: "echo ok | tail -n 1", TimeoutMs: 60_000},
		{Name: "lint", Run: "true", TimeoutMs: 60_000},
	})
	assert.Nil(t, orch.Snapshot().InputRequiredIssues["ENG-1"])
	assert.Equal(t, "Done", issueState(t, mt))
	runs, err := store.Runs("ENG-1")
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.Len(t, runs[0].Records, 3)
	for _, res := range attest.VerifyRun(runs[0], pub) {
		assert.True(t, res.Verified, "%s: %s", res.Record.Name, res.Reason)
	}
}

// TestDaemonVerifyRefusesTreeChangingCheck (signed checks): a check that
// writes a new non-ignored file proves a tree that is not the one handed
// on, so the run is held.
func TestDaemonVerifyRefusesTreeChangingCheck(t *testing.T) {
	orch, mt, _, _ := runVerifyWorker(t, passingSelfEvidence, []config.VerifyCheck{
		{Name: "test", Run: "echo generated > generated.txt", TimeoutMs: 60_000},
	})
	entry := orch.Snapshot().InputRequiredIssues["ENG-1"]
	require.NotNil(t, entry)
	assert.Contains(t, entry.Context, "the working tree changed while the checks ran")
	assert.Equal(t, "In Progress", issueState(t, mt))
}

// TestDaemonVerifyIgnoresAgentEvidenceForVerifiedChecks (signed checks): a
// require_evidence name that verify runs is proved by the daemon's run
// alone, so an agent that wrote no evidence file still moves on.
func TestDaemonVerifyIgnoresAgentEvidenceForVerifiedChecks(t *testing.T) {
	orch, mt, _, _ := runVerifyWorker(t, "", []config.VerifyCheck{{Name: "test", Run: "true", TimeoutMs: 60_000}})
	assert.Nil(t, orch.Snapshot().InputRequiredIssues["ENG-1"])
	assert.Equal(t, "Done", issueState(t, mt))
}
