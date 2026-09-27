package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
)

// TestPRMergedDispatchBindsPRFields (CORE-108): the PRMergedEvent the adapter
// builds reaches the automation's Liquid bindings as trigger.pr_url,
// trigger.pr_branch and trigger.pr_base_branch.
func TestPRMergedDispatchBindsPRFields(t *testing.T) {
	o := New(&config.Config{}, nil, nil, nil)
	o.SetPRMergedAutomations([]PRMergedAutomation{{ID: "after-merge", ProfileName: "docs"}})
	issue := domain.Issue{ID: "id-108", Identifier: "ENG-108", State: "In Review"}

	o.DispatchPRMergedAutomations(context.Background(), issue, PRMergedEvent{
		PRURL: "https://github.com/acme/app/pull/7", PRNumber: 7,
		Branch: "itervox/ENG-108", BaseRef: "main", MergedSHA: "cafe108", MergedAt: time.Now(),
	})

	var ev OrchestratorEvent
	select {
	case ev = <-o.events:
	case <-time.After(time.Second):
		t.Fatal("no EventDispatchAutomation sent")
	}
	require.Equal(t, EventDispatchAutomation, ev.Type)
	require.NotNil(t, ev.Automation)
	trigger, ok := automationTriggerBindings(ev.Automation)["trigger"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "https://github.com/acme/app/pull/7", trigger["pr_url"])
	assert.Equal(t, "itervox/ENG-108", trigger["pr_branch"])
	assert.Equal(t, "main", trigger["pr_base_branch"])
}
