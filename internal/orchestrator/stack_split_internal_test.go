package orchestrator

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/guardrails"
)

// mergeLaneRefusal is why merge_pr must not merge identifier's pull request
// number pr (0: unknown), whose head is headSHA ("" when unknown): what
// MergeLaneRefusalForPR decides from the loop's answer. Pure over State.
func mergeLaneRefusal(state State, identifier string, pr int, headSHA string) string {
	reason, _ := mergeLaneDecision(state, identifier, pr).refusalFor(headSHA)
	return reason
}

func stackSplitEvent(rec StackSplitRecord) OrchestratorEvent {
	return OrchestratorEvent{Type: EventStackSplit, Identifier: "ENG-1", stackSplit: &rec}
}

// guardrails.lanes.split, per pull request: after a code-first split the
// issue's lane is human (the pipeline push), yet merge_pr may merge the
// lower, code-only pull request by its own lane while its head is the
// commit the split pushed; the pipeline pull request and a request without
// a number keep the issue's lane. A pipeline-first split grants nothing.
// Real State values, through the loop handlers.
func TestStackSplitMergeLaneRefusalPerPR(t *testing.T) {
	const split = "1111111111111111111111111111111111111111"
	o := &Orchestrator{}
	state := laneState(t)
	o.handleGuardrailLane(&state, laneEvent(guardrails.LaneHuman, ".github/workflows/ci.yml"), time.Now())
	rec := StackSplitRecord{RunID: "run1", Order: StackSplitCodeFirst, CodeLane: guardrails.LaneAuto,
		CodeFiles: []string{"pkg/fix.go"}, CodeFilesTotal: 1, PipelineFiles: []string{".github/workflows/ci.yml"}, PipelineFilesTotal: 1,
		LowerBranch: "eng-1-fix-code", UpperBranch: "eng-1-fix", Reason: "the signed checks pass on the code change alone", LowerSHA: split}
	o.handleStackSplit(&state, stackSplitEvent(rec), time.Now())
	assert.Contains(t, mergeLaneRefusal(state, "ENG-1", 7, split), MergeRefusalLaneHuman, "before the lower PR's number is known: the issue's lane")

	rec.LowerPR, rec.LowerPRURL = 7, "https://example.invalid/o/r/pull/7"
	o.handleStackSplit(&state, stackSplitEvent(rec), time.Now())
	assert.Empty(t, mergeLaneRefusal(state, "ENG-1", 7, split), "the code PR's own lane is auto")
	assert.Contains(t, mergeLaneRefusal(state, "ENG-1", 8, split), MergeRefusalLaneHuman, "the pipeline PR")
	assert.Contains(t, mergeLaneRefusal(state, "ENG-1", 0, split), MergeRefusalLaneHuman, "no PR number")

	feed := 0
	for _, ev := range state.Feed {
		if ev.Kind == FeedStackSplit {
			feed++
			assert.Equal(t, StackSplitCodeFirst, ev.Payload.Outcome)
			assert.Equal(t, 1, ev.Payload.Count)
		}
	}
	assert.Equal(t, 1, feed, "an update of the same decision emits no second feed event")

	// The code side reached second_opinion: its PR needs a reviewer on
	// another backend, like any second-opinion PR.
	rec.CodeLane = guardrails.LaneSecondOpinion
	o.handleStackSplit(&state, stackSplitEvent(rec), time.Now())
	assert.True(t, strings.HasPrefix(mergeLaneRefusal(state, "ENG-1", 7, split), MergeRefusalLaneSecondOpinion+":"))
	state.ReviewVerdicts["ENG-1"] = UpsertReviewVerdict(state.ReviewVerdicts["ENG-1"],
		ReviewVerdict{Profile: "codex-reviewer", Verdict: ReviewVerdictApprove, RecordedAt: time.Now().Add(time.Minute)})
	stampVerdictBackend(&state, "ENG-1", "codex-reviewer", "codex")
	assert.Empty(t, mergeLaneRefusal(state, "ENG-1", 7, split))

	rec.Order, rec.CodeLane = StackSplitPipelineFirst, guardrails.LaneAuto
	o.handleStackSplit(&state, stackSplitEvent(rec), time.Now())
	assert.Contains(t, mergeLaneRefusal(state, "ENG-1", 7, split), MergeRefusalLaneHuman, "a pipeline-first lower PR is the pipeline change")
}

// The guard bypass: the lower, code-only pull request's own (auto) lane
// holds only for the commit the split pushed. Once anything else reached
// its branch (the top PR merged into it, a later push), it is judged by
// the issue's lane; with no issue record at all it still fails closed, and
// a split record without a pushed commit (an older record) grants nothing.
func TestStackSplitLowerPRLaneOnlyAtSplitCommit(t *testing.T) {
	const split, moved = "1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222"
	o := &Orchestrator{}
	state := laneState(t)
	rec := StackSplitRecord{RunID: "run1", Order: StackSplitCodeFirst, CodeLane: guardrails.LaneAuto,
		CodeFiles: []string{"pkg/fix.go"}, CodeFilesTotal: 1, PipelineFiles: []string{".github/workflows/ci.yml"}, PipelineFilesTotal: 1,
		LowerBranch: "eng-1-fix-code", UpperBranch: "eng-1-fix", LowerPR: 7, LowerSHA: split}
	o.handleStackSplit(&state, stackSplitEvent(rec), time.Now())

	assert.Empty(t, mergeLaneRefusal(state, "ENG-1", 7, split), "at the split's commit: its own lane")
	assert.Contains(t, mergeLaneRefusal(state, "ENG-1", 7, moved), MergeRefusalLaneHuman,
		"moved, and no issue record yet: fails closed on the split's pipeline files")
	assert.Contains(t, mergeLaneRefusal(state, "ENG-1", 7, moved), ".github/workflows/ci.yml")
	assert.Contains(t, mergeLaneRefusal(state, "ENG-1", 7, ""), MergeRefusalLaneHuman, "an unknown head is not the split's commit")

	o.handleGuardrailLane(&state, laneEvent(guardrails.LaneHuman, ".github/deploy.yml"), time.Now())
	assert.Contains(t, mergeLaneRefusal(state, "ENG-1", 7, moved), ".github/deploy.yml", "moved: the issue's lane")
	assert.Empty(t, mergeLaneRefusal(state, "ENG-1", 7, split))

	rec.LowerSHA = ""
	o.handleStackSplit(&state, stackSplitEvent(rec), time.Now())
	assert.Contains(t, mergeLaneRefusal(state, "ENG-1", 7, split), MergeRefusalLaneHuman, "no pushed commit recorded: no grant")
}

// MergeLaneRefusalForPR asks for the pull request's head only for a split's
// lower pull request, judges it by its own lane only at the split's commit,
// and then names that commit for the merge to insist on.
func TestMergeLaneRefusalForPRReadsHeadOnlyForLowerPR(t *testing.T) {
	const split = "1111111111111111111111111111111111111111"
	cfg := &config.Config{}
	o := &Orchestrator{cfg: cfg, events: make(chan OrchestratorEvent, 4)}
	state := laneState(t)
	o.handleGuardrailLane(&state, laneEvent(guardrails.LaneHuman, ".github/workflows/ci.yml"), time.Now())
	o.handleStackSplit(&state, stackSplitEvent(StackSplitRecord{RunID: "run1", Order: StackSplitCodeFirst, CodeLane: guardrails.LaneAuto,
		PipelineFiles: []string{".github/workflows/ci.yml"}, PipelineFilesTotal: 1, LowerPR: 7, LowerSHA: split}), time.Now())
	done := make(chan struct{})
	o.loopExited.Store(&done)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case ev := <-o.events:
				o.handleMergeLaneCheck(state, ev)
			case <-stop:
				return
			}
		}
	}()
	ctx := context.Background()
	asked := 0
	head := func(h string, err error) func(context.Context) (string, error) {
		return func(context.Context) (string, error) { asked++; return h, err }
	}

	reason, match := o.MergeLaneRefusalForPR(ctx, "ENG-1", 8, head(split, nil))
	assert.Contains(t, reason, MergeRefusalLaneHuman)
	assert.Empty(t, match)
	assert.Zero(t, asked, "not the lower PR: no head lookup")

	reason, match = o.MergeLaneRefusalForPR(ctx, "ENG-1", 7, head(split, nil))
	assert.Empty(t, reason)
	assert.Equal(t, split, match, "the merge must insist on the judged commit")
	assert.Equal(t, 1, asked)

	reason, match = o.MergeLaneRefusalForPR(ctx, "ENG-1", 7, head("3333333333333333333333333333333333333333", nil))
	assert.Contains(t, reason, MergeRefusalLaneHuman)
	assert.Empty(t, match)

	reason, _ = o.MergeLaneRefusalForPR(ctx, "ENG-1", 7, head("", errors.New("gh failed")))
	assert.Contains(t, reason, MergeRefusalLaneHuman, "an unreadable head fails closed")
	reason, _ = o.MergeLaneRefusalForPR(ctx, "ENG-1", 7, nil)
	assert.Contains(t, reason, MergeRefusalLaneHuman, "no way to read the head fails closed")
}

// A later run never continues a stack split's lower branch as the issue's
// working pull request.
func TestIsStackSplitLowerBranch(t *testing.T) {
	assert.True(t, isStackSplitLowerBranch("eng-1-fix-code", "eng-1-fix"))
	assert.True(t, isStackSplitLowerBranch("eng-1-fix-pipeline", "eng-1-fix"))
	assert.False(t, isStackSplitLowerBranch("eng-1-fix", "eng-1-fix"))
	assert.False(t, isStackSplitLowerBranch("eng-1-fix-codex", "eng-1-fix"))
	assert.False(t, isStackSplitLowerBranch("-code", ""))
}

// State.StackSplits persists across a restart (the per-PR merge policy
// survives it), is deep-copied by Clone, and is pruned once the issue is
// terminal.
func TestStackSplitsPersistCloneAndPrune(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".itervox", "stack_splits.json")
	o := &Orchestrator{}
	o.SetStackSplitsFile(path)
	state := laneState(t)
	o.handleStackSplit(&state, stackSplitEvent(StackSplitRecord{RunID: "run1", Order: StackSplitCodeFirst,
		CodeFiles: []string{"pkg/fix.go"}, PipelineFiles: []string{".github/workflows/ci.yml"}, LowerPR: 3}), time.Now())

	cp := state.Clone()
	cp.StackSplits["ENG-1"].CodeFiles[0] = "mutated"
	assert.Equal(t, "pkg/fix.go", state.StackSplits["ENG-1"].CodeFiles[0])

	loaded := o.loadStackSplitsFromDisk(NewState(&config.Config{}))
	require.Contains(t, loaded.StackSplits, "ENG-1")
	assert.Equal(t, 3, loaded.StackSplits["ENG-1"].LowerPR)
	assert.Equal(t, []string{".github/workflows/ci.yml"}, loaded.StackSplits["ENG-1"].PipelineFiles)

	o.pruneStackSplits(&state, func(ident string) bool { return ident != "ENG-1" })
	assert.Empty(t, state.StackSplits)
	assert.Empty(t, o.loadStackSplitsFromDisk(NewState(&config.Config{})).StackSplits, "the prune is persisted")
}

// The split happens only where Itervox pushes: a run whose agent pushes
// itself (push: agent) is never split, whatever guardrails.lanes.split says.
func TestStackSplitIgnoredWithoutDaemonPush(t *testing.T) {
	cfg := &config.Config{}
	cfg.Guardrails.Enabled = true
	cfg.Guardrails.Lanes.Split = true
	cfg.Guardrails.Lanes.Human = []string{".github/**"}
	o := &Orchestrator{cfg: cfg}
	assert.False(t, o.stackSplitApplies(false), "an agent's own push is never split")
	assert.True(t, o.stackSplitApplies(true))
	assert.Contains(t, config.StackSplitIgnoredReason(cfg), "push: daemon", "the startup warning names the cause")

	cfg.Agent.Push = config.PushDaemon
	assert.Empty(t, config.StackSplitIgnoredReason(cfg))
	cfg.Guardrails.Lanes.Split = false
	assert.False(t, o.stackSplitApplies(true), "off by default")
}
