package orchestrator

import (
	"testing"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
)

// TestPROpenedCountersIncrementOnDispatchAndDedup — codex-B4 named.
// Asserts AutomationDispatchesPROpenedTotal increments on the first
// successful dispatch and AutomationDroppedPROpenedDedupTotal increments on
// the second.
func TestPROpenedCountersIncrementOnDispatchAndDedup(t *testing.T) {
	cfg := &config.Config{}
	state := NewState(cfg)
	issue := domain.Issue{Identifier: "ENG-1", State: "In Review"}
	dispatch := AutomationDispatch{
		AutomationID: "pr-opened-auto",
		ProfileName:  "reviewer",
		Trigger: AutomationTriggerContext{
			Type:         config.AutomationTriggerPROpened,
			AutomationID: "pr-opened-auto",
			PRURL:        "https://github.com/x/y/pull/42",
		},
	}

	if !claimPROpenedDedup(&state, issue, dispatch) {
		t.Fatal("first dispatch should record")
	}
	if state.AutomationDispatchesPROpenedTotal != 1 {
		t.Errorf("dispatches counter = %d; want 1", state.AutomationDispatchesPROpenedTotal)
	}
	if claimPROpenedDedup(&state, issue, dispatch) {
		t.Fatal("second dispatch must be deduped")
	}
	if state.AutomationDroppedPROpenedDedupTotal != 1 {
		t.Errorf("dedup-drops counter = %d; want 1", state.AutomationDroppedPROpenedDedupTotal)
	}
}
