package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// CORE-044 — log-level discipline and LastTrackerError.

// scriptedPollTracker fails FetchCandidateIssues with err while err is
// non-nil, and succeeds with an empty candidate list otherwise.
type scriptedPollTracker struct {
	*tracker.MemoryTracker
	mu  sync.Mutex
	err error
}

func (s *scriptedPollTracker) setErr(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
}

func (s *scriptedPollTracker) FetchCandidateIssues(ctx context.Context) ([]domain.Issue, error) {
	s.mu.Lock()
	err := s.err
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return s.MemoryTracker.FetchCandidateIssues(ctx)
}

type capturedRecord struct {
	Level string `json:"level"`
	Msg   string `json:"msg"`
}

// recordsMatching returns the captured records whose msg contains substr.
func recordsMatching(t *testing.T, buf *syncBuffer, substr string) []capturedRecord {
	t.Helper()
	var out []capturedRecord
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec capturedRecord
		require.NoError(t, json.Unmarshal([]byte(line), &rec), line)
		if strings.Contains(rec.Msg, substr) {
			out = append(out, rec)
		}
	}
	return out
}

func newPollTestOrchestrator(t *testing.T, tr *scriptedPollTracker) (*Orchestrator, *syncBuffer) {
	t.Helper()
	cfg := refreshTestCfg()
	cfg.Tracker.Kind = "linear"
	buf := &syncBuffer{}
	o := &Orchestrator{
		tracker: tr,
		events:  make(chan OrchestratorEvent, 16),
		cfg:     cfg,
		Logger:  slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	return o, buf
}

// TestPollFailureEscalatesToError: consecutive tracker outages (anything that
// is not a *tracker.RateLimitedError) log at Warn below the threshold and at
// Error from the threshold on, and are recorded in State.LastTrackerError as
// kind "outage", op "poll". A successful poll clears both.
func TestPollFailureEscalatesToError(t *testing.T) {
	tr := &scriptedPollTracker{MemoryTracker: tracker.NewMemoryTracker(nil, nil, nil)}
	tr.setErr(errors.New("linear: dial tcp: connection refused"))
	o, buf := newPollTestOrchestrator(t, tr)
	state := NewState(o.cfg)

	for i := 1; i <= PollFailureEscalationThreshold; i++ {
		state = o.onTick(context.Background(), state)
		assert.Equal(t, i, state.ConsecutivePollFailures)
	}
	recs := recordsMatching(t, buf, "fetch candidates failed")
	require.Len(t, recs, PollFailureEscalationThreshold)
	for i, rec := range recs {
		want := "WARN"
		if i+1 >= PollFailureEscalationThreshold {
			want = "ERROR"
		}
		assert.Equal(t, want, rec.Level, "poll failure #%d", i+1)
	}
	require.False(t, state.LastTrackerError.At.IsZero(), "LastTrackerError must be recorded")
	assert.Equal(t, TrackerErrorKindOutage, state.LastTrackerError.Kind)
	assert.Equal(t, TrackerErrorOpPoll, state.LastTrackerError.Op)
	assert.Contains(t, state.LastTrackerError.Message, "connection refused")

	tr.setErr(nil)
	state = o.onTick(context.Background(), state)
	assert.Zero(t, state.ConsecutivePollFailures, "a successful poll resets the failure run")
	assert.True(t, state.LastTrackerError.At.IsZero(), "a successful poll clears a poll LastTrackerError")
}

// TestPollRateLimitedDoesNotEscalate: a rate-limited poll is expected and
// bounded, so N of them stay at Warn, do not count toward the escalation
// threshold, and record kind "rate_limited" with the published reset.
func TestPollRateLimitedDoesNotEscalate(t *testing.T) {
	reset := time.Now().Add(10 * time.Minute).UTC().Truncate(time.Second)
	tr := &scriptedPollTracker{MemoryTracker: tracker.NewMemoryTracker(nil, nil, nil)}
	tr.setErr(&tracker.RateLimitedError{Adapter: "linear", ResetAt: reset})
	o, buf := newPollTestOrchestrator(t, tr)
	state := NewState(o.cfg)

	for range PollFailureEscalationThreshold + 2 {
		state = o.onTick(context.Background(), state)
	}
	recs := recordsMatching(t, buf, "fetch candidates")
	require.Len(t, recs, PollFailureEscalationThreshold+2)
	for _, rec := range recs {
		assert.Equal(t, "WARN", rec.Level, "a rate-limited poll must never escalate: %s", rec.Msg)
	}
	assert.Zero(t, state.ConsecutivePollFailures, "rate-limited polls must not count toward the threshold")
	assert.Equal(t, TrackerErrorKindRateLimited, state.LastTrackerError.Kind)
	assert.Equal(t, TrackerErrorOpPoll, state.LastTrackerError.Op)
	assert.True(t, state.LastTrackerError.ResetAt.Equal(reset), "ResetAt carries the published reset")

	// A rate-limited poll in the middle of an outage does not reset the
	// outage run either: it neither counts nor clears.
	tr.setErr(errors.New("boom"))
	state = o.onTick(context.Background(), state)
	tr.setErr(&tracker.RateLimitedError{Adapter: "linear", ResetAt: reset})
	state = o.onTick(context.Background(), state)
	assert.Equal(t, 1, state.ConsecutivePollFailures)
}

// failingStateSink fails every UpdateIssueState, standing in for a tracker
// that rejects the failed-state move.
type failingStateSink struct{}

func (failingStateSink) UpdateIssueState(context.Context, string, string, string, string) error {
	return errors.New("linear: 500 internal server error")
}
func (failingStateSink) CreateComment(context.Context, string, string, string) error { return nil }
func (failingStateSink) CreateKeyedComment(context.Context, string, string, string, string) error {
	return nil
}

// TestFailedStateMoveFailureRecordsLastTrackerError: the failed-state move
// runs on a goroutine that must never touch State; its failure comes back to
// the event loop on the completion event, which records LastTrackerError
// with op "update_state".
func TestFailedStateMoveFailureRecordsLastTrackerError(t *testing.T) {
	cfg := refreshTestCfg()
	o := &Orchestrator{
		tracker: tracker.NewMemoryTracker(nil, nil, nil),
		events:  make(chan OrchestratorEvent, 4),
		cfg:     cfg,
		sink:    failingStateSink{},
	}
	state := NewState(cfg)
	state = o.asyncDiscardAndTransitionTo(state, "id-1", "ENG-1", "Failed", "In Progress")
	o.discardWg.Wait()

	var ev OrchestratorEvent
	select {
	case ev = <-o.events:
	case <-time.After(5 * time.Second):
		t.Fatal("no completion event from the failed-state move goroutine")
	}
	require.Equal(t, EventDiscardComplete, ev.Type)
	assert.True(t, state.LastTrackerError.At.IsZero(), "the goroutine must not have touched State")

	state = o.handleEvent(context.Background(), state, ev)
	assert.Equal(t, TrackerErrorOpUpdateState, state.LastTrackerError.Op)
	assert.Equal(t, TrackerErrorKindOutage, state.LastTrackerError.Kind)
	assert.Contains(t, state.LastTrackerError.Message, "500 internal server error")
	assert.NotContains(t, state.DiscardingIdentifiers, "ENG-1", "the completion must still release the issue")

	// A poll success does not clear a write failure: it says nothing about
	// whether the write path works.
	o.tracker = &scriptedPollTracker{MemoryTracker: tracker.NewMemoryTracker(nil, nil, nil)}
	state = o.onTick(context.Background(), state)
	assert.Equal(t, TrackerErrorOpUpdateState, state.LastTrackerError.Op)
}

// TestLastTrackerErrorSurvivesClone: the snapshot path clones State, and the
// field must travel with it.
func TestLastTrackerErrorSurvivesClone(t *testing.T) {
	s := NewState(refreshTestCfg())
	s.LastTrackerError = TrackerErrorInfo{At: time.Now(), Op: TrackerErrorOpPoll, Kind: TrackerErrorKindOutage, Message: "x"}
	s.ConsecutivePollFailures = 2
	c := s.Clone()
	assert.Equal(t, s.LastTrackerError, c.LastTrackerError)
	assert.Equal(t, 2, c.ConsecutivePollFailures)
}

// CORE-167: the WARN "turn failed" line keeps the agent-reported message
// whole and only the tail of stderr.
func TestTurnFailedWarnLineBoundsStderr(t *testing.T) {
	stderr := strings.Repeat("noise line\n", 1000) + "ROOT CAUSE: disk full"
	got := boundedTurnFailureLog(errors.New("turn 1: agent said no | stderr: " + stderr))
	assert.True(t, strings.HasPrefix(got, "turn 1: agent said no | stderr: [...truncated...]"), got[:60])
	assert.True(t, strings.HasSuffix(got, "ROOT CAUSE: disk full"))
	assert.LessOrEqual(t, len(got), len("turn 1: agent said no | stderr: [...truncated...]")+turnFailedLogStderrTail)

	short := "turn 1: x | stderr: small"
	assert.Equal(t, short, boundedTurnFailureLog(errors.New(short)))
}
