package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/prwatch"
)

// TestHistoryRowsCarryCostAndPROutcome: the API history rows carry each
// run's cache tokens, attempt and PR, and the PR's outcome from the ledger
// (absent when the ledger no longer holds it).
func TestHistoryRowsCarryCostAndPROutcome(t *testing.T) {
	runs := []orchestrator.CompletedRun{
		{Identifier: "ENG-1", Status: "succeeded", CacheReadTokens: 900, CacheWriteTokens: 40, Attempt: 1,
			PRURL: "https://github.com/acme/app/pull/7"},
		{Identifier: "ENG-2", Status: "failed", PRURL: "https://github.com/acme/app/pull/8"},
		{Identifier: "ENG-3", Status: "succeeded"},
	}
	rows := historyRows(runs, []orchestrator.PROutcome{
		{PRURL: "https://github.com/acme/app/pull/7", State: orchestrator.PROutcomeMerged},
	})
	require.Len(t, rows, 3)
	assert.Equal(t, 900, rows[0].CacheReadTokens)
	assert.Equal(t, 40, rows[0].CacheWriteTokens)
	assert.Equal(t, 1, rows[0].Attempt)
	assert.Equal(t, "https://github.com/acme/app/pull/7", rows[0].PRURL)
	assert.Equal(t, "merged", rows[0].PROutcome)
	assert.Empty(t, rows[1].PROutcome, "not in the ledger")
	assert.Empty(t, rows[2].PRURL)
}

type fakeOutcomeOrch struct {
	tracked []orchestrator.PROutcome
	got     []orchestrator.PROutcomeReport
}

func (f *fakeOutcomeOrch) TrackedPROutcomes() []orchestrator.PROutcome { return f.tracked }
func (f *fakeOutcomeOrch) RecordPROutcome(_ context.Context, rep orchestrator.PROutcomeReport) bool {
	f.got = append(f.got, rep)
	return true
}

// TestPROutcomeWatcherWiring: the watcher tracks the ledger's open and
// closed PRs and hands what it observes to RecordPROutcome unchanged.
func TestPROutcomeWatcherWiring(t *testing.T) {
	f := &fakeOutcomeOrch{tracked: []orchestrator.PROutcome{
		{PRURL: "https://github.com/acme/app/pull/7", Identifier: "ENG-1", State: orchestrator.PROutcomeOpen},
	}}
	tracked := trackedPROutcomesFor(f)()
	require.Len(t, tracked, 1)
	assert.Equal(t, prwatch.Tracked{Managed: prwatch.Managed{URL: "https://github.com/acme/app/pull/7", Identifier: "ENG-1"}, State: "open"}, tracked[0])

	at := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	prOutcomeSink(f)(t.Context(), prwatch.Outcome{PRURL: "https://github.com/acme/app/pull/7", State: prwatch.OutcomeMerged, At: at, Source: prwatch.SourcePoll})
	require.Len(t, f.got, 1)
	assert.Equal(t, orchestrator.PROutcomeReport{PRURL: "https://github.com/acme/app/pull/7", State: orchestrator.PROutcomeMerged, At: at, Source: "poll"}, f.got[0])
	assert.Equal(t, orchestrator.PROutcomeMerged, prwatch.OutcomeMerged, "the two packages name states alike")
	assert.Equal(t, orchestrator.PROutcomeClosed, prwatch.OutcomeClosed)
	assert.Equal(t, orchestrator.PROutcomeOpen, prwatch.OutcomeOpen)
}
