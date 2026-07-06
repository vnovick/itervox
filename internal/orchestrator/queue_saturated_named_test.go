package orchestrator

import (
	"strings"
	"testing"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
)

// saturationRejectionCase is the shared shape behind the seven literally-named
// `Test<Trigger>Dispatch_QueueSaturated_RecordsRejection` tests from
// todolist4 P2-11. Each test instance differs only in its trigger.Type.
func saturationRejectionCase(t *testing.T, triggerType, automationID string) {
	t.Helper()
	cfg := &config.Config{}
	cfg.Agent.MaxAutomationQueueLength = 1
	state := NewState(cfg)
	issue := domain.Issue{ID: "id-1", Identifier: "ENG-1", State: "Todo"}
	dispatch := AutomationDispatch{
		AutomationID: automationID,
		ProfileName:  "p",
		Trigger:      AutomationTriggerContext{Type: triggerType, AutomationID: automationID},
	}
	recordAutomationQueueRejected(&state, dispatch, issue, "queue_saturated", time.Now())

	if state.AutomationQueueBackpressure.RejectedSinceBoot != 1 {
		t.Errorf("RejectedSinceBoot = %d; want 1", state.AutomationQueueBackpressure.RejectedSinceBoot)
	}
	if !state.AutomationQueueBackpressure.Saturated {
		t.Error("expected Saturated to be true")
	}
	last := state.AutomationQueueBackpressure.LastRejectedReason
	if !strings.Contains(last, triggerType) || !strings.Contains(last, automationID) {
		t.Errorf("LastRejectedReason = %q; expected to mention trigger type %q and automation %q",
			last, triggerType, automationID)
	}
}

func TestPROpenedDispatch_QueueSaturated_RecordsRejection(t *testing.T) {
	saturationRejectionCase(t, config.AutomationTriggerPROpened, "pr-opened-auto")
}

func TestInputRequiredDispatch_QueueSaturated_RecordsRejection(t *testing.T) {
	saturationRejectionCase(t, config.AutomationTriggerInputRequired, "input-responder")
}

func TestTrackerCommentDispatch_QueueSaturated_RecordsRejection(t *testing.T) {
	saturationRejectionCase(t, config.AutomationTriggerTrackerComment, "review-on-comment")
}

func TestIssueEnteredStateDispatch_QueueSaturated_RecordsRejection(t *testing.T) {
	saturationRejectionCase(t, config.AutomationTriggerIssueEnteredState, "state-entered")
}

func TestIssueMovedToBacklogDispatch_QueueSaturated_RecordsRejection(t *testing.T) {
	saturationRejectionCase(t, config.AutomationTriggerIssueMovedBacklog, "backlog-move")
}

func TestRunFailedDispatch_QueueSaturated_RecordsRejection(t *testing.T) {
	saturationRejectionCase(t, config.AutomationTriggerRunFailed, "run-failed")
}

func TestRateLimitedDispatch_QueueSaturated_RecordsRejection(t *testing.T) {
	saturationRejectionCase(t, config.AutomationTriggerRateLimited, "rate-limited")
}

func TestBlockersResolvedDispatch_QueueSaturated_RecordsRejection(t *testing.T) {
	saturationRejectionCase(t, config.AutomationTriggerBlockersResolved, "blockers-resolved")
}
