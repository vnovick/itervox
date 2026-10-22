package orchestrator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// TestProvideInputViaSlackIsRecordedOnTheFeed (#230): a human reply the
// hub's Slack app relays is an ordinary human reply (it resumes the issue)
// whose input_answered feed event says via "slack"; an unknown channel is
// refused before anything is queued.
func TestProvideInputViaSlackIsRecordedOnTheFeed(t *testing.T) {
	cfg := testConfig()
	cfg.Agent.MaxConcurrentAgents = 0
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "Pick a colour", State: "In Progress"}
	o := New(cfg, tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates), &blockedRunner{}, nil)
	state := NewState(cfg)
	state.InputRequiredIssues["ENG-1"] = &InputRequiredEntry{
		IssueID: "id1", Identifier: "ENG-1", SessionID: "s1", Context: "Which colour?", Backend: "claude", QueuedAt: time.Now(),
	}

	require.Error(t, o.ProvideInputVia("ENG-1", "blue", "sms"), "only the dashboard and slack are human channels")
	require.NoError(t, o.ProvideInputVia("ENG-1", "blue", InputViaSlack))
	ev := <-o.events
	require.Equal(t, EventProvideInput, ev.Type)
	assert.False(t, ev.FromAgent, "a Slack reply is a human reply")
	state = o.handleEvent(t.Context(), state, ev)

	assert.Nil(t, state.InputRequiredIssues["ENG-1"], "the reply answered the question")
	require.NotEmpty(t, state.Feed)
	last := state.Feed[len(state.Feed)-1]
	assert.Equal(t, FeedInputAnswered, last.Kind)
	assert.Equal(t, "slack", last.Payload.Via)
}
