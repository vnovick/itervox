package orchestrator

import (
	"testing"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
)

// TestQueueBackpressure_StructuredLastRejectedFieldsPopulated — todolist4 P2-2.
// The colon-joined LastRejectedReason is preserved for backwards-compat
// with the dashboard, while the new structured fields surface the same
// data without parse-the-string fragility.
func TestQueueBackpressure_StructuredLastRejectedFieldsPopulated(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.MaxAutomationQueueLength = 1
	state := NewState(cfg)
	issue := domain.Issue{ID: "id-1", Identifier: "ENG-X", State: "Todo"}
	dispatch := AutomationDispatch{
		AutomationID: "rate-limited-fallback",
		ProfileName:  "fallback",
		Trigger: AutomationTriggerContext{
			Type:         config.AutomationTriggerRateLimited,
			AutomationID: "rate-limited-fallback",
		},
	}
	recordAutomationQueueRejected(&state, dispatch, issue, "queue_saturated", time.Now())

	bp := state.AutomationQueueBackpressure
	if bp.LastRejectedAutomationID != "rate-limited-fallback" {
		t.Errorf("LastRejectedAutomationID = %q; want rate-limited-fallback", bp.LastRejectedAutomationID)
	}
	if bp.LastRejectedTrigger != config.AutomationTriggerRateLimited {
		t.Errorf("LastRejectedTrigger = %q; want %q", bp.LastRejectedTrigger, config.AutomationTriggerRateLimited)
	}
	if bp.LastRejectedIdentifier != "ENG-X" {
		t.Errorf("LastRejectedIdentifier = %q; want ENG-X", bp.LastRejectedIdentifier)
	}
}
