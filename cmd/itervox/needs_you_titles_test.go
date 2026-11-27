package main

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/vnovick/itervox/internal/server"
)

// TestIssueTitlesForNeedsYou: the "Needs you" lane names an issue by the
// title its running row or run history carries, the running row winning.
func TestIssueTitlesForNeedsYou(t *testing.T) {
	got := issueTitles(
		[]server.RunningRow{{Identifier: "ENG-1", Title: "Live title"}, {Identifier: "ENG-3"}},
		[]server.HistoryRow{{Identifier: "ENG-1", Title: "Old title"}, {Identifier: "ENG-2", Title: "Past run"}},
	)
	assert.Equal(t, map[string]string{"ENG-1": "Live title", "ENG-2": "Past run"}, got)

	lane := server.BuildNeedsYou([]server.InputRequiredRow{{Identifier: "ENG-2", State: "input_required", Context: "Q?",
		QueuedAt: "2026-10-22T10:00:00Z"}}, got, "web-app")
	assert.Equal(t, "Past run", lane[0].Title)
}
