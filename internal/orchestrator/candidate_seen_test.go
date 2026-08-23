package orchestrator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// TestCandidateSeenRowsSortedAndDefensive proves candidateSeenRows sorts by
// Identifier, dereferences a present UpdatedAt, zero-values a nil UpdatedAt,
// and skips a row with no identifier at all.
func TestCandidateSeenRowsSortedAndDefensive(t *testing.T) {
	updated := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	issues := []domain.Issue{
		{Identifier: "ENG-9", State: "Todo"},
		{Identifier: "ENG-1", State: "Todo", UpdatedAt: &updated},
		{Identifier: "", State: "Todo"},
	}

	rows := candidateSeenRows(issues)

	require.Equal(t, []CandidateSeenRow{
		{Identifier: "ENG-1", UpdatedAt: updated},
		{Identifier: "ENG-9"},
	}, rows)
}

// TestEventLoopPopulatesCandidateSeen drives a real orchestrator + real State
// through one onTick, and asserts the published snapshot carries a
// CandidateSeen row per candidate issue. Never mocks State (repo rule) — uses
// tracker.NewMemoryTracker and NewState, mirroring
// TestEventLoopPopulatesInferredDeps (inferred_deps_test.go). This is also
// the fix round's required integration coverage: it proves CandidateSeen
// reaches the snapshot through the real event loop, not just through a pure
// unit test of candidateSeenRows.
func TestEventLoopPopulatesCandidateSeen(t *testing.T) {
	cfg := dependencyAuditConfig()

	updated := time.Date(2026, 8, 4, 11, 0, 0, 0, time.UTC)
	issueA := domain.Issue{ID: "issue-1", Identifier: "ENG-1", State: "Todo", UpdatedAt: &updated}
	issueB := domain.Issue{ID: "issue-2", Identifier: "ENG-2", State: "Todo"}
	mt := tracker.NewMemoryTracker([]domain.Issue{issueA, issueB}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)

	o := New(cfg, mt, nil, nil)

	state := NewState(cfg)
	out := o.onTick(t.Context(), state)

	require.Equal(t, []CandidateSeenRow{
		{Identifier: "ENG-1", UpdatedAt: updated},
		{Identifier: "ENG-2"},
	}, out.CandidateSeen, "onTick should have populated CandidateSeen from the candidate fetch")

	o.storeSnap(out)
	snap := o.Snapshot()
	require.Equal(t, out.CandidateSeen, snap.CandidateSeen, "snapshot must carry CandidateSeen through storeSnap/Snapshot")
}

// TestSnapshotDeepCopiesCandidateSeen mirrors TestSnapshotDeepCopiesInferredDeps:
// storeSnap/Snapshot must not alias the live State.CandidateSeen slice.
func TestSnapshotDeepCopiesCandidateSeen(t *testing.T) {
	cfg := &config.Config{}
	o := New(cfg, nil, nil, nil)
	state := NewState(cfg)
	state.CandidateSeen = []CandidateSeenRow{{Identifier: "ENG-1"}}

	o.storeSnap(state)
	snap := o.Snapshot()

	// Mutate the live state's slice after publishing the snapshot.
	state.CandidateSeen[0].Identifier = "mutated"
	state.CandidateSeen = append(state.CandidateSeen, CandidateSeenRow{Identifier: "ENG-9"})

	require.Equal(t, "ENG-1", snap.CandidateSeen[0].Identifier, "snapshot aliases the live CandidateSeen slice")
	require.Len(t, snap.CandidateSeen, 1, "snapshot aliases the live CandidateSeen backing array")
}

// TestStoreSnapDeepCopiesCandidateSeen checks storeSnap's own deep copy in
// isolation from Snapshot()'s independent copy, mirroring
// TestStoreSnapDeepCopiesInferredDeps — reads o.lastSnap directly so a bug in
// storeSnap's own copy can't be masked by Snapshot() re-copying on read.
func TestStoreSnapDeepCopiesCandidateSeen(t *testing.T) {
	o := &Orchestrator{}
	state := State{CandidateSeen: []CandidateSeenRow{{Identifier: "ENG-1"}}}

	o.storeSnap(state)

	state.CandidateSeen[0].Identifier = "mutated"
	state.CandidateSeen = append(state.CandidateSeen, CandidateSeenRow{Identifier: "ENG-9"})

	o.snapMu.RLock()
	got := o.lastSnap.CandidateSeen
	o.snapMu.RUnlock()

	require.Equal(t, "ENG-1", got[0].Identifier, "storeSnap aliases the live CandidateSeen slice")
	require.Len(t, got, 1, "storeSnap aliases the live CandidateSeen backing array")
}
