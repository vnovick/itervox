package main

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	agenttest "github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
)

// ---------------------------------------------------------------------------
// Input-required replay: FetchIssueDetail call count
//
// The replay ran one FetchIssueDetail per blocked issue per tick — one of the
// largest contributors to Linear rate-limit pressure. These tests assert the
// call COUNT, which is invisible in the replay's return value: every one of
// them passes on the old code if you only check what was dispatched.
// ---------------------------------------------------------------------------

// countingDetailTracker counts FetchIssueDetail calls, delegating everything
// else to the wrapped tracker.
type countingDetailTracker struct {
	tracker.Tracker

	mu      sync.Mutex
	fetches int
}

func (t *countingDetailTracker) FetchIssueDetail(ctx context.Context, issueID string) (*domain.Issue, error) {
	t.mu.Lock()
	t.fetches++
	t.mu.Unlock()
	return t.Tracker.FetchIssueDetail(ctx, issueID)
}

func (t *countingDetailTracker) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.fetches
}

// replayCacheHarness builds an orchestrator holding one persisted blocked
// issue, ready for direct replayInputRequiredAutomations calls.
type replayCacheHarness struct {
	orch *orchestrator.Orchestrator
	tr   *countingDetailTracker
	ctx  context.Context
}

func newReplayCacheHarness(t *testing.T) *replayCacheHarness {
	t.Helper()

	cfg := &config.Config{
		// The orchestrator's own loop re-fetches detail for blocked issues on
		// every poll, which would swamp the counter these tests read. A long
		// interval leaves it quiet after startup so the only fetches observed
		// are the ones the replay makes.
		Polling: config.PollingConfig{IntervalMs: 3_600_000},
		Tracker: config.TrackerConfig{
			ActiveStates:    []string{"Todo"},
			TerminalStates:  []string{"Done"},
			CompletionState: "Done",
		},
		Agent: config.AgentConfig{
			Command:             "claude",
			MaxConcurrentAgents: 2,
			Profiles:            map[string]config.AgentProfile{"responder": {Command: "claude"}},
			TurnTimeoutMs:       60000,
			ReadTimeoutMs:       30000,
		},
	}

	issue := domain.Issue{
		ID:         "id-1",
		Identifier: "ENG-1",
		Title:      "Needs answer",
		State:      "Todo",
		Labels:     []string{"triage"},
	}
	tr := &countingDetailTracker{
		Tracker: tracker.NewMemoryTracker(
			[]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates),
	}

	runner := agenttest.NewFakeRunner([]agent.StreamEvent{
		{Type: "system", SessionID: "s1"},
		{Type: "result", SessionID: "s1"},
	})
	orch := orchestrator.New(cfg, tr, runner, nil)

	irFile := filepath.Join(t.TempDir(), "input_required.json")
	require.NoError(t, os.WriteFile(irFile, []byte(`{
  "awaiting": {
    "ENG-1": {
      "issue_id": "id-1",
      "identifier": "ENG-1",
      "context": "Continue with the existing branch",
      "question_comment_id": "q-1",
      "queued_at": "2026-04-20T16:47:06+03:00"
    }
  }
}`), 0o644))
	orch.SetInputRequiredFile(irFile)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	runDone := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		select {
		case <-runDone:
		case <-time.After(2 * time.Second):
			t.Fatal("orchestrator did not stop before test cleanup")
		}
	})
	go func() {
		_ = orch.Run(ctx)
		close(runDone)
	}()
	require.Eventually(t, func() bool {
		_, ok := orch.Snapshot().InputRequiredIssues["ENG-1"]
		return ok
	}, 3*time.Second, 20*time.Millisecond)

	h := &replayCacheHarness{orch: orch, tr: tr, ctx: ctx}
	h.settle(t)
	return h
}

// settle waits until the fetch counter stops moving, so startup work — or an
// agent run kicked off by a previous dispatch — cannot be attributed to the
// next tick measured.
func (h *replayCacheHarness) settle(t *testing.T) {
	t.Helper()
	stable, last := 0, -1
	require.Eventually(t, func() bool {
		current := h.tr.count()
		if current == last {
			stable++
		} else {
			stable, last = 0, current
		}
		return stable >= 3
	}, 3*time.Second, 50*time.Millisecond, "tracker fetch count never settled")
}

// tick runs one replay and reports how many FetchIssueDetail calls it made.
func (h *replayCacheHarness) tick(
	t *testing.T,
	automations []orchestrator.InputRequiredAutomation,
	state inputRequiredReplayState,
	now time.Time,
) (inputRequiredReplayState, int) {
	t.Helper()
	before := h.tr.count()
	next := replayInputRequiredAutomations(h.ctx, h.tr, h.orch, automations, state, now)
	return next, h.tr.count() - before
}

func replayAutomation(id string) orchestrator.InputRequiredAutomation {
	return orchestrator.InputRequiredAutomation{
		ID:                id,
		ProfileName:       "responder",
		States:            []string{"Todo"},
		LabelsAny:         []string{"triage"},
		InputContextRegex: regexp.MustCompile(`continue|branch`),
	}
}

// ---------------------------------------------------------------------------
// Cache-reuse guards, exercised directly.
//
// These two rules decide whether reuse is SAFE rather than whether it happens,
// and neither is reachable through the replay harness: one needs the blocked
// context to move between ticks, the other needs a tracker failure.
// ---------------------------------------------------------------------------

type stubDetailTracker struct {
	tracker.Tracker

	fetches int
	err     error
}

func (s *stubDetailTracker) FetchIssueDetail(_ context.Context, issueID string) (*domain.Issue, error) {
	s.fetches++
	if s.err != nil {
		return nil, s.err
	}
	return &domain.Issue{ID: issueID, Identifier: "ENG-1", State: "Todo"}, nil
}

func blockedEntry() *orchestrator.InputRequiredEntry {
	return &orchestrator.InputRequiredEntry{IssueID: "id-1", Identifier: "ENG-1"}
}

// A new question comment moves blockedKey, and the cached detail predates it.
// Reusing it would evaluate the new context against the old issue.
func TestReplayInputRequiredIssueDetail_RefetchesWhenBlockedKeyChanged(t *testing.T) {
	now := time.Now()
	tr := &stubDetailTracker{}
	prev := map[string]inputRequiredDetailCacheEntry{
		"id-1": {issue: &domain.Issue{ID: "id-1"}, blockedKey: "comment:q-1", fetchedAt: now},
	}
	next := map[string]inputRequiredDetailCacheEntry{}

	issue := replayInputRequiredIssueDetail(
		context.Background(), tr, prev, next, blockedEntry(), "comment:q-2", now)

	require.NotNil(t, issue)
	assert.Equal(t, 1, tr.fetches, "a moved blocked context must not reuse the prior fetch")
	assert.Equal(t, "comment:q-2", next["id-1"].blockedKey)
}

// The same blocked context inside the TTL is the case reuse exists for.
func TestReplayInputRequiredIssueDetail_ReusesSameBlockedKeyWithinTTL(t *testing.T) {
	now := time.Now()
	tr := &stubDetailTracker{}
	cached := &domain.Issue{ID: "id-1", Title: "from cache"}
	prev := map[string]inputRequiredDetailCacheEntry{
		"id-1": {issue: cached, blockedKey: "comment:q-1", fetchedAt: now.Add(-inputRequiredDetailTTL / 2)},
	}
	next := map[string]inputRequiredDetailCacheEntry{}

	issue := replayInputRequiredIssueDetail(
		context.Background(), tr, prev, next, blockedEntry(), "comment:q-1", now)

	assert.Zero(t, tr.fetches)
	assert.Same(t, cached, issue)
	assert.Equal(t, cached, next["id-1"].issue, "a reused entry must survive into the next tick")
}

// A failed fetch must not be remembered across ticks: a transient tracker
// error would otherwise suppress replay for a whole TTL, which is the
// read-failure-blocks-progress loop this work exists to avoid.
func TestReplayInputRequiredIssueDetail_NeverReusesAFailureAcrossTicks(t *testing.T) {
	now := time.Now()
	tr := &stubDetailTracker{}
	prev := map[string]inputRequiredDetailCacheEntry{
		"id-1": {issue: nil, blockedKey: "comment:q-1", fetchedAt: now}, // last tick's failure
	}
	next := map[string]inputRequiredDetailCacheEntry{}

	issue := replayInputRequiredIssueDetail(
		context.Background(), tr, prev, next, blockedEntry(), "comment:q-1", now)

	require.NotNil(t, issue, "the retry must be allowed to succeed")
	assert.Equal(t, 1, tr.fetches)
}

// Within one tick a failure IS honoured, so one unreachable issue costs a
// single request per tick rather than one per rule evaluation.
func TestReplayInputRequiredIssueDetail_HonoursAFailureWithinTheSameTick(t *testing.T) {
	now := time.Now()
	tr := &stubDetailTracker{err: assert.AnError}
	next := map[string]inputRequiredDetailCacheEntry{}
	prev := map[string]inputRequiredDetailCacheEntry{}

	for range 3 {
		issue := replayInputRequiredIssueDetail(
			context.Background(), tr, prev, next, blockedEntry(), "comment:q-1", now)
		assert.Nil(t, issue)
	}

	assert.Equal(t, 1, tr.fetches, "a failing issue must cost one request per tick, not one per lookup")
}

// Once every rule has fired, the detail cannot change the outcome, so the
// steady-state cost of a blocked issue must be zero requests per tick.
func TestReplayInputRequired_SkipsDetailFetchWhenNoAutomationPending(t *testing.T) {
	h := newReplayCacheHarness(t)
	automations := []orchestrator.InputRequiredAutomation{replayAutomation("responder-a")}
	start := time.Now()

	// Tick 1 evaluates the rule, so it must fetch.
	state, fetched := h.tick(t, automations, inputRequiredReplayState{}, start)
	require.Equal(t, 1, fetched, "the first tick must fetch to evaluate the rule")
	h.settle(t)

	// Ticks 2 and 3 are spaced beyond the TTL, so any saving here comes from
	// the pending-rule check rather than from cache reuse.
	state, fetched = h.tick(t, automations, state, start.Add(5*inputRequiredDetailTTL))
	assert.Zero(t, fetched, "a blocked issue whose rules have all fired must cost no request")

	_, fetched = h.tick(t, automations, state, start.Add(10*inputRequiredDetailTTL))
	assert.Zero(t, fetched, "and must keep costing nothing on later ticks")
}

// A newly added rule is pending, so it must be evaluated against a fresh
// fetch — the skip must not become a permanent block on new automations.
func TestReplayInputRequired_FetchesAgainForNewlyAddedAutomation(t *testing.T) {
	h := newReplayCacheHarness(t)
	automations := []orchestrator.InputRequiredAutomation{replayAutomation("responder-a")}
	start := time.Now()

	state, fetched := h.tick(t, automations, inputRequiredReplayState{}, start)
	require.Equal(t, 1, fetched)
	h.settle(t)

	automations = append(automations, replayAutomation("responder-b"))
	_, fetched = h.tick(t, automations, state, start.Add(5*inputRequiredDetailTTL))

	assert.Equal(t, 1, fetched, "a pending rule must be evaluated against a fresh fetch")
}

// With a rule permanently pending — one that never matches — consecutive ticks
// inside the TTL must reuse the prior fetch instead of re-requesting.
func TestReplayInputRequired_ReusesDetailAcrossTicksWithinTTL(t *testing.T) {
	h := newReplayCacheHarness(t)
	// States never matches, so this rule stays pending forever and every tick
	// reaches the fetch path.
	never := replayAutomation("responder-never")
	never.States = []string{"NoSuchState"}
	automations := []orchestrator.InputRequiredAutomation{never}
	start := time.Now()

	state, fetched := h.tick(t, automations, inputRequiredReplayState{}, start)
	require.Equal(t, 1, fetched)

	// Two more ticks well inside the TTL.
	state, fetched = h.tick(t, automations, state, start.Add(5*time.Second))
	assert.Zero(t, fetched, "a tick inside the TTL must reuse the prior fetch")
	state, fetched = h.tick(t, automations, state, start.Add(10*time.Second))
	assert.Zero(t, fetched, "and so must the next one")

	// Past the TTL the detail is refreshed, so a label or state change made
	// while the issue is blocked is eventually picked up.
	_, fetched = h.tick(t, automations, state, start.Add(inputRequiredDetailTTL+time.Second))
	assert.Equal(t, 1, fetched, "the TTL must expire and refresh the detail")
}
