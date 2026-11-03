package main

import (
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
)

// TestCronAutomationLimitRotatesThroughSchedulerPath (#176) drives the real
// scheduler entry point (runCronAutomation) against a running event loop:
// with 7 matching issues and filter.limit 3, the scheduler hands every match
// to the event loop (it no longer truncates to the first 3) and three ticks
// reach all 7 issues. No slot is free, so each served issue waits in the
// automation queue — which is also what the rotation skips on later ticks.
func TestCronAutomationLimitRotatesThroughSchedulerPath(t *testing.T) {
	rule := config.AutomationConfig{
		ID: "sweep", Enabled: true, Profile: "qa", Instructions: firedInstructions,
		Trigger: config.AutomationTriggerConfig{Type: config.AutomationTriggerCron, Cron: "* * * * *"},
		Filter:  config.AutomationFilterConfig{States: []string{"Todo"}, Limit: 3},
	}
	cfg := pollTestConfig(rule)
	cfg.Agent.MaxConcurrentAgents = 0
	issues := make([]domain.Issue, 0, 7)
	for _, n := range []string{"5", "1", "7", "3", "2", "6", "4"} {
		issues = append(issues, pollIssue(n, "Todo"))
	}
	tr := newPollFake(cfg, issues...)
	orch, _, ctx := runPollOrchestrator(t, cfg, tr)

	entries := compileAutomations(cfg).cron
	require.Len(t, entries, 1)

	queued := func() []string {
		var ids []string
		for _, entry := range orch.Snapshot().AutomationQueue {
			ids = append(ids, entry.Issue.Identifier)
		}
		slices.Sort(ids)
		return ids
	}
	waitQueued := func(n int) {
		t.Helper()
		require.Eventually(t, func() bool { return len(queued()) == n }, 3*time.Second, 10*time.Millisecond)
	}

	runCronAutomation(ctx, cfg, tr, orch, entries[0], cfg.Tracker.ActiveStates, time.Now())
	waitQueued(3)
	assert.Equal(t, []string{"ENG-1", "ENG-2", "ENG-3"}, queued(), "tick 1 serves the first three")

	runCronAutomation(ctx, cfg, tr, orch, entries[0], cfg.Tracker.ActiveStates, time.Now())
	waitQueued(6)

	runCronAutomation(ctx, cfg, tr, orch, entries[0], cfg.Tracker.ActiveStates, time.Now())
	waitQueued(7)
	assert.Equal(t, []string{"ENG-1", "ENG-2", "ENG-3", "ENG-4", "ENG-5", "ENG-6", "ENG-7"}, queued(),
		"three ticks with limit 3 must reach all seven matching issues")
	assert.Equal(t, "ENG-7", orch.Snapshot().CronCursors["sweep"])
}
