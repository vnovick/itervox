package orchestrator

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/outbox"
	"github.com/vnovick/itervox/internal/tracker"
)

// blockingCommentTracker is a memory tracker whose CreateComment blocks until
// release is closed — a tracker API that hangs.
type blockingCommentTracker struct {
	*tracker.MemoryTracker
	entered chan string
	release chan struct{}
}

func (b *blockingCommentTracker) CreateComment(ctx context.Context, issueID, body string) (*domain.Comment, error) {
	b.entered <- body
	<-b.release
	return b.MemoryTracker.CreateComment(ctx, issueID, body)
}

// recordingCommentSink records every comment the orchestrator writes.
type recordingCommentSink struct {
	mu       sync.Mutex
	comments []string
}

func (r *recordingCommentSink) UpdateIssueState(context.Context, string, string, string, string) error {
	return nil
}

func (r *recordingCommentSink) CreateComment(_ context.Context, _, _ string, body string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.comments = append(r.comments, body)
	return nil
}

func (r *recordingCommentSink) CreateKeyedComment(ctx context.Context, issueID, identifier, _ string, body string) error {
	return r.CreateComment(ctx, issueID, identifier, body)
}

func (r *recordingCommentSink) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.comments...)
}

func maxRetriesConfig() *config.Config {
	cfg := testConfig()
	cfg.Agent.MaxRetries = 1
	return cfg
}

// exhaustedExit is the worker exit that crosses max_retries (attempt 1,
// max_retries 1).
func exhaustedExit(issue domain.Issue, errText string) OrchestratorEvent {
	return OrchestratorEvent{
		Type:    EventWorkerExited,
		IssueID: issue.ID,
		Error:   errors.New(errText),
		RunEntry: &RunEntry{
			Issue:          issue,
			TerminalReason: TerminalFailed,
			RetryAttempt:   intPtr(1),
			StartedAt:      time.Now(),
		},
	}
}

// TestMaxRetriesExhaustedComment_DirectSinkDoesNotBlockLoop (CORE-103 a): with
// the direct sink (tracker.outbox: false) the exhaustion comment is network
// I/O, so a hung tracker must not hold the event loop.
func TestMaxRetriesExhaustedComment_DirectSinkDoesNotBlockLoop(t *testing.T) {
	cfg := maxRetriesConfig()
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "Flaky", State: "In Progress"}
	bt := &blockingCommentTracker{
		MemoryTracker: tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates),
		entered:       make(chan string, 4),
		release:       make(chan struct{}),
	}
	o := New(cfg, bt, &blockedRunner{}, nil)
	require.False(t, o.sinkEnqueuesLocally(), "precondition: direct sink")
	state := NewState(cfg)
	state.Running[issue.ID] = &RunEntry{Issue: issue, StartedAt: time.Now()}

	released := false
	defer func() {
		if !released {
			close(bt.release)
		}
		o.commentWg.Wait()
	}()

	returned := make(chan State, 1)
	go func() { returned <- o.handleEvent(context.Background(), state, exhaustedExit(issue, "boom")) }() // test driver
	select {
	case state = <-returned:
	case <-time.After(time.Second):
		t.Fatal("EventWorkerExited handler blocked on the tracker comment (event loop stalled)")
	}
	// The loop is free: a subsequent event is processed while the comment is
	// still blocked in the tracker.
	next := make(chan struct{})
	go func() { // test driver
		o.handleEvent(context.Background(), state, OrchestratorEvent{
			Type: EventWorkerExited, IssueID: "other",
			Error:    errors.New("late exit"),
			RunEntry: &RunEntry{Issue: domain.Issue{ID: "other", Identifier: "ENG-2"}, TerminalReason: TerminalFailed},
		})
		close(next)
	}()
	select {
	case <-next:
	case <-time.After(time.Second):
		t.Fatal("subsequent event not processed within 1s")
	}
	select {
	case body := <-bt.entered:
		assert.Contains(t, body, "maximum retries exhausted")
	case <-time.After(5 * time.Second):
		t.Fatal("the exhaustion comment was never attempted")
	}
	close(bt.release)
	released = true
	o.commentWg.Wait()
}

// TestMaxRetriesExhaustedComment_OutboxSinkEnqueuesBeforeHandlerReturns
// (CORE-103 a): with the outbox sink the comment is a local enqueue done on
// the loop, so it is durable (and ordered) by the time the handler returns.
func TestMaxRetriesExhaustedComment_OutboxSinkEnqueuesBeforeHandlerReturns(t *testing.T) {
	cfg := maxRetriesConfig()
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "Flaky", State: "In Progress"}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	o := New(cfg, mt, &blockedRunner{}, nil)
	ob, err := outbox.New(t.TempDir() + "/outbox.json")
	require.NoError(t, err)
	o.SetWriteSink(NewOutboxWriteSink(ob))
	o.SetOutbox(ob)
	state := NewState(cfg)
	state.Running[issue.ID] = &RunEntry{Issue: issue, StartedAt: time.Now()}

	// CORE-167: the public comment body is redacted.
	o.handleEvent(context.Background(), state, exhaustedExit(issue, "boom token=lin_api_abcdefghijklmnopqrstuvwxyz0123456789ABCD"))

	queued := ob.Snapshot()
	require.Len(t, queued, 1, "the exhaustion comment must be in the outbox when the handler returns")
	assert.Equal(t, outbox.KindCreateComment, queued[0].Kind)
	assert.Contains(t, queued[0].Body, "maximum retries exhausted")
	assert.Contains(t, queued[0].Body, tracker.ManagedCommentMarker)
	assert.NotContains(t, queued[0].Body, "lin_api_abcdefghijklmnopqrstuvwxyz0123456789ABCD")
	o.commentWg.Wait()
}

// TestRateLimitedSwitch_SingleCombinedComment (CORE-103 b): an accepted
// rate_limited switch on retry exhaustion yields ONE managed comment that
// carries both the exhaustion and the switch; with no matching rule the
// exhaustion comment is still posted.
func TestRateLimitedSwitch_SingleCombinedComment(t *testing.T) {
	const rlErr = "rate_limit_exceeded: 429 Too Many Requests"
	run := func(t *testing.T, withRule bool) []string {
		cfg := maxRetriesConfig()
		cfg.Agent.MaxSwitchesPerIssuePerWindow = 5
		cfg.Agent.SwitchWindowHours = 6
		cfg.Agent.Profiles = map[string]config.AgentProfile{"codex-coder": {}}
		issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "Throttled", State: "In Progress"}
		mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
		o := New(cfg, mt, &blockedRunner{}, nil)
		sink := &recordingCommentSink{}
		o.SetWriteSink(sink)
		if withRule {
			o.SetRateLimitedAutomations([]RateLimitedAutomation{{
				ID: "switch-to-codex", ProfileName: "codex-coder",
				SwitchToProfile: "codex-coder", SwitchToBackend: "codex", AutoResume: true,
			}})
		}
		state := NewState(cfg)
		// A busy slot makes the recovery queue instead of starting a worker.
		state.Running["busy"] = &RunEntry{Issue: domain.Issue{ID: "busy", Identifier: "BUSY-1"}}
		state.Running[issue.ID] = &RunEntry{Issue: issue, StartedAt: time.Now(), ProfileName: "claude-coder", Backend: "claude"}
		state = o.handleEvent(context.Background(), state, exhaustedExit(issue, rlErr))
		if withRule {
			require.Equal(t, "codex-coder", state.IssueProfiles["ENG-1"], "precondition: the switch was accepted")
		}
		o.commentWg.Wait()
		return sink.all()
	}

	t.Run("accepted switch", func(t *testing.T) {
		comments := run(t, true)
		require.Len(t, comments, 1, "exactly one managed comment per accepted switch, got %q", comments)
		assert.Contains(t, comments[0], "rate-limit auto-switch")
		assert.Contains(t, comments[0], "maximum retries exhausted")
		assert.Contains(t, comments[0], tracker.ManagedCommentMarker)
	})
	t.Run("no matching rule", func(t *testing.T) {
		comments := run(t, false)
		require.Len(t, comments, 1, "got %q", comments)
		assert.Contains(t, comments[0], "maximum retries exhausted")
		assert.False(t, strings.Contains(comments[0], "auto-switch"))
	})
}
