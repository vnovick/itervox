package orchestrator

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
)

func TestIssueStatusHistoryObservedTrackerChange(t *testing.T) {
	t.Parallel()

	state := NewState(&config.Config{})
	now := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	issue := domain.Issue{ID: "issue-1", Identifier: "ENG-1", State: "Todo"}

	recordObservedIssueState(&state, issue, now)
	require.Empty(t, state.IssueStatusHistory["ENG-1"])
	require.Equal(t, "Todo", state.PrevIssueStates["ENG-1"])

	issue.State = "In Progress"
	recordObservedIssueState(&state, issue, now.Add(time.Minute))
	recordObservedIssueState(&state, issue, now.Add(2*time.Minute))

	require.Len(t, state.IssueStatusHistory["ENG-1"], 1)
	got := state.IssueStatusHistory["ENG-1"][0]
	require.Equal(t, "Todo", got.FromState)
	require.Equal(t, "In Progress", got.ToState)
	require.Equal(t, StatusSourceTrackerObserved, got.Source)
}

func TestIssueStatusHistoryRecordsDashboardMutationEvent(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	o := New(cfg, nil, nil, nil)
	state := NewState(cfg)
	state.PrevIssueStates["ENG-1"] = "Todo"
	now := time.Date(2026, 5, 20, 11, 0, 0, 0, time.UTC)

	state = o.handleEvent(context.Background(), state, OrchestratorEvent{
		Type: EventIssueStatusChanged,
		StatusChange: &IssueStatusChange{
			Identifier: "ENG-1",
			ToState:    "Done",
			Source:     StatusSourceDashboard,
			At:         now,
		},
	})

	require.Len(t, state.IssueStatusHistory["ENG-1"], 1)
	got := state.IssueStatusHistory["ENG-1"][0]
	require.Equal(t, "Todo", got.FromState)
	require.Equal(t, "Done", got.ToState)
	require.Equal(t, StatusSourceDashboard, got.Source)
	require.Equal(t, now, got.At)
}

func TestIssueStatusHistoryRecordsAutomationMetadata(t *testing.T) {
	t.Parallel()

	state := NewState(&config.Config{})
	state.PrevIssueStates["ENG-2"] = "Backlog"
	appendIssueStatusChange(&state, IssueStatusChange{
		Identifier:   "ENG-2",
		ToState:      "Todo",
		Source:       StatusSourceAutomation,
		AutomationID: "unblock-manager",
		TriggerType:  "blockers_resolved",
		ProfileName:  "pm",
		Backend:      "codex",
		WorkerHost:   "ssh-a",
		At:           time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC),
	})

	got := state.IssueStatusHistory["ENG-2"][0]
	require.Equal(t, "Backlog", got.FromState)
	require.Equal(t, "Todo", got.ToState)
	require.Equal(t, "unblock-manager", got.AutomationID)
	require.Equal(t, "blockers_resolved", got.TriggerType)
	require.Equal(t, "pm", got.ProfileName)
	require.Equal(t, "codex", got.Backend)
	require.Equal(t, "ssh-a", got.WorkerHost)
}

func TestIssueStatusHistoryRetentionCapsEntries(t *testing.T) {
	t.Parallel()

	state := NewState(&config.Config{})
	state.PrevIssueStates["ENG-3"] = "S0"
	for i := 1; i <= maxIssueStatusHistory+5; i++ {
		appendIssueStatusChange(&state, IssueStatusChange{
			Identifier: "ENG-3",
			ToState:    fmt.Sprintf("S%d", i),
			Source:     StatusSourceSystem,
			At:         time.Date(2026, 5, 20, 12, i, 0, 0, time.UTC),
		})
	}

	got := state.IssueStatusHistory["ENG-3"]
	require.Len(t, got, maxIssueStatusHistory)
	require.Equal(t, "S6", got[0].ToState)
}
