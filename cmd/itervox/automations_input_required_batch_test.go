package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
// Input-required replay: BATCHED detail reads (issue #42)
//
// The replay was the single largest consumer in issue #42's incident (~30% of
// 1,203 requests over 13 minutes), costing one FetchIssueDetail per blocked
// issue per tick. These tests assert the call counts, which are invisible in
// the replay's return value.
//
// The sibling cache tests use countingDetailTracker, which embeds the Tracker
// INTERFACE and so does not expose DetailBatcher — they therefore still
// exercise the per-issue path, which is exactly what makes them a useful
// control. This file uses MemoryTracker directly, which does implement it.
// ---------------------------------------------------------------------------

// replayBatchHarness holds an orchestrator with several blocked issues.
type replayBatchHarness struct {
	orch *orchestrator.Orchestrator
	tr   *tracker.MemoryTracker
	ctx  context.Context
}

func newReplayBatchHarness(t *testing.T, issueCount int) *replayBatchHarness {
	t.Helper()

	cfg := &config.Config{
		// A long poll interval keeps the orchestrator's own loop quiet after
		// startup, so the only reads attributed to a tick are the replay's.
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

	issues := make([]domain.Issue, 0, issueCount)
	awaiting := make([]string, 0, issueCount)
	for i := 1; i <= issueCount; i++ {
		id := fmt.Sprintf("id-%d", i)
		identifier := fmt.Sprintf("ENG-%d", i)
		issues = append(issues, domain.Issue{
			ID:         id,
			Identifier: identifier,
			Title:      "Needs answer",
			State:      "Todo",
			Labels:     []string{"triage"},
		})
		awaiting = append(awaiting, fmt.Sprintf(`    %q: {
      "issue_id": %q,
      "identifier": %q,
      "context": "Continue with the existing branch",
      "question_comment_id": "q-%d",
      "queued_at": "2026-04-20T16:47:06+03:00"
    }`, identifier, id, identifier, i))
	}

	tr := tracker.NewMemoryTracker(issues, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	runner := agenttest.NewFakeRunner([]agent.StreamEvent{
		{Type: "system", SessionID: "s1"},
		{Type: "result", SessionID: "s1"},
	})
	orch := orchestrator.New(cfg, tr, runner, nil)

	irFile := filepath.Join(t.TempDir(), "input_required.json")
	body := "{\n  \"awaiting\": {\n" + strings.Join(awaiting, ",\n") + "\n  }\n}"
	require.NoError(t, os.WriteFile(irFile, []byte(body), 0o644))
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
		return len(orch.Snapshot().InputRequiredIssues) == issueCount
	}, 3*time.Second, 20*time.Millisecond, "input-required entries never loaded")

	h := &replayBatchHarness{orch: orch, tr: tr, ctx: ctx}
	h.settle(t)
	return h
}

// settle waits until the per-issue counter stops moving, so startup work is
// not attributed to the tick under measurement.
func (h *replayBatchHarness) settle(t *testing.T) {
	t.Helper()
	stable, last := 0, -1
	require.Eventually(t, func() bool {
		current := h.tr.DetailCalls()
		if current == last {
			stable++
		} else {
			stable, last = 0, current
		}
		return stable >= 3
	}, 3*time.Second, 50*time.Millisecond, "tracker fetch count never settled")
}

// tick runs one replay, reporting per-issue and batched call deltas.
func (h *replayBatchHarness) tick(
	t *testing.T,
	automations []orchestrator.InputRequiredAutomation,
	state inputRequiredReplayState,
	now time.Time,
) (next inputRequiredReplayState, perIssue, batched int) {
	t.Helper()
	beforeSingle, beforeBatch := h.tr.DetailCalls(), h.tr.DetailBatchCalls()
	next = replayInputRequiredAutomations(h.ctx, h.tr, h.orch, automations, state, now)
	return next, h.tr.DetailCalls() - beforeSingle, h.tr.DetailBatchCalls() - beforeBatch
}

// TestReplayInputRequired_BatchesDetailFetchesAcrossIssues is issue #42's fix
// for the replay: three blocked issues must cost ONE request, not three.
//
// perIssue == 0 is the assertion that carries the claim. A batch fired
// alongside three per-issue fetches would have saved nothing, so asserting only
// batched == 1 would pass with the bug fully present.
func TestReplayInputRequired_BatchesDetailFetchesAcrossIssues(t *testing.T) {
	h := newReplayBatchHarness(t, 3)
	automations := []orchestrator.InputRequiredAutomation{replayAutomation("responder-a")}

	_, perIssue, batched := h.tick(t, automations, inputRequiredReplayState{}, time.Now())

	assert.Equal(t, 1, batched, "three blocked issues must collapse into one batched request")
	assert.Zero(t, perIssue, "no per-issue fetch may survive the batch — that is the saving")
}

// TestReplayInputRequired_BatchSeedsCacheForLaterTicks pins that the batched
// read populates the same cache the per-issue path fed, so the TTL reuse that
// already existed is not lost to the new path.
func TestReplayInputRequired_BatchSeedsCacheForLaterTicks(t *testing.T) {
	h := newReplayBatchHarness(t, 3)
	automations := []orchestrator.InputRequiredAutomation{replayAutomation("responder-a")}
	start := time.Now()

	state, _, batched := h.tick(t, automations, inputRequiredReplayState{}, start)
	require.Equal(t, 1, batched)
	h.settle(t)

	// Same rule, still inside the TTL: the seeded cache must answer it.
	_, perIssue, batched := h.tick(t, automations, state, start.Add(inputRequiredDetailTTL/2))
	assert.Zero(t, perIssue, "a cached tick must not fall back to per-issue reads")
	assert.Zero(t, batched, "nor re-issue the batch — the seeded cache already answers it")
}

// TestReplayInputRequired_NoAutomationsCostsNoRequest pins that the prefetch is
// gated on there being work to do. Prefetching unconditionally would spend a
// request per tick on a daemon with no input-required rules configured at all —
// adding cost to the quietest possible configuration.
func TestReplayInputRequired_NoAutomationsCostsNoRequest(t *testing.T) {
	h := newReplayBatchHarness(t, 3)

	_, perIssue, batched := h.tick(t, nil, inputRequiredReplayState{}, time.Now())

	assert.Zero(t, batched, "no automations means no reason to prefetch anything")
	assert.Zero(t, perIssue)
}
