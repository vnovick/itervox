package orchestrator

import (
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/memory"
	"github.com/vnovick/itervox/internal/rules"
	"github.com/vnovick/itervox/internal/tracker"
)

// lessonsOnCfg is the memory config Load gives a workflow without a memory:
// block (#203): memory and lessons on, template drafting unless a test
// names a profile.
func lessonsOnCfg() config.MemoryConfig {
	m := memoryOn
	m.Lessons = config.LessonsConfig{
		Enabled: true, Scope: "private", MaxPerIssue: config.DefaultLessonMaxPerIssue,
		RateWindowHours: config.DefaultLessonRateWindowHours, DraftTokenBudget: config.DefaultLessonDraftTokenBudget,
		DraftTimeoutMs: config.DefaultLessonDraftTimeoutMs, StaleAfterRuns: config.DefaultLessonStaleAfterRuns,
	}
	return m
}

// lessonFixture is an orchestrator with memory and lessons on, a workflow in
// a temp dir and the private memory file redirected beside it. No dispatch
// slots: an input reply queues its resume without starting a worker.
type lessonFixture struct {
	o     *Orchestrator
	cfg   *config.Config
	state State
	wf    string
	store memory.Store
}

func newLessonFixture(t *testing.T, runner agent.Runner) *lessonFixture {
	t.Helper()
	root := t.TempDir()
	wf := filepath.Join(root, "repo", "WORKFLOW.md")
	t.Setenv(memory.UserMemoryEnv, filepath.Join(root, "home", "memory.json"))
	t.Setenv(rules.UserRulesEnv, filepath.Join(root, "home", "rules.json"))
	cfg := testConfig()
	cfg.WorkflowPath = wf
	cfg.Memory = lessonsOnCfg()
	cfg.Agent.MaxConcurrentAgents = 0
	cfg.Agent.Profiles = map[string]config.AgentProfile{}
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "Add the export button", State: "In Progress"}
	o := New(cfg, tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates), runner, nil)
	return &lessonFixture{o: o, cfg: cfg, state: NewState(cfg), wf: wf, store: memory.Store{WorkflowPath: wf}}
}

// reply puts ENG-1 in input-required with question and answers it from
// the dashboard (EventProvideInput) on the event loop.
func (f *lessonFixture) reply(t *testing.T, question, answer string) {
	t.Helper()
	f.state.InputRequiredIssues["ENG-1"] = &InputRequiredEntry{
		IssueID: "id1", Identifier: "ENG-1", SessionID: "s1", Context: question, Backend: "claude", QueuedAt: time.Now(),
	}
	f.state = f.o.handleEvent(t.Context(), f.state, OrchestratorEvent{Type: EventProvideInput, Identifier: "ENG-1", Message: answer})
}

// awaitLesson waits for the drafting goroutine's EventLessonDrafted and
// applies it on the (test-driven) event loop; other events are dropped.
func (f *lessonFixture) awaitLesson(t *testing.T) LessonReport {
	t.Helper()
	return awaitLessonEvent(t, f.o, &f.state)
}

func awaitLessonEvent(t *testing.T, o *Orchestrator, state *State) LessonReport {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-o.events:
			if ev.Type != EventLessonDrafted {
				continue
			}
			*state = o.handleEvent(t.Context(), *state, ev)
			require.NotNil(t, ev.Lesson)
			return *ev.Lesson
		case <-deadline:
			t.Fatal("no EventLessonDrafted within 10s")
		}
	}
}

// lessons lists the lesson entries of both files.
func (f *lessonFixture) lessons(t *testing.T) []memory.Entry {
	t.Helper()
	var out []memory.Entry
	for _, file := range f.store.List() {
		require.NoError(t, file.Err)
		for _, e := range file.Entries {
			if e.Type == memory.TypeLesson {
				out = append(out, e)
			}
		}
	}
	return out
}

const (
	lessonQuestion = "Should I add the export button with a new npm dependency? It needs a CSV library."
	lessonReply    = "No, don't add a dependency for CSV; use the existing csv helper in src/lib instead."
)

// TestLessonProposedFromInputReplyCorrection (#203): a human's reply to an
// input-required question that tells the agent to do something differently
// proposes a lesson (status proposed, source kind lesson, the issue and the
// correction as evidence, drafted from the template when no profile is
// configured) in the private memory file. A reply that only answers or
// agrees proposes nothing.
func TestLessonProposedFromInputReplyCorrection(t *testing.T) {
	f := newLessonFixture(t, &blockedRunner{})

	f.reply(t, "Which port should the dev server use?", "Yes, go ahead.")
	require.Empty(t, f.o.events, "an approval is not a correction: nothing drafted")
	assert.Len(t, f.state.LessonCorrections["ENG-1"], 1, "the reply is still recorded as what changed")

	f.reply(t, lessonQuestion, lessonReply)
	rep := f.awaitLesson(t)
	require.NoError(t, rep.Err)
	assert.Equal(t, memory.LessonProposed, rep.Outcome)
	assert.Equal(t, "template", rep.DraftedBy)

	got := f.lessons(t)
	require.Len(t, got, 1)
	l := got[0]
	assert.Equal(t, rep.EntryID, l.ID)
	assert.Equal(t, memory.StatusProposed, l.Status)
	assert.Equal(t, memory.ScopePrivate, l.Scope)
	assert.Equal(t, "lesson", l.Source.Kind)
	assert.Equal(t, "ENG-1", l.Source.Issue)
	require.NotNil(t, l.Lesson)
	assert.Equal(t, memory.TriggerInputReply, l.Lesson.Trigger)
	assert.Equal(t, lessonReply, l.Lesson.Correction)
	assert.Equal(t, 1, l.Lesson.Evidence)
	assert.Contains(t, l.Lesson.Situation, "Should I add the export button")
	assert.True(t, strings.HasPrefix(l.Text, "When an agent asks"), l.Text)
	assert.Contains(t, l.Text, "use the existing csv helper")
	assert.Nil(t, l.Lesson.ApprovedAt)
	assert.Len(t, f.state.LessonProposals["ENG-1"], 1, "the proposal took a rate-limit slot")
}

// TestLessonProposedAfterFailedThenSucceededRun (#203): a worker run that
// failed, followed by a successful run on the same issue after a change (a
// steering message), proposes a failed_then_succeeded lesson whose evidence
// names both runs and whose "worked" is the change. A retry that succeeds
// with nothing changed proposes nothing.
func TestLessonProposedAfterFailedThenSucceededRun(t *testing.T) {
	f := newProvenanceFixture(t)
	t.Setenv(memory.UserMemoryEnv, filepath.Join(t.TempDir(), "memory.json"))
	f.cfg.Memory = lessonsOnCfg()
	exit := func(entry *RunEntry, reason TerminalReason) {
		attempt := 0
		f.state = f.o.handleEvent(t.Context(), f.state, OrchestratorEvent{
			Type: EventWorkerExited, IssueID: f.issue.ID, RunID: entry.RunID,
			RunEntry: &RunEntry{Issue: f.issue, TerminalReason: reason, RetryAttempt: &attempt},
		})
	}
	store := memory.Store{WorkflowPath: f.wf}

	// Failed, then succeeded with nothing changed: no lesson.
	r0 := f.start(t)
	exit(r0, TerminalFailed)
	r1 := f.start(t)
	exit(r1, TerminalSucceeded)
	require.Empty(t, f.o.events, "a plain retry that worked teaches nothing")

	// Failed, then a steer, then succeeded: a lesson.
	failed := f.start(t)
	exit(failed, TerminalFailed)
	fixed := f.start(t)
	const change = "Run the database migrations ahead of the integration tests"
	f.state = f.o.handleEvent(t.Context(), f.state, OrchestratorEvent{
		Type: EventSessionMessageDelivered, RunID: fixed.RunID,
		SessionDelivery: &SessionDelivery{Identifier: "ENG-1", IDs: []string{"m1"}, Texts: []string{change}, Delivery: SteeringResume},
	})
	require.Empty(t, f.o.events, "the steer alone is not a correction")
	exit(fixed, TerminalSucceeded)

	rep := awaitLessonEvent(t, f.o, &f.state)
	require.NoError(t, rep.Err)
	require.Equal(t, memory.LessonProposed, rep.Outcome)
	var lesson *memory.Entry
	for _, file := range store.List() {
		for _, e := range file.Entries {
			if e.ID == rep.EntryID {
				lesson = &e
			}
		}
	}
	require.NotNil(t, lesson)
	require.NotNil(t, lesson.Lesson)
	assert.Equal(t, memory.TriggerFailedThenSucceeded, lesson.Lesson.Trigger)
	assert.Equal(t, memory.StatusProposed, lesson.Status)
	assert.Contains(t, lesson.Lesson.Worked, "database migrations")
	assert.Contains(t, lesson.Lesson.WentWrong, "failed")
	assert.Equal(t, []string{failed.RunID, fixed.RunID}, lesson.Source.RunIDs, "evidence: the failed run and the run that worked")
	assert.NotContains(t, lesson.Source.RunIDs, r0.RunID, "an earlier, already-settled failure is not evidence")
	assert.Empty(t, f.state.LessonCorrections["ENG-1"], "the success clears the issue's corrections")
}

// TestProposedLessonNotUsedUntilApproved (#203): a proposed lesson never
// reaches a run's memory selection; once approved (here into the project
// file, with edited text) it does.
func TestProposedLessonNotUsedUntilApproved(t *testing.T) {
	f := newLessonFixture(t, &blockedRunner{})
	f.reply(t, lessonQuestion, lessonReply)
	rep := f.awaitLesson(t)
	require.Equal(t, memory.LessonProposed, rep.Outcome)

	issue := domain.Issue{Identifier: "ENG-2", Title: "Export the CSV report", Labels: []string{"csv"}}
	sel, errs := resolveMemoryFor(f.cfg, "", issue, rules.Set{})
	require.Empty(t, errs)
	assert.NotContains(t, sel.IDs(), rep.EntryID, "a proposed lesson is not used")
	assert.NotContains(t, sel.Block(), "csv helper")

	edited := "When a feature needs CSV, use the csv helper in src/lib instead of adding a dependency."
	approved, err := f.store.ApproveLesson(rep.EntryID, memory.ApproveOptions{Scope: memory.ScopeProject, Text: &edited})
	require.NoError(t, err)
	assert.Equal(t, memory.StatusActive, approved.Status)
	assert.Equal(t, memory.ScopeProject, approved.Scope)
	require.NotNil(t, approved.Lesson.ApprovedAt)

	sel, _ = resolveMemoryFor(f.cfg, "", issue, rules.Set{})
	assert.Contains(t, sel.IDs(), rep.EntryID, "an approved lesson is used")
	assert.Contains(t, sel.Block(), edited)
}

// TestRejectedLessonNotProposedAgain (#203): a rejected lesson is
// remembered (lessons-rejected.json beside the workflow); the same
// correction, or a near-duplicate wording of it, is not proposed again, and
// the suppressed attempt gives its rate-limit slot back.
func TestRejectedLessonNotProposedAgain(t *testing.T) {
	f := newLessonFixture(t, &blockedRunner{})
	f.reply(t, lessonQuestion, lessonReply)
	rep := f.awaitLesson(t)
	require.Equal(t, memory.LessonProposed, rep.Outcome)

	_, err := f.store.RejectLesson(rep.EntryID)
	require.NoError(t, err)
	require.Empty(t, f.lessons(t))
	rejected, err := f.store.Rejected()
	require.NoError(t, err)
	require.Len(t, rejected, 1)
	assert.NotContains(t, rejected[0].Correction, "No, don't", "only normalized words are kept, not the text")

	for _, again := range []string{lessonReply, "Don't add a CSV dependency, use the existing csv helper in src/lib."} {
		f.reply(t, lessonQuestion, again)
		rep := f.awaitLesson(t)
		require.NoError(t, rep.Err)
		assert.Equal(t, memory.LessonSuppressed, rep.Outcome, again)
		assert.Empty(t, f.lessons(t), "nothing proposed again for %q", again)
	}
	assert.Len(t, f.state.LessonProposals["ENG-1"], 1, "suppressed attempts give their rate-limit slots back")
}

// gateRunner is a fake drafting backend: RunTurn reports that it started,
// waits for release (or its context), then answers with a JSON lesson. It
// never runs a CLI.
type gateRunner struct {
	started  chan struct{}
	release  chan struct{}
	finished atomic.Bool
	calls    atomic.Int64
	prompts  chan string
	tokens   int
}

func newGateRunner() *gateRunner {
	return &gateRunner{started: make(chan struct{}, 8), release: make(chan struct{}), prompts: make(chan string, 8)}
}

func (g *gateRunner) RunTurn(ctx context.Context, _ agent.Logger, onProgress func(agent.TurnResult), _ *string, prompt, _, _, _, _ string, _, _ int, mode agent.PermissionMode) (agent.TurnResult, error) {
	g.calls.Add(1)
	g.prompts <- prompt
	g.started <- struct{}{}
	defer g.finished.Store(true)
	if g.tokens > 0 && onProgress != nil {
		onProgress(agent.TurnResult{InputTokens: g.tokens, OutputTokens: g.tokens})
	}
	select {
	case <-g.release:
	case <-ctx.Done():
		return agent.TurnResult{Failed: true}, ctx.Err()
	}
	if mode != agent.PermissionSandbox {
		return agent.TurnResult{Failed: true, FailureText: "drafting must run sandboxed"}, nil
	}
	return agent.TurnResult{
		ResultText:  `{"text": "When a feature needs CSV, reuse the csv helper instead of adding a dependency.", "situation": "a feature needs CSV", "wentWrong": "adding a dependency", "worked": "the csv helper"}`,
		InputTokens: 300, OutputTokens: 40,
	}, nil
}

// TestLessonDraftingRunsOffEventLoop (#203): with a lesson profile the
// drafting call runs through the agent runner on a goroutine: the event
// loop's handler returns while the call is still in flight (and handles
// further events), nothing is written until the call answers, the result
// comes back as EventLessonDrafted, and Run's shutdown joins an in-flight
// draft through lessonWg once its context is cancelled.
func TestLessonDraftingRunsOffEventLoop(t *testing.T) {
	g := newGateRunner()
	f := newLessonFixture(t, g)
	f.cfg.Memory.Lessons.Profile = "lesson-writer"
	f.cfg.Agent.Profiles["lesson-writer"] = config.AgentProfile{Command: "claude"}
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.o.runCtx.Store(&runCtx)

	returned := make(chan struct{})
	go func() {
		defer close(returned)
		f.reply(t, lessonQuestion, lessonReply)
	}()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("the event loop's handler blocked on the drafting call")
	}
	select {
	case <-g.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the drafting call never started")
	}
	assert.False(t, g.finished.Load(), "the handler returned while the drafting call was in flight")
	prompt := <-g.prompts
	assert.Contains(t, prompt, lessonReply, "the evidence goes to the drafter")
	assert.Contains(t, prompt, "Reply with ONLY a JSON object")
	assert.Empty(t, f.lessons(t), "nothing is written before the draft answers")

	// The loop keeps handling events meanwhile.
	f.state = f.o.handleEvent(t.Context(), f.state, OrchestratorEvent{Type: EventFailureRecorded, Failure: &FailureRecord{Kind: FailureKindClient, Message: "x"}})

	close(g.release)
	rep := f.awaitLesson(t)
	require.NoError(t, rep.Err)
	assert.Equal(t, memory.LessonProposed, rep.Outcome)
	assert.Equal(t, "profile:lesson-writer", rep.DraftedBy)
	got := f.lessons(t)
	require.Len(t, got, 1)
	assert.Equal(t, "When a feature needs CSV, reuse the csv helper instead of adding a dependency.", got[0].Text)
	assert.Equal(t, "profile:lesson-writer", got[0].Lesson.DraftedBy)

	// Shutdown: an in-flight draft ends with Run's context and is joined.
	g2 := newGateRunner()
	f.o.runner = g2
	f.state.LessonProposals = nil
	f.reply(t, "Should I write the migration by hand?", "Never write migrations by hand; always generate them with the ORM CLI.")
	<-g2.started
	cancel()
	joined := make(chan struct{})
	go func() { f.o.lessonWg.Wait(); close(joined) }()
	select {
	case <-joined:
	case <-time.After(5 * time.Second):
		t.Fatal("lessonWg did not join the drafting goroutine after cancel")
	}
}

// TestLessonDraftOverBudgetFallsBackToTemplate (#203): a drafting call that
// passes memory.lessons.draft_token_budget is cancelled and the lesson is
// drafted from the template instead.
func TestLessonDraftOverBudgetFallsBackToTemplate(t *testing.T) {
	g := newGateRunner()
	g.tokens = 10_000
	f := newLessonFixture(t, g)
	f.cfg.Memory.Lessons.Profile = "lesson-writer"
	f.cfg.Memory.Lessons.DraftTokenBudget = 1000
	f.cfg.Agent.Profiles["lesson-writer"] = config.AgentProfile{Command: "codex"}
	f.reply(t, lessonQuestion, lessonReply)
	rep := f.awaitLesson(t)
	require.NoError(t, rep.Err)
	assert.Equal(t, int64(1), g.calls.Load(), "the backend was called once")
	assert.Equal(t, "template", rep.DraftedBy, "over budget: the template drafted it")
	got := f.lessons(t)
	require.Len(t, got, 1)
	assert.True(t, strings.HasPrefix(got[0].Text, "When an agent asks"), got[0].Text)
}

// TestLessonDraftDefaultsToReviewerProfile (#203): with no
// memory.lessons.profile the reviewer profile's backend drafts; "template"
// never calls a backend.
func TestLessonDraftDefaultsToReviewerProfile(t *testing.T) {
	f := newLessonFixture(t, &blockedRunner{})
	assert.Empty(t, f.o.lessonDraftPlan().Command, "no reviewer profile: the template")
	f.cfg.Agent.ReviewerProfile = "reviewer"
	f.cfg.Agent.Profiles["reviewer"] = config.AgentProfile{Command: "codex --profile review"}
	plan := f.o.lessonDraftPlan()
	assert.Equal(t, "reviewer", plan.Profile)
	assert.Equal(t, "codex --profile review", plan.Command)
	f.cfg.Memory.Lessons.Profile = config.LessonProfileTemplate
	assert.Empty(t, f.o.lessonDraftPlan().Command)
}

// TestLessonRateLimitedPerIssue (#203): an issue proposes at most
// memory.lessons.max_per_issue lessons in the window; other issues are not
// affected and the window frees slots as it slides.
func TestLessonRateLimitedPerIssue(t *testing.T) {
	var state State
	now := time.Now()
	for i := range 2 {
		assert.True(t, takeLessonSlot(&state, "ENG-1", now.Add(time.Duration(i)*time.Minute), 2, time.Hour))
	}
	assert.False(t, takeLessonSlot(&state, "ENG-1", now.Add(3*time.Minute), 2, time.Hour), "third in the window")
	assert.True(t, takeLessonSlot(&state, "ENG-2", now, 2, time.Hour), "per issue")
	assert.True(t, takeLessonSlot(&state, "ENG-1", now.Add(61*time.Minute), 2, time.Hour), "the window slid")
	assert.Len(t, state.LessonProposals["ENG-1"], 1, "stamps older than the window are dropped")
	releaseLessonSlot(&state, "ENG-1", now.Add(61*time.Minute))
	assert.Empty(t, state.LessonProposals["ENG-1"], "a released slot is given back")

	f := newLessonFixture(t, &blockedRunner{})
	f.cfg.Memory.Lessons.MaxPerIssue = 1
	f.reply(t, lessonQuestion, lessonReply)
	require.Equal(t, memory.LessonProposed, f.awaitLesson(t).Outcome)
	f.reply(t, "Should I log the token?", "Never log tokens; redact them instead of printing.")
	require.Empty(t, f.o.events, "rate-limited: nothing drafted")
	assert.Len(t, f.lessons(t), 1)
}

// TestLessonNearDuplicateBumpsEvidence (#203): the same correction seen
// again bumps the existing proposal's evidence instead of proposing a
// second lesson, and skips the drafting call.
func TestLessonNearDuplicateBumpsEvidence(t *testing.T) {
	g := newGateRunner()
	close(g.release)
	f := newLessonFixture(t, g)
	f.cfg.Memory.Lessons.Profile = "lesson-writer"
	f.cfg.Agent.Profiles["lesson-writer"] = config.AgentProfile{Command: "claude"}
	f.reply(t, lessonQuestion, lessonReply)
	require.Equal(t, memory.LessonProposed, f.awaitLesson(t).Outcome)
	f.reply(t, lessonQuestion, "Don't add a dependency for CSV, use the existing csv helper in src/lib.")
	rep := f.awaitLesson(t)
	assert.Equal(t, memory.LessonBumped, rep.Outcome)
	assert.Equal(t, int64(1), g.calls.Load(), "a duplicate is not drafted again")
	got := f.lessons(t)
	require.Len(t, got, 1)
	assert.Equal(t, 2, got[0].Lesson.Evidence)
	assert.InDelta(t, 0.7, got[0].Lesson.Confidence, 1e-9)
	assert.Len(t, f.state.LessonProposals["ENG-1"], 1, "a bump gives its slot back")
}
