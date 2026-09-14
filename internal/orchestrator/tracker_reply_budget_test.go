package orchestrator

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSelectTrackerReplyCheckBatch pins the per-tick request budget on the
// input-required reply check.
//
// The loop used to range over every InputRequiredIssues entry and spend one
// FetchIssueDetail on each, every tick, with no budget, no ordering and no
// TTL — so its request rate scaled with the number of stuck issues. At the
// default 30s poll that is 120 ticks/hour, and issue #42 reported a backlog
// of 19 such issues: ~2,280 requests/hour from this loop alone, against
// Linear's documented 2,500/hour ceiling, before any real work.
//
// A fixed budget makes the cost constant in the backlog size, and ordering
// by LastReplyCheckAt keeps it fair: every entry is still reached within
// ceil(N/budget) ticks instead of some entries starving under Go's
// randomized map iteration.
func TestSelectTrackerReplyCheckBatch(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	entries := map[string]*InputRequiredEntry{
		"ENG-1": {Identifier: "ENG-1", LastReplyCheckAt: now.Add(-1 * time.Minute)},
		"ENG-2": {Identifier: "ENG-2"}, // never checked — must go first
		"ENG-3": {Identifier: "ENG-3", LastReplyCheckAt: now.Add(-10 * time.Minute)},
		"ENG-4": {Identifier: "ENG-4", LastReplyCheckAt: now.Add(-5 * time.Minute)},
	}

	batch := selectTrackerReplyCheckBatch(entries, 2)
	require.Len(t, batch, 2, "the budget is a hard cap on requests per tick")
	assert.Equal(t, []string{"ENG-2", "ENG-3"}, batch,
		"never-checked first, then least-recently-checked")

	all := selectTrackerReplyCheckBatch(entries, 10)
	assert.Equal(t, []string{"ENG-2", "ENG-3", "ENG-4", "ENG-1"}, all,
		"a budget above the entry count returns every entry, still ordered")
}

// TestSelectTrackerReplyCheckBatchIsDeterministic guards against Go's
// randomized map iteration leaking into dispatch behaviour: two entries with
// identical timestamps must always come back in the same order.
func TestSelectTrackerReplyCheckBatchIsDeterministic(t *testing.T) {
	same := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	entries := map[string]*InputRequiredEntry{
		"ENG-3": {Identifier: "ENG-3", LastReplyCheckAt: same},
		"ENG-1": {Identifier: "ENG-1", LastReplyCheckAt: same},
		"ENG-2": {Identifier: "ENG-2", LastReplyCheckAt: same},
	}
	for range 20 {
		assert.Equal(t, []string{"ENG-1", "ENG-2", "ENG-3"},
			selectTrackerReplyCheckBatch(entries, 3))
	}
}

// TestSelectTrackerReplyCheckBatchNilEntriesSkipped keeps a nil map value
// (possible after a concurrent delete on an earlier tick) from panicking the
// event loop.
func TestSelectTrackerReplyCheckBatchNilEntriesSkipped(t *testing.T) {
	entries := map[string]*InputRequiredEntry{"ENG-1": nil, "ENG-2": {Identifier: "ENG-2"}}
	assert.Equal(t, []string{"ENG-2"}, selectTrackerReplyCheckBatch(entries, 5))
}

// TestSnapshotDoesNotShareInputRequiredEntries pins that a snapshot handed
// to HTTP handler goroutines shares no mutable state with the event loop.
//
// State.InputRequiredIssues is a map of POINTERS, and Snapshot used to
// duplicate it with maps.Clone — a shallow copy that hands out the very
// entries the loop owns. That was harmless while the loop only inserted and
// deleted whole entries, and became a data race as soon as
// checkTrackerReplies began stamping LastReplyCheckAt in place on the entry
// it selected. `go test -race` reproduced it against the clone.
//
// Asserting on pointer identity rather than racing goroutines makes the
// guarantee deterministic: no amount of scheduling luck can hide a shared
// pointer.
func TestSnapshotDoesNotShareInputRequiredEntries(t *testing.T) {
	o := &Orchestrator{}
	entry := &InputRequiredEntry{Identifier: "ENG-1"}
	resume := &PendingInputResumeEntry{Identifier: "ENG-2"}
	o.lastSnap = State{
		InputRequiredIssues: map[string]*InputRequiredEntry{"ENG-1": entry},
		PendingInputResumes: map[string]*PendingInputResumeEntry{"ENG-2": resume},
	}

	snap := o.Snapshot()

	require.NotNil(t, snap.InputRequiredIssues["ENG-1"])
	assert.NotSame(t, entry, snap.InputRequiredIssues["ENG-1"],
		"a snapshot must not share the entry the event loop stamps in place")
	require.NotNil(t, snap.PendingInputResumes["ENG-2"])
	assert.NotSame(t, resume, snap.PendingInputResumes["ENG-2"],
		"same pointer-map shape, same snapshot path — copy it too")

	// The event loop's stamp must be invisible to the already-taken snapshot.
	stamped := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	entry.LastReplyCheckAt = stamped
	assert.True(t, snap.InputRequiredIssues["ENG-1"].LastReplyCheckAt.IsZero(),
		"mutating the live entry must not reach a snapshot already handed out")
}

// TestLastReplyCheckAtSurvivesDiskRoundTrip pins that the reply-check
// fairness ordering is durable.
//
// A config reload re-enters run(), which builds a fresh Orchestrator and
// reloads the input-required queue from disk. LastReplyCheckAt was not in
// the on-disk shape, so every reload zeroed it for every entry and
// selectTrackerReplyCheckBatch fell through to its identifier tie-break —
// restarting at the alphabetically-first entries. With a backlog larger than
// the budget, anything sorting later was never checked at all, which is the
// starvation the ordering exists to prevent. Operators iterating on
// WORKFLOW.md reload often.
func TestLastReplyCheckAtSurvivesDiskRoundTrip(t *testing.T) {
	dir := t.TempDir()
	o := &Orchestrator{inputRequiredFile: filepath.Join(dir, "input_required.json")}

	checked := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	o.saveInputRequiredToDisk(
		map[string]*InputRequiredEntry{
			"ENG-1": {IssueID: "id1", Identifier: "ENG-1", QueuedAt: checked, LastReplyCheckAt: checked},
			"ENG-2": {IssueID: "id2", Identifier: "ENG-2", QueuedAt: checked}, // never checked
		},
		map[string]*PendingInputResumeEntry{},
	)

	restored := o.loadInputRequiredFromDisk(State{
		InputRequiredIssues:   map[string]*InputRequiredEntry{},
		PendingInputResumes:   map[string]*PendingInputResumeEntry{},
		PrevActiveIdentifiers: map[string]struct{}{},
	})

	require.Contains(t, restored.InputRequiredIssues, "ENG-1")
	assert.True(t, restored.InputRequiredIssues["ENG-1"].LastReplyCheckAt.Equal(checked),
		"a checked entry must not look never-checked after a reload")
	require.Contains(t, restored.InputRequiredIssues, "ENG-2")
	assert.True(t, restored.InputRequiredIssues["ENG-2"].LastReplyCheckAt.IsZero(),
		"a never-checked entry round-trips as never-checked, so it still sorts first")

	// The ordering the persistence exists to preserve.
	assert.Equal(t, []string{"ENG-2", "ENG-1"},
		selectTrackerReplyCheckBatch(restored.InputRequiredIssues, 2))
}
