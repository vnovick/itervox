package main

import (
	"context"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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
	"github.com/vnovick/itervox/internal/tracker/store"
)

// #155 — the automation event poll fetches only the states its rules need,
// on a configurable interval that stretches near the rate limit, and is
// served by the tracker store when that is on.

var pollAllStates = []string{"Backlog", "Todo", "In Progress", "Ready for QA", "Needs Clarification", "Done", "Canceled"}

// pollFake is a remote-adapter stand-in over the memory tracker that answers
// list reads by state, as Linear and GitHub do, and counts every request.
// Candidate reads return nothing so the orchestrator never dispatches a
// regular worker that would compete with the automation runs under test.
type pollFake struct {
	*tracker.MemoryTracker
	byStates    atomic.Int64
	candidates  atomic.Int64
	incremental atomic.Int64

	mu     sync.Mutex
	asked  [][]string
	stamps map[string]time.Time
	clock  time.Time
	rl     *tracker.RateLimitSnapshot
}

var longAgo = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func pollIssue(id, state string) domain.Issue {
	created := longAgo
	return domain.Issue{ID: "id-" + id, Identifier: "ENG-" + id, Title: "Issue " + id, State: state, CreatedAt: &created, UpdatedAt: &created}
}

func newPollFake(cfg *config.Config, issues ...domain.Issue) *pollFake {
	f := &pollFake{
		MemoryTracker: tracker.NewMemoryTracker(issues, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates),
		stamps:        map[string]time.Time{},
		clock:         longAgo,
	}
	for _, iss := range issues {
		f.stamps[iss.ID] = longAgo
	}
	return f
}

func (f *pollFake) FetchCandidateIssues(context.Context) ([]domain.Issue, error) {
	f.candidates.Add(1)
	return nil, nil
}

func (f *pollFake) FetchIssuesByStates(ctx context.Context, states []string) ([]domain.Issue, error) {
	f.byStates.Add(1)
	f.mu.Lock()
	f.asked = append(f.asked, slices.Clone(states))
	f.mu.Unlock()
	return f.list(ctx, states)
}

func (f *pollFake) list(ctx context.Context, states []string) ([]domain.Issue, error) {
	issues, err := f.MemoryTracker.FetchIssuesByStates(ctx, states)
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range issues {
		at := f.stamps[issues[i].ID]
		issues[i].UpdatedAt = &at
	}
	return issues, err
}

// FetchIssuesUpdatedSince makes the fake an incremental adapter (#114), so
// the store between full pulls asks only for what changed.
func (f *pollFake) FetchIssuesUpdatedSince(ctx context.Context, since time.Time) ([]domain.Issue, []string, error) {
	f.incremental.Add(1)
	all, err := f.list(ctx, pollAllStates)
	if err != nil {
		return nil, nil, err
	}
	var changed []domain.Issue
	for _, iss := range all {
		if iss.UpdatedAt.After(since) {
			changed = append(changed, iss)
		}
	}
	return changed, nil, nil
}

func (f *pollFake) RateLimitSnapshot() *tracker.RateLimitSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rl == nil {
		return nil
	}
	cp := *f.rl
	return &cp
}

func (f *pollFake) setBudget(remaining, limit int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rl = &tracker.RateLimitSnapshot{RequestsRemaining: remaining, RequestsLimit: limit}
}

// move changes an issue's state and stamps its update time.
func (f *pollFake) move(id, state string) {
	f.SetIssueState(id, state)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clock = f.clock.Add(time.Minute)
	f.stamps[id] = f.clock
}

func (f *pollFake) askedStates() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.asked)
}

// promptRunner records every automation run's prompt.
type promptRunner struct {
	agent.Runner
	mu      sync.Mutex
	prompts []string
}

func (r *promptRunner) RunTurn(ctx context.Context, log agent.Logger, onProgress func(agent.TurnResult), sessionID *string, prompt, workspacePath, command, workerHost, logDir string, readTimeoutMs, turnTimeoutMs int, _ agent.PermissionMode) (agent.TurnResult, error) {
	r.mu.Lock()
	r.prompts = append(r.prompts, prompt)
	r.mu.Unlock()
	return r.Runner.RunTurn(ctx, log, onProgress, sessionID, prompt, workspacePath, command, workerHost, logDir, readTimeoutMs, turnTimeoutMs, agent.PermissionBypass)
}

func (r *promptRunner) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.prompts)
}

// fired returns the "fired:" lines the automation instructions rendered.
func (r *promptRunner) fired() []string {
	var out []string
	for _, p := range r.all() {
		for line := range strings.SplitSeq(p, "\n") {
			if strings.HasPrefix(line, "fired:") {
				out = append(out, line)
			}
		}
	}
	slices.Sort(out)
	return out
}

const firedInstructions = "fired:{{ issue.identifier }} prev={{ trigger.previous_state }} now={{ trigger.current_state }}"

func pollTestConfig(autos ...config.AutomationConfig) *config.Config {
	return &config.Config{
		Polling: config.PollingConfig{IntervalMs: 60000},
		Tracker: config.TrackerConfig{
			Kind:            "linear",
			BacklogStates:   []string{"Backlog"},
			ActiveStates:    []string{"Todo", "In Progress"},
			TerminalStates:  []string{"Done", "Canceled"},
			CompletionState: "Done",
		},
		Agent: config.AgentConfig{
			Command:             "claude",
			MaxConcurrentAgents: 8,
			MaxTurns:            1,
			Profiles:            map[string]config.AgentProfile{"qa": {Command: "claude"}},
			TurnTimeoutMs:       60000,
			ReadTimeoutMs:       30000,
		},
		Automations: autos,
	}
}

func enteredRule(id, state string) config.AutomationConfig {
	return config.AutomationConfig{ID: id, Enabled: true, Profile: "qa", Instructions: firedInstructions,
		Trigger: config.AutomationTriggerConfig{Type: config.AutomationTriggerIssueEnteredState, State: state}}
}

func backlogRule(id string) config.AutomationConfig {
	return config.AutomationConfig{ID: id, Enabled: true, Profile: "qa", Instructions: firedInstructions,
		Trigger: config.AutomationTriggerConfig{Type: config.AutomationTriggerIssueMovedBacklog}}
}

func commentRule(id string, filter config.AutomationFilterConfig) config.AutomationConfig {
	return config.AutomationConfig{ID: id, Enabled: true, Profile: "qa", Instructions: firedInstructions + " comment={{ trigger.comment_body }}",
		Trigger: config.AutomationTriggerConfig{Type: config.AutomationTriggerTrackerComment}, Filter: filter}
}

// runPollOrchestrator starts the orchestrator's event loop for dispatches
// and stops it at cleanup.
func runPollOrchestrator(t *testing.T, cfg *config.Config, tr tracker.Tracker) (*orchestrator.Orchestrator, *promptRunner, context.Context) {
	t.Helper()
	runner := &promptRunner{Runner: agenttest.NewFakeRunner([]agent.StreamEvent{
		{Type: "system", SessionID: "s1"},
		{Type: "result", SessionID: "s1"},
	})}
	cfg.Workspace.Root = t.TempDir()
	orch := orchestrator.New(cfg, tr, runner, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	done := make(chan struct{})
	go func() {
		_ = orch.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("orchestrator did not stop")
		}
	})
	time.Sleep(20 * time.Millisecond)
	return orch, runner, ctx
}

// TestAutomationPollFetchesOnlyTheStatesAutomationsNeed pins the poll's state
// set per rule mix, and that the poll asks the tracker for exactly that set.
func TestAutomationPollFetchesOnlyTheStatesAutomationsNeed(t *testing.T) {
	cases := []struct {
		name  string
		rules []config.AutomationConfig
		want  []string
	}{
		{
			name:  "no event-polled rule polls nothing",
			rules: []config.AutomationConfig{{ID: "rf", Enabled: true, Profile: "qa", Trigger: config.AutomationTriggerConfig{Type: config.AutomationTriggerRunFailed}}},
			want:  nil,
		},
		{
			name:  "issue_entered_state: trigger state plus active, no backlog, no terminal, no completion",
			rules: []config.AutomationConfig{enteredRule("qa", "Ready for QA")},
			want:  []string{"Todo", "In Progress", "Ready for QA"},
		},
		{
			name:  "a terminal state is polled when a trigger names it",
			rules: []config.AutomationConfig{enteredRule("done", "Done")},
			want:  []string{"Todo", "In Progress", "Done"},
		},
		{
			name:  "issue_moved_to_backlog adds the backlog states",
			rules: []config.AutomationConfig{backlogRule("bl")},
			want:  []string{"Backlog", "Todo", "In Progress"},
		},
		{
			name:  "tracker_comment_added without a state filter watches the active states",
			rules: []config.AutomationConfig{commentRule("c", config.AutomationFilterConfig{LabelsAny: []string{"triage"}})},
			want:  []string{"Todo", "In Progress"},
		},
		{
			name:  "tracker_comment_added whose states bound the match polls only those",
			rules: []config.AutomationConfig{commentRule("c", config.AutomationFilterConfig{States: []string{"Needs Clarification"}})},
			want:  []string{"Needs Clarification"},
		},
		{
			name: "match_mode any does not bound the match: active plus the named states",
			rules: []config.AutomationConfig{commentRule("c", config.AutomationFilterConfig{
				MatchMode: config.AutomationFilterMatchAny, States: []string{"Canceled"}, LabelsAny: []string{"x"},
			})},
			want: []string{"Todo", "In Progress", "Canceled"},
		},
		{
			name: "a mix is the union, deduplicated case-insensitively",
			rules: []config.AutomationConfig{
				enteredRule("qa", "ready for qa"),
				commentRule("c", config.AutomationFilterConfig{States: []string{"Ready for QA", "todo"}}),
			},
			want: []string{"Todo", "In Progress", "ready for qa"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := pollTestConfig(tc.rules...)
			entries := compileAutomations(cfg).polledEvents
			got := automationPollStates(cfg, entries, cfg.Tracker.ActiveStates)
			if tc.want == nil {
				assert.Empty(t, got)
			} else {
				assert.ElementsMatch(t, tc.want, got)
			}
			for _, never := range []string{"Backlog", "Done", "Canceled"} {
				if !slices.ContainsFunc(tc.want, func(s string) bool { return strings.EqualFold(s, never) }) {
					assert.NotContains(t, got, never)
				}
			}

			// The poll asks the tracker for exactly that set (or nothing).
			tr := newPollFake(cfg, pollIssue("1", "Todo"))
			pollAutomationEvents(t.Context(), cfg, tr, orchestrator.New(cfg, tr, nil, nil), entries,
				automationPollState{issues: map[string]observedAutomationIssue{}}, cfg.Tracker.ActiveStates, time.Now())
			asked := tr.askedStates()
			if tc.want == nil {
				assert.Empty(t, asked, "no event-polled rule: the tracker is not asked")
				return
			}
			require.Len(t, asked, 1)
			assert.ElementsMatch(t, tc.want, asked[0])
		})
	}
}

// pollTwice runs a baseline poll, applies change, and polls again.
func pollTwice(t *testing.T, ctx context.Context, cfg *config.Config, tr *pollFake, orch *orchestrator.Orchestrator, change func()) {
	t.Helper()
	entries := compileAutomations(cfg).polledEvents
	require.NotEmpty(t, entries)
	start := time.Now()
	state := pollAutomationEvents(ctx, cfg, tr, orch, entries, automationPollState{issues: map[string]observedAutomationIssue{}}, cfg.Tracker.ActiveStates, start)
	change()
	pollAutomationEvents(ctx, cfg, tr, orch, entries, state, cfg.Tracker.ActiveStates, start.Add(15*time.Second))
}

// TestAutomationPollIssueEnteredStateStillFires: the move from an active
// state fires with its previous state; the move from Backlog, which the poll
// no longer fetches, still fires (previous state unknown); an issue created
// in the trigger state does not, as before.
func TestAutomationPollIssueEnteredStateStillFires(t *testing.T) {
	cfg := pollTestConfig(enteredRule("qa-ready", "Ready for QA"))
	// ENG-4 is created after the baseline poll (in a state it does not
	// fetch) and then lands in the trigger state: a creation, not a move.
	fresh := pollIssue("4", "Triage")
	createdLater := time.Now().Add(time.Minute)
	fresh.CreatedAt = &createdLater
	tr := newPollFake(cfg, pollIssue("1", "In Progress"), pollIssue("2", "Backlog"), pollIssue("3", "Todo"), fresh)
	orch, runner, ctx := runPollOrchestrator(t, cfg, tr)

	pollTwice(t, ctx, cfg, tr, orch, func() {
		tr.move("id-1", "Ready for QA")
		tr.move("id-2", "Ready for QA")
		tr.move("id-4", "Ready for QA")
	})
	require.Eventually(t, func() bool { return len(runner.fired()) == 2 }, 5*time.Second, 25*time.Millisecond)
	time.Sleep(150 * time.Millisecond)
	assert.Equal(t, []string{
		"fired:ENG-1 prev=In Progress now=Ready for QA",
		"fired:ENG-2 prev= now=Ready for QA",
	}, runner.fired(), "ENG-3 did not move; ENG-4 was created after the baseline poll")
	for _, asked := range tr.askedStates() {
		assert.NotContains(t, asked, "Backlog")
		assert.NotContains(t, asked, "Done")
	}
}

// TestAutomationPollIssueMovedToBacklogStillFires: a move into Backlog from
// an active state fires with its previous state, and from a terminal state
// (not polled) with an unknown one.
func TestAutomationPollIssueMovedToBacklogStillFires(t *testing.T) {
	cfg := pollTestConfig(backlogRule("groom"))
	tr := newPollFake(cfg, pollIssue("1", "Todo"), pollIssue("2", "Done"), pollIssue("3", "Backlog"))
	orch, runner, ctx := runPollOrchestrator(t, cfg, tr)

	pollTwice(t, ctx, cfg, tr, orch, func() {
		tr.move("id-1", "Backlog")
		tr.move("id-2", "Backlog")
	})
	require.Eventually(t, func() bool { return len(runner.fired()) == 2 }, 5*time.Second, 25*time.Millisecond)
	time.Sleep(150 * time.Millisecond)
	assert.Equal(t, []string{
		"fired:ENG-1 prev=Todo now=Backlog",
		"fired:ENG-2 prev= now=Backlog",
	}, runner.fired(), "ENG-3 stayed in Backlog and does not fire")
	for _, asked := range tr.askedStates() {
		assert.NotContains(t, asked, "Done")
	}
}

// TestAutomationPollTrackerCommentStillFires: a new comment on an active
// issue fires a comment rule with no state filter.
func TestAutomationPollTrackerCommentStillFires(t *testing.T) {
	cfg := pollTestConfig(commentRule("reply", config.AutomationFilterConfig{}))
	tr := newPollFake(cfg, pollIssue("1", "In Progress"))
	tr.AddHumanComment("id-1", "first")
	orch, runner, ctx := runPollOrchestrator(t, cfg, tr)

	entries := compileAutomations(cfg).polledEvents
	start := time.Now()
	state := automationPollState{issues: map[string]observedAutomationIssue{}}
	for i := range 2 { // first sighting, then the comment baseline
		state = pollAutomationEvents(ctx, cfg, tr, orch, entries, state, cfg.Tracker.ActiveStates, start.Add(time.Duration(i)*15*time.Second))
	}
	assert.Empty(t, runner.fired(), "an existing comment does not fire")
	tr.AddHumanComment("id-1", "please look")
	pollAutomationEvents(ctx, cfg, tr, orch, entries, state, cfg.Tracker.ActiveStates, start.Add(45*time.Second))
	require.Eventually(t, func() bool { return len(runner.fired()) == 1 }, 5*time.Second, 25*time.Millisecond)
	assert.Equal(t, []string{"fired:ENG-1 prev= now=In Progress comment=please look"}, runner.fired())
}

// TestAutomationPollCronStillFires: the cron path, which shares the
// scheduler loop the event poll moved out of, still fires from
// startAutomations.
func TestAutomationPollCronStillFires(t *testing.T) {
	cfg := pollTestConfig(config.AutomationConfig{ID: "nightly", Enabled: true, Profile: "qa", Instructions: firedInstructions,
		Trigger: config.AutomationTriggerConfig{Type: config.AutomationTriggerCron, Cron: "* * * * *"},
		Filter:  config.AutomationFilterConfig{States: []string{"Todo"}}})
	tr := newPollFake(cfg, pollIssue("1", "Todo"), pollIssue("2", "Backlog"))
	orch, runner, ctx := runPollOrchestrator(t, cfg, tr)
	startAutomations(ctx, cfg, tr, orch)
	require.Eventually(t, func() bool { return len(runner.fired()) == 1 }, 5*time.Second, 25*time.Millisecond)
	assert.Equal(t, []string{"fired:ENG-1 prev= now=Todo"}, runner.fired())
}

// TestAutomationPollIntervalIsConfigurable: polling.automation_interval_ms sets how
// often the event poll lists issues, independent of the 15s cron tick.
func TestAutomationPollIntervalIsConfigurable(t *testing.T) {
	assert.Equal(t, 15*time.Second, newAutomationPollCadence(0).nextDelay(nil), "unset: the 15s default")
	assert.Equal(t, 2*time.Minute, newAutomationPollCadence(120000).nextDelay(nil))

	count := func(intervalMs int, wait time.Duration) int64 {
		cfg := pollTestConfig(enteredRule("qa", "Ready for QA"))
		cfg.Polling.AutomationIntervalMs = intervalMs
		tr := newPollFake(cfg, pollIssue("1", "Todo"))
		orch, _, ctx := runPollOrchestrator(t, cfg, tr)
		before := tr.byStates.Load()
		startAutomations(ctx, cfg, tr, orch)
		time.Sleep(wait)
		return tr.byStates.Load() - before
	}
	fast := count(40, 600*time.Millisecond)
	assert.GreaterOrEqual(t, fast, int64(6), "a 40ms interval polls many times in 600ms")
	assert.Equal(t, int64(1), count(60000, 600*time.Millisecond), "a 60s interval polls once (at start) in 600ms")
}

// TestAutomationPollDoesNotRunWithoutEventRules: with no event-polled rule
// the scheduler never lists issues, however short the interval.
func TestAutomationPollDoesNotRunWithoutEventRules(t *testing.T) {
	cfg := pollTestConfig(config.AutomationConfig{ID: "rf", Enabled: true, Profile: "qa",
		Trigger: config.AutomationTriggerConfig{Type: config.AutomationTriggerRunFailed}})
	cfg.Polling.AutomationIntervalMs = 20
	tr := newPollFake(cfg, pollIssue("1", "Todo"))
	orch, _, ctx := runPollOrchestrator(t, cfg, tr)
	startAutomations(ctx, cfg, tr, orch)
	time.Sleep(300 * time.Millisecond)
	assert.Zero(t, tr.byStates.Load())
}

// TestAutomationPollBacksOffNearTheRateLimit: below 20% of the request
// budget the interval doubles per poll up to 8x, and returns to the
// configured interval once the budget recovers; an adapter without counters
// never backs off.
func TestAutomationPollBacksOffNearTheRateLimit(t *testing.T) {
	cfg := pollTestConfig()
	tr := newPollFake(cfg)
	c := newAutomationPollCadence(15000)

	tr.setBudget(2000, 2500)
	assert.Equal(t, 15*time.Second, c.nextDelay(tr), "plenty of budget")
	tr.setBudget(500, 2500) // exactly 20%: not below
	assert.Equal(t, 15*time.Second, c.nextDelay(tr))

	tr.setBudget(240, 2500)
	var got []time.Duration
	for range 5 {
		got = append(got, c.nextDelay(tr))
	}
	assert.Equal(t, []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 2 * time.Minute, 2 * time.Minute}, got,
		"doubling per poll, capped at 8x")

	tr.setBudget(2400, 2500) // the hour reset
	assert.Equal(t, 15*time.Second, c.nextDelay(tr), "restored once the budget recovers")

	tr.mu.Lock()
	tr.rl = &tracker.RateLimitSnapshot{} // no limit reported
	tr.mu.Unlock()
	assert.Equal(t, 15*time.Second, c.nextDelay(tr), "fails open without a limit")

	// End to end: a starved budget slows the running poll.
	cfg = pollTestConfig(enteredRule("qa", "Ready for QA"))
	cfg.Polling.AutomationIntervalMs = 40
	live := newPollFake(cfg, pollIssue("1", "Todo"))
	live.setBudget(10, 2500)
	orch, _, ctx := runPollOrchestrator(t, cfg, live)
	startAutomations(ctx, cfg, live, orch)
	time.Sleep(700 * time.Millisecond)
	// 40ms base, starved: polls at 0, 80, 240, 560 ms (then every 320ms).
	polls := live.byStates.Load()
	assert.LessOrEqual(t, polls, int64(5), "backed off: far fewer than the 17 unthrottled polls")
	assert.GreaterOrEqual(t, polls, int64(3), "still polling, just slower")
}

// TestAutomationPollIsServedByTheStore: with the tracker store on, the poll's
// state set becomes a store view after one fetch, every later poll is
// answered locally, and a move is picked up by the store's incremental
// "updated since" pull (#114) — no list request beyond the store's own sync.
func TestAutomationPollIsServedByTheStore(t *testing.T) {
	cfg := pollTestConfig(enteredRule("qa-ready", "Ready for QA"))
	up := newPollFake(cfg, pollIssue("1", "In Progress"), pollIssue("2", "Backlog"), pollIssue("3", "Done"))
	clock := &storeClock{at: time.Now()}
	board := deduplicateStates(cfg.Tracker.BacklogStates, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates, cfg.Tracker.CompletionState)
	tr, s, err := store.Wrap(up, store.Config{
		Path:             t.TempDir() + "/tracker_store.json",
		Scope:            trackerStoreScope(cfg),
		ActiveStates:     cfg.Tracker.ActiveStates,
		Views:            [][]string{board},
		SyncInterval:     time.Hour, // the test drives each sync
		FullSyncInterval: 24 * time.Hour,
		Now:              clock.now,
	})
	require.NoError(t, err)
	orch, runner, ctx := runPollOrchestrator(t, cfg, tr)

	entries := compileAutomations(cfg).polledEvents
	pollSet := automationPollStates(cfg, entries, cfg.Tracker.ActiveStates)
	pollSetRequests := func() int {
		n := 0
		for _, asked := range up.askedStates() {
			if slices.Equal(asked, pollSet) {
				n++
			}
		}
		return n
	}
	start := time.Now()
	state := automationPollState{issues: map[string]observedAutomationIssue{}}
	state = pollAutomationEvents(ctx, cfg, tr, orch, entries, state, cfg.Tracker.ActiveStates, start)
	require.Equal(t, 1, pollSetRequests(), "the poll's state set is fetched once, then kept as a store view")
	time.Sleep(100 * time.Millisecond) // let the orchestrator's startup reads settle
	lists := up.byStates.Load() + up.candidates.Load()

	for i := 1; i <= 10; i++ {
		state = pollAutomationEvents(ctx, cfg, tr, orch, entries, state, cfg.Tracker.ActiveStates, start.Add(time.Duration(i)*15*time.Second))
	}
	assert.Equal(t, lists, up.byStates.Load()+up.candidates.Load(), "ten more polls, no list request")

	up.move("id-1", "Ready for QA")
	syncStore(t, s, clock)
	state = pollAutomationEvents(ctx, cfg, tr, orch, entries, state, cfg.Tracker.ActiveStates, start.Add(11*15*time.Second))
	require.Eventually(t, func() bool { return len(runner.fired()) == 1 }, 5*time.Second, 25*time.Millisecond)
	assert.Equal(t, []string{"fired:ENG-1 prev=In Progress now=Ready for QA"}, runner.fired())

	up.move("id-2", "Ready for QA") // from Backlog, outside the poll's set
	syncStore(t, s, clock)
	pollAutomationEvents(ctx, cfg, tr, orch, entries, state, cfg.Tracker.ActiveStates, start.Add(12*15*time.Second))
	require.Eventually(t, func() bool { return len(runner.fired()) == 2 }, 5*time.Second, 25*time.Millisecond)
	assert.Contains(t, runner.fired(), "fired:ENG-2 prev= now=Ready for QA")

	assert.Equal(t, lists, up.byStates.Load()+up.candidates.Load(),
		"the moves arrived through the store's incremental pulls, not list requests")
	assert.Equal(t, 1, pollSetRequests())
	assert.Equal(t, int64(2), up.incremental.Load(), "one 'updated since' request per store sync")
}

// storeClock is the store's clock; advancing it a sync interval makes the
// store's Run pull at once.
type storeClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *storeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

// syncStore advances the store's clock one sync interval and runs the
// store's own sync loop until it has committed one (incremental) pull.
func syncStore(t *testing.T, s *store.Store, clock *storeClock) {
	t.Helper()
	clock.mu.Lock()
	clock.at = clock.at.Add(time.Hour)
	want := clock.at
	clock.mu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	require.Eventually(t, func() bool {
		at, err := s.Status()
		return err == nil && at.Equal(want)
	}, 5*time.Second, 10*time.Millisecond)
	cancel()
	<-done
}
