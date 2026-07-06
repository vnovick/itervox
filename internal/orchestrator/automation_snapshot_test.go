package orchestrator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/domain"
)

// F-1 acceptance: recordHistory must propagate AutomationID, TriggerType, and
// CommentCount from the live RunEntry onto the resulting CompletedRun. Manual
// runs (no automation context) must leave the fields zero so the JSON layer
// can omit them.
func TestRecordHistoryPropagatesAutomationFields(t *testing.T) {
	o := &Orchestrator{}
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "Test"}
	startedAt := time.Now().Add(-2 * time.Minute)

	t.Run("automation run carries automation context", func(t *testing.T) {
		o.completedRuns = nil
		live := &RunEntry{
			Issue:        issue,
			Kind:         "automation",
			ProfileName:  "qa-bot",
			AutomationID: "pr-on-input",
			TriggerType:  "input_required",
			CommentCount: 2,
			StartedAt:    startedAt,
		}
		o.recordHistory(live, issue, time.Now(), "succeeded")
		hist := o.RunHistory()
		require.Len(t, hist, 1)
		assert.Equal(t, "pr-on-input", hist[0].AutomationID)
		assert.Equal(t, "input_required", hist[0].TriggerType)
		assert.Equal(t, 2, hist[0].CommentCount)
		assert.Equal(t, "automation", hist[0].Kind)
		assert.Equal(t, "qa-bot", hist[0].ProfileName)
	})

	t.Run("manual run leaves automation fields empty", func(t *testing.T) {
		o.completedRuns = nil
		live := &RunEntry{
			Issue:     issue,
			Kind:      "worker",
			StartedAt: startedAt,
		}
		o.recordHistory(live, issue, time.Now(), "succeeded")
		hist := o.RunHistory()
		require.Len(t, hist, 1)
		assert.Empty(t, hist[0].AutomationID, "manual run must not stamp an AutomationID")
		assert.Empty(t, hist[0].TriggerType, "manual run must not stamp a TriggerType")
		assert.Zero(t, hist[0].CommentCount, "manual run starts with zero comments")
	})
}

// Field-propagation through startAutomationRun is exercised end-to-end by
// TestOrchestratorAutomationDispatchPipelineSnapshotCarriesAutomationContext
// in integration_test.go — that test runs a real orchestrator with a mock
// runner + tracker, dispatches an automation, and asserts that
// orch.Snapshot().Running carries AutomationID + TriggerType. Doing this via
// the integration harness avoids the goroutine-flake risk of calling
// startAutomationRun directly with nil deps.
