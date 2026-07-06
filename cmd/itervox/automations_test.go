package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
)

func TestCompileAutomations_SplitsCronAndInputRequired(t *testing.T) {
	cfg := &config.Config{
		Agent: config.AgentConfig{
			Profiles: map[string]config.AgentProfile{
				"qa":              {Command: "claude"},
				"input-responder": {Command: "claude"},
				"reviewer":        {Command: "claude"},
				"pm":              {Command: "claude", AllowedActions: []string{config.AgentActionMoveState}},
			},
		},
		Automations: []config.AutomationConfig{
			{
				ID:      "qa-ready",
				Enabled: true,
				Profile: "qa",
				Trigger: config.AutomationTriggerConfig{
					Type:     "cron",
					Cron:     "0 */2 * * *",
					Timezone: "UTC",
				},
			},
			{
				ID:      "input-responder",
				Enabled: true,
				Profile: "input-responder",
				Trigger: config.AutomationTriggerConfig{
					Type: "input_required",
				},
				Filter: config.AutomationFilterConfig{
					InputContextRegex: "continue|branch",
				},
			},
			{
				ID:      "state-entry",
				Enabled: true,
				Profile: "qa",
				Trigger: config.AutomationTriggerConfig{
					Type:  "issue_entered_state",
					State: "Ready for QA",
				},
			},
			{
				ID:      "comment-watch",
				Enabled: true,
				Profile: "reviewer",
				Trigger: config.AutomationTriggerConfig{
					Type: "tracker_comment_added",
				},
			},
			{
				ID:      "failed-run",
				Enabled: true,
				Profile: "reviewer",
				Trigger: config.AutomationTriggerConfig{
					Type: "run_failed",
				},
			},
			{
				ID:      "unblock-backlog",
				Enabled: true,
				Profile: "pm",
				Trigger: config.AutomationTriggerConfig{
					Type: config.AutomationTriggerBlockersResolved,
				},
				Filter: config.AutomationFilterConfig{
					States: []string{"Backlog"},
				},
				Policy: config.AutomationPolicyConfig{
					MoveToState: "Todo",
				},
			},
		},
	}

	compiled := compileAutomations(cfg)
	require.Len(t, compiled.cron, 1)
	require.Len(t, compiled.inputRequired, 1)
	require.Len(t, compiled.polledEvents, 2)
	require.Len(t, compiled.runFailed, 1)
	require.Len(t, compiled.blockersResolved, 1)
	assert.Equal(t, "qa-ready", compiled.cron[0].cfg.ID)
	assert.Equal(t, "input-responder", compiled.inputRequired[0].ID)
	assert.Equal(t, "input-responder", compiled.inputRequired[0].ProfileName)
	assert.Equal(t, "Todo", compiled.blockersResolved[0].MoveToState)
}

func TestLogAutomationCompileSummaryIncludesRateLimitedRules(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() {
		slog.SetDefault(prev)
	})

	logAutomationCompileSummary(1, compiledAutomationSet{
		rateLimited: []orchestrator.RateLimitedAutomation{
			{ID: "rate-limit-fallback", ProfileName: "fallback-codex"},
		},
	})

	out := buf.String()
	assert.Contains(t, out, `"registered":1`)
	assert.Contains(t, out, `"dropped":0`)
	assert.Contains(t, out, `"rate_limited":1`)
}

func TestLogAutomationCompileSummaryIncludesBlockersResolvedRules(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() {
		slog.SetDefault(prev)
	})

	logAutomationCompileSummary(1, compiledAutomationSet{
		blockersResolved: []orchestrator.BlockersResolvedAutomation{
			{ID: "unblock-backlog-to-todo", ProfileName: "pm"},
		},
	})

	out := buf.String()
	assert.Contains(t, out, `"registered":1`)
	assert.Contains(t, out, `"dropped":0`)
	assert.Contains(t, out, `"blockers_resolved":1`)
}

func TestCompileAutomationsRateLimitedRequiresUsableSwitchProfile(t *testing.T) {
	disabled := false
	cfg := &config.Config{
		Agent: config.AgentConfig{
			Profiles: map[string]config.AgentProfile{
				"default":  {Command: "claude"},
				"fallback": {Command: "codex", Enabled: &disabled},
			},
		},
		Automations: []config.AutomationConfig{{
			ID:      "disabled-switch-profile",
			Enabled: true,
			Profile: "default",
			Trigger: config.AutomationTriggerConfig{Type: config.AutomationTriggerRateLimited},
			Policy: config.AutomationPolicyConfig{
				AutoResume:      true,
				SwitchToProfile: "fallback",
			},
		}, {
			ID:      "missing-switch-profile",
			Enabled: true,
			Profile: "default",
			Trigger: config.AutomationTriggerConfig{Type: config.AutomationTriggerRateLimited},
			Policy: config.AutomationPolicyConfig{
				AutoResume:      true,
				SwitchToProfile: "missing",
			},
		}},
	}

	compiled := compileAutomations(cfg)
	assert.Empty(t, compiled.rateLimited)
}

func TestAutomationCompileConfigViewUsesRuntimeProfiles(t *testing.T) {
	baseCfg := &config.Config{
		Agent: config.AgentConfig{
			Profiles: map[string]config.AgentProfile{
				"default": {Command: "claude"},
			},
		},
		Automations: []config.AutomationConfig{{
			ID:      "rate-limit-switch",
			Enabled: true,
			Profile: "default",
			Trigger: config.AutomationTriggerConfig{Type: config.AutomationTriggerRateLimited},
			Policy: config.AutomationPolicyConfig{
				AutoResume:      true,
				SwitchToProfile: "fallback",
			},
		}},
	}
	runtimeCfg := *baseCfg
	runtimeCfg.Agent.Profiles = map[string]config.AgentProfile{
		"default":  {Command: "claude"},
		"fallback": {Command: "codex"},
	}
	orch := orchestrator.New(&runtimeCfg, tracker.NewMemoryTracker(nil, nil, nil), &agenttest.FakeRunner{}, nil)

	tickCfg := automationCompileConfigView(baseCfg, orch)
	compiled := compileAutomations(&tickCfg)

	require.Len(t, compiled.rateLimited, 1)
	assert.Equal(t, "fallback", compiled.rateLimited[0].SwitchToProfile)
}

func TestShouldSkipAutomatedIssueAllowsQueueableNoSlots(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.MaxConcurrentAgents = 1
	cfg.Agent.MaxConcurrentAgentsByState = map[string]int{}
	cfg.Tracker.ActiveStates = []string{"Todo"}
	cfg.Tracker.TerminalStates = []string{"Done"}
	state := orchestrator.NewState(cfg)
	state.Running["busy"] = &orchestrator.RunEntry{
		Issue: domain.Issue{ID: "busy", Identifier: "ENG-BUSY", State: "Todo"},
	}

	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "Queued", State: "Todo"}

	require.False(t, shouldSkipAutomatedIssue(state, cfg, issue))
}

func TestShouldSkipAutomatedIssueAllowsQueueableBlockers(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.MaxConcurrentAgents = 3
	cfg.Agent.MaxConcurrentAgentsByState = map[string]int{}
	cfg.Tracker.ActiveStates = []string{"Todo"}
	cfg.Tracker.TerminalStates = []string{"Done"}
	state := orchestrator.NewState(cfg)
	blockerIdentifier := "ENG-0"
	blockerState := "In Progress"
	issue := domain.Issue{
		ID:         "id1",
		Identifier: "ENG-1",
		Title:      "Blocked",
		State:      "Todo",
		BlockedBy: []domain.BlockerRef{{
			Identifier: &blockerIdentifier,
			State:      &blockerState,
		}},
	}

	require.False(t, shouldSkipAutomatedIssue(state, cfg, issue))
}

// TestShouldSkipAutomatedIssueAllowsQueueablePausedByState is the
// baseline-half regression for AUTO-2: the poll producer (automations.go)
// gates issue_entered_state / issue_moved_backlog dispatch on
// shouldSkipAutomatedIssue BEFORE the transition edge is recorded into
// next.issues. Previously "paused_by_state:<state>" was not in the queueable
// allowlist, so shouldSkipAutomatedIssue returned true and the producer
// dropped the trigger entirely; because the baseline still advances
// unconditionally, the transition edge was lost forever (next poll's
// prevIssue already reflects the new state, so the trigger never re-fires).
// With paused_by_state now queueable, the producer must NOT skip — the event
// gets produced and durably queued instead of vanishing.
func TestShouldSkipAutomatedIssueAllowsQueueablePausedByState(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.MaxConcurrentAgents = 3
	cfg.Agent.MaxConcurrentAgentsByState = map[string]int{}
	cfg.Agent.PauseDispatchWhenAnyInState = []string{"In Review"}
	cfg.Tracker.ActiveStates = []string{"Todo"}
	cfg.Tracker.TerminalStates = []string{"Done"}
	state := orchestrator.NewState(cfg)
	state.PrevIssueStates = map[string]string{"other-id": "In Review"}

	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "Paused window", State: "Todo"}

	require.False(t, shouldSkipAutomatedIssue(state, cfg, issue))
}

func TestShouldSkipAutomatedIssueStillSkipsNonQueueableTerminal(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.MaxConcurrentAgents = 3
	cfg.Agent.MaxConcurrentAgentsByState = map[string]int{}
	cfg.Tracker.ActiveStates = []string{"Todo"}
	cfg.Tracker.TerminalStates = []string{"Done"}
	state := orchestrator.NewState(cfg)
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", State: "Done"}

	require.True(t, shouldSkipAutomatedIssue(state, cfg, issue))
}

func TestMatchesAutomationFilter_ChecksLabelsAndInputContext(t *testing.T) {
	entry := compiledAutomation{
		cfg: config.AutomationConfig{
			ID: "input-responder",
			Filter: config.AutomationFilterConfig{
				MatchMode:         "all",
				LabelsAny:         []string{"qa", "triage"},
				InputContextRegex: "continue|branch",
			},
		},
		inputContextRe: regexp.MustCompile("continue|branch"),
	}

	issue := domain.Issue{
		Identifier: "ENG-42",
		Labels:     []string{"triage"},
	}

	assert.True(t, matchesAutomationFilter(issue, entry, "Continue with the existing branch"))
	assert.False(t, matchesAutomationFilter(issue, entry, "Need approval for production migration"))
	assert.False(t, matchesAutomationFilter(domain.Issue{Identifier: "ENG-42", Labels: []string{"docs"}}, entry, "Continue with the existing branch"))
}

func TestMatchesAutomationFilter_AnyMatchMode(t *testing.T) {
	entry := compiledAutomation{
		cfg: config.AutomationConfig{
			ID: "comment-watch",
			Filter: config.AutomationFilterConfig{
				MatchMode:       "any",
				LabelsAny:       []string{"triage"},
				IdentifierRegex: "^ENG-42$",
			},
		},
		identifierRe: regexp.MustCompile("^ENG-42$"),
	}

	assert.True(t, matchesAutomationFilter(domain.Issue{Identifier: "ENG-42"}, entry, ""))
	assert.True(t, matchesAutomationFilter(domain.Issue{Identifier: "ENG-99", Labels: []string{"triage"}}, entry, ""))
	assert.False(t, matchesAutomationFilter(domain.Issue{Identifier: "ENG-99", Labels: []string{"docs"}}, entry, ""))
}

// Default match mode + explicit filter.states must narrow the fetch to ONLY
// those states — this is the branch cron anchors (states: [Backlog]) and
// completion-state sweeps (states: [In Review]) rely on to avoid scanning the
// whole active/backlog pool. Regression guard for gaps_11-era sweep configs.
func TestCronAutomationFetchStates_DefaultModeUsesExplicitStatesOnly(t *testing.T) {
	cfg := &config.Config{
		Tracker: config.TrackerConfig{
			BacklogStates: []string{"Backlog"},
			ActiveStates:  []string{"Todo", "In Progress"},
		},
	}
	entry := compiledAutomation{
		cfg: config.AutomationConfig{
			Filter: config.AutomationFilterConfig{
				// MatchMode unset = default all-conditions mode.
				States: []string{"In Review"},
			},
		},
	}

	states := cronAutomationFetchStates(cfg, entry, cfg.Tracker.ActiveStates)

	assert.Equal(t, []string{"In Review"}, states)
}

// No filter.states in default mode falls back to the backlog+active union —
// the pool a label-anchored cron without an explicit states filter scans.
func TestCronAutomationFetchStates_DefaultModeNoStatesUsesBacklogAndActive(t *testing.T) {
	cfg := &config.Config{
		Tracker: config.TrackerConfig{
			BacklogStates: []string{"Backlog"},
			ActiveStates:  []string{"Todo", "In Progress"},
		},
	}
	entry := compiledAutomation{cfg: config.AutomationConfig{}}

	states := cronAutomationFetchStates(cfg, entry, cfg.Tracker.ActiveStates)

	assert.ElementsMatch(t, []string{"Backlog", "Todo", "In Progress"}, states)
}

func TestCronAutomationFetchStates_IncludesExplicitStatesInAnyMode(t *testing.T) {
	cfg := &config.Config{
		Tracker: config.TrackerConfig{
			BacklogStates: []string{"Backlog"},
			ActiveStates:  []string{"Todo", "In Progress"},
		},
	}
	entry := compiledAutomation{
		cfg: config.AutomationConfig{
			Filter: config.AutomationFilterConfig{
				MatchMode: config.AutomationFilterMatchAny,
				States:    []string{"Needs Clarification", "Ready for QA"},
			},
		},
	}

	states := cronAutomationFetchStates(cfg, entry, cfg.Tracker.ActiveStates)

	assert.ElementsMatch(t, []string{"Backlog", "Todo", "In Progress", "Needs Clarification", "Ready for QA"}, states)
}

func TestAutomationPollStates_IncludesFilterStates(t *testing.T) {
	cfg := &config.Config{
		Tracker: config.TrackerConfig{
			BacklogStates:   []string{"Backlog"},
			ActiveStates:    []string{"Todo", "In Progress"},
			TerminalStates:  []string{"Done"},
			CompletionState: "Done",
		},
	}
	entries := []compiledAutomation{
		{
			cfg: config.AutomationConfig{
				ID: "comment-watch",
				Trigger: config.AutomationTriggerConfig{
					Type: config.AutomationTriggerTrackerComment,
				},
				Filter: config.AutomationFilterConfig{
					MatchMode: config.AutomationFilterMatchAny,
					States:    []string{"Needs Clarification"},
				},
			},
		},
		{
			cfg: config.AutomationConfig{
				ID: "state-entry",
				Trigger: config.AutomationTriggerConfig{
					Type:  config.AutomationTriggerIssueEnteredState,
					State: "Ready for QA",
				},
				Filter: config.AutomationFilterConfig{
					States: []string{"Ready for QA", "Needs Clarification"},
				},
			},
		},
	}

	states := automationPollStates(cfg, entries, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates, cfg.Tracker.CompletionState)

	assert.ElementsMatch(t, []string{"Backlog", "Todo", "In Progress", "Done", "Needs Clarification", "Ready for QA"}, states)
}

func TestAutomationManagedCommentMarkers(t *testing.T) {
	body := "QA failed. Moving back to Todo."
	marked := tracker.MarkManagedComment(body)

	assert.Contains(t, marked, body)
	assert.True(t, isAutomationManagedComment(domain.Comment{Body: marked}))
	assert.True(t, isAutomationManagedComment(domain.Comment{AuthorName: "Itervox"}))
	// Third arm: the input-required notification prefix is managed even
	// without the marker or author (it predates MarkManagedComment).
	assert.True(t, isAutomationManagedComment(domain.Comment{
		Body:       "🤖 **Agent needs your input**\nWhich approach should I take?",
		AuthorName: "alice",
	}))
	assert.False(t, isAutomationManagedComment(domain.Comment{Body: body, AuthorName: "alice"}))
}

func TestAutomationProducersPausedUsesQueueBackpressure(t *testing.T) {
	assert.False(t, automationProducersPaused(orchestrator.State{}))
	assert.True(t, automationProducersPaused(orchestrator.State{
		AutomationQueueBackpressure: orchestrator.AutomationQueueBackpressure{
			PausedProducers: true,
		},
	}))
}

type pollTracker struct {
	mu          sync.Mutex
	poll        int
	issuesByRun [][]domain.Issue
	detailByRun []map[string]domain.Issue
}

func (t *pollTracker) FetchCandidateIssues(context.Context) ([]domain.Issue, error) {
	return nil, nil
}

func (t *pollTracker) FetchIssuesByStates(context.Context, []string) ([]domain.Issue, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.issuesByRun) == 0 {
		return nil, nil
	}
	index := t.poll
	if index >= len(t.issuesByRun) {
		index = len(t.issuesByRun) - 1
	}
	t.poll++
	return append([]domain.Issue(nil), t.issuesByRun[index]...), nil
}

func (t *pollTracker) FetchIssueStatesByIDs(context.Context, []string) ([]domain.Issue, error) {
	return nil, nil
}

func (t *pollTracker) CreateComment(context.Context, string, string) (*domain.Comment, error) {
	return nil, nil
}

func (t *pollTracker) CreateIssue(context.Context, string, string, string, string) (*domain.Issue, error) {
	return nil, nil
}

func (t *pollTracker) UpdateIssueState(context.Context, string, string) error {
	return nil
}

func (t *pollTracker) FetchIssueDetail(_ context.Context, issueID string) (*domain.Issue, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.detailByRun) == 0 {
		return nil, fmt.Errorf("issue %s not found", issueID)
	}
	index := t.poll - 1
	if index < 0 {
		index = 0
	}
	if index >= len(t.detailByRun) {
		index = len(t.detailByRun) - 1
	}
	issue, ok := t.detailByRun[index][issueID]
	if !ok {
		return nil, fmt.Errorf("issue %s not found", issueID)
	}
	cp := issue
	return &cp, nil
}

func (t *pollTracker) FetchIssueByIdentifier(_ context.Context, identifier string) (*domain.Issue, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, batch := range t.issuesByRun {
		for _, issue := range batch {
			if issue.Identifier == identifier {
				cp := issue
				return &cp, nil
			}
		}
	}
	return nil, fmt.Errorf("issue %s not found", identifier)
}

func (t *pollTracker) SetIssueBranch(context.Context, string, string) error {
	return nil
}

type doneRunner struct {
	agent.Runner
	done chan struct{}
}

func (r *doneRunner) RunTurn(ctx context.Context, log agent.Logger, onProgress func(agent.TurnResult), sessionID *string, prompt, workspacePath, command, workerHost, logDir string, readTimeoutMs, turnTimeoutMs int) (agent.TurnResult, error) {
	res, err := r.Runner.RunTurn(ctx, log, onProgress, sessionID, prompt, workspacePath, command, workerHost, logDir, readTimeoutMs, turnTimeoutMs)
	select {
	case r.done <- struct{}{}:
	default:
	}
	return res, err
}

func TestPollAutomationEvents_TrackerCommentRequiresNewEligibleComment(t *testing.T) {
	cfg := &config.Config{
		Polling: config.PollingConfig{IntervalMs: 50},
		Tracker: config.TrackerConfig{
			BacklogStates:   []string{"Backlog"},
			ActiveStates:    []string{"Todo"},
			TerminalStates:  []string{"Done"},
			CompletionState: "Done",
		},
		Agent: config.AgentConfig{
			Command:             "claude",
			MaxConcurrentAgents: 1,
			Profiles: map[string]config.AgentProfile{
				"pm": {Command: "claude"},
			},
			TurnTimeoutMs: 60000,
			ReadTimeoutMs: 30000,
		},
	}
	entries := []compiledAutomation{
		{
			cfg: config.AutomationConfig{
				ID:      "comment-watch",
				Enabled: true,
				Profile: "pm",
				Trigger: config.AutomationTriggerConfig{
					Type: config.AutomationTriggerTrackerComment,
				},
				Filter: config.AutomationFilterConfig{
					LabelsAny: []string{"triage"},
				},
			},
		},
	}

	comment1 := domain.Comment{ID: "c1", Body: "old comment", AuthorName: "alice"}
	comment2 := domain.Comment{ID: "c2", Body: "new comment", AuthorName: "alice"}
	comment2Edited := domain.Comment{ID: "c2", Body: "edited new comment", AuthorName: "alice"}
	issueNoMatch := domain.Issue{ID: "id-1", Identifier: "ENG-1", Title: "Triage me", State: "Todo", Labels: []string{"docs"}}
	issueMatch := domain.Issue{ID: "id-1", Identifier: "ENG-1", Title: "Triage me", State: "Todo", Labels: []string{"triage"}}

	tr := &pollTracker{
		issuesByRun: [][]domain.Issue{
			{issueNoMatch},
			{issueMatch},
			{issueMatch},
			{issueMatch},
		},
		detailByRun: []map[string]domain.Issue{
			{"id-1": {ID: "id-1", Identifier: "ENG-1", Title: "Triage me", State: "Todo", Labels: []string{"docs"}, Comments: []domain.Comment{comment1}}},
			{"id-1": {ID: "id-1", Identifier: "ENG-1", Title: "Triage me", State: "Todo", Labels: []string{"triage"}, Comments: []domain.Comment{comment1}}},
			{"id-1": {ID: "id-1", Identifier: "ENG-1", Title: "Triage me", State: "Todo", Labels: []string{"triage"}, Comments: []domain.Comment{comment2}}},
			{"id-1": {ID: "id-1", Identifier: "ENG-1", Title: "Triage me", State: "Todo", Labels: []string{"triage"}, Comments: []domain.Comment{comment2Edited}}},
		},
	}

	runner := &doneRunner{
		Runner: agenttest.NewFakeRunner([]agent.StreamEvent{
			{Type: "system", SessionID: "s1"},
			{Type: "result", SessionID: "s1"},
		}),
		done: make(chan struct{}, 4),
	}
	orch := orchestrator.New(cfg, tr, runner, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	time.Sleep(20 * time.Millisecond)

	state := automationPollState{issues: make(map[string]observedAutomationIssue)}
	state = pollAutomationEvents(ctx, cfg, tr, orch, entries, state, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates, cfg.Tracker.CompletionState, time.Now())
	state = pollAutomationEvents(ctx, cfg, tr, orch, entries, state, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates, cfg.Tracker.CompletionState, time.Now().Add(time.Minute))

	select {
	case <-runner.done:
		t.Fatal("stale comment should not dispatch when issue only became newly eligible")
	default:
	}

	state = pollAutomationEvents(ctx, cfg, tr, orch, entries, state, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates, cfg.Tracker.CompletionState, time.Now().Add(2*time.Minute))
	require.Eventually(t, func() bool {
		return len(orch.RunHistory()) == 1
	}, 3*time.Second, 25*time.Millisecond, "expected new comment id to dispatch automation")

	pollAutomationEvents(ctx, cfg, tr, orch, entries, state, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates, cfg.Tracker.CompletionState, time.Now().Add(3*time.Minute))
	time.Sleep(250 * time.Millisecond)
	assert.Len(t, orch.RunHistory(), 1, "editing the latest comment body with the same id should not dispatch again")
}

func TestPollAutomationEvents_DropsIssueSnapshotsWhenIssueIsAbsent(t *testing.T) {
	entries := []compiledAutomation{
		{
			cfg: config.AutomationConfig{
				ID:      "qa-ready",
				Enabled: true,
				Profile: "qa",
				Trigger: config.AutomationTriggerConfig{
					Type:  config.AutomationTriggerIssueEnteredState,
					State: "Ready for QA",
				},
			},
		},
	}
	cfg := &config.Config{
		Tracker: config.TrackerConfig{
			ActiveStates:    []string{"Todo"},
			TerminalStates:  []string{"Done"},
			CompletionState: "Done",
		},
		Agent: config.AgentConfig{
			Profiles: map[string]config.AgentProfile{
				"qa": {Command: "claude"},
			},
		},
		Automations: []config.AutomationConfig{
			entries[0].cfg,
		},
	}
	todoIssue := domain.Issue{ID: "id-1", Identifier: "ENG-1", Title: "QA me", State: "Todo"}
	readyIssue := domain.Issue{ID: "id-1", Identifier: "ENG-1", Title: "QA me", State: "Ready for QA"}

	tr := &pollTracker{
		issuesByRun: [][]domain.Issue{
			{todoIssue},
			{},
			{readyIssue},
			{todoIssue},
			{readyIssue},
		},
		detailByRun: []map[string]domain.Issue{
			{"id-1": todoIssue},
			{},
			{"id-1": readyIssue},
			{"id-1": todoIssue},
			{"id-1": readyIssue},
		},
	}

	runner := &doneRunner{
		Runner: agenttest.NewFakeRunner([]agent.StreamEvent{
			{Type: "system", SessionID: "s1"},
			{Type: "result", SessionID: "s1"},
		}),
		done: make(chan struct{}, 4),
	}
	orch := orchestrator.New(cfg, tr, runner, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go orch.Run(ctx) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	state := automationPollState{issues: make(map[string]observedAutomationIssue)}
	state = pollAutomationEvents(ctx, cfg, tr, orch, entries, state, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates, cfg.Tracker.CompletionState, time.Now())
	require.Contains(t, state.issues, "id-1")

	state = pollAutomationEvents(ctx, cfg, tr, orch, entries, state, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates, cfg.Tracker.CompletionState, time.Now().Add(time.Minute))
	assert.Empty(t, state.issues, "issues absent from the current poll should not keep stale snapshots")
}

func TestReplayInputRequiredAutomations_DispatchesPersistedBlockedIssueOncePerAutomation(t *testing.T) {
	cfg := &config.Config{
		Polling: config.PollingConfig{IntervalMs: 20},
		Tracker: config.TrackerConfig{
			ActiveStates:    []string{"Todo"},
			TerminalStates:  []string{"Done"},
			CompletionState: "Done",
		},
		Agent: config.AgentConfig{
			Command:             "claude",
			MaxConcurrentAgents: 2,
			Profiles: map[string]config.AgentProfile{
				"responder": {Command: "claude"},
			},
			TurnTimeoutMs: 60000,
			ReadTimeoutMs: 30000,
		},
	}

	issue := domain.Issue{
		ID:         "id-1",
		Identifier: "ENG-1",
		Title:      "Needs answer",
		State:      "Todo",
		Labels:     []string{"triage"},
	}
	tr := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)

	runner := &countingDoneRunner{
		Runner: agenttest.NewFakeRunner([]agent.StreamEvent{
			{Type: "system", SessionID: "s1"},
			{Type: "result", SessionID: "s1"},
		}),
		done: make(chan struct{}, 4),
	}
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	}, 2*time.Second, 20*time.Millisecond)

	automations := []orchestrator.InputRequiredAutomation{
		{
			ID:                "input-responder-a",
			ProfileName:       "responder",
			States:            []string{"Todo"},
			LabelsAny:         []string{"triage"},
			InputContextRegex: regexp.MustCompile(`continue|branch`),
		},
	}

	replayState := replayInputRequiredAutomations(ctx, tr, orch, automations, inputRequiredReplayState{}, time.Now())
	require.Eventually(t, func() bool {
		return len(orch.RunHistory()) == 1
	}, 3*time.Second, 25*time.Millisecond)

	replayState = replayInputRequiredAutomations(ctx, tr, orch, automations, replayState, time.Now().Add(time.Minute))
	time.Sleep(200 * time.Millisecond)
	assert.Len(t, orch.RunHistory(), 1, "same automation must not replay twice for the same blocked question")

	automations = append(automations, orchestrator.InputRequiredAutomation{
		ID:                "input-responder-b",
		ProfileName:       "responder",
		States:            []string{"Todo"},
		LabelsAny:         []string{"triage"},
		InputContextRegex: regexp.MustCompile(`continue|branch`),
	})
	_ = replayInputRequiredAutomations(ctx, tr, orch, automations, replayState, time.Now().Add(2*time.Minute))
	require.Eventually(t, func() bool {
		return len(orch.RunHistory()) == 2
	}, 3*time.Second, 25*time.Millisecond)
}

func TestStartAutomations_ReplaysPersistedInputRequiredIssueOnStartup(t *testing.T) {
	cfg := &config.Config{
		Polling: config.PollingConfig{IntervalMs: 20},
		Tracker: config.TrackerConfig{
			ActiveStates:    []string{"Todo"},
			TerminalStates:  []string{"Done"},
			CompletionState: "Done",
		},
		Agent: config.AgentConfig{
			Command:             "claude",
			MaxConcurrentAgents: 2,
			Profiles: map[string]config.AgentProfile{
				"responder": {Command: "claude"},
			},
			TurnTimeoutMs: 60000,
			ReadTimeoutMs: 30000,
		},
		Automations: []config.AutomationConfig{
			{
				ID:      "input-responder",
				Enabled: true,
				Profile: "responder",
				Trigger: config.AutomationTriggerConfig{
					Type: config.AutomationTriggerInputRequired,
				},
				Filter: config.AutomationFilterConfig{
					States:            []string{"Todo"},
					LabelsAny:         []string{"triage"},
					InputContextRegex: `continue|branch`,
				},
			},
		},
	}

	issue := domain.Issue{
		ID:         "id-1",
		Identifier: "ENG-1",
		Title:      "Needs answer",
		State:      "Todo",
		Labels:     []string{"triage"},
	}
	tr := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)

	runner := &countingDoneRunner{
		Runner: agenttest.NewFakeRunner([]agent.StreamEvent{
			{Type: "system", SessionID: "s1"},
			{Type: "result", SessionID: "s1"},
		}),
		done: make(chan struct{}, 4),
	}
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	runDone := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		select {
		case <-runDone:
		case <-time.After(2 * time.Second):
			t.Fatal("orchestrator did not stop before test cleanup")
		}
	})

	startAutomations(ctx, cfg, tr, orch)
	go func() {
		_ = orch.Run(ctx)
		close(runDone)
	}()

	require.Eventually(t, func() bool {
		return len(orch.RunHistory()) == 1
	}, 3*time.Second, 25*time.Millisecond)
}

type countingDoneRunner struct {
	agent.Runner
	mu        sync.Mutex
	done      chan struct{}
	callCount int
}

func (r *countingDoneRunner) RunTurn(ctx context.Context, log agent.Logger, onProgress func(agent.TurnResult), sessionID *string, prompt, workspacePath, command, workerHost, logDir string, readTimeoutMs, turnTimeoutMs int) (agent.TurnResult, error) {
	r.mu.Lock()
	r.callCount++
	r.mu.Unlock()
	res, err := r.Runner.RunTurn(ctx, log, onProgress, sessionID, prompt, workspacePath, command, workerHost, logDir, readTimeoutMs, turnTimeoutMs)
	select {
	case r.done <- struct{}{}:
	default:
	}
	return res, err
}

func (r *countingDoneRunner) CallCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.callCount
}

// TestPollAutomationEvents_PlanDrivenLabelGate locks in the label-gated
// plan-driven implementation pattern documented for v0.2.0 (issue #32).
// The contract under test:
//   - An issue carrying the `has-plan` label that transitions into
//     "In Progress" dispatches under the dedicated `plan-driven` profile,
//     attributed to automation `plan-driven-impl`.
//   - An issue WITHOUT the label undergoing the same transition is not
//     pre-empted — the rule must reject it via labels_any.
//
// This test runs under `make verify`, which is part of `make qa-current`,
// so the launch-blog-post use case stays green release-to-release.
func TestPollAutomationEvents_PlanDrivenLabelGate(t *testing.T) {
	cfg := &config.Config{
		Polling: config.PollingConfig{IntervalMs: 50},
		Tracker: config.TrackerConfig{
			BacklogStates:   []string{"Backlog"},
			ActiveStates:    []string{"In Progress"},
			TerminalStates:  []string{"Done"},
			CompletionState: "Done",
		},
		Agent: config.AgentConfig{
			Command:             "claude",
			MaxConcurrentAgents: 2,
			Profiles: map[string]config.AgentProfile{
				"default":     {Command: "claude"},
				"plan-driven": {Command: "claude", Prompt: "Invoke the exec-plan skill. The plan in the description is authoritative."},
			},
			TurnTimeoutMs: 60000,
			ReadTimeoutMs: 30000,
		},
	}
	entries := []compiledAutomation{
		{
			cfg: config.AutomationConfig{
				ID:           "plan-driven-impl",
				Enabled:      true,
				Profile:      "plan-driven",
				Instructions: "Invoke the exec-plan skill now. Plan starts after `## Implementation Plan`.",
				Trigger: config.AutomationTriggerConfig{
					Type:  config.AutomationTriggerIssueEnteredState,
					State: "In Progress",
				},
				Filter: config.AutomationFilterConfig{
					MatchMode: config.AutomationFilterMatchAll,
					LabelsAny: []string{"has-plan"},
				},
			},
		},
	}

	labeledBacklog := domain.Issue{
		ID:         "id-plan",
		Identifier: "ENG-99",
		Title:      "Implement search filters",
		State:      "Backlog",
		Labels:     []string{"has-plan"},
	}
	labeledInProgress := domain.Issue{
		ID:         "id-plan",
		Identifier: "ENG-99",
		Title:      "Implement search filters",
		State:      "In Progress",
		Labels:     []string{"has-plan"},
	}
	unlabeledBacklog := domain.Issue{
		ID:         "id-other",
		Identifier: "ENG-100",
		Title:      "Other issue without plan",
		State:      "Backlog",
	}
	unlabeledInProgress := domain.Issue{
		ID:         "id-other",
		Identifier: "ENG-100",
		Title:      "Other issue without plan",
		State:      "In Progress",
	}

	tr := &pollTracker{
		issuesByRun: [][]domain.Issue{
			{labeledBacklog, unlabeledBacklog},
			{labeledInProgress, unlabeledInProgress},
		},
		detailByRun: []map[string]domain.Issue{
			{"id-plan": labeledBacklog, "id-other": unlabeledBacklog},
			{"id-plan": labeledInProgress, "id-other": unlabeledInProgress},
		},
	}

	runner := &doneRunner{
		Runner: agenttest.NewFakeRunner([]agent.StreamEvent{
			{Type: "system", SessionID: "s1"},
			{Type: "result", SessionID: "s1"},
		}),
		done: make(chan struct{}, 4),
	}
	orch := orchestrator.New(cfg, tr, runner, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go orch.Run(ctx) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	state := automationPollState{issues: make(map[string]observedAutomationIssue)}
	state = pollAutomationEvents(ctx, cfg, tr, orch, entries, state, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates, cfg.Tracker.CompletionState, time.Now())
	state = pollAutomationEvents(ctx, cfg, tr, orch, entries, state, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates, cfg.Tracker.CompletionState, time.Now().Add(time.Minute))

	require.Eventually(t, func() bool {
		return len(orch.RunHistory()) == 1
	}, 3*time.Second, 25*time.Millisecond,
		"label-gated plan-driven automation must dispatch exactly one run for the labeled issue")

	history := orch.RunHistory()
	require.Len(t, history, 1)
	run := history[0]
	assert.Equal(t, "ENG-99", run.Identifier, "only the has-plan-labeled issue should dispatch — unlabeled issue must be filtered out")
	assert.Equal(t, "plan-driven-impl", run.AutomationID, "run must be attributed to the plan-driven automation")
	assert.Equal(t, config.AutomationTriggerIssueEnteredState, run.TriggerType, "run must record issue_entered_state as the trigger")

	// One more poll with no further state changes — must not produce a second
	// dispatch for the same transition.
	pollAutomationEvents(ctx, cfg, tr, orch, entries, state, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates, cfg.Tracker.CompletionState, time.Now().Add(2*time.Minute))
	time.Sleep(150 * time.Millisecond)
	assert.Len(t, orch.RunHistory(), 1, "a steady-state poll must not re-dispatch the same transition")
}
