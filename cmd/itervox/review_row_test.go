package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
)

// TestReviewRow (#135): issue detail carries the issue's verdicts, and the
// quorum only once every reviewer has run.
func TestReviewRow(t *testing.T) {
	state := orchestrator.NewState(&config.Config{})
	assert.Nil(t, reviewRow(state, "ENG-1"), "no review yet")

	at := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	state.ReviewVerdicts["ENG-1"] = []orchestrator.ReviewVerdict{{
		Profile: "reviewer-codex", Verdict: "block", Reasons: []string{"no tests"},
		Comments: []orchestrator.ReviewComment{{Path: "a.go", Line: 7, Body: "nil map"}}, RecordedAt: at,
	}}
	state.ReviewChainIndex["ENG-1"] = 1
	// A previous round's result never shows under a review in progress.
	state.ReviewOutcomes["ENG-1"] = orchestrator.ReviewOutcome{Approvals: 2}
	row := reviewRow(state, "ENG-1")
	require.NotNil(t, row)
	assert.True(t, row.InProgress)
	assert.Nil(t, row.Outcome, "no quorum while reviewers are still running")
	assert.Equal(t, []server.ReviewVerdictRow{{
		Profile: "reviewer-codex", Verdict: "block", Reasons: []string{"no tests"},
		Comments: []server.ReviewCommentRow{{Path: "a.go", Line: 7, Body: "nil map"}}, RecordedAt: at,
	}}, row.Verdicts)

	delete(state.ReviewChainIndex, "ENG-1")
	state.ReviewOutcomes["ENG-1"] = orchestrator.ReviewOutcome{Blocked: true, Blocks: 1, Quorum: "any_block"}
	row = reviewRow(state, "ENG-1")
	require.NotNil(t, row)
	assert.False(t, row.InProgress)
	assert.Equal(t, &server.ReviewOutcomeRow{Blocked: true, Blocks: 1, Quorum: "any_block"}, row.Outcome)

	// A fresh chain just started: in progress, nothing recorded yet.
	state.ReviewChainIndex["ENG-2"] = 0
	row = reviewRow(state, "ENG-2")
	require.NotNil(t, row)
	assert.True(t, row.InProgress)
	assert.Empty(t, row.Verdicts)
}
