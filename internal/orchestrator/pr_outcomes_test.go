package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/metrics"
)

const outcomePR = "https://github.com/acme/app/pull/7"

// exitWithPR sends a run's success exit carrying a PR (and usage) through
// the event loop's real exit handler.
func (f *provenanceFixture) exitWithPR(t *testing.T, entry *RunEntry, attempt int, pr string) CompletedRun {
	t.Helper()
	f.state = f.o.handleEvent(t.Context(), f.state, OrchestratorEvent{
		Type: EventWorkerExited, IssueID: f.issue.ID, RunID: entry.RunID,
		RunEntry: &RunEntry{Issue: f.issue, TerminalReason: TerminalSucceeded, RetryAttempt: &attempt, PRURL: pr,
			InputTokens: 120, OutputTokens: 30, TotalTokens: 150, CacheReadTokens: 800, CacheWriteTokens: 64},
	})
	hist := f.o.RunHistory()
	require.NotEmpty(t, hist)
	return hist[len(hist)-1]
}

func (f *provenanceFixture) outcome(t *testing.T, state, source string) {
	t.Helper()
	f.state = f.o.handleEvent(t.Context(), f.state, OrchestratorEvent{Type: EventPROutcome,
		PROutcome: &PROutcomeReport{PRURL: outcomePR, State: state, Source: source}})
}

// TestCompletedRunRecordsCacheTokensAttemptAndOpensOutcome: a run's exit
// through the event loop writes its prompt-cache tokens and retry attempt
// onto the CompletedRun, counts its tokens in itervox_agent_tokens_total, and
// records the PR it carries as an open outcome.
func TestCompletedRunRecordsCacheTokensAttemptAndOpensOutcome(t *testing.T) {
	f := newProvenanceFixture(t)
	before := metrics.RunTokensCount(metrics.TokensCacheRead)
	entry := f.start(t)
	two := 2
	entry.RetryAttempt = &two
	run := f.exitWithPR(t, entry, 2, outcomePR)

	assert.Equal(t, 800, run.CacheReadTokens)
	assert.Equal(t, 64, run.CacheWriteTokens)
	assert.Equal(t, 2, run.Attempt)
	assert.Equal(t, 150, run.TotalTokens)
	assert.Equal(t, outcomePR, run.PRURL)
	assert.Equal(t, before+800, metrics.RunTokensCount(metrics.TokensCacheRead))

	pr, ok := f.state.PROutcomes[outcomePR]
	require.True(t, ok)
	assert.Equal(t, PROutcomeOpen, pr.State)
	assert.Equal(t, "ENG-1", pr.Identifier)
	assert.Equal(t, entry.RunID, pr.RunID)
}

// TestPROutcomeTransitionsThroughEventLoop: open → merged → reverted; a
// late "closed" never undoes a merge; an unknown PR changes nothing; a PR
// closed unmerged can be reopened and then merged; reverted is final.
func TestPROutcomeTransitionsThroughEventLoop(t *testing.T) {
	f := newProvenanceFixture(t)
	f.exitWithPR(t, f.start(t), 0, outcomePR)
	mergedBefore := metrics.PROutcomeCount(PROutcomeMerged)

	f.outcome(t, PROutcomeClosed, "poll")
	assert.Equal(t, PROutcomeClosed, f.state.PROutcomes[outcomePR].State)
	f.outcome(t, PROutcomeOpen, "webhook") // reopened
	assert.Equal(t, PROutcomeOpen, f.state.PROutcomes[outcomePR].State)
	assert.True(t, f.state.PROutcomes[outcomePR].ClosedAt.IsZero())

	f.outcome(t, PROutcomeMerged, "merge_pr")
	pr := f.state.PROutcomes[outcomePR]
	assert.Equal(t, PROutcomeMerged, pr.State)
	assert.Equal(t, "merge_pr", pr.Source)
	assert.False(t, pr.ClosedAt.IsZero())
	assert.Equal(t, mergedBefore+1, metrics.PROutcomeCount(PROutcomeMerged))

	f.outcome(t, PROutcomeClosed, "poll")
	assert.Equal(t, PROutcomeMerged, f.state.PROutcomes[outcomePR].State, "a merge is never undone by a close")

	f.state = f.o.handleEvent(t.Context(), f.state, OrchestratorEvent{Type: EventPROutcome,
		PROutcome: &PROutcomeReport{PRURL: "https://github.com/acme/app/pull/99", State: PROutcomeMerged}})
	assert.NotContains(t, f.state.PROutcomes, "https://github.com/acme/app/pull/99", "an unknown PR is not added")

	// The revert scan (#217) marks the merged PR reverted; another number
	// changes nothing.
	f.state = f.o.handleEvent(t.Context(), f.state, OrchestratorEvent{Type: EventRevertsScanned,
		Reverts: &RevertScanReport{Reverted: []RevertedPR{{Number: 8}}}})
	assert.Equal(t, PROutcomeMerged, f.state.PROutcomes[outcomePR].State)
	f.state = f.o.handleEvent(t.Context(), f.state, OrchestratorEvent{Type: EventRevertsScanned,
		Reverts: &RevertScanReport{Reverted: []RevertedPR{{Number: 7, Subject: `Revert "Add x (#7)"`}}}})
	pr = f.state.PROutcomes[outcomePR]
	assert.Equal(t, PROutcomeReverted, pr.State)
	assert.Equal(t, "revert_scan", pr.Source)
	assert.False(t, pr.RevertedAt.IsZero())

	f.outcome(t, PROutcomeOpen, "webhook")
	f.outcome(t, PROutcomeMerged, "poll")
	assert.Equal(t, PROutcomeReverted, f.state.PROutcomes[outcomePR].State, "reverted is final")

	// A second run on the same PR does not reset its outcome.
	f.exitWithPR(t, f.start(t), 0, outcomePR)
	assert.Equal(t, PROutcomeReverted, f.state.PROutcomes[outcomePR].State)
}

// TestRevertScanRunsForMergedOutcomesWithoutLessons: the revert scan (#217)
// starts while lessons are off once the outcome ledger holds a merged PR,
// and not before.
func TestRevertScanRunsForMergedOutcomesWithoutLessons(t *testing.T) {
	f := newProvenanceFixture(t)
	require.False(t, f.o.lessonsOn())
	scans := make(chan struct{}, 4)
	f.o.RevertScan = func(context.Context) ([]RevertedPR, error) { scans <- struct{}{}; return nil, nil }
	now := time.Now()
	f.o.maybeScanReverts(&f.state, now)
	assert.False(t, f.state.RevertScanInFlight, "nothing to scan for")

	f.state.PROutcomes[outcomePR] = PROutcome{PRURL: outcomePR, Identifier: "ENG-1", State: PROutcomeMerged, UpdatedAt: now}
	f.o.maybeScanReverts(&f.state, now)
	assert.True(t, f.state.RevertScanInFlight)
	select {
	case <-scans:
	case <-time.After(2 * time.Second):
		t.Fatal("the revert scan did not run")
	}
	f.o.lessonWg.Wait()
}

// TestPROutcomesPersistAndPrune: the ledger round-trips through
// pr_outcomes.json (an invalid state or an entry without an issue is
// dropped on load), entries unchanged past the retention are pruned, and
// the ledger is bounded with the oldest change dropped first.
func TestPROutcomesPersistAndPrune(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pr_outcomes.json")
	f := newProvenanceFixture(t)
	f.o.SetPROutcomesFile(path)
	now := time.Now()
	f.state.PROutcomes = map[string]PROutcome{
		outcomePR:                            {PRURL: outcomePR, Identifier: "ENG-1", State: PROutcomeMerged, OpenedAt: now, UpdatedAt: now},
		"https://github.com/acme/app/pull/1": {Identifier: "ENG-2", State: "bogus", UpdatedAt: now},
		"https://github.com/acme/app/pull/2": {State: PROutcomeOpen, UpdatedAt: now},
	}
	f.o.savePROutcomesToDisk(f.state.Clone())
	f.o.ledger(ledgerPROutcomes).settle()
	_, err := os.Stat(path)
	require.NoError(t, err)

	loaded := f.o.loadPROutcomesFromDisk(NewState(f.cfg))
	require.Len(t, loaded.PROutcomes, 1)
	assert.Equal(t, PROutcomeMerged, loaded.PROutcomes[outcomePR].State)
	assert.Equal(t, outcomePR, loaded.PROutcomes[outcomePR].PRURL)

	st := NewState(f.cfg)
	st.PROutcomes[outcomePR] = PROutcome{PRURL: outcomePR, Identifier: "ENG-1", State: PROutcomeMerged,
		UpdatedAt: now.Add(-prOutcomeRetention - time.Hour)}
	for i := range maxPROutcomes + 5 {
		url := fmt.Sprintf("https://github.com/acme/app/pull/%d", 1000+i)
		st.PROutcomes[url] = PROutcome{PRURL: url, Identifier: "ENG-3", State: PROutcomeOpen,
			UpdatedAt: now.Add(time.Duration(i) * time.Second)}
	}
	prunePROutcomes(&st, now.Add(time.Hour))
	assert.Len(t, st.PROutcomes, maxPROutcomes)
	assert.NotContains(t, st.PROutcomes, outcomePR, "past retention")
	assert.NotContains(t, st.PROutcomes, "https://github.com/acme/app/pull/1000", "oldest change dropped first")
	assert.Contains(t, st.PROutcomes, fmt.Sprintf("https://github.com/acme/app/pull/%d", 1000+maxPROutcomes+4))
}

// TestTrackedPROutcomesListsOpenAndClosed: the watcher's outcome poll
// tracks open and closed-unmerged PRs, never merged or reverted ones.
func TestTrackedPROutcomesListsOpenAndClosed(t *testing.T) {
	f := newProvenanceFixture(t)
	now := time.Now()
	for n, s := range map[int]string{1: PROutcomeOpen, 2: PROutcomeClosed, 3: PROutcomeMerged, 4: PROutcomeReverted} {
		url := fmt.Sprintf("https://github.com/acme/app/pull/%d", n)
		f.state.PROutcomes[url] = PROutcome{PRURL: url, Identifier: "ENG-1", State: s, UpdatedAt: now}
	}
	f.o.storeSnap(f.state)
	tracked := f.o.TrackedPROutcomes()
	require.Len(t, tracked, 2)
	assert.Equal(t, PROutcomeOpen, tracked[0].State)
	assert.Equal(t, PROutcomeClosed, tracked[1].State)
	assert.Len(t, f.o.PROutcomes(), 4)
}
