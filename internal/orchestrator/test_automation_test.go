package orchestrator

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/config"
)

// T-10: TestAutomation looks up the rule by ID, then queues an
// EventDispatchAutomation tagged with TriggerType="test". This test asserts
// the queued event carries the right rule id, profile, and trigger-type
// stamp.
func TestTestAutomationQueuesEventWithTestTriggerType(t *testing.T) {
	cfg := &config.Config{}
	cfg.Automations = []config.AutomationConfig{
		{
			ID:           "pr-on-input",
			Enabled:      true,
			Profile:      "reviewer",
			Instructions: "Audit the PR.",
			Trigger:      config.AutomationTriggerConfig{Type: config.AutomationTriggerInputRequired},
		},
	}
	o := New(cfg, nil, nil, nil)

	require.NoError(t, o.TestAutomation(t.Context(), "pr-on-input", "ENG-1"))

	// Drain the event channel and inspect the queued dispatch.
	select {
	case ev := <-o.events:
		require.Equal(t, EventDispatchAutomation, ev.Type)
		require.NotNil(t, ev.Issue)
		require.NotNil(t, ev.Automation)
		assert.Equal(t, "ENG-1", ev.Issue.Identifier)
		assert.Equal(t, "pr-on-input", ev.Automation.AutomationID)
		assert.Equal(t, "reviewer", ev.Automation.ProfileName)
		assert.Equal(t, TestAutomationTriggerType, ev.Automation.Trigger.Type,
			"test fires must be tagged so the timeline filter chip can include them")
	default:
		t.Fatal("expected an EventDispatchAutomation on the events channel")
	}
}

func TestTestAutomationReturnsErrorForUnknownRule(t *testing.T) {
	o := New(&config.Config{}, nil, nil, nil)
	err := o.TestAutomation(t.Context(), "does-not-exist", "ENG-1")
	require.Error(t, err)
	assert.True(t, errors.Is(err, errAutomationNotFound))
}

func TestTestAutomationRejectsEmptyArguments(t *testing.T) {
	o := New(&config.Config{}, nil, nil, nil)

	require.Error(t, o.TestAutomation(t.Context(), "", "ENG-1"))
	require.Error(t, o.TestAutomation(t.Context(), "rule", ""))
}
