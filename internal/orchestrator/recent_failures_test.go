package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/metrics"
	"github.com/vnovick/itervox/internal/tracker"
)

// CORE-046 — State.RecentFailures: one bounded, event-loop-owned ring of
// operator-relevant failures (worker failed/stalled, tracker poll/write
// failures, persistence write errors, outbox delivery failures, recovered
// panics, web client errors).

func failureTestOrch() (*Orchestrator, State) {
	cfg := &config.Config{}
	cfg.Tracker.ActiveStates = []string{"Todo"}
	cfg.Agent.MaxConcurrentAgents = 2
	cfg.Agent.MaxRetries = 3
	return New(cfg, nil, nil, nil), NewState(cfg)
}

func failureEvent(kind FailureKind, msg string, occurred time.Time) OrchestratorEvent {
	return OrchestratorEvent{Type: EventFailureRecorded, Failure: &FailureRecord{
		Kind: kind, Message: msg, OccurredAt: occurred,
	}}
}

func TestRecentFailures_BoundedAndCloned(t *testing.T) {
	o, state := failureTestOrch()
	base := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	for i := range RecentFailuresCap + 1 {
		state = o.handleEvent(context.Background(), state,
			failureEvent(FailureKindPersist, fmt.Sprintf("write failed #%d", i), base.Add(time.Duration(i)*time.Second)))
	}
	require.Len(t, state.RecentFailures, RecentFailuresCap, "101 events leave 100 entries")
	assert.Equal(t, "write failed #1", state.RecentFailures[0].Message, "the oldest entry (#0) was evicted")
	assert.Equal(t, fmt.Sprintf("write failed #%d", RecentFailuresCap), state.RecentFailures[RecentFailuresCap-1].Message)

	clone := state.Clone()
	clone.RecentFailures[0].Message = "mutated"
	clone.RecentFailures = append(clone.RecentFailures[:1], clone.RecentFailures[2:]...)
	assert.Equal(t, "write failed #1", state.RecentFailures[0].Message, "Clone() copy is independent")
	assert.Equal(t, "write failed #2", state.RecentFailures[1].Message, "Clone() copy is independent")
}

func TestRecentFailures_OrderedByRecordedAtNotOccurredAt(t *testing.T) {
	o, state := failureTestOrch()
	now := time.Now()
	state = o.handleEvent(context.Background(), state, failureEvent(FailureKindOutbox, "newer", now))
	// An off-loop producer delivers an OLDER failure after the newer one.
	state = o.handleEvent(context.Background(), state, failureEvent(FailureKindOutbox, "older", now.Add(-time.Hour)))
	require.Len(t, state.RecentFailures, 2)
	last := state.RecentFailures[1]
	assert.Equal(t, "older", last.Message, "appended last = newest ring entry, whatever its occurredAt")
	assert.True(t, last.OccurredAt.Before(state.RecentFailures[0].OccurredAt))
	assert.False(t, last.RecordedAt.Before(state.RecentFailures[0].RecordedAt), "recordedAt is set by the loop, monotonic")
}

func TestRecentFailures_CoalescesConsecutiveRepeats(t *testing.T) {
	o, state := failureTestOrch()
	now := time.Now()
	for i := range 3 {
		state = o.handleEvent(context.Background(), state, failureEvent(FailureKindPersist, "disk full", now.Add(time.Duration(i)*time.Second)))
	}
	require.Len(t, state.RecentFailures, 1, "a retrying failure must not flood the ring")
	assert.Equal(t, 3, state.RecentFailures[0].Count)
	assert.Equal(t, now.Add(2*time.Second), state.RecentFailures[0].OccurredAt, "occurredAt tracks the latest repeat")
}

func TestRecentFailures_RedactsThenTruncates(t *testing.T) {
	o, state := failureTestOrch()
	secret := "ghp_" + strings.Repeat("A1b2C3d4", 5)
	msg := "push failed with token " + secret + " " + strings.Repeat("x", 4096)
	state = o.handleEvent(context.Background(), state, failureEvent(FailureKindOutbox, msg, time.Now()))
	require.Len(t, state.RecentFailures, 1)
	got := state.RecentFailures[0].Message
	assert.NotContains(t, got, secret)
	assert.LessOrEqual(t, len(got), failureMessageMaxBytes)
}

func TestRecentFailures_WorkerFailureIsClassifiedNotRaw(t *testing.T) {
	o, state := failureTestOrch()
	raw := "turn 2 failed: stderr: OPENAI_API_KEY=sk-proj-abcdefghijklmnopqrstuvwxyz0123456789 prompt text here"
	state = o.handleEvent(context.Background(), state, OrchestratorEvent{
		Type: EventWorkerExited, IssueID: "id1", Error: errors.New(raw),
		RunEntry: &RunEntry{
			Issue:          domain.Issue{ID: "id1", Identifier: "ENG-1", State: "Todo"},
			TerminalReason: TerminalFailed, RetryAttempt: intPtr(0),
		},
	})
	require.Len(t, state.RecentFailures, 1)
	f := state.RecentFailures[0]
	assert.Equal(t, FailureKindWorkerFailed, f.Kind)
	assert.Equal(t, "ENG-1", f.Identifier)
	assert.NotContains(t, f.Message, "sk-proj", "never the agent's failure text")
	assert.NotContains(t, f.Message, "prompt text", "never the agent's failure text")
	assert.Contains(t, f.Message, "attempt 1")
}

func TestRecentFailures_StalledAndCancelledExits(t *testing.T) {
	o, state := failureTestOrch()
	state = o.handleEvent(context.Background(), state, OrchestratorEvent{
		Type: EventWorkerExited, IssueID: "id1",
		RunEntry: &RunEntry{Issue: domain.Issue{ID: "id1", Identifier: "ENG-1"}, TerminalReason: TerminalStalled},
	})
	state = o.handleEvent(context.Background(), state, OrchestratorEvent{
		Type: EventWorkerExited, IssueID: "id2", Error: context.Canceled,
		RunEntry: &RunEntry{Issue: domain.Issue{ID: "id2", Identifier: "ENG-2"}, TerminalReason: TerminalFailed},
	})
	require.Len(t, state.RecentFailures, 1, "a cancelled worker is not a failure")
	assert.Equal(t, FailureKindWorkerStalled, state.RecentFailures[0].Kind)
	assert.Equal(t, "ENG-1", state.RecentFailures[0].Identifier)
}

func TestRecentFailures_TrackerPollAndWriteFailures(t *testing.T) {
	o, state := failureTestOrch()
	now := time.Now()
	state = o.recordPollFailure(state, errors.New("linear: 502 bad gateway"), now)
	state = o.recordPollFailure(state, &tracker.RateLimitedError{ResetAt: now.Add(time.Minute)}, now)
	state = o.handleEvent(context.Background(), state, OrchestratorEvent{
		Type: EventDiscardComplete, Identifier: "ENG-3", Error: errors.New("linear: update failed"),
	})
	require.Len(t, state.RecentFailures, 2, "a rate-limited poll is expected and bounded, not a failure")
	assert.Equal(t, FailureKindTrackerPoll, state.RecentFailures[0].Kind)
	assert.Contains(t, state.RecentFailures[0].Message, "502")
	assert.Equal(t, FailureKindTrackerWrite, state.RecentFailures[1].Kind)
	assert.Equal(t, "ENG-3", state.RecentFailures[1].Identifier)
}

func TestRecentFailures_RecordFailureDropIsCountedNotRetried(t *testing.T) {
	o, _ := failureTestOrch()
	for len(o.events) < cap(o.events) {
		o.events <- OrchestratorEvent{Type: EventWorkerUpdate}
	}
	before := metrics.EventsDroppedCount()
	ok := o.RecordFailure(FailureRecord{Kind: FailureKindOutbox, Message: "x"})
	assert.False(t, ok)
	assert.Equal(t, int64(1), o.FailureEventsDropped())
	assert.Equal(t, before+1, metrics.EventsDroppedCount())
	assert.Len(t, o.events, cap(o.events), "a dropped failure is never re-sent")
}

func TestRecentFailures_PersistWriteErrorIsRecorded(t *testing.T) {
	o, _ := failureTestOrch()
	o.SetPausedFile(filepath.Join(t.TempDir(), "paused.json"))
	o.persistRetryInterval = time.Hour
	o.persistWriteFile = func(string, []byte, fs.FileMode) error { return errors.New("EIO: disk on fire") }
	o.startPersistence()
	defer o.stopPersistence()
	st := NewState(&config.Config{})
	st.PausedIdentifiers["ENG-1"] = "u1"
	o.storeSnap(st)
	require.Eventually(t, func() bool { return len(o.events) > 0 }, 5*time.Second, 5*time.Millisecond)
	ev := <-o.events
	require.Equal(t, EventFailureRecorded, ev.Type)
	require.NotNil(t, ev.Failure)
	assert.Equal(t, FailureKindPersist, ev.Failure.Kind)
	assert.Contains(t, ev.Failure.Message, "EIO")
	assert.Equal(t, "paused", ev.Failure.Source)
}

func TestRecentFailures_RecoveredPanicIsRecorded(t *testing.T) {
	o, _ := failureTestOrch()
	onPanic := o.withPanicFailure("discard-transition", "ENG-9", nil)
	func() {
		defer RecoverGoroutine("discard-transition", "ENG-9", onPanic)
		panic("boom secret-ish value")
	}()
	require.Len(t, o.events, 1)
	ev := <-o.events
	require.NotNil(t, ev.Failure)
	assert.Equal(t, FailureKindPanic, ev.Failure.Kind)
	assert.Equal(t, "ENG-9", ev.Failure.Identifier)
	assert.NotContains(t, ev.Failure.Message, "boom", "the panic value stays in the daemon log")
}

func TestRecentFailures_SeededRingSurvivesIntoRun(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tracker.ActiveStates = []string{"Todo"}
	cfg.Polling.IntervalMs = 60_000
	o := New(cfg, tracker.NewMemoryTracker(nil, []string{"Todo"}, []string{"Done"}), nil, nil)
	o.SeedRecentFailures([]FailureRecord{{Kind: FailureKindPersist, Message: "from previous run", OccurredAt: time.Now(), RecordedAt: time.Now(), Count: 1}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = o.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	require.Eventually(t, func() bool {
		fs := o.Snapshot().RecentFailures
		return len(fs) >= 1 && fs[0].Message == "from previous run"
	}, 5*time.Second, 10*time.Millisecond)
}
