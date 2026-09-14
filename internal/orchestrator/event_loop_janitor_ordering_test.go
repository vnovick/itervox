package orchestrator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// absentTrackerStub wraps MemoryTracker so a test can simulate hard-deleting
// an issue: it vanishes from candidate polls AND detail fetches return
// tracker.ErrNotFound (MemoryTracker alone has no removal API). All calls
// happen on the test goroutine via onTick — no locking needed.
type absentTrackerStub struct {
	*tracker.MemoryTracker
	candidates []domain.Issue
	deleted    map[string]bool
}

func (s *absentTrackerStub) FetchCandidateIssues(_ context.Context) ([]domain.Issue, error) {
	out := make([]domain.Issue, len(s.candidates))
	copy(out, s.candidates)
	return out, nil
}

func (s *absentTrackerStub) FetchIssueDetail(ctx context.Context, issueID string) (*domain.Issue, error) {
	if s.deleted[issueID] {
		return nil, tracker.ErrNotFound
	}
	return s.MemoryTracker.FetchIssueDetail(ctx, issueID)
}

func (s *absentTrackerStub) FetchIssueByIdentifier(ctx context.Context, identifier string) (*domain.Issue, error) {
	if s.deleted[identifier] {
		return nil, tracker.ErrNotFound
	}
	return s.MemoryTracker.FetchIssueByIdentifier(ctx, identifier)
}

// gaps_11 G-2 — loop-level regression for the two-tick grace window. The
// unit tests seed prevActive directly and therefore passed even while
// event_loop.go overwrote PrevActiveIdentifiers BEFORE the janitor call
// (prev == current at janitor time). This test drives real onTick calls so
// the production capture-before-overwrite ordering is what is exercised:
// a deleted issue's ledger entries must survive ONE absent poll and be gone
// after TWO consecutive absent polls.
//
// This test does not, in practice, launch an off-loop dependency-refresh
// batch on any of its three ticks — but NOT because cfg leaves
// DependencyAuditRefreshBatchSize/TimeoutMs at zero. After Gap F,
// reconcileDependencyRefresh clamps a non-positive value to a default
// (100 rows / 30s) instead of treating it as "disabled", so it IS reached
// and DOES run its selection logic every tick. The real reasons no batch
// launches here are (1) Gap E — LIVE-1 (and, on tick 1, DEL-1) are already
// in the tracker's active states, so the inline candidate loop
// (event_loop.go) audits them with the tick's own `now` before
// reconcileDependencyRefresh runs, and selectDependencyRefreshBatch skips
// any row whose LastAuditedAt equals `now` — and (2) no blockers_resolved
// automation is registered, so pendingBlockersResolvedStates also returns
// nothing to scan. What this test is actually about is the janitor's
// two-tick grace window (gaps_11 G-2), which is orthogonal to how a
// DependencyAudit row eventually gets dropped for a hard-deleted issue — in
// production that is the off-loop refresh observing tracker.ErrNotFound
// (see applyDependencyRefreshResult's MissingKeys handling); here it is
// simulated directly (see the delete() call below) so the test stays
// focused on the janitor ordering bug it exists to catch.
func TestEventLoopTickOrdering_PruneAbsentSeesPriorTickSet(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tracker.ActiveStates = []string{"Todo"}
	cfg.Tracker.TerminalStates = []string{"Done"}
	cfg.Agent.MaxConcurrentAgents = 3
	cfg.Agent.MaxConcurrentAgentsByState = map[string]int{}

	// Both issues carry an unresolved blocker so the dispatch path skips
	// them (nil runner) — the test isolates the janitor behaviour.
	blockerState := "In Progress"
	blockerIdent := "ENG-0"
	blockerID := "eng-0-id"
	blocked := []domain.BlockerRef{{
		ID:         &blockerID,
		Identifier: &blockerIdent,
		State:      &blockerState,
	}}
	live := domain.Issue{ID: "live-1", Identifier: "LIVE-1", Title: "stays", State: "Todo", BlockedBy: blocked}
	del := domain.Issue{ID: "del-1", Identifier: "DEL-1", Title: "gets deleted", State: "Todo", BlockedBy: blocked}

	stub := &absentTrackerStub{
		MemoryTracker: tracker.NewMemoryTracker(
			[]domain.Issue{live, del}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates),
		candidates: []domain.Issue{live, del},
		deleted:    map[string]bool{},
	}
	o := New(cfg, stub, nil, nil)
	state := NewState(cfg)
	state.IssueProfiles["DEL-1"] = "reviewer"
	state.IssueBackends["DEL-1"] = "claude"
	// Prior session observation so the tick-1 observation appends a real
	// status-history row (keeps PrevIssueStates alive under the 7-day
	// retention, which the G-12 emission below reads ToState from).
	state.PrevIssueStates["DEL-1"] = "Backlog"

	// Tick 1: both issues present. Nothing pruned.
	state = o.onTick(t.Context(), state)
	require.Equal(t, "reviewer", state.IssueProfiles["DEL-1"])
	require.Empty(t, state.Running)

	// Operator hard-deletes DEL-1 from the tracker.
	stub.candidates = []domain.Issue{live}
	stub.deleted["del-1"] = true
	stub.deleted["DEL-1"] = true
	// Simulate the off-loop dependency-refresh batch that, in production,
	// would land between now and the next tick and drop DEL-1's audit row
	// on tracker.ErrNotFound (see applyDependencyRefreshResult's MissingKeys
	// handling). This test does not drive that batch for real (cfg leaves
	// the refresh disabled), so it reproduces the row's absence directly —
	// buildPresentPredicate (janitor.go) treats any DependencyAudit
	// reference as "still present", so without this the two-tick-grace-
	// window assertions below would pass even with the gaps_11 G-2 ordering
	// bug reintroduced, because DEL-1 would still be "present" via the
	// audit row regardless of the janitor's prevActive/currentActive
	// bookkeeping.
	delete(state.DependencyAudit, del.ID)

	// Tick 2: first absent poll — the grace window must preserve the
	// ledger entries (prevActive from tick 1 still contains DEL-1).
	state = o.onTick(t.Context(), state)
	require.Equal(t, "reviewer", state.IssueProfiles["DEL-1"],
		"one absent poll must NOT prune (two-tick grace window)")
	require.Equal(t, "claude", state.IssueBackends["DEL-1"])

	// Tick 3: second consecutive absent poll — entries must be pruned.
	state = o.onTick(t.Context(), state)
	_, profilePresent := state.IssueProfiles["DEL-1"]
	require.False(t, profilePresent, "two consecutive absent polls must prune IssueProfiles")
	_, backendPresent := state.IssueBackends["DEL-1"]
	require.False(t, backendPresent, "two consecutive absent polls must prune IssueBackends")

	// gaps_11 G-12 — the prune must leave a timeline row explaining why.
	found := false
	for _, c := range state.IssueStatusHistory["DEL-1"] {
		if c.Source == StatusSourceJanitor && c.Reason == JanitorReasonAbsentFromTracker {
			found = true
			break
		}
	}
	require.True(t, found, "expected janitor/absent_from_tracker row; got %+v",
		state.IssueStatusHistory["DEL-1"])

	// The live issue's bookkeeping is untouched throughout.
	_, liveActive := state.PrevActiveIdentifiers["LIVE-1"]
	require.True(t, liveActive)
}
