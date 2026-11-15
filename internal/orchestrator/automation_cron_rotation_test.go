package orchestrator

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
)

const cronRotationRuleID = "nightly-sweep"

// cronRotationCfg leaves no free slot, so every accepted cron dispatch lands
// in the automation queue where the test can see which issues a tick served.
func cronRotationCfg() *config.Config {
	cfg := automationBaseCfg()
	cfg.Agent.MaxConcurrentAgents = 0
	return cfg
}

func cronRotationIssues(n int) []domain.Issue {
	issues := make([]domain.Issue, 0, n)
	for i := range n {
		issues = append(issues, domain.Issue{
			ID:         fmt.Sprintf("uuid-%d", i+1),
			Identifier: fmt.Sprintf("ENG-%d", i+1),
			Title:      "T",
			State:      "Todo",
		})
	}
	return issues
}

func cronRotationBatch(candidates []domain.Issue, limit int) CronAutomationBatch {
	return CronAutomationBatch{
		Dispatch: AutomationDispatch{
			AutomationID: cronRotationRuleID,
			ProfileName:  "default",
			Trigger: AutomationTriggerContext{
				Type:         config.AutomationTriggerCron,
				AutomationID: cronRotationRuleID,
				Cron:         "0 * * * *",
			},
		},
		Candidates: candidates,
		Limit:      limit,
	}
}

// servedThisTick drains the queue (as if each queued run started and
// finished) and returns the identifiers the tick served, in queue order.
func servedThisTick(state *State) []string {
	served := make([]string, 0, len(state.AutomationQueueOrder))
	for _, entry := range sortedAutomationQueue(*state) {
		served = append(served, entry.Issue.Identifier)
	}
	state.AutomationQueue = map[string]*AutomationQueueEntry{}
	state.AutomationQueueOrder = []string{}
	return served
}

// TestCronLimitRotatesThroughEveryMatchingIssue (#176): 7 matches with
// limit 3 — three ticks serve all 7, wrapping, and no issue is served twice
// before every issue has been served once.
func TestCronLimitRotatesThroughEveryMatchingIssue(t *testing.T) {
	cfg := cronRotationCfg()
	o := New(cfg, nil, nil, nil)
	state := NewState(cfg)
	// Candidates arrive unsorted; the rotation orders them itself.
	issues := cronRotationIssues(7)
	candidates := []domain.Issue{issues[4], issues[0], issues[6], issues[2], issues[1], issues[5], issues[3]}

	var ticks [][]string
	for range 3 {
		got := o.dispatchCronAutomationBatch(context.Background(), &state, cronRotationBatch(candidates, 3), time.Now())
		require.Equal(t, 3, got)
		ticks = append(ticks, servedThisTick(&state))
	}

	assert.Equal(t, [][]string{
		{"ENG-1", "ENG-2", "ENG-3"},
		{"ENG-4", "ENG-5", "ENG-6"},
		{"ENG-7", "ENG-1", "ENG-2"},
	}, ticks)

	var order []string
	for _, tick := range ticks {
		order = append(order, tick...)
	}
	firstSeven := map[string]struct{}{}
	for _, id := range order[:7] {
		_, dup := firstSeven[id]
		require.Falsef(t, dup, "%s served twice before every issue was served once: %v", id, order)
		firstSeven[id] = struct{}{}
	}
	assert.Len(t, firstSeven, 7, "three ticks must serve all seven matching issues")
	assert.Equal(t, "ENG-2", state.CronCursors[cronRotationRuleID])
}

// TestCronLimitRotationDropsIssuesThatLeftTheFilter: the cursor is a
// position, so an issue leaving the filter (even the cursor issue itself)
// does not stall or reset the rotation.
func TestCronLimitRotationDropsIssuesThatLeftTheFilter(t *testing.T) {
	cfg := cronRotationCfg()
	o := New(cfg, nil, nil, nil)
	state := NewState(cfg)
	issues := cronRotationIssues(7)

	o.dispatchCronAutomationBatch(context.Background(), &state, cronRotationBatch(issues, 3), time.Now())
	require.Equal(t, []string{"ENG-1", "ENG-2", "ENG-3"}, servedThisTick(&state))

	// ENG-3 (the cursor) and ENG-5 left the filter.
	remaining := []domain.Issue{issues[0], issues[1], issues[3], issues[5], issues[6]}
	o.dispatchCronAutomationBatch(context.Background(), &state, cronRotationBatch(remaining, 3), time.Now())
	assert.Equal(t, []string{"ENG-4", "ENG-6", "ENG-7"}, servedThisTick(&state))
}

// TestCronLimitCursorSurvivesRestart (#176): the rotation cursor is
// persisted with the automation queue, so a restarted daemon resumes the
// rotation instead of re-serving the first N issues.
func TestCronLimitCursorSurvivesRestart(t *testing.T) {
	cfg := cronRotationCfg()
	path := filepath.Join(t.TempDir(), "automation_queue.json")
	issues := cronRotationIssues(7)

	writer := New(cfg, nil, nil, nil)
	writer.SetAutomationQueueFile(path)
	state := NewState(cfg)
	writer.dispatchCronAutomationBatch(context.Background(), &state, cronRotationBatch(issues, 3), time.Now())
	require.Equal(t, []string{"ENG-1", "ENG-2", "ENG-3"}, servedThisTick(&state))
	writer.storeSnap(state)

	reader := New(cfg, nil, nil, nil)
	reader.SetAutomationQueueFile(path)
	restored := reader.loadAutomationQueueFromDisk(NewState(cfg))
	require.Equal(t, "ENG-3", restored.CronCursors[cronRotationRuleID])

	reader.dispatchCronAutomationBatch(context.Background(), &restored, cronRotationBatch(issues, 3), time.Now())
	assert.Equal(t, []string{"ENG-4", "ENG-5", "ENG-6"}, servedThisTick(&restored),
		"after a restart the rotation must continue after the persisted cursor")
}

// TestCronLimitSkipsIssueAlreadyRunningForAutomation (#176): an issue with a
// live run from this rule is skipped and its slot goes to the next issue. A
// run from a different rule does not exempt the issue.
func TestCronLimitSkipsIssueAlreadyRunningForAutomation(t *testing.T) {
	cfg := cronRotationCfg()
	o := New(cfg, nil, nil, nil)
	state := NewState(cfg)
	issues := cronRotationIssues(5)
	state.Running[issues[1].ID] = &RunEntry{Issue: issues[1], AutomationID: cronRotationRuleID, Kind: "automation"}
	state.Running[issues[2].ID] = &RunEntry{Issue: issues[2], AutomationID: "some-other-rule", Kind: "automation"}

	o.dispatchCronAutomationBatch(context.Background(), &state, cronRotationBatch(issues, 3), time.Now())
	assert.Equal(t, []string{"ENG-1", "ENG-3", "ENG-4"}, servedThisTick(&state),
		"ENG-2 is already running for this rule, so its slot goes to ENG-4")
	assert.Equal(t, "ENG-4", state.CronCursors[cronRotationRuleID])
}

// TestCronLimitSkipsIssueAlreadyQueuedForAutomation: a dispatch of this rule
// already waiting in the queue is not re-served and does not use a slot.
func TestCronLimitSkipsIssueAlreadyQueuedForAutomation(t *testing.T) {
	cfg := cronRotationCfg()
	o := New(cfg, nil, nil, nil)
	state := NewState(cfg)
	issues := cronRotationIssues(5)
	batch := cronRotationBatch(issues, 2)
	require.True(t, enqueueAutomation(&state, issues[0], batch.Dispatch, "no_slots", time.Now()))

	o.dispatchCronAutomationBatch(context.Background(), &state, batch, time.Now())
	assert.Equal(t, []string{"ENG-1", "ENG-2", "ENG-3"}, servedThisTick(&state),
		"ENG-1 was already queued; the two new slots go to ENG-2 and ENG-3")
}

// TestCronLimitBatchEventGoesThroughEventLoop: DispatchCronAutomationBatch
// hands the tick to the event loop as one event; nothing is selected off-loop.
func TestCronLimitBatchEventGoesThroughEventLoop(t *testing.T) {
	cfg := cronRotationCfg()
	o := New(cfg, nil, nil, nil)
	batch := cronRotationBatch(cronRotationIssues(4), 2)

	require.True(t, o.DispatchCronAutomationBatch(context.Background(), batch))
	ev := <-o.events
	require.Equal(t, EventDispatchCronBatch, ev.Type)
	require.NotNil(t, ev.CronBatch)
	assert.Len(t, ev.CronBatch.Candidates, 4)
	assert.Equal(t, 2, ev.CronBatch.Limit)

	state := o.handleEvent(t.Context(), NewState(cfg), ev)
	assert.Equal(t, []string{"ENG-1", "ENG-2"}, servedThisTick(&state))
	assert.Equal(t, "ENG-2", state.CronCursors[cronRotationRuleID])
}

// TestCronLimitCursorStopsAtLastAcceptedDispatch: an issue whose dispatch the
// loop refused (here: the automation queue is full) is not counted as served,
// so the cursor stops before it and the next tick starts with it.
func TestCronLimitCursorStopsAtLastAcceptedDispatch(t *testing.T) {
	cfg := cronRotationCfg()
	cfg.Agent.MaxAutomationQueueLength = 2
	o := New(cfg, nil, nil, nil)
	state := NewState(cfg)
	issues := cronRotationIssues(5)

	got := o.dispatchCronAutomationBatch(context.Background(), &state, cronRotationBatch(issues, 3), time.Now())
	require.Equal(t, 2, got, "the queue holds two; the third dispatch is refused")
	assert.Equal(t, []string{"ENG-1", "ENG-2"}, servedThisTick(&state))
	assert.Equal(t, "ENG-2", state.CronCursors[cronRotationRuleID])

	o.dispatchCronAutomationBatch(context.Background(), &state, cronRotationBatch(issues, 2), time.Now())
	assert.Equal(t, []string{"ENG-3", "ENG-4"}, servedThisTick(&state), "the refused issue comes up first next tick")
}
