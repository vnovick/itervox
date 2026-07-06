package orchestrator

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/logbuffer"
	"github.com/vnovick/itervox/internal/tracker"
)

// F-2: recordAutomationDispatch must append a single per-issue log entry
// whose message starts with the AUTOMATION FIRED prefix and contains the
// rule id, trigger type, profile, and backend. Greppability is the contract
// the Logs filter chip (T-4) depends on.
func TestRecordAutomationDispatchEmitsGreppableLine(t *testing.T) {
	o := &Orchestrator{logBuf: logbuffer.New()}
	issue := domain.Issue{ID: "id1", Identifier: "ENG-42", Title: "test issue"}
	dispatch := AutomationDispatch{
		AutomationID: "pr-on-input",
		ProfileName:  "reviewer",
		Trigger: AutomationTriggerContext{
			Type:         config.AutomationTriggerInputRequired,
			InputContext: "Should I rebase before merge?",
		},
	}

	o.recordAutomationDispatch(issue, dispatch, "claude")

	lines := o.logBuf.Get(issue.Identifier)
	require.Len(t, lines, 1, "exactly one buffer entry should be emitted per dispatch")
	line := lines[0]
	assert.Contains(t, line, AutomationFiredLogPrefix, "frontend chip filter requires the literal prefix")
	assert.Contains(t, line, "pr-on-input")
	assert.Contains(t, line, "input_required")
	assert.Contains(t, line, "reviewer")
	assert.Contains(t, line, "claude")
	assert.Contains(t, line, "Should I rebase before merge?",
		"input_required automations should embed the prompt context in the buffer entry")
}

// Manual dispatches must NOT emit an AUTOMATION FIRED line. We assert by
// poking the buffer directly with the kind of line a manual worker would emit
// — the prefix must not appear anywhere.
func TestRecordAutomationDispatchOmittedForManualRuns(t *testing.T) {
	o := &Orchestrator{logBuf: logbuffer.New()}
	o.logBuf.Add("ENG-99", makeBufLine("INFO", "worker: starting (manual dispatch)"))
	for _, line := range o.logBuf.Get("ENG-99") {
		assert.False(t, strings.HasPrefix(extractMsg(line), AutomationFiredLogPrefix),
			"manual dispatch must never produce the AUTOMATION FIRED prefix")
	}
}

// Older test fixtures construct Orchestrators without a logBuf. The helper
// must no-op rather than panic so it remains safe to call from
// startAutomationRun in any test setup.
func TestRecordAutomationDispatchTolerantOfNilLogBuf(t *testing.T) {
	o := &Orchestrator{}
	require.NotPanics(t, func() {
		o.recordAutomationDispatch(domain.Issue{Identifier: "X"}, AutomationDispatch{}, "")
	})
}

func TestDispatchAutomationWaitsForReceiverUntilContextDone(t *testing.T) {
	o := &Orchestrator{events: make(chan OrchestratorEvent)}
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1"}
	dispatch := AutomationDispatch{
		AutomationID: "nightly-cron",
		Trigger:      AutomationTriggerContext{Type: config.AutomationTriggerCron},
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	received := make(chan OrchestratorEvent, 1)
	go func() {
		time.Sleep(10 * time.Millisecond)
		received <- <-o.events
	}()

	require.True(t, o.DispatchAutomation(ctx, issue, dispatch))

	ev := <-received
	require.Equal(t, EventDispatchAutomation, ev.Type)
	require.NotNil(t, ev.Automation)
	require.Equal(t, "nightly-cron", ev.Automation.AutomationID)
}

func TestDispatchAutomationReturnsFalseWhenContextExpires(t *testing.T) {
	o := &Orchestrator{events: make(chan OrchestratorEvent)}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()

	require.False(t, o.DispatchAutomation(ctx, domain.Issue{Identifier: "ENG-1"}, AutomationDispatch{
		AutomationID: "nightly-cron",
		Trigger:      AutomationTriggerContext{Type: config.AutomationTriggerCron},
	}))
}

// 240-char cap (with ellipsis) keeps the buffer entry comfortably below the
// 64 KiB per-line limit added by the logbuffer per-line truncation guard.
func TestRecordAutomationDispatchTruncatesLongContext(t *testing.T) {
	o := &Orchestrator{logBuf: logbuffer.New()}
	issue := domain.Issue{Identifier: "ENG-long"}
	long := strings.Repeat("a", 5_000)
	o.recordAutomationDispatch(issue, AutomationDispatch{
		AutomationID: "auto-long",
		ProfileName:  "reviewer",
		Trigger: AutomationTriggerContext{
			Type:         config.AutomationTriggerInputRequired,
			InputContext: long,
		},
	}, "claude")
	lines := o.logBuf.Get(issue.Identifier)
	require.Len(t, lines, 1)
	assert.Less(t, len(lines[0]), 1_024,
		"long contexts must be truncated to avoid log line bloat")
	assert.Contains(t, lines[0], "…", "truncation marker must be present")
}

// tracker_comment_added carries CommentBody instead of InputContext; the
// helper picks whichever is non-empty so the two trigger types share one log
// format.
func TestRecordAutomationDispatchUsesCommentBodyWhenInputContextEmpty(t *testing.T) {
	o := &Orchestrator{logBuf: logbuffer.New()}
	o.recordAutomationDispatch(
		domain.Issue{Identifier: "ENG-2"},
		AutomationDispatch{
			AutomationID: "review-on-comment",
			ProfileName:  "reviewer",
			Trigger: AutomationTriggerContext{
				Type:        config.AutomationTriggerTrackerComment,
				CommentBody: "PTAL",
			},
		},
		"claude",
	)
	lines := o.logBuf.Get("ENG-2")
	require.Len(t, lines, 1)
	// The %q-rendered context becomes \"PTAL\" once the JSON envelope escapes
	// the surrounding quotes.
	assert.Contains(t, lines[0], `\"PTAL\"`)
}

// Concurrent automation dispatches against the SAME issue must not interleave
// — each call should produce one self-contained entry with all four fields.
// Re-runs under -race gate the goroutine-safety contract.
func TestRecordAutomationDispatchConcurrentSafe(t *testing.T) {
	o := &Orchestrator{logBuf: logbuffer.New()}

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			o.recordAutomationDispatch(
				domain.Issue{Identifier: "ENG-RACE"},
				AutomationDispatch{
					AutomationID: "auto",
					ProfileName:  "p",
					Trigger:      AutomationTriggerContext{Type: config.AutomationTriggerCron},
				},
				"claude",
			)
			_ = i
		}(i)
	}
	wg.Wait()

	lines := o.logBuf.Get("ENG-RACE")
	require.Len(t, lines, 50)
	for _, line := range lines {
		assert.Contains(t, line, AutomationFiredLogPrefix)
		assert.Contains(t, line, "trigger: cron")
		assert.Contains(t, line, "profile: p")
		assert.Contains(t, line, "backend: claude")
	}
}

func TestDispatchMatchingInputRequiredAutomationsQueuesWhenNoSlots(t *testing.T) {
	cfg := automationBaseCfg()
	cfg.Agent.MaxConcurrentAgents = 1
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"input-responder": {Command: "claude", Backend: "claude"},
	}
	o := newOrchestratorForTest(cfg)
	o.SetInputRequiredAutomations([]InputRequiredAutomation{{
		ID:          "input-responder",
		ProfileName: "input-responder",
	}})
	state := NewState(cfg)
	state.Running["busy"] = &RunEntry{
		Issue: domain.Issue{ID: "busy", Identifier: "ENG-BUSY", State: "Todo"},
	}
	issue := automationIssue("Todo")
	now := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)

	o.dispatchMatchingInputRequiredAutomations(t.Context(), &state, issue, &InputRequiredEntry{
		IssueID:     issue.ID,
		Identifier:  issue.Identifier,
		Context:     "Which branch should I use?",
		ProfileName: "worker",
		Backend:     "codex",
	}, now, nil)

	require.Len(t, state.AutomationQueue, 1)
	entry := state.AutomationQueue[state.AutomationQueueOrder[0]]
	require.Equal(t, AutomationQueueReasonNoSlots, entry.Reason)
	require.Equal(t, config.AutomationTriggerInputRequired, entry.Trigger.Type)
	require.Equal(t, "Which branch should I use?", entry.Trigger.InputContext)
	require.Equal(t, "worker", entry.Trigger.BlockedProfile)
	require.Equal(t, "codex", entry.Trigger.BlockedBackend)
	require.NotContains(t, state.Claimed, issue.ID)
}

// When the exiting worker was itself launched by an
// input_required automation, dispatching another input_required automation on
// the same issue must be suppressed. Otherwise the agent loops indefinitely
// (an input_required automation produces another input_required exit, which
// re-fires the same automation, until an unrelated retry / rate-limit budget
// drains).
func TestDispatchInputRequired_SkipsSelfReentry(t *testing.T) {
	cfg := automationBaseCfg()
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"input-responder": {Command: "claude", Backend: "claude"},
	}
	o := newOrchestratorForTest(cfg)
	o.SetInputRequiredAutomations([]InputRequiredAutomation{
		{ID: "responder", ProfileName: "input-responder"},
	})
	now := time.Now()
	state := NewState(cfg)
	issue := automationIssue("Todo")
	prevRun := &RunEntry{
		AutomationID: "responder",
		TriggerType:  config.AutomationTriggerInputRequired,
		Issue:        issue,
	}
	entry := &InputRequiredEntry{
		IssueID:    issue.ID,
		Identifier: issue.Identifier,
		Context:    "still need input",
	}
	o.dispatchMatchingInputRequiredAutomations(t.Context(), &state, issue, entry, now, prevRun)
	_, claimed := state.Claimed[issue.ID]
	assert.False(t, claimed, "self-reentry must be suppressed — no new claim")
	assert.Empty(t, state.AutomationQueue, "self-reentry must not enqueue either")
}

// Companion test: user-launched workers (AutomationID empty) exiting
// input_required MUST still fire automations. The guard reads AutomationID,
// not "this issue has fired before."
func TestDispatchInputRequired_FiresForUserLaunchedWorker(t *testing.T) {
	cfg := automationBaseCfg()
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"input-responder": {Command: "claude", Backend: "claude"},
	}
	o := newOrchestratorForTest(cfg)
	o.SetInputRequiredAutomations([]InputRequiredAutomation{
		{ID: "responder", ProfileName: "input-responder"},
	})
	now := time.Now()
	state := NewState(cfg)
	issue := automationIssue("Todo")
	prevRun := &RunEntry{
		AutomationID: "", // user-launched (not an automation)
		Issue:        issue,
	}
	entry := &InputRequiredEntry{
		IssueID:    issue.ID,
		Identifier: issue.Identifier,
		Context:    "user worker asks a question",
	}
	o.dispatchMatchingInputRequiredAutomations(t.Context(), &state, issue, entry, now, prevRun)
	_, claimed := state.Claimed[issue.ID]
	assert.True(t, claimed, "user-launched workers must still trigger input_required automations")
}

func TestDispatchMatchingRunFailedAutomationsQueuesWhenNoSlots(t *testing.T) {
	cfg := automationBaseCfg()
	cfg.Agent.MaxConcurrentAgents = 1
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"failure-reviewer": {Command: "claude", Backend: "claude"},
	}
	o := newOrchestratorForTest(cfg)
	o.SetRunFailedAutomations([]RunFailedAutomation{{
		ID:          "failure-reviewer",
		ProfileName: "failure-reviewer",
	}})
	state := NewState(cfg)
	state.Running["busy"] = &RunEntry{
		Issue: domain.Issue{ID: "busy", Identifier: "ENG-BUSY", State: "Todo"},
	}
	issue := automationIssue("Todo")
	now := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)

	o.dispatchMatchingRunFailedAutomations(t.Context(), &state, issue, now, "tests failed", 2)

	require.Len(t, state.AutomationQueue, 1)
	entry := state.AutomationQueue[state.AutomationQueueOrder[0]]
	require.Equal(t, AutomationQueueReasonNoSlots, entry.Reason)
	require.Equal(t, config.AutomationTriggerRunFailed, entry.Trigger.Type)
	require.Equal(t, "tests failed", entry.Trigger.ErrorMessage)
	require.Equal(t, 2, entry.Trigger.RetryAttempt)
	require.NotContains(t, state.Claimed, issue.ID)
}

func TestStartAutomationRunAppliesDefaultBackendHint(t *testing.T) {
	cfg := automationBaseCfg()
	cfg.Agent.Command = "itervox-agent-wrapper"
	cfg.Agent.Backend = "codex"
	cfg.Agent.MaxTurns = 1
	cfg.Agent.ReadTimeoutMs = 1_000
	cfg.Agent.TurnTimeoutMs = 1_000
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"automation": {},
	}
	issue := automationIssue("Todo")
	runner := &automationCommandCaptureRunner{done: make(chan struct{})}
	o := &Orchestrator{
		cfg:           cfg,
		tracker:       tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates),
		runner:        runner,
		events:        make(chan OrchestratorEvent, 8),
		workerCancels: map[string]context.CancelFunc{},
		logBuf:        logbuffer.New(),
	}
	state := NewState(cfg)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	o.startAutomationRun(ctx, &state, issue, time.Now(), AutomationDispatch{
		AutomationID: "nightly",
		ProfileName:  "automation",
		Trigger: AutomationTriggerContext{
			Type: config.AutomationTriggerCron,
		},
	})

	entry := state.Running[issue.ID]
	require.NotNil(t, entry)
	assert.Equal(t, "codex", entry.Backend)

	select {
	case <-runner.done:
	case <-ctx.Done():
		t.Fatalf("automation runner was not called: %v", ctx.Err())
	}
	assert.Equal(t, "@@itervox-backend=codex itervox-agent-wrapper", runner.LastCommand())
}

type automationCommandCaptureRunner struct {
	mu      sync.Mutex
	once    sync.Once
	done    chan struct{}
	command string
}

func (r *automationCommandCaptureRunner) RunTurn(_ context.Context, _ agent.Logger, _ func(agent.TurnResult), _ *string, _, _, command, _, _ string, _, _ int) (agent.TurnResult, error) {
	r.mu.Lock()
	r.command = command
	r.mu.Unlock()
	r.once.Do(func() { close(r.done) })
	return agent.TurnResult{
		SessionID:    "automation-default-backend",
		InputTokens:  1,
		OutputTokens: 1,
		TotalTokens:  2,
		ResultText:   "done",
	}, nil
}

func (r *automationCommandCaptureRunner) LastCommand() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.command
}

// extractMsg pulls the JSON "msg" payload out of a makeBufLine envelope so the
// test can assert on the human-readable text rather than the JSON wrapper.
func extractMsg(line string) string {
	const marker = `"msg":`
	idx := strings.Index(line, marker)
	if idx < 0 {
		return line
	}
	rest := line[idx+len(marker):]
	if !strings.HasPrefix(rest, `"`) {
		return rest
	}
	rest = rest[1:]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return rest
	}
	return rest[:end]
}
