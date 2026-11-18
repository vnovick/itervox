package orchestrator

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/guardrails"
)

func stackSplitEvent(rec StackSplitRecord) OrchestratorEvent {
	return OrchestratorEvent{Type: EventStackSplit, Identifier: "ENG-1", stackSplit: &rec}
}

// guardrails.lanes.split, per pull request: after a code-first split the
// issue's lane is human (the pipeline push), yet merge_pr may merge the
// lower, code-only pull request by its own lane; the pipeline pull request
// and a request without a number keep the issue's lane. A pipeline-first
// split grants nothing. Real State values, through the loop handlers.
func TestStackSplitMergeLaneRefusalPerPR(t *testing.T) {
	o := &Orchestrator{}
	state := laneState(t)
	o.handleGuardrailLane(&state, laneEvent(guardrails.LaneHuman, ".github/workflows/ci.yml"), time.Now())
	rec := StackSplitRecord{RunID: "run1", Order: StackSplitCodeFirst, CodeLane: guardrails.LaneAuto,
		CodeFiles: []string{"pkg/fix.go"}, CodeFilesTotal: 1, PipelineFiles: []string{".github/workflows/ci.yml"}, PipelineFilesTotal: 1,
		LowerBranch: "eng-1-fix-code", UpperBranch: "eng-1-fix", Reason: "the signed checks pass on the code change alone"}
	o.handleStackSplit(&state, stackSplitEvent(rec), time.Now())
	assert.Contains(t, mergeLaneRefusal(state, "ENG-1", 7), MergeRefusalLaneHuman, "before the lower PR's number is known: the issue's lane")

	rec.LowerPR, rec.LowerPRURL = 7, "https://example.invalid/o/r/pull/7"
	o.handleStackSplit(&state, stackSplitEvent(rec), time.Now())
	assert.Empty(t, mergeLaneRefusal(state, "ENG-1", 7), "the code PR's own lane is auto")
	assert.Contains(t, mergeLaneRefusal(state, "ENG-1", 8), MergeRefusalLaneHuman, "the pipeline PR")
	assert.Contains(t, mergeLaneRefusal(state, "ENG-1", 0), MergeRefusalLaneHuman, "no PR number")

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
	assert.True(t, strings.HasPrefix(mergeLaneRefusal(state, "ENG-1", 7), MergeRefusalLaneSecondOpinion+":"))
	state.ReviewVerdicts["ENG-1"] = UpsertReviewVerdict(state.ReviewVerdicts["ENG-1"],
		ReviewVerdict{Profile: "codex-reviewer", Verdict: ReviewVerdictApprove, RecordedAt: time.Now().Add(time.Minute)})
	stampVerdictBackend(&state, "ENG-1", "codex-reviewer", "codex")
	assert.Empty(t, mergeLaneRefusal(state, "ENG-1", 7))

	rec.Order, rec.CodeLane = StackSplitPipelineFirst, guardrails.LaneAuto
	o.handleStackSplit(&state, stackSplitEvent(rec), time.Now())
	assert.Contains(t, mergeLaneRefusal(state, "ENG-1", 7), MergeRefusalLaneHuman, "a pipeline-first lower PR is the pipeline change")
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
