package orchestrator

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
)

// TestAutomationQueueNotRewrittenWhenUnchanged (#210): storeSnap runs on
// every event-loop turn, but the automation queue (with the dependency
// ledger and cron cursors it carries) is encoded and handed to the ledger
// only when one of those persisted fields changed — including an in-place
// edit of an entry or an audit row.
func TestAutomationQueueNotRewrittenWhenUnchanged(t *testing.T) {
	cfg := &config.Config{}
	path := filepath.Join(t.TempDir(), "automation_queue.json")
	o := New(cfg, nil, nil, nil)
	o.SetAutomationQueueFile(path)

	blocker := "ENG-1"
	todo := "Todo"
	now := time.Now()
	state := NewState(cfg)
	state.AutomationQueue = map[string]*AutomationQueueEntry{
		"q1": {ID: "q1", AutomationID: "rule", Issue: domain.Issue{ID: "ENG-2", Identifier: "ENG-2", Labels: []string{"a"},
			BlockedBy: []domain.BlockerRef{{Identifier: &blocker, State: &todo}}}, QueuedAt: now},
	}
	state.AutomationQueueOrder = []string{"q1"}
	state.DependencyAudit = map[string]*DependencyAuditEntry{
		"ENG-2": {Identifier: "ENG-2", IssueState: "Todo", Status: DependencyAuditBlocked,
			BlockedBy: []domain.BlockerRef{{Identifier: &blocker, State: &todo}}, LastAuditedAt: now},
	}
	state.CronCursors = map[string]string{"rule": "ENG-2"}

	o.storeSnap(state)
	require.Equal(t, 1, o.automationQueueEncodes)
	first, err := os.ReadFile(path)
	require.NoError(t, err)

	for range 5 {
		o.storeSnap(state)
	}
	state.Running["ENG-9"] = &RunEntry{Issue: domain.Issue{ID: "ENG-9", Identifier: "ENG-9"}}
	o.storeSnap(state) // a change outside the queue
	assert.Equal(t, 1, o.automationQueueEncodes, "an unchanged queue is not encoded again")
	again, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, first, again)

	steps := []struct {
		name   string
		mutate func()
	}{
		{"an entry edited in place", func() { state.AutomationQueue["q1"].AttemptCount++ }},
		{"an audit row edited in place", func() { state.DependencyAudit["ENG-2"].LastAuditedAt = now.Add(time.Minute) }},
		{"the order", func() { state.AutomationQueueOrder = append(state.AutomationQueueOrder, "q2") }},
		{"backpressure", func() { state.AutomationQueueBackpressure.RejectedSinceBoot++ }},
		{"the transition seq", func() { state.DependencyTransitionSeq++ }},
		{"a cron cursor", func() { state.CronCursors["rule"] = "ENG-3" }},
		{"an entry's issue blocker", func() { done := "Done"; state.AutomationQueue["q1"].Issue.BlockedBy[0].State = &done }},
	}
	for i, step := range steps {
		step.mutate()
		o.storeSnap(state)
		o.storeSnap(state)
		assert.Equal(t, i+2, o.automationQueueEncodes, "%s is a change, saved once", step.name)
	}

	restored := New(cfg, nil, nil, nil)
	restored.SetAutomationQueueFile(path)
	got := restored.loadAutomationQueueFromDisk(NewState(cfg))
	assert.Equal(t, "ENG-3", got.CronCursors["rule"], "the last change reached disk")
	require.NotNil(t, got.DependencyAudit["ENG-2"])
	assert.True(t, got.DependencyAudit["ENG-2"].LastAuditedAt.Equal(now.Add(time.Minute)), "an in-place audit edit reached disk")
}
