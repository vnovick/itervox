package orchestrator

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
)

func TestAutomationQueuePersistenceRoundTrip(t *testing.T) {
	cfg := &config.Config{}
	path := filepath.Join(t.TempDir(), "automation_queue.json")
	now := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	issue := domain.Issue{
		ID:         "issue-uuid",
		Identifier: "ENG-1",
		Title:      "Persist queued automation",
		State:      "Todo",
		Labels:     []string{"backend"},
	}
	dispatch := AutomationDispatch{
		AutomationID:      "rate-limit-switch",
		ProfileName:       "fallback",
		Instructions:      "Continue with fallback profile",
		AutoResume:        true,
		UseIssueLifecycle: true,
		Trigger: AutomationTriggerContext{
			Type:              config.AutomationTriggerRateLimited,
			FiredAt:           now,
			AutomationID:      "rate-limit-switch",
			FailedProfile:     "claude-coder",
			FailedBackend:     "claude",
			SwitchedToProfile: "codex-coder",
			SwitchedToBackend: "codex",
		},
	}

	state := NewState(cfg)
	enqueueAutomation(&state, issue, dispatch, "no_slots", now)

	writer := New(cfg, nil, nil, nil)
	writer.SetAutomationQueueFile(path)
	writer.storeSnap(state)

	reader := New(cfg, nil, nil, nil)
	reader.SetAutomationQueueFile(path)
	loaded := reader.loadAutomationQueueFromDisk(NewState(cfg))

	require.Equal(t, state.AutomationQueueOrder, loaded.AutomationQueueOrder)
	require.Len(t, loaded.AutomationQueue, 1)
	entry := loaded.AutomationQueue[loaded.AutomationQueueOrder[0]]
	require.NotNil(t, entry)
	require.Equal(t, "rate-limit-switch", entry.AutomationID)
	require.Equal(t, "fallback", entry.ProfileName)
	require.Equal(t, "Continue with fallback profile", entry.Instructions)
	require.True(t, entry.AutoResume)
	require.True(t, entry.UseIssueLifecycle)
	require.Equal(t, issue.Identifier, entry.Issue.Identifier)
	require.Equal(t, []string{"backend"}, entry.Issue.Labels)
	require.Equal(t, AutomationQueueReasonNoSlots, entry.Reason)
	require.Equal(t, now, entry.QueuedAt)
	require.Equal(t, config.AutomationTriggerRateLimited, entry.Trigger.Type)
	require.Equal(t, "claude-coder", entry.Trigger.FailedProfile)
	require.Equal(t, "claude", entry.Trigger.FailedBackend)
	require.Equal(t, "codex-coder", entry.Trigger.SwitchedToProfile)
	require.Equal(t, "codex", entry.Trigger.SwitchedToBackend)
}

func TestAutomationQueuePersistenceUsesCurrentConfigMaxLength(t *testing.T) {
	writerCfg := &config.Config{}
	writerCfg.Agent.MaxAutomationQueueLength = 1
	readerCfg := &config.Config{}
	readerCfg.Agent.MaxAutomationQueueLength = 3
	path := filepath.Join(t.TempDir(), "automation_queue.json")
	now := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	dispatch := AutomationDispatch{
		AutomationID: "queue-cap",
		ProfileName:  "pm",
		Instructions: "Run when capacity allows",
		Trigger: AutomationTriggerContext{
			Type:         config.AutomationTriggerCron,
			FiredAt:      now,
			AutomationID: "queue-cap",
		},
	}
	issue := domain.Issue{
		ID:         "issue-uuid-1",
		Identifier: "ENG-1",
		Title:      "Persist queued automation",
		State:      "Todo",
	}

	state := NewState(writerCfg)
	require.True(t, enqueueAutomation(&state, issue, dispatch, "no_slots", now))
	require.Equal(t, 1, state.AutomationQueueBackpressure.MaxLength)
	require.True(t, state.AutomationQueueBackpressure.Saturated)

	writer := New(writerCfg, nil, nil, nil)
	writer.SetAutomationQueueFile(path)
	writer.storeSnap(state)

	reader := New(readerCfg, nil, nil, nil)
	reader.SetAutomationQueueFile(path)
	loaded := reader.loadAutomationQueueFromDisk(NewState(readerCfg))

	require.Equal(t, 3, loaded.AutomationQueueBackpressure.MaxLength)
	require.Equal(t, 1, loaded.AutomationQueueBackpressure.Length)
	require.False(t, loaded.AutomationQueueBackpressure.Saturated)
	require.False(t, loaded.AutomationQueueBackpressure.PausedProducers)

	nextIssue := domain.Issue{
		ID:         "issue-uuid-2",
		Identifier: "ENG-2",
		Title:      "Second queued automation",
		State:      "Todo",
	}
	require.True(t, enqueueAutomation(&loaded, nextIssue, dispatch, "no_slots", now.Add(time.Minute)))
	require.Equal(t, 3, loaded.AutomationQueueBackpressure.MaxLength)
	require.Equal(t, 2, loaded.AutomationQueueBackpressure.Length)
	require.Len(t, loaded.AutomationQueue, 2)
}
