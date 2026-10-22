package orchestrator

import (
	"context"
	"errors"
	"os"
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
)

// Lessons round 2 (#217): reviewer-ask, reverted-PR and recipe triggers,
// and enforcement suggestions. Fake runners only: no test here runs a real
// agent CLI.

// lessonEntries lists the lesson entries of both memory files of wf.
func lessonEntries(t *testing.T, wf string) []memory.Entry {
	t.Helper()
	var out []memory.Entry
	for _, file := range (memory.Store{WorkflowPath: wf}).List() {
		require.NoError(t, file.Err)
		for _, e := range file.Entries {
			if e.Type == memory.TypeLesson {
				out = append(out, e)
			}
		}
	}
	return out
}

// TestLessonProposedFromRepeatedReviewerAsk (#217): a reviewer's blocking
// verdict asking for a change no reviewer asked for before proposes
// nothing; the same kind of change asked for again on another issue (a
// different wording, normalized similarity) proposes a reviewer_ask lesson
// through the event loop, with both runs as evidence. Approvals and the
// daemon's own synthetic reasons never count as asks.
func TestLessonProposedFromRepeatedReviewerAsk(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	root := t.TempDir()
	t.Setenv(memory.UserMemoryEnv, filepath.Join(root, "home", "memory.json"))
	t.Setenv(rules.UserRulesEnv, filepath.Join(root, "home", "rules.json"))
	eng1 := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "Add the export button", State: "In Progress"}
	eng2 := domain.Issue{ID: "id2", Identifier: "ENG-2", Title: "Fix the date filter", State: "In Progress"}
	o, rr := breakerOrchestrator(t, config.BackendFallbackConfig{}, eng1, eng2)
	o.cfg.WorkflowPath = filepath.Join(root, "repo", "WORKFLOW.md")
	o.cfg.Memory = lessonsOnCfg()
	o.cfg.Memory.Lessons.Profile = config.LessonProfileTemplate
	ws := t.TempDir()
	o.workspace = &stubWorkspaceProvider{path: ws}
	o.cfg.Agent.ReviewerProfile = "reviewer"
	o.cfg.Agent.ReviewQuorum = config.ReviewQuorumAnyBlock
	state := NewState(o.cfg)

	reviews := 0
	review := func(issue domain.Issue, v ReviewVerdict) string {
		t.Helper()
		o.dispatchReviewerForIssue(ctx, &state, issue, "reviewer", time.Now())
		run := state.Running[issue.ID]
		require.NotNil(t, run)
		reviews++
		awaitRunnerCalls(t, rr, reviews)
		writeVerdictFile(t, ws, issue.Identifier, "reviewer", v)
		state = o.handleEvent(ctx, state, OrchestratorEvent{Type: EventWorkerExited, IssueID: issue.ID, RunID: run.RunID,
			RunEntry: &RunEntry{Issue: issue, TerminalReason: TerminalSucceeded}})
		return run.RunID
	}
	noLessonEvent := func(msg string) {
		t.Helper()
		for {
			select {
			case ev := <-o.events:
				require.NotEqual(t, EventLessonDrafted, ev.Type, msg)
			default:
				return
			}
		}
	}

	first := review(eng1, ReviewVerdict{Verdict: ReviewVerdictBlock, Reasons: []string{
		"Add a regression test for the bug fix before merging the change.",
	}})
	noLessonEvent("a first ask is not a lesson yet")
	require.Len(t, state.ReviewerAsks, 1, "the ask is recorded")

	review(eng2, ReviewVerdict{Verdict: ReviewVerdictApprove, Reasons: []string{"Add a regression test for the bug fix before merging the change."}})
	noLessonEvent("an approval asks for nothing")
	review(eng2, ReviewVerdict{Verdict: ReviewVerdictBlock, Reasons: []string{"no parseable verdict recorded: add a regression test for the bug fix"}})
	noLessonEvent("the daemon's synthetic reason is not a reviewer's ask")

	second := review(eng2, ReviewVerdict{Verdict: ReviewVerdictBlock, Reasons: []string{
		"Please add a regression test for this bug fix before merging.",
	}})
	rep := awaitLessonEvent(t, o, &state)
	require.NoError(t, rep.Err)
	require.Equal(t, memory.LessonProposed, rep.Outcome)
	assert.Equal(t, "ENG-2", rep.Identifier)

	got := lessonEntries(t, o.cfg.WorkflowPath)
	require.Len(t, got, 1)
	l := got[0]
	require.NotNil(t, l.Lesson)
	assert.Equal(t, memory.TriggerReviewerAsk, l.Lesson.Trigger)
	assert.Equal(t, memory.StatusProposed, l.Status)
	assert.Equal(t, "ENG-2", l.Source.Issue)
	assert.Equal(t, []string{first, second}, l.Source.RunIDs, "evidence: both reviewer runs")
	assert.Contains(t, l.Lesson.WentWrong, "asked for this change 2 times")
	assert.Contains(t, l.Lesson.WentWrong, "ENG-1")
	assert.Contains(t, l.Text, "regression test")
}

// TestLessonProposedFromRevertedAgentPR (#217): the revert scan (supplied
// by cmd/itervox, run off the loop from the tick) reports PR #7 reverted;
// the agent worker run that opened it gets a reverted_pr lesson naming the
// PR and the revert. A reviewer run on the same PR and a PR no run opened
// are ignored, the scan is paced (no second scan before revertScanEvery)
// and a revert already seen this session is not proposed again.
func TestLessonProposedFromRevertedAgentPR(t *testing.T) {
	f := newLessonFixture(t, &blockedRunner{})
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.o.runCtx.Store(&runCtx)
	const pr = "https://github.com/acme/app/pull/7"
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "Add the export button"}
	now := time.Now()
	f.o.recordHistory(&RunEntry{RunID: "run-w", Kind: "worker", ProfileName: "coder", PRURL: pr, StartedAt: now.Add(-time.Hour)}, issue, now.Add(-50*time.Minute), "succeeded")
	f.o.recordHistory(&RunEntry{RunID: "run-r", Kind: "reviewer", PRURL: pr, StartedAt: now.Add(-40 * time.Minute)}, issue, now.Add(-30*time.Minute), "succeeded")

	var scans atomic.Int64
	f.o.RevertScan = func(context.Context) ([]RevertedPR, error) {
		scans.Add(1)
		return []RevertedPR{{Number: 7, Subject: `Revert "Add the export button (#7)"`}, {Number: 99, Subject: `Revert "Someone else's PR (#99)"`}}, nil
	}
	awaitScan := func() {
		t.Helper()
		deadline := time.After(10 * time.Second)
		for {
			select {
			case ev := <-f.o.events:
				if ev.Type == EventRevertsScanned {
					f.state = f.o.handleEvent(t.Context(), f.state, ev)
					return
				}
			case <-deadline:
				t.Fatal("no EventRevertsScanned within 10s")
			}
		}
	}

	f.state = f.o.tickPrelude(t.Context(), f.state, now)
	assert.True(t, f.state.RevertScanInFlight)
	awaitScan()
	rep := f.awaitLesson(t)
	require.NoError(t, rep.Err)
	require.Equal(t, memory.LessonProposed, rep.Outcome)
	got := f.lessons(t)
	require.Len(t, got, 1)
	l := got[0]
	assert.Equal(t, memory.TriggerRevertedPR, l.Lesson.Trigger)
	assert.Equal(t, "ENG-1", l.Source.Issue)
	assert.Equal(t, []string{"run-w"}, l.Source.RunIDs, "the worker run that opened the PR, not the reviewer")
	assert.Equal(t, []string{pr}, l.Source.Links)
	assert.Equal(t, []string{"coder"}, l.Profiles)
	assert.Contains(t, l.Lesson.Correction, `Revert "Add the export button (#7)"`)
	assert.Contains(t, l.Lesson.WentWrong, "reverted")

	// Paced: a tick within revertScanEvery starts no scan.
	f.state = f.o.tickPrelude(t.Context(), f.state, now.Add(time.Minute))
	assert.False(t, f.state.RevertScanInFlight)
	assert.Equal(t, int64(1), scans.Load())

	// The next scan reports the same revert: already seen, nothing proposed.
	f.state = f.o.tickPrelude(t.Context(), f.state, now.Add(revertScanEvery+time.Minute))
	awaitScan()
	assert.Equal(t, int64(2), scans.Load())
	assert.Len(t, f.state.LessonProposals["ENG-1"], 1, "a revert seen before is not proposed again (no drafting slot taken)")
	require.Empty(t, f.o.events, "a revert seen before is not proposed again")
	assert.Len(t, f.lessons(t), 1)

	// A failed scan is logged and frees the next one.
	f.o.RevertScan = func(context.Context) ([]RevertedPR, error) { return nil, errors.New("git: no base branch") }
	f.state = f.o.tickPrelude(t.Context(), f.state, now.Add(2*revertScanEvery+2*time.Minute))
	awaitScan()
	assert.False(t, f.state.RevertScanInFlight)
}

// TestRecipeLessonFromHardWonSuccess (#217): a worker run that succeeds
// after two failed attempts, with no recorded change to explain it,
// proposes a recipe lesson: what worked and the verification that proved
// it, read from the successful run's handoff. A success after a single
// failure with a short turn count is not hard-won and proposes nothing.
func TestRecipeLessonFromHardWonSuccess(t *testing.T) {
	f := newProvenanceFixture(t)
	t.Setenv(memory.UserMemoryEnv, filepath.Join(t.TempDir(), "memory.json"))
	f.cfg.Memory = lessonsOnCfg()
	ws := t.TempDir()
	f.o.workspace = &stubWorkspaceProvider{path: ws}
	exit := func(entry *RunEntry, reason TerminalReason) {
		attempt := 0
		f.state = f.o.handleEvent(t.Context(), f.state, OrchestratorEvent{
			Type: EventWorkerExited, IssueID: f.issue.ID, RunID: entry.RunID,
			RunEntry: &RunEntry{Issue: f.issue, TerminalReason: reason, RetryAttempt: &attempt},
		})
	}
	writeHandoff := func(name, body string) {
		dir := filepath.Join(ws, HandoffDirRelPath)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
	}

	// One failure, then a plain success: not hard-won.
	r0 := f.start(t)
	exit(r0, TerminalFailed)
	r1 := f.start(t)
	writeHandoff("2026-10-01T09-00-00Z_implementer.md", "# ENG-1\n\n## Summary\n\nRetried.\n")
	exit(r1, TerminalSucceeded)
	require.Empty(t, f.o.events, "one failure and a short run is not hard-won")

	// Two failures, then a success with nothing recorded in between.
	a := f.start(t)
	exit(a, TerminalFailed)
	b := f.start(t)
	exit(b, TerminalFailed)
	c := f.start(t)
	writeHandoff("2026-10-02T09-00-00Z_implementer.md", "# ENG-1 handoff\n\n"+
		"## What changed\n\nPinned the flaky clock in the scheduler tests with a fake time source instead of sleeping.\n\n"+
		"## Verification\n\n- go test -race -count=20 ./internal/scheduler/... passes\n")
	writeHandoff("2026-10-02T10-00-00Z_implementer.partial.md", "# ENG-1 partial\n\n## Summary\n\nhalf done\n")
	exit(c, TerminalSucceeded)

	rep := awaitLessonEvent(t, f.o, &f.state)
	require.NoError(t, rep.Err)
	require.Equal(t, memory.LessonProposed, rep.Outcome)
	got := lessonEntries(t, f.wf)
	require.Len(t, got, 1)
	l := got[0]
	require.NotNil(t, l.Lesson)
	assert.Equal(t, memory.TriggerRecipe, l.Lesson.Trigger)
	assert.Contains(t, l.Lesson.Worked, "fake time source")
	assert.Contains(t, l.Lesson.Verification, "go test -race -count=20")
	assert.Contains(t, l.Lesson.WentWrong, "2 failed attempts")
	assert.Equal(t, []string{a.RunID, b.RunID, c.RunID}, l.Source.RunIDs, "evidence: the failed attempts and the run that worked")
	assert.Contains(t, l.Tags, "recipe")
	assert.True(t, strings.HasPrefix(l.Text, "Recipe: when "), l.Text)
	assert.Contains(t, l.Text, "Verify with: - go test -race")
}

// enforceRunner is a fake drafting backend that answers a lesson prompt with
// a lesson and an enforcement prompt with a lint-rule suggestion. It never
// runs a CLI.
type enforceRunner struct {
	lessonCalls, enforceCalls atomic.Int64
}

func (r *enforceRunner) RunTurn(_ context.Context, _ agent.Logger, _ func(agent.TurnResult), _ *string, prompt, _, _, _, _ string, _, _ int, mode agent.PermissionMode) (agent.TurnResult, error) {
	if mode != agent.PermissionSandbox {
		return agent.TurnResult{Failed: true, FailureText: "drafting must run sandboxed"}, nil
	}
	if strings.Contains(prompt, "STRONGEST enforcement") {
		r.enforceCalls.Add(1)
		return agent.TurnResult{ResultText: `{"kind": "lint", "title": "Ban new CSV dependencies", "draft": "Add an eslint no-restricted-imports rule for csv packages pointing at src/lib/csv."}`,
			InputTokens: 200, OutputTokens: 40}, nil
	}
	r.lessonCalls.Add(1)
	return agent.TurnResult{ResultText: `{"text": "When a feature needs CSV, reuse the csv helper instead of adding a dependency.", "situation": "a feature needs CSV", "wentWrong": "adding a dependency", "worked": "the csv helper"}`,
		InputTokens: 300, OutputTokens: 40}, nil
}

// TestEnforcementSuggestionAtThresholdCreatesIssue (#217): once the same
// lesson has been seen memory.lessons.enforce_threshold (3) times, the
// drafting goroutine drafts an enforcement suggestion through the budgeted
// drafting call (a fake runner) and records it on the lesson; nothing is
// suggested below the threshold. Accepting it creates an "Enforce: <lesson>"
// issue in the tracker carrying the draft and the evidence, marks the
// suggestion accepted with the issue and puts the lesson in the "enforced"
// state. No code is written: the only effects are the memory file and the
// tracker issue.
func TestEnforcementSuggestionAtThresholdCreatesIssue(t *testing.T) {
	r := &enforceRunner{}
	f := newLessonFixture(t, r)
	f.cfg.Memory.Lessons.Profile = "lesson-writer"
	f.cfg.Agent.Profiles["lesson-writer"] = config.AgentProfile{Command: "claude"}
	f.cfg.Tracker.BacklogStates = []string{"Backlog"}

	replies := []string{
		lessonReply,
		"Don't add a dependency for CSV, use the existing csv helper in src/lib.",
		"No, do not add a CSV dependency; use the existing csv helper in src/lib instead.",
	}
	for i, reply := range replies {
		f.reply(t, lessonQuestion, reply)
		rep := f.awaitLesson(t)
		require.NoError(t, rep.Err)
		if i < len(replies)-1 {
			assert.Nil(t, rep.Enforcement, "seen %d times: below the threshold", i+1)
		} else {
			require.NotNil(t, rep.Enforcement, "seen 3 times: enforcement suggested")
		}
	}
	assert.Equal(t, int64(1), r.lessonCalls.Load(), "the lesson itself was drafted once")
	assert.Equal(t, int64(1), r.enforceCalls.Load(), "the suggestion was drafted once, through the same runner")

	got := f.lessons(t)
	require.Len(t, got, 1)
	l := got[0]
	assert.Equal(t, 3, l.Lesson.Evidence)
	enf := l.Lesson.Enforcement
	require.NotNil(t, enf)
	assert.Equal(t, memory.EnforceLint, enf.Kind)
	assert.Equal(t, memory.EnforcementSuggested, enf.Status)
	assert.Equal(t, "profile:lesson-writer", enf.DraftedBy)
	assert.Contains(t, enf.Draft, "no-restricted-imports")

	// A further sighting does not suggest again.
	f.reply(t, lessonQuestion, "Never add a dependency for CSV; use the existing csv helper in src/lib.")
	assert.Nil(t, f.awaitLesson(t).Enforcement)
	assert.Equal(t, int64(1), r.enforceCalls.Load())

	out, created, err := f.o.AcceptLessonEnforcement(t.Context(), l.ID)
	require.NoError(t, err)
	require.NotNil(t, created)
	assert.Equal(t, "Enforce: "+l.Text, created.Title)
	assert.Equal(t, "Backlog", created.State, "created in the first backlog state")
	require.NotNil(t, created.Description)
	assert.Contains(t, *created.Description, "no-restricted-imports", "the issue carries the draft")
	assert.Contains(t, *created.Description, "seen 4 times", "and the evidence")
	assert.Contains(t, *created.Description, "ENG-1")
	fetched, err := f.o.tracker.FetchIssueByIdentifier(t.Context(), created.Identifier)
	require.NoError(t, err)
	require.NotNil(t, fetched, "the issue is in the tracker")

	require.NotNil(t, out.Lesson.Enforcement)
	assert.Equal(t, memory.EnforcementAccepted, out.Lesson.Enforcement.Status)
	assert.Equal(t, created.Identifier, out.Lesson.Enforcement.Issue)
	assert.Equal(t, memory.StatusProposed, out.Status, "accepting never approves or changes the lesson itself")

	_, _, err = f.o.AcceptLessonEnforcement(t.Context(), l.ID)
	require.ErrorIs(t, err, memory.ErrConflict, "an accepted suggestion cannot create a second issue")
	_, err = f.o.DismissLessonEnforcement(l.ID)
	require.ErrorIs(t, err, memory.ErrConflict)

	approved, err := f.store.ApproveLesson(l.ID, memory.ApproveOptions{})
	require.NoError(t, err)
	health, reason := memory.HealthOf(approved, nil, 20)
	assert.Equal(t, memory.LessonEnforced, health, "Not helping gains an enforced state")
	assert.Contains(t, reason, created.Identifier)
}

// TestEnforcementSuggestionFallsBackToTemplate (#217): with no drafting
// profile the suggestion comes from the template (a rules-layer entry with
// the enforcement ladder); a dismissed suggestion is not suggested again.
func TestEnforcementSuggestionFallsBackToTemplate(t *testing.T) {
	f := newLessonFixture(t, &blockedRunner{})
	f.cfg.Memory.Lessons.EnforceThreshold = 2
	f.reply(t, lessonQuestion, lessonReply)
	require.Nil(t, f.awaitLesson(t).Enforcement)
	f.reply(t, lessonQuestion, "Don't add a dependency for CSV, use the existing csv helper in src/lib.")
	rep := f.awaitLesson(t)
	require.NotNil(t, rep.Enforcement)
	assert.Equal(t, memory.EnforceRule, rep.Enforcement.Kind)
	assert.Equal(t, "template", rep.Enforcement.DraftedBy)
	assert.Contains(t, rep.Enforcement.Draft, "a lint rule, a type, a test")

	out, err := f.o.DismissLessonEnforcement(rep.EntryID)
	require.NoError(t, err)
	assert.Equal(t, memory.EnforcementDismissed, out.Lesson.Enforcement.Status)
	f.reply(t, lessonQuestion, "No, do not add a CSV dependency; use the existing csv helper in src/lib instead.")
	assert.Nil(t, f.awaitLesson(t).Enforcement, "a dismissed suggestion is not suggested again")
}
