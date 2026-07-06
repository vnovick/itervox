package orchestrator

import (
	"context"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
)

// DispatchPROpenedAutomations queues an EventDispatchAutomation for every
// matching pr_opened rule, populating the PR-context fields on the trigger
// snapshot. The events channel is buffered so we can read what was sent
// without spinning the orchestrator's event loop.
func TestDispatchPROpenedAutomations_QueuesMatchingRules(t *testing.T) {
	o := &Orchestrator{
		cfg:    &config.Config{},
		events: make(chan OrchestratorEvent, 8),
	}
	o.SetPROpenedAutomations([]PROpenedAutomation{
		{
			ID:          "pr-reviewer",
			ProfileName: "reviewer",
		},
		{
			ID:              "pr-only-eng",
			ProfileName:     "reviewer",
			IdentifierRegex: regexp.MustCompile(`^ENG-`),
		},
	})

	issue := domain.Issue{ID: "id1", Identifier: "ENG-7", State: "In Progress"}
	o.DispatchPROpenedAutomations(t.Context(), issue, "https://github.com/x/y/pull/42", "feat-7", "main")

	// Drain the events channel.
	var events []OrchestratorEvent
	for len(o.events) > 0 {
		events = append(events, <-o.events)
	}
	require.Len(t, events, 2, "both rules should match ENG-7")
	for _, ev := range events {
		require.Equal(t, EventDispatchAutomation, ev.Type)
		require.NotNil(t, ev.Automation)
		assert.Equal(t, config.AutomationTriggerPROpened, ev.Automation.Trigger.Type)
		assert.Equal(t, "https://github.com/x/y/pull/42", ev.Automation.Trigger.PRURL)
		assert.Equal(t, "feat-7", ev.Automation.Trigger.PRBranch)
		assert.Equal(t, "main", ev.Automation.Trigger.PRBaseBranch)
	}
}

// IdentifierRegex must filter out non-matching issues — a rule scoped to
// `^ENG-` should not fire for `BUG-1`.
func TestDispatchPROpenedAutomations_IdentifierRegexFilter(t *testing.T) {
	o := &Orchestrator{
		cfg:    &config.Config{},
		events: make(chan OrchestratorEvent, 8),
	}
	o.SetPROpenedAutomations([]PROpenedAutomation{
		{
			ID:              "pr-only-eng",
			ProfileName:     "reviewer",
			IdentifierRegex: regexp.MustCompile(`^ENG-`),
		},
	})

	o.DispatchPROpenedAutomations(t.Context(), domain.Issue{Identifier: "BUG-1"}, "https://x/y/pull/1", "b", "main")
	assert.Empty(t, o.events, "BUG-1 must not trigger an ENG-only pr_opened rule")
}

// A worker discovers a new PR before it sends TerminalSucceeded. The
// pr_opened automation event must be accepted after the exit event so the event
// loop clears state.Running first; otherwise automation dispatch is skipped as
// already_running.
func TestSendExitWithBranchThenPROpenedAutomations_QueuesExitBeforeAutomation(t *testing.T) {
	o := &Orchestrator{
		cfg:    &config.Config{Agent: config.AgentConfig{BaseBranch: "origin/main"}},
		events: make(chan OrchestratorEvent, 8),
	}
	o.SetPROpenedAutomations([]PROpenedAutomation{{ID: "pr-reviewer", ProfileName: "reviewer"}})

	issue := domain.Issue{ID: "id1", Identifier: "ENG-7", Title: "T", State: "In Progress"}
	o.sendExitWithBranchThenPROpenedAutomations(
		t.Context(),
		issue,
		0,
		TerminalSucceeded,
		nil,
		"feature/eng-7",
		"https://github.com/x/y/pull/42",
		"https://github.com/x/y/pull/42",
		"feature/eng-7",
	)

	first := <-o.events
	require.Equal(t, EventWorkerExited, first.Type)
	require.NotNil(t, first.RunEntry)
	assert.Equal(t, "https://github.com/x/y/pull/42", first.RunEntry.PRURL)

	second := <-o.events
	require.Equal(t, EventDispatchAutomation, second.Type)
	require.NotNil(t, second.Automation)
	assert.Equal(t, "pr-reviewer", second.Automation.AutomationID)
	assert.Equal(t, config.AutomationTriggerPROpened, second.Automation.Trigger.Type)
	assert.Equal(t, "feature/eng-7", second.Automation.Trigger.PRBranch)
	assert.Empty(t, o.events)
}

// When `openedPRURL` is empty (resumed worker found the
// tracker comment already posted, OR CreateComment failed) but `prURL` is set,
// the helper must still dispatch the pr_opened automation. The previous gate
// required `openedPRURL != ""` which silently swallowed the reviewer on every
// resumed worker — the most common production path.
func TestSendExitWithBranchThenPROpenedAutomations_FiresOnResumeWithoutOpenedPRURL(t *testing.T) {
	o := &Orchestrator{
		cfg:    &config.Config{Agent: config.AgentConfig{BaseBranch: "origin/main"}},
		events: make(chan OrchestratorEvent, 8),
	}
	o.SetPROpenedAutomations([]PROpenedAutomation{{ID: "pr-reviewer", ProfileName: "reviewer"}})
	issue := domain.Issue{ID: "id1", Identifier: "ENG-7", Title: "T", State: "In Progress"}
	o.sendExitWithBranchThenPROpenedAutomations(
		t.Context(),
		issue,
		0,
		TerminalSucceeded,
		nil,
		"feature/eng-7",
		"https://github.com/x/y/pull/42", // detectedPRURL
		"",                               // openedPRURL — empty (resumed run, comment already posted)
		"",                               // openedPRBranch
	)
	first := <-o.events
	require.Equal(t, EventWorkerExited, first.Type)
	second := <-o.events
	require.Equal(t, EventDispatchAutomation, second.Type, "pr_opened dispatch must fire even with openedPRURL empty")
	assert.Equal(t, "https://github.com/x/y/pull/42", second.Automation.Trigger.PRURL)
	assert.Equal(t, "feature/eng-7", second.Automation.Trigger.PRBranch,
		"PR branch must fall back to the worker's active branch when openedPRBranch is empty")
}

// Dedup: a re-dispatch for the same
// (issue, prURL, automationID) triple must be skipped.
func TestDispatchOrQueueAutomation_PROpenedDedup(t *testing.T) {
	cfg := automationBaseCfg()
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"reviewer": {Command: "claude", Backend: "claude"},
	}
	o := newOrchestratorForTest(cfg)
	now := time.Now()
	state := NewState(cfg)
	issue := automationIssue("In Progress")
	dispatch := AutomationDispatch{
		AutomationID: "pr-reviewer",
		ProfileName:  "reviewer",
		Trigger: AutomationTriggerContext{
			Type:         config.AutomationTriggerPROpened,
			PRURL:        "https://github.com/x/y/pull/1",
			AutomationID: "pr-reviewer",
		},
	}
	// First call: ledger empty → proceed past dedup. We don't care about the
	// downstream startAutomationRun return; only that dedup recorded the key.
	o.dispatchOrQueueAutomation(t.Context(), &state, issue, dispatch, now)
	require.Len(t, state.PROpenedDispatched, 1, "first dispatch must record the dedup key")
	// Second call: must short-circuit via dedup and return false.
	accepted := o.dispatchOrQueueAutomation(t.Context(), &state, issue, dispatch, now)
	assert.False(t, accepted, "dedup must reject the second dispatch")
	assert.Len(t, state.PROpenedDispatched, 1, "ledger must not grow on dedup")
}

// SetPROpenedAutomations / snapPROpenedAutomations under -race: same
// invariant as the input-required and run-failed registries.
func TestPROpenedAutomations_RaceSafe(t *testing.T) {
	o := &Orchestrator{}
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 100 {
				_ = i
				o.SetPROpenedAutomations([]PROpenedAutomation{
					{ID: "pr", ProfileName: "reviewer"},
				})
				_ = o.snapPROpenedAutomations()
			}
		}()
	}
	wg.Wait()
	assert.Len(t, o.snapPROpenedAutomations(), 1)
}

// When the events channel is full, DispatchPROpenedAutomations waits only
// until the caller's bounded context expires. The worker must not deadlock
// forever, but one-shot PR events should no longer be dropped immediately.
func TestDispatchPROpenedAutomations_BoundedWaitOnFullChannel(t *testing.T) {
	// Buffered to cap=0 → any send blocks unless there's a receiver. We
	// don't start one; the helper must give up immediately via the
	// default branch.
	o := &Orchestrator{events: make(chan OrchestratorEvent)}
	o.SetPROpenedAutomations([]PROpenedAutomation{{ID: "x", ProfileName: "y"}})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() {
		o.DispatchPROpenedAutomations(ctx, domain.Issue{Identifier: "ENG-1"}, "u", "b", "")
		close(done)
	}()
	select {
	case <-done:
		// Returned once the bounded context expired.
	case <-time.After(time.Second):
		t.Fatal("DispatchPROpenedAutomations did not respect the bounded context")
	}
}
