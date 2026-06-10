package depsanalysis

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

func TestFetchIssues_ReturnsIssuesAndTrackerEdges(t *testing.T) {
	s := func(v string) *string { return &v }
	mt := tracker.NewMemoryTracker(
		[]domain.Issue{
			{ID: "1", Identifier: "ENG-1", Title: "Foundation", State: "Todo"},
			{
				ID: "2", Identifier: "ENG-2", Title: "Builds on ENG-1", State: "Todo",
				Description: s("ENG-2 depends on ENG-1."),
				BlockedBy: []domain.BlockerRef{
					{Identifier: s("ENG-1"), State: s("Todo")},
				},
			},
			{ID: "3", Identifier: "ENG-3", Title: "Done already", State: "Done"},
		},
		[]string{"Todo"},
		[]string{"Done"},
	)
	states := DedupeStateNames([]string{"Todo"}, []string{"Done"})
	issues, edges, err := FetchIssues(context.Background(), mt, states)
	require.NoError(t, err)
	require.Len(t, issues, 3)
	require.Len(t, edges, 1)
	assert.Equal(t, "ENG-1", edges[0].Source)
	assert.Equal(t, "ENG-2", edges[0].Target)
	// The description must be flattened into the analyzer issue payload.
	found := false
	for _, iss := range issues {
		if iss.Identifier == "ENG-2" && iss.Description == "ENG-2 depends on ENG-1." {
			found = true
		}
	}
	assert.True(t, found, "analyzer issue payload must carry description text")
}

func TestFetchIssues_NoStatesReturnsEmpty(t *testing.T) {
	mt := tracker.NewMemoryTracker(nil, nil, nil)
	issues, edges, err := FetchIssues(context.Background(), mt, nil)
	require.NoError(t, err)
	assert.Empty(t, issues)
	assert.Empty(t, edges)
}

func TestFetchIssues_NilTrackerErrors(t *testing.T) {
	_, _, err := FetchIssues(context.Background(), nil, []string{"Todo"})
	require.Error(t, err)
}

func TestDedupeStateNames_PreservesOrderAndDeduplicatesCaseSensitively(t *testing.T) {
	got := DedupeStateNames([]string{"Todo", "In Progress"}, []string{"Done"}, []string{"Todo"})
	assert.Equal(t, []string{"Todo", "In Progress", "Done"}, got)
}
