package orchestrator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/vnovick/itervox/internal/tracker"
)

// TestMeasureTrackerReplyRequestReduction is issue #62's "measured, not
// asserted" acceptance for the reply-check path.
//
// It runs the SAME path twice over the same backlog — once against a tracker
// that implements DetailBatcher and once against one that does not — and
// reports both request counts. The non-batching run is the honest "before":
// it is the identical code path taking its fallback branch, not a
// reconstruction of the old code.
//
// Representative backlog: the reply check is bounded by
// trackerReplyCheckPerTickBudget (5) per tick, so one tick reads at most 5
// entries no matter how large the backlog is. The reduction per tick is
// therefore budget → 1, and the interesting number is what a full sweep of a
// realistic backlog costs.
func TestMeasureTrackerReplyRequestReduction(t *testing.T) {
	const backlog = 40 // representative input-required backlog

	issues := issuesWithQuestion(backlog)

	// Batched.
	batchTr := tracker.NewMemoryTracker(issues, []string{"In Progress"}, []string{"Done"})
	batchOrch := &Orchestrator{tracker: batchTr, events: make(chan OrchestratorEvent, 256)}
	batchState := inputRequiredState(issues)

	// Not batched — same path, fallback branch.
	plainInner := tracker.NewMemoryTracker(issues, []string{"In Progress"}, []string{"Done"})
	plainTr := &nonBatchingTracker{Tracker: plainInner}
	plainOrch := &Orchestrator{tracker: plainTr, events: make(chan OrchestratorEvent, 256)}
	plainState := inputRequiredState(issues)

	// A full sweep: enough ticks for the budget to reach every entry.
	ticks := backlog / trackerReplyCheckPerTickBudget
	for i := 0; i < ticks; i++ {
		batchState = batchOrch.checkTrackerReplies(context.Background(), batchState)
		plainState = plainOrch.checkTrackerReplies(context.Background(), plainState)
	}

	batched := batchTr.DetailBatchCalls() + batchTr.DetailCalls()
	perIssue := plainTr.detailCalls

	t.Logf("backlog=%d ticks=%d budget=%d", backlog, ticks, trackerReplyCheckPerTickBudget)
	t.Logf("  per-issue path: %d tracker requests", perIssue)
	t.Logf("  batched path:   %d tracker requests", batched)
	t.Logf("  reduction:      %.1fx (%d fewer requests)",
		float64(perIssue)/float64(batched), perIssue-batched)

	// The measurement is the point, but pin the shape so a regression that
	// quietly restores per-issue reads fails here too.
	assert.Equal(t, ticks, batched,
		"a full sweep must cost one request per tick, not one per entry")
	assert.Equal(t, backlog, perIssue,
		"the fallback must still read every entry — otherwise the comparison is not like-for-like")
	assert.Less(t, batched, perIssue/4,
		"batching must be a large reduction, not a marginal one")
}

// TestMeasureBatchScalesWithBudgetNotBacklog pins the property that makes the
// reduction hold as a backlog grows: one tick costs ONE request whether the
// budget covers 5 entries or 50. Per-issue cost scales with the budget; batched
// cost does not.
func TestMeasureBatchScalesWithBudgetNotBacklog(t *testing.T) {
	for _, backlog := range []int{5, 20, 100} {
		issues := issuesWithQuestion(backlog)
		tr := tracker.NewMemoryTracker(issues, []string{"In Progress"}, []string{"Done"})
		o := &Orchestrator{tracker: tr, events: make(chan OrchestratorEvent, 512)}

		o.checkTrackerReplies(context.Background(), inputRequiredState(issues))

		assert.Equal(t, 1, tr.DetailBatchCalls(),
			"backlog=%d: one tick must cost exactly one request regardless of backlog size", backlog)
		assert.Zero(t, tr.DetailCalls(), "backlog=%d: no per-issue fallback", backlog)
	}
}
