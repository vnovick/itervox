package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
)

// TestAdapterEmitPRMergedPopulatesEventFields (CORE-108): the adapter hands
// DispatchPRMergedAutomations a PRMergedEvent with the PR url, base branch
// AND head branch (Branch — the field trigger.pr_branch binds to), which it
// used to drop.
func TestAdapterEmitPRMergedPopulatesEventFields(t *testing.T) {
	issue := domain.Issue{ID: "id-108", Identifier: "ENG-108", State: "In Review"}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, []string{"In Review"}, []string{"Done"})
	var got []orchestrator.PRMergedEvent
	adapter := &orchestratorAdapter{tr: mt, dispatchPRMerged: func(_ context.Context, is domain.Issue, ev orchestrator.PRMergedEvent) {
		assert.Equal(t, "ENG-108", is.Identifier)
		got = append(got, ev)
	}}

	require.NoError(t, adapter.EmitPRMerged(context.Background(), "ENG-108",
		"https://github.com/acme/app/pull/7", 7, "cafe108", "main", "itervox/ENG-108"))

	require.Len(t, got, 1)
	assert.Equal(t, "https://github.com/acme/app/pull/7", got[0].PRURL)
	assert.Equal(t, 7, got[0].PRNumber)
	assert.Equal(t, "main", got[0].BaseRef)
	assert.Equal(t, "itervox/ENG-108", got[0].Branch)
	assert.Equal(t, "cafe108", got[0].MergedSHA)
	assert.False(t, got[0].MergedAt.IsZero())
}
