package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
)

// okStateSink is a write sink whose writes succeed immediately.
type okStateSink struct{ recordingCommentSink }

func discardHarness(t *testing.T) (*Orchestrator, State, domain.Issue) {
	t.Helper()
	cfg := testConfig()
	o := New(cfg, nil, nil, nil)
	o.SetWriteSink(&okStateSink{})
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "Stuck", State: "In Progress"}
	return o, NewState(cfg), issue
}

// takeDiscardComplete receives the discard goroutine's completion from the
// channel WITHOUT handing it to the loop — the lost-event case.
func takeDiscardComplete(t *testing.T, o *Orchestrator) OrchestratorEvent {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-o.events:
			if ev.Type != EventDiscardComplete {
				continue // e.g. the status-change record the goroutine also sends
			}
			o.discardWg.Wait()
			return ev
		case <-deadline:
			t.Fatal("discard goroutine sent no completion")
			return OrchestratorEvent{}
		}
	}
}

// TestDiscardingIdentifiersExpire (CORE-109): a marker whose
// EventDiscardComplete is lost no longer blocks dispatch until restart — the
// janitor expires it after discardMarkerTTL, and not before.
func TestDiscardingIdentifiersExpire(t *testing.T) {
	o, state, issue := discardHarness(t)
	state = o.asyncDiscardAndTransitionTo(state, issue.ID, issue.Identifier, "Failed", "In Progress")
	_ = takeDiscardComplete(t, o) // dropped: never reaches handleEvent
	require.Equal(t, "discarding", IneligibleReason(issue, state, o.cfg))

	marker := state.DiscardingIdentifiers[issue.Identifier]
	assert.Zero(t, expireDiscardMarkers(&state, marker.At.Add(discardMarkerTTL-time.Second)),
		"a marker younger than discardMarkerTTL must be kept")
	require.Equal(t, "discarding", IneligibleReason(issue, state, o.cfg))

	assert.Equal(t, 1, expireDiscardMarkers(&state, marker.At.Add(discardMarkerTTL+time.Second)))
	assert.NotEqual(t, "discarding", IneligibleReason(issue, state, o.cfg))

	// The TTL is derived from the goroutine's real bound: one direct-sink
	// attempt on a fresh postRunTimeout context plus the 30 s completion send.
	assert.Greater(t, discardMarkerTTL, postRunTimeout+30*time.Second)
}

// TestStaleDiscardCompleteDoesNotClearNewerMarker (CORE-109): a completion
// that arrives after its marker expired and a new discard started for the
// same identifier must not release the newer marker.
func TestStaleDiscardCompleteDoesNotClearNewerMarker(t *testing.T) {
	o, state, issue := discardHarness(t)
	state = o.asyncDiscardAndTransitionTo(state, issue.ID, issue.Identifier, "Failed", "In Progress")
	gen1 := state.DiscardingIdentifiers[issue.Identifier].Gen
	late := takeDiscardComplete(t, o)
	require.Equal(t, gen1, late.DiscardGen)

	require.Equal(t, 1, expireDiscardMarkers(&state, time.Now().Add(discardMarkerTTL+time.Minute)))
	state = o.asyncDiscardAndTransitionTo(state, issue.ID, issue.Identifier, "Failed", "In Progress")
	gen2 := state.DiscardingIdentifiers[issue.Identifier].Gen
	current := takeDiscardComplete(t, o)
	require.NotEqual(t, gen1, gen2)

	state = o.handleEvent(context.Background(), state, late)
	require.Contains(t, state.DiscardingIdentifiers, issue.Identifier, "the stale Gen-1 completion must not clear the Gen-2 marker")
	assert.Equal(t, gen2, state.DiscardingIdentifiers[issue.Identifier].Gen)

	state = o.handleEvent(context.Background(), state, current)
	assert.NotContains(t, state.DiscardingIdentifiers, issue.Identifier, "the matching completion releases the issue")
}
