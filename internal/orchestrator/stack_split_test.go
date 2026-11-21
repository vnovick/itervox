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

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/guardrails"
	"github.com/vnovick/itervox/internal/orchestrator"
)

// Stack split (guardrails.lanes.split) end to end: a real orchestrator, a
// fake shell agent (never a real CLI) committing in a real clone with a
// bare remote, real signed checks, and the daemon's push and PR opener.

const ciFile = ".github/workflows/ci.yml"

func splitRig(t *testing.T, check string, extra ...string) *patchOutRig {
	t.Helper()
	return newPatchOutRig(t, patchOutOpts{
		file: "pkg/fix.go", extra: extra, split: true, human: []string{".github/**"}, completion: "In Review",
		verify: []config.VerifyCheck{{Name: "test", Run: check, TimeoutMs: 60_000}},
	})
}

// waitApproval waits for the daemon's push to raise a human-lane approval.
func (r *patchOutRig) waitApproval() *orchestrator.InputRequiredEntry {
	r.t.Helper()
	var entry *orchestrator.InputRequiredEntry
	require.Eventually(r.t, func() bool {
		entry = r.orch.Snapshot().InputRequiredIssues["ENG-1"]
		return entry != nil && entry.GuardrailApprovalID != ""
	}, 30*time.Second, 20*time.Millisecond, "the pipeline push never raised an approval")
	return entry
}

func (r *patchOutRig) approveAndSettle() {
	r.t.Helper()
	require.NoError(r.t, r.orch.ProvideInput("ENG-1", "approve"))
	require.Eventually(r.t, func() bool { return issueState(r.t, r.mt) == "In Review" }, 30*time.Second, 20*time.Millisecond,
		"the approved run never moved on")
}

func (r *patchOutRig) remoteFiles(ref string) []string {
	r.t.Helper()
	return strings.Split(laneGit(r.t, r.remote, "ls-tree", "-r", "--name-only", ref), "\n")
}

func (r *patchOutRig) split() orchestrator.StackSplitRecord {
	r.t.Helper()
	var rec orchestrator.StackSplitRecord
	require.Eventually(r.t, func() bool {
		var ok bool
		rec, ok = r.orch.Snapshot().StackSplits["ENG-1"]
		return ok
	}, 30*time.Second, 20*time.Millisecond, "no stack split decision recorded")
	return rec
}

// headOf reads a pull request's head as merge_pr does, here from the
// remote branch the PR is for.
func (r *patchOutRig) headOf(branch string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return r.remoteRef("refs/heads/" + branch), nil }
}

func (r *patchOutRig) feedKinds(kind string) []orchestrator.FeedEvent {
	var out []orchestrator.FeedEvent
	for _, ev := range r.orch.Snapshot().Feed {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

// No human-lane file: the split changes nothing — one branch, one PR, no
// decision recorded.
func TestStackSplitNoHumanFilesOnePR(t *testing.T) {
	r := splitRig(t, "test -f pkg/fix.go", "pkg/other.go")
	require.Eventually(t, func() bool { return issueState(t, r.mt) == "In Review" }, 30*time.Second, 20*time.Millisecond,
		"the run never moved on")
	assert.Nil(t, r.orch.Snapshot().InputRequiredIssues["ENG-1"], "the run must succeed, not hold")
	assert.NotEmpty(t, r.remoteRef("refs/heads/eng-1-fix"))
	assert.Empty(t, r.remoteRef("refs/heads/eng-1-fix-code"))
	assert.Empty(t, r.remoteRef("refs/heads/eng-1-fix-pipeline"))
	assert.Equal(t, []string{"eng-1-fix->"}, r.openedPRs())
	assert.NotContains(t, r.orch.Snapshot().StackSplits, "ENG-1")
	assert.Empty(t, r.feedKinds(orchestrator.FeedStackSplit))
}

// The checks pass on the code alone: the code PR goes first against the
// base, pushed and opened before any human looks at the pipeline change;
// the pipeline change is stacked on it and waits for its approval. merge_pr
// may merge the code PR (its own lane is auto) and never the pipeline PR.
// A lockfile changed in the same run stays with the code.
func TestStackSplitCodePassesAloneGoesFirst(t *testing.T) {
	r := splitRig(t, "test -f pkg/fix.go", ciFile, "web/package-lock.json")
	entry := r.waitApproval()
	assert.Equal(t, []string{ciFile}, entry.GuardrailLanePaths, "the top push carries only the pipeline change")

	code := r.remoteRef("refs/heads/eng-1-fix-code")
	require.NotEmpty(t, code, "the code branch is pushed before the pipeline approval")
	assert.Empty(t, r.remoteRef("refs/heads/eng-1-fix"), "the pipeline branch waits for its approval")
	assert.Equal(t, []string{"eng-1-fix-code->"}, r.openedPRs(), "the code PR is open while the pipeline waits")
	files := r.remoteFiles("refs/heads/eng-1-fix-code")
	assert.Contains(t, files, "pkg/fix.go")
	assert.Contains(t, files, "web/package-lock.json", "the lockfile stays with the code")
	assert.NotContains(t, files, ciFile)

	rec := r.split()
	assert.Equal(t, orchestrator.StackSplitCodeFirst, rec.Order)
	assert.Equal(t, []string{ciFile}, rec.PipelineFiles)
	assert.ElementsMatch(t, []string{"pkg/fix.go", "web/package-lock.json"}, rec.CodeFiles)
	assert.Equal(t, guardrails.LaneAuto, rec.CodeLane)
	assert.Equal(t, 1, rec.LowerPR)
	assert.Contains(t, rec.Reason, "pass on the code change alone")

	r.approveAndSettle()
	top := r.remoteRef("refs/heads/eng-1-fix")
	require.NotEmpty(t, top)
	assert.Equal(t, code, laneGit(t, r.remote, "rev-parse", top+"^"), "the pipeline commit is stacked on the code commit")
	assert.Contains(t, r.remoteFiles(top), ciFile)
	assert.Equal(t, []string{"eng-1-fix-code->", "eng-1-fix->eng-1-fix-code"}, r.openedPRs())
	assert.Len(t, r.feedKinds(orchestrator.FeedStackSplit), 1, "one feed event per decision")

	ctx := context.Background()
	assert.Equal(t, code, r.split().LowerSHA, "the record names the commit the split pushed")
	reason, match := r.orch.MergeLaneRefusalForPR(ctx, "ENG-1", 1, r.headOf("eng-1-fix-code"))
	assert.Empty(t, reason, "the code PR merges by its own lane")
	assert.Equal(t, code, match, "and only at the split's commit")
	reason, _ = r.orch.MergeLaneRefusalForPR(ctx, "ENG-1", 2, r.headOf("eng-1-fix"))
	assert.Contains(t, reason, orchestrator.MergeRefusalLaneHuman)
	assert.Contains(t, r.orch.MergeLaneRefusal(ctx, "ENG-1"), orchestrator.MergeRefusalLaneHuman, "without a PR number: the issue's lane")

	// The guard bypass: the pipeline PR is merged into the code branch
	// first (GitHub fast-forwards it to the top commit). The code PR now
	// carries the pipeline change and is a human's to merge.
	laneGit(t, r.remote, "update-ref", "refs/heads/eng-1-fix-code", top)
	reason, match = r.orch.MergeLaneRefusalForPR(ctx, "ENG-1", 1, r.headOf("eng-1-fix-code"))
	assert.Contains(t, reason, orchestrator.MergeRefusalLaneHuman, "a pipeline change that reached the code branch is never auto")
	assert.Empty(t, match)
}

// The checks fail on the code alone and pass on the pipeline change alone:
// the pipeline PR goes first (after its approval) and the code is stacked
// on it, its own push needing no approval.
func TestStackSplitCodeFailsAlonePipelineFirst(t *testing.T) {
	r := splitRig(t, "test -f "+ciFile, ciFile)
	entry := r.waitApproval()
	assert.Equal(t, []string{ciFile}, entry.GuardrailLanePaths)
	assert.Empty(t, r.remoteRef("refs/heads/eng-1-fix-pipeline"), "held for its approval")
	assert.Empty(t, r.openedPRs())

	r.approveAndSettle()
	pipe := r.remoteRef("refs/heads/eng-1-fix-pipeline")
	top := r.remoteRef("refs/heads/eng-1-fix")
	require.NotEmpty(t, pipe)
	require.NotEmpty(t, top)
	assert.NotContains(t, r.remoteFiles(pipe), "pkg/fix.go", "the pipeline branch carries only the pipeline change")
	assert.Equal(t, pipe, laneGit(t, r.remote, "rev-parse", top+"^"), "the code is stacked on the pipeline change")
	assert.Equal(t, []string{"eng-1-fix-pipeline->", "eng-1-fix->eng-1-fix-pipeline"}, r.openedPRs())
	assert.Nil(t, r.orch.Snapshot().InputRequiredIssues["ENG-1"], "the code push needed no second approval")

	rec := r.split()
	assert.Equal(t, orchestrator.StackSplitPipelineFirst, rec.Order)
	assert.Contains(t, rec.Reason, "fail on the code change alone")
	reason, _ := r.orch.MergeLaneRefusalForPR(context.Background(), "ENG-1", 1, r.headOf("eng-1-fix-pipeline"))
	assert.Contains(t, reason, orchestrator.MergeRefusalLaneHuman, "the pipeline PR is a human's to merge")
}

// The checks fail on either side alone: no broken middle is offered — one
// combined PR, as without the split, and the reason is recorded.
func TestStackSplitBothOrdersFailFallsBackToOnePR(t *testing.T) {
	r := splitRig(t, "test -f "+ciFile+" && test -f pkg/fix.go", ciFile)
	entry := r.waitApproval()
	assert.Equal(t, []string{ciFile}, entry.GuardrailLanePaths)
	r.approveAndSettle()
	assert.NotEmpty(t, r.remoteRef("refs/heads/eng-1-fix"))
	assert.Empty(t, r.remoteRef("refs/heads/eng-1-fix-code"))
	assert.Empty(t, r.remoteRef("refs/heads/eng-1-fix-pipeline"))
	assert.Equal(t, "change", laneGit(t, r.remote, "log", "-1", "--format=%s", "refs/heads/eng-1-fix"), "the agent's own commit, unsplit")
	assert.Equal(t, []string{"eng-1-fix->"}, r.openedPRs())

	rec := r.split()
	assert.Equal(t, orchestrator.StackSplitCombined, rec.Order)
	assert.Contains(t, rec.Reason, "fail on the code change alone")
	assert.Contains(t, rec.Reason, "on the pipeline change alone")
	assert.Contains(t, rec.Reason, "hooks.after_create", "without a hook the reason says the checks may lack dependencies")
	feed := r.feedKinds(orchestrator.FeedStackSplit)
	require.Len(t, feed, 1)
	assert.Equal(t, orchestrator.StackSplitCombined, feed[0].Payload.Outcome)
}

// A lower branch that already exists on the remote at another commit is
// not Itervox's to push over: one combined PR, the reason recorded, and
// the existing branch left as it was.
func TestStackSplitExistingRemoteLowerBranchFallsBackToCombined(t *testing.T) {
	var existing string
	r := newPatchOutRig(t, patchOutOpts{
		file: "pkg/fix.go", extra: []string{ciFile}, split: true, human: []string{".github/**"}, completion: "In Review",
		verify: []config.VerifyCheck{{Name: "test", Run: "test -f pkg/fix.go", TimeoutMs: 60_000}},
		setup: func(ws, _ string) {
			laneGit(t, ws, "commit", "-q", "--allow-empty", "-m", "someone else's work")
			laneGit(t, ws, "push", "-q", "origin", "HEAD:refs/heads/eng-1-fix-code")
			existing = laneGit(t, ws, "rev-parse", "HEAD")
			laneGit(t, ws, "reset", "-q", "--hard", "HEAD^")
		},
	})
	entry := r.waitApproval()
	assert.ElementsMatch(t, []string{ciFile}, entry.GuardrailLanePaths, "the combined push waits for its approval")
	rec := r.split()
	assert.Equal(t, orchestrator.StackSplitCombined, rec.Order)
	assert.Contains(t, rec.Reason, "eng-1-fix-code already exists on the remote")
	r.approveAndSettle()
	assert.Equal(t, existing, r.remoteRef("refs/heads/eng-1-fix-code"), "the existing branch is untouched")
	assert.NotEmpty(t, r.remoteRef("refs/heads/eng-1-fix"))
	assert.Equal(t, []string{"eng-1-fix->"}, r.openedPRs())
}

// The checks in the split's temporary worktrees see what the workspace's
// hooks.after_create installs (an ignored file standing in for
// node_modules): without it the code alone would fail and the run would
// fall back to one PR.
func TestStackSplitRunsAfterCreateHookInTempWorktrees(t *testing.T) {
	r := newPatchOutRig(t, patchOutOpts{
		file: "pkg/fix.go", extra: []string{ciFile}, split: true, human: []string{".github/**"}, completion: "In Review",
		verify:      []config.VerifyCheck{{Name: "test", Run: "test -f .deps/installed && test -f pkg/fix.go", TimeoutMs: 60_000}},
		afterCreate: "mkdir -p .deps && touch .deps/installed",
		setup: func(ws, _ string) {
			require.NoError(t, os.WriteFile(filepath.Join(ws, ".gitignore"), []byte(".deps/\n"), 0o644))
			laneGit(t, ws, "add", ".gitignore")
			laneGit(t, ws, "commit", "-q", "-m", "ignore deps")
			laneGit(t, ws, "push", "-q", "origin", "HEAD:main")
			require.NoError(t, os.MkdirAll(filepath.Join(ws, ".deps"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(ws, ".deps", "installed"), nil, 0o644))
		},
	})
	r.waitApproval()
	rec := r.split()
	assert.Equal(t, orchestrator.StackSplitCodeFirst, rec.Order, "reason: %s", rec.Reason)
	assert.NotEmpty(t, r.remoteRef("refs/heads/eng-1-fix-code"))
}

// The top push is refused, and the agent changes the code again when the
// run resumes: the lower branch is fast-forwarded from the commit Itervox
// pushed (never pushed non-fast-forward, never split from scratch), the top
// is stacked on it, and both pull requests are the ones already open.
func TestStackSplitResumeFastForwardsLowerBranch(t *testing.T) {
	r := newPatchOutRig(t, patchOutOpts{
		file: "pkg/fix.go", extra: []string{ciFile}, split: true, human: []string{".github/**"}, completion: "In Review", rerun: true,
		verify: []config.VerifyCheck{{Name: "test", Run: "test -f pkg/fix.go", TimeoutMs: 60_000}},
	})
	first := r.waitApproval()
	code1 := r.remoteRef("refs/heads/eng-1-fix-code")
	require.NotEmpty(t, code1)
	assert.Equal(t, code1, r.split().LowerSHA)
	require.NoError(t, r.orch.ProvideInput("ENG-1", "no"))

	var held *orchestrator.InputRequiredEntry
	require.Eventually(t, func() bool {
		held = r.orch.Snapshot().InputRequiredIssues["ENG-1"]
		return held != nil && held.GuardrailApprovalID == ""
	}, 30*time.Second, 20*time.Millisecond, "the refused top push never held the run")
	require.NoError(t, r.orch.ProvideInput("ENG-1", "try again"))

	var second *orchestrator.InputRequiredEntry
	require.Eventually(t, func() bool {
		second = r.orch.Snapshot().InputRequiredIssues["ENG-1"]
		return second != nil && second.GuardrailApprovalID != "" && second.GuardrailApprovalID != first.GuardrailApprovalID
	}, 60*time.Second, 20*time.Millisecond, "the resumed run never reached the top push (the lower branch was not updated)")
	code2 := r.remoteRef("refs/heads/eng-1-fix-code")
	require.NotEqual(t, code1, code2, "the new code change reached the lower branch")
	assert.Equal(t, code1, laneGit(t, r.remote, "rev-parse", code2+"^"), "a fast-forward from the commit Itervox pushed")
	assert.Equal(t, "change 2", strings.TrimSpace(laneGit(t, r.remote, "show", code2+":pkg/fix.go")))
	assert.NotContains(t, r.remoteFiles(code2), ciFile)
	rec := r.split()
	assert.Equal(t, orchestrator.StackSplitCodeFirst, rec.Order)
	assert.Equal(t, code2, rec.LowerSHA)
	assert.Equal(t, 1, rec.LowerPR)
	assert.Contains(t, rec.Reason, "fast-forwarded")

	r.approveAndSettle()
	top := r.remoteRef("refs/heads/eng-1-fix")
	require.NotEmpty(t, top)
	assert.Equal(t, code2, laneGit(t, r.remote, "rev-parse", top+"^"))
	assert.Equal(t, []string{"eng-1-fix-code->", "eng-1-fix->eng-1-fix-code"}, r.openedPRs(), "no pull request is opened twice")
}
