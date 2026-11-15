package orchestrator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// TestUndeliveredSessionMessagesAreDroppedWhenTheRunExits: the event loop
// hands an operator message to the run and records it queued; the run's
// exit marks whatever it did not deliver dropped, with a reason, and leaves
// delivered messages alone.
func TestUndeliveredSessionMessagesAreDroppedWhenTheRunExits(t *testing.T) {
	o := New(dependencyAuditConfig(), tracker.NewMemoryTracker(nil, nil, nil), nil, nil)
	state := NewState(dependencyAuditConfig())
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "T", State: "In Progress"}
	entry := &RunEntry{Issue: issue, StartedAt: time.Now(), AgentSessionID: "sess-1"}
	o.startRun(t.Context(), &state, entry)
	state.Running[issue.ID] = entry

	send := func(id string) {
		state = o.handleEvent(t.Context(), state, OrchestratorEvent{
			Type: EventSessionMessage, Identifier: "sess-1",
			SessionMessage: &SessionMessage{ID: id, Identifier: "ENG-1", RunID: entry.RunID, Text: id, Status: SessionMessageQueued},
		})
	}
	send("m1")
	send("m2")
	require.Len(t, state.SessionMessages["ENG-1"], 2)
	assert.Equal(t, agent.SteerMessage{ID: "m1", Text: "m1"}, <-entry.run.steer, "the run receives the message")

	state = o.handleEvent(t.Context(), state, OrchestratorEvent{
		Type: EventSessionMessageDelivered, RunID: entry.RunID,
		SessionDelivery: &SessionDelivery{Identifier: "ENG-1", IDs: []string{"m1"}, Delivery: SteeringLive},
	})
	attempt := 0
	state = o.handleEvent(t.Context(), state, OrchestratorEvent{
		Type: EventWorkerExited, IssueID: issue.ID, RunID: entry.RunID,
		RunEntry: &RunEntry{Issue: issue, TerminalReason: TerminalSucceeded, RetryAttempt: &attempt},
	})
	msgs := state.SessionMessages["ENG-1"]
	assert.Equal(t, SessionMessageDelivered, msgs[0].Status)
	assert.Equal(t, SteeringLive, msgs[0].Delivery)
	assert.Equal(t, SessionMessageDropped, msgs[1].Status)
	assert.NotEmpty(t, msgs[1].Reason)
}

// TestSessionMessageHistoryIsBounded: at most maxSessionMessagesPerIssue
// messages are kept per issue, newest last.
func TestSessionMessageHistoryIsBounded(t *testing.T) {
	state := NewState(dependencyAuditConfig())
	for i := range maxSessionMessagesPerIssue + 5 {
		appendSessionMessage(&state, SessionMessage{ID: string(rune('a' + i)), Identifier: "ENG-1"})
	}
	msgs := state.SessionMessages["ENG-1"]
	require.Len(t, msgs, maxSessionMessagesPerIssue)
	assert.Equal(t, string(rune('a'+5)), msgs[0].ID)
}

// TestSteeringIsLiveOnlyForLocalClaudeWithLiveSteering pins the delivery
// mode the panel shows.
func TestSteeringIsLiveOnlyForLocalClaudeWithLiveSteering(t *testing.T) {
	assert.Equal(t, SteeringLive, steeringFor("claude", "", true))
	assert.Equal(t, SteeringLive, steeringFor("", "", true))
	assert.Equal(t, SteeringResume, steeringFor("claude", "", false))
	assert.Equal(t, SteeringResume, steeringFor("claude", "host-1", true))
	assert.Equal(t, SteeringResume, steeringFor("codex", "", true))
}
