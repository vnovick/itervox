package orchestrator_test

import (
	"context"
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
	assert.Empty(t, r.orch.MergeLaneRefusalForPR(ctx, "ENG-1", 1), "the code PR merges by its own lane")
	assert.Contains(t, r.orch.MergeLaneRefusalForPR(ctx, "ENG-1", 2), orchestrator.MergeRefusalLaneHuman)
	assert.Contains(t, r.orch.MergeLaneRefusal(ctx, "ENG-1"), orchestrator.MergeRefusalLaneHuman, "without a PR number: the issue's lane")
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
	assert.Contains(t, r.orch.MergeLaneRefusalForPR(context.Background(), "ENG-1", 1), orchestrator.MergeRefusalLaneHuman,
		"the pipeline PR is a human's to merge")
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
	feed := r.feedKinds(orchestrator.FeedStackSplit)
	require.Len(t, feed, 1)
	assert.Equal(t, orchestrator.StackSplitCombined, feed[0].Payload.Outcome)
}
