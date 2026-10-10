package orchestrator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// TestPendingExitsOnlyForTheCurrentRun: deliverExit marks the issue, the
// mark counts only for a run that started before it (a leftover mark from an
// earlier run whose exit was dropped is ignored), and handling the exit
// clears it.
func TestPendingExitsOnlyForTheCurrentRun(t *testing.T) {
	o := New(dependencyAuditConfig(), tracker.NewMemoryTracker(nil, nil, nil), nil, nil)
	state := NewState(dependencyAuditConfig())
	state.Running["id1"] = &RunEntry{Issue: domain.Issue{ID: "id1", Identifier: "ENG-1"}, StartedAt: time.Now().Add(-time.Minute)}
	assert.Empty(t, o.pendingExits(state))

	o.exitsSent.Store("id1", time.Now())
	assert.Equal(t, map[string]bool{"id1": true}, o.pendingExits(state))

	state.Running["id1"].StartedAt = time.Now().Add(time.Second) // a later run
	assert.Empty(t, o.pendingExits(state), "a mark from before this run started is stale")

	state = o.handleEvent(t.Context(), state, OrchestratorEvent{Type: EventWorkerExited, IssueID: "id1"})
	_, marked := o.exitsSent.Load("id1")
	assert.False(t, marked, "handling the exit clears the mark")
}
