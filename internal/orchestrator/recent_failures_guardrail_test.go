package orchestrator

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRecentFailures_GuardrailRefusalFloodIsOneEntry: an agent that
// repeats one refused command shape (84 hook_bypass refusals in a minute,
// each naming a different file) flooded the ring and evicted every other
// failure. A guardrail refusal now coalesces by (kind, issue, guardrail
// kind) like a poll failure, keeping the latest command and a count; a
// different guardrail kind or issue is still its own entry.
func TestRecentFailures_GuardrailRefusalFloodIsOneEntry(t *testing.T) {
	o, state := failureTestOrch()
	now := time.Now()
	ctx := context.Background()
	state = o.handleEvent(ctx, state, failureEvent(FailureKindPersist, "disk full", now))
	refusal := func(issue, kind, cmd string, at time.Time) OrchestratorEvent {
		return OrchestratorEvent{Type: EventFailureRecorded, Identifier: issue, Failure: &FailureRecord{
			Kind: FailureKindGuardrail, Identifier: issue, Source: kind, Message: "refused: " + cmd, OccurredAt: at,
		}}
	}
	for i := range RecentFailuresCap + 20 {
		state = o.handleEvent(ctx, state, refusal("ENG-1", "hook_bypass",
			fmt.Sprintf("git -c core.hooksPath=/dev/null commit -m %d", i), now.Add(time.Duration(i)*time.Millisecond)))
	}
	state = o.handleEvent(ctx, state, refusal("ENG-1", "push_protected", "git push origin main", now))
	state = o.handleEvent(ctx, state, refusal("ENG-2", "hook_bypass", "git -c core.hooksPath=/dev/null push", now))

	require.Len(t, state.RecentFailures, 4, "the earlier failure survives the flood: %+v", state.RecentFailures)
	assert.Equal(t, FailureKindPersist, state.RecentFailures[0].Kind)
	flood := state.RecentFailures[1]
	assert.Equal(t, "hook_bypass", flood.Source)
	assert.Equal(t, "ENG-1", flood.Identifier)
	assert.Equal(t, RecentFailuresCap+20, flood.Count)
	assert.Contains(t, flood.Message, fmt.Sprintf("-m %d", RecentFailuresCap+19), "the latest command")
	assert.Equal(t, "push_protected", state.RecentFailures[2].Source)
	assert.Equal(t, "ENG-2", state.RecentFailures[3].Identifier)
}
