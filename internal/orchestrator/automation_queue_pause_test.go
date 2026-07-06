package orchestrator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
)

// TestPauseWindow_DrainParksQueuedEntryInsteadOfDeleting is the promoted
// AUTO-2 probe: a durably queued automation entry must SURVIVE a drain pass
// that runs while pause_dispatch_when_any_in_state is active for an unrelated
// issue. Before the fix, ineligibleReasonForAutomation's
// "paused_by_state:<state>" reason was not in automationQueueableReason's
// allowlist, so the drain pass permanently deleted the queued entry (spec D7
// requires triggers be durably queued, not dropped).
func TestPauseWindow_DrainParksQueuedEntryInsteadOfDeleting(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.MaxConcurrentAgents = 4
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"helper": {Prompt: "x"},
	}
	cfg.Agent.PauseDispatchWhenAnyInState = []string{"In Review"}

	o := New(cfg, nil, nil, nil)
	state := NewState(cfg)

	// Unrelated issue observed in the pause state at last poll.
	state.PrevIssueStates = map[string]string{"other-id": "In Review"}

	issue := domain.Issue{ID: "i1", Identifier: "ENG-1", Title: "t", State: "Todo"}
	dispatch := AutomationDispatch{
		AutomationID: "notify",
		ProfileName:  "helper",
		Trigger: AutomationTriggerContext{
			Type:         config.AutomationTriggerIssueEnteredState,
			FiredAt:      time.Now(),
			AutomationID: "notify",
		},
	}
	now := time.Now()
	require.True(t, enqueueAutomation(&state, issue, dispatch, "no_slots", now))
	require.Len(t, state.AutomationQueue, 1)

	// Sanity: the pause guard fires for this state shape.
	_, paused := pausedByAnyInState(state)
	require.True(t, paused, "pause guard must be active for this probe")

	o.drainAutomationQueue(t.Context(), &state, now.Add(time.Hour))

	require.Len(t, state.AutomationQueue, 1,
		"queued automation must survive a transient pause_dispatch window (fails => durable queue entry dropped)")

	entries := sortedAutomationQueue(state)
	require.Len(t, entries, 1)
	entry := entries[0]
	require.Equal(t, AutomationQueueReasonPausedByState, entry.Reason)
	require.Equal(t, "In Review", entry.ReasonDetail)
	require.Equal(t, AutomationQueueQueued, entry.Status)
}

// TestAutomationQueueableReason_PausedByState verifies the reason-classifier
// unit directly: a "paused_by_state:<state>" ineligibility reason must be
// queueable and must surface the paused state as the reason detail.
func TestAutomationQueueableReason_PausedByState(t *testing.T) {
	queueable, reason, detail := automationQueueableReason("paused_by_state:In Review")
	require.True(t, queueable)
	require.Equal(t, AutomationQueueReasonPausedByState, reason)
	require.Equal(t, "In Review", detail)
}
