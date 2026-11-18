package orchestrator

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/guardrails"
)

// laneState is a real State with ENG-1's worker run (run1, claude) live.
func laneState(t *testing.T) State {
	t.Helper()
	state := NewState(&config.Config{})
	state.Running["id1"] = &RunEntry{Issue: domain.Issue{ID: "id1", Identifier: "ENG-1"}, SessionID: "run1", Backend: "claude"}
	return state
}

func laneEvent(lane string, paths ...string) OrchestratorEvent {
	return OrchestratorEvent{Type: EventGuardrailLane, Identifier: "ENG-1",
		guardrailLane: &guardrailLaneEvent{RunID: "run1", Report: guardrails.LaneReport{Lane: lane, Paths: paths, PathsTotal: len(paths)}}}
}

// guardrails.lanes: after a second-opinion push, merge_pr needs an
// approving verdict from a reviewer on a backend the writer did not use,
// recorded after the last such push. A same-backend approval, a block, or
// an approval that predates the push does not count. Real State values,
// through the event-loop handlers.
func TestSecondOpinionNeedsDifferentBackendReviewer(t *testing.T) {
	o := &Orchestrator{}
	state := laneState(t)
	pushedAt := time.Now()
	o.handleGuardrailLane(&state, laneEvent(guardrails.LaneSecondOpinion, "auth/token.go"), pushedAt)
	rec := state.PushLanes["ENG-1"]
	require.Equal(t, guardrails.LaneSecondOpinion, rec.Lane)
	assert.Equal(t, []string{"claude"}, rec.WriterBackends)
	assert.Equal(t, []string{"auth/token.go"}, rec.Paths)

	refused := func() string { return mergeLaneRefusal(state, "ENG-1", 0) }
	assert.True(t, strings.HasPrefix(refused(), MergeRefusalLaneSecondOpinion+":"), "no review yet: %s", refused())
	assert.Contains(t, refused(), "other than the writer's (claude)")

	verdict := func(profile, backend, v string, at time.Time) {
		state.ReviewVerdicts["ENG-1"] = UpsertReviewVerdict(state.ReviewVerdicts["ENG-1"],
			ReviewVerdict{Profile: profile, Verdict: v, RecordedAt: at})
		stampVerdictBackend(&state, "ENG-1", profile, backend)
	}
	verdict("claude-reviewer", "claude", ReviewVerdictApprove, pushedAt.Add(time.Minute))
	assert.NotEmpty(t, refused(), "a reviewer on the writer's backend is not a second opinion")
	verdict("codex-reviewer", "codex", ReviewVerdictBlock, pushedAt.Add(time.Minute))
	assert.NotEmpty(t, refused(), "a block is not an approval")
	verdict("codex-reviewer", "codex", ReviewVerdictApprove, pushedAt.Add(-time.Minute))
	assert.NotEmpty(t, refused(), "an approval of the code before the push does not count")
	verdict("codex-reviewer", "codex", ReviewVerdictApprove, pushedAt.Add(time.Minute))
	assert.Empty(t, refused(), "an approval from another backend after the push lets the merge through")
	assert.Equal(t, "codex", state.ReviewVerdicts["ENG-1"][1].Backend, "the loop stamps the reviewer's backend")

	// A later push from a codex run makes codex a writer too: the codex
	// approval no longer counts, and it predates the new push anyway.
	state.Running["id1"].Backend = "codex"
	o.handleGuardrailLane(&state, laneEvent(guardrails.LaneSecondOpinion, "auth/session.go"), pushedAt.Add(2*time.Minute))
	assert.Equal(t, []string{"claude", "codex"}, state.PushLanes["ENG-1"].WriterBackends)
	assert.Equal(t, []string{"auth/token.go", "auth/session.go"}, state.PushLanes["ENG-1"].Paths)
	assert.NotEmpty(t, refused())

	// A human-lane push raises the record to human: never auto-merged,
	// whatever the reviews say.
	o.handleGuardrailLane(&state, laneEvent(guardrails.LaneHuman, ".github/workflows/ci.yml"), pushedAt.Add(3*time.Minute))
	verdict("gemini-reviewer", "gemini", ReviewVerdictApprove, pushedAt.Add(time.Hour))
	assert.True(t, strings.HasPrefix(refused(), MergeRefusalLaneHuman+":"), refused())
	assert.Equal(t, []string{".github/workflows/ci.yml"}, state.PushLanes["ENG-1"].Paths, "the higher lane's paths replace the lower's")
	// ...and a later second-opinion push does not lower it.
	o.handleGuardrailLane(&state, laneEvent(guardrails.LaneSecondOpinion, "auth/x.go"), pushedAt.Add(4*time.Minute))
	assert.Equal(t, guardrails.LaneHuman, state.PushLanes["ENG-1"].Lane)

	assert.Empty(t, mergeLaneRefusal(state, "ENG-2", 0), "an issue whose pushes stayed in the auto lane merges as before")
}

// PushLanes persists across a restart (so a restart never forgets that a
// pull request is a human's to merge), is deep-copied by Clone, and is
// pruned once the issue is terminal; a tmux session's report is ignored.
func TestPushLanesPersistCloneAndPrune(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".itervox", "push_lanes.json")
	o := &Orchestrator{}
	o.SetPushLanesFile(path)
	state := laneState(t)
	o.handleGuardrailLane(&state, laneEvent(guardrails.LaneHuman, ".github/workflows/ci.yml"), time.Now())

	cp := state.Clone()
	cp.PushLanes["ENG-1"].Paths[0] = "mutated"
	assert.Equal(t, ".github/workflows/ci.yml", state.PushLanes["ENG-1"].Paths[0])

	loaded := o.loadPushLanesFromDisk(NewState(&config.Config{}))
	assert.Equal(t, guardrails.LaneHuman, loaded.PushLanes["ENG-1"].Lane)
	assert.Equal(t, []string{".github/workflows/ci.yml"}, loaded.PushLanes["ENG-1"].Paths)

	o.prunePushLanes(&state, func(ident string) bool { return ident != "ENG-1" })
	assert.Empty(t, state.PushLanes)
	assert.Empty(t, o.loadPushLanesFromDisk(NewState(&config.Config{})).PushLanes, "the prune is persisted")

	state.TmuxSessions = map[string]*TmuxSession{"itervox-a": {}}
	o.handleGuardrailLane(&state, OrchestratorEvent{Type: EventGuardrailLane, Identifier: "itervox-a",
		guardrailLane: &guardrailLaneEvent{RunID: "itervox-a", Report: guardrails.LaneReport{Lane: guardrails.LaneSecondOpinion}}}, time.Now())
	assert.Empty(t, state.PushLanes, "a tmux session has no pull request to merge")
}
