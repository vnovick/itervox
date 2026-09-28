package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
)

// TestSnapshotPauseReasons (M6-close BH-M6-3): the snapshot carries why each
// paused issue is paused, as `pauseReasons` (identifier → reason, a sibling
// of `pausedWithPR`), so the dashboard can tell a failure pause from an
// operator's own pause. Only currently paused issues are listed; a paused
// issue with no recorded reason (a legacy pause file) is omitted, never
// guessed.
func TestSnapshotPauseReasons(t *testing.T) {
	s := orchestrator.State{
		PausedIdentifiers: map[string]string{"ENG-1": "i1", "ENG-2": "i2", "ENG-3": "i3", "ENG-4": "i4", "ENG-5": "i5"},
		PauseReasons: map[string]string{
			"ENG-1": orchestrator.PauseReasonUserCancelled,
			"ENG-2": orchestrator.PauseReasonRetriesExhausted,
			"ENG-3": orchestrator.PauseReasonTransitionFailed,
			"ENG-4": orchestrator.PauseReasonUserDismissedInput,
			"ENG-9": orchestrator.PauseReasonRetriesExhausted, // no longer paused
		},
	}
	got := pauseReasonsMap(s)
	assert.Equal(t, map[string]string{
		"ENG-1": "user_cancelled", "ENG-2": "retries_exhausted",
		"ENG-3": "transition_failed", "ENG-4": "user_dismissed_input",
	}, got)

	b, err := json.Marshal(server.StateSnapshot{Paused: []string{"ENG-1"}, PauseReasons: got})
	require.NoError(t, err)
	var wire map[string]any
	require.NoError(t, json.Unmarshal(b, &wire))
	assert.Contains(t, wire, "pauseReasons")

	b, err = json.Marshal(server.StateSnapshot{})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(b, &wire))
	wire = map[string]any{}
	require.NoError(t, json.Unmarshal(b, &wire))
	_, present := wire["pauseReasons"]
	assert.False(t, present, "omitted when nothing is paused")
}
