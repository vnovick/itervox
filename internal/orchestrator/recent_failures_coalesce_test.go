package orchestrator

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/tracker"
)

// BH-M2-2: a sustained poll outage whose error text varies (a rotating
// address, a request id, a counter) used to append a new ring entry per
// poll — 100 polls, ~50 min at the default interval, and every worker and
// persist failure was flushed out, contradicting the "a sustained poll
// outage cannot flush every other failure out of the ring" invariant.
// Poll failures now coalesce by (kind, source) into one entry that keeps
// the latest message and the total count.
func TestRecentFailures_PollOutageWithRotatingTextKeepsOtherEntries(t *testing.T) {
	tr := &scriptedPollTracker{MemoryTracker: tracker.NewMemoryTracker(nil, nil, nil)}
	o, _ := newPollTestOrchestrator(t, tr)
	state := NewState(o.cfg)
	now := time.Now()
	appendRecentFailure(&state, FailureRecord{Kind: FailureKindWorkerFailed, Identifier: "ENG-1", Message: "worker run failed on attempt 1 (agent error)"}, now)
	appendRecentFailure(&state, FailureRecord{Kind: FailureKindPersist, Source: "paused", Message: "write paused.json: disk full"}, now)

	for i := range 200 {
		tr.setErr(fmt.Errorf("linear: dial tcp 10.0.%d.%d:443: connection refused", i/250, i%250))
		state = o.onTick(context.Background(), state)
	}

	var polls []FailureRecord
	kinds := map[FailureKind]int{}
	for _, f := range state.RecentFailures {
		kinds[f.Kind]++
		if f.Kind == FailureKindTrackerPoll {
			polls = append(polls, f)
		}
	}
	assert.Equal(t, 1, kinds[FailureKindWorkerFailed], "the worker failure must survive the outage")
	assert.Equal(t, 1, kinds[FailureKindPersist], "the persist failure must survive the outage")
	require.Equal(t, 1, len(polls), "the outage is one coalesced entry")
	assert.Equal(t, 200, polls[0].Count)
	assert.Contains(t, polls[0].Message, "10.0.0.199", "the latest message is kept")
	assert.Equal(t, FailureKindTrackerPoll, state.RecentFailures[len(state.RecentFailures)-1].Kind, "the coalesced entry is the newest")
}
