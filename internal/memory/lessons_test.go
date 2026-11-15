package memory

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLooksLikeCorrection (#203): a message that tells the agent to do
// something differently is a correction; an answer or an approval is not.
func TestLooksLikeCorrection(t *testing.T) {
	for text, want := range map[string]bool{
		"Don't add a dependency, use the csv helper instead.": true,
		"Never push to main; open a PR.":                      true,
		"You should run pnpm, not npm.":                       true,
		"Use port 5173 rather than 3000.":                     true,
		"Yes, go ahead.":                                      false,
		"ok":                                                  false,
		"Port 8080.":                                          false,
		"The staging database lives in eu-west-1.":            false,
		"LGTM, thanks!":                                       false,
	} {
		assert.Equal(t, want, LooksLikeCorrection(text), text)
	}
}

// TestTemplateDraftFillsWhenDoInsteadOf (#203): the template fallback is
// "When <situation>, <correction>. Instead of: <what went wrong>." and stays
// within the text cap however long the evidence.
func TestTemplateDraftFillsWhenDoInsteadOf(t *testing.T) {
	d := TemplateDraft(Candidate{
		Trigger: TriggerInputReply, Situation: "an agent asks “Add a CSV dependency?”",
		WentWrong: "adding a new dependency", Corrections: []string{"Use the csv helper in src/lib"},
	})
	assert.Equal(t, "When an agent asks “Add a CSV dependency?”, use the csv helper in src/lib. Instead of: adding a new dependency.", d.Text)
	assert.Equal(t, "template", d.DraftedBy)

	d = TemplateDraft(Candidate{Trigger: TriggerSteering, Title: "Export button", Corrections: []string{"PR titles start with the area"}})
	assert.Equal(t, "When working on issues like “Export button”, PR titles start with the area.", d.Text, "acronyms keep their case")

	long := TemplateDraft(Candidate{Trigger: TriggerSteering, Situation: strings.Repeat("s", 5000), WentWrong: strings.Repeat("w", 5000), Corrections: []string{strings.Repeat("c ", 5000)}})
	assert.LessOrEqual(t, len(long.Text), MaxTextBytes)
	e := NewLessonEntry(Candidate{Trigger: TriggerSteering, Corrections: []string{strings.Repeat("c ", 5000)}}, long)
	assert.NoError(t, validate(withDefaults(e)))
}

func withDefaults(e Entry) Entry {
	e.ID, e.Scope = "m-1", ScopePrivate
	return e
}

func lessonCandidate(correction string) Candidate {
	return Candidate{Trigger: TriggerInputReply, Issue: "ENG-1", Corrections: []string{correction}, RunIDs: []string{"run-1"}}
}

// TestProposeLessonDedupesNearDuplicates (#203): a near-duplicate of an
// existing lesson (by text or by the correction it came from) bumps that
// lesson's evidence, run ids and confidence instead of proposing another;
// a different lesson is proposed; with auto_approve it is written active.
func TestProposeLessonDedupesNearDuplicates(t *testing.T) {
	s, _, _ := testStore(t)
	first := lessonCandidate("Don't add a dependency for CSV; use the csv helper in src/lib.")
	res, err := s.ProposeLesson(NewLessonEntry(first, TemplateDraft(first)), ProposeOptions{})
	require.NoError(t, err)
	require.Equal(t, LessonProposed, res.Outcome)
	assert.Equal(t, ScopePrivate, res.Entry.Scope)

	again := lessonCandidate("Use the csv helper in src/lib, don't add a CSV dependency")
	again.RunIDs = []string{"run-2"}
	assert.Equal(t, LessonBumped, s.PrecheckLesson(again.CorrectionText()))
	res2, err := s.ProposeLesson(NewLessonEntry(again, TemplateDraft(again)), ProposeOptions{})
	require.NoError(t, err)
	assert.Equal(t, LessonBumped, res2.Outcome)
	assert.Equal(t, res.Entry.ID, res2.Entry.ID)
	assert.Equal(t, 2, res2.Entry.Lesson.Evidence)
	assert.Equal(t, []string{"run-1", "run-2"}, res2.Entry.Source.RunIDs)
	assert.InDelta(t, 0.7, res2.Entry.Lesson.Confidence, 1e-9)
	assert.Equal(t, res.Entry.Text, res2.Entry.Text, "the wording is kept")

	other := lessonCandidate("Never log the API token, redact it.")
	assert.Equal(t, LessonNew, s.PrecheckLesson(other.CorrectionText()))
	res3, err := s.ProposeLesson(NewLessonEntry(other, TemplateDraft(other)), ProposeOptions{Scope: ScopeProject, AutoApprove: true})
	require.NoError(t, err)
	assert.Equal(t, LessonApproved, res3.Outcome)
	assert.Equal(t, StatusActive, res3.Entry.Status)
	assert.Equal(t, ScopeProject, res3.Entry.Scope)
	require.NotNil(t, res3.Entry.Lesson.ApprovedAt)

	assert.Less(t, Similarity("use the csv helper", "never log the token"), DuplicateSimilarity)
	assert.GreaterOrEqual(t, Similarity("Use the csv helper in src/lib.", "use src/lib csv helper"), DuplicateSimilarity)
}

// TestProposeLessonFallsBackToProjectWhenPrivateOff (#203): proposals go to
// the private file by default, the project file when private memory is off.
func TestProposeLessonFallsBackToProjectWhenPrivateOff(t *testing.T) {
	s, _, project := testStore(t)
	t.Setenv(UserMemoryEnv, "off")
	c := lessonCandidate("Always rebase onto main instead of merging it.")
	res, err := s.ProposeLesson(NewLessonEntry(c, TemplateDraft(c)), ProposeOptions{Scope: ScopePrivate})
	require.NoError(t, err)
	assert.Equal(t, ScopeProject, res.Entry.Scope)
	_, err = os.Stat(project)
	assert.NoError(t, err)
}

// TestApproveAndRejectLesson (#203): approve moves a proposed lesson to the
// chosen scope with the reviewer's edit and stamps approvedAt; approving or
// rejecting a non-proposed entry is a conflict; reject remembers the
// lesson's normalized words in lessons-rejected.json, never its text, and
// secrets are redacted from an edit.
func TestApproveAndRejectLesson(t *testing.T) {
	s, user, project := testStore(t)
	c := lessonCandidate("Don't commit generated files; add them to .gitignore instead.")
	res, err := s.ProposeLesson(NewLessonEntry(c, TemplateDraft(c)), ProposeOptions{})
	require.NoError(t, err)

	edited := "When code is generated, gitignore it instead of committing it. token " + ghToken
	got, err := s.ApproveLesson(res.Entry.ID, ApproveOptions{Scope: ScopeProject, Text: &edited})
	require.NoError(t, err)
	assert.Equal(t, StatusActive, got.Status)
	assert.Equal(t, ScopeProject, got.Scope)
	assert.NotContains(t, got.Text, ghToken)
	require.NotNil(t, got.Lesson.ApprovedAt)
	raw, _ := os.ReadFile(user)
	assert.NotContains(t, string(raw), got.ID, "moved out of the private file")
	raw, _ = os.ReadFile(project)
	assert.Contains(t, string(raw), got.ID)

	_, err = s.ApproveLesson(got.ID, ApproveOptions{})
	assert.True(t, errors.Is(err, ErrConflict), "already active")
	_, err = s.RejectLesson(got.ID)
	assert.True(t, errors.Is(err, ErrConflict), "only proposals are rejected")
	_, err = s.ApproveLesson("nope", ApproveOptions{})
	assert.True(t, errors.Is(err, ErrNotFound))
	_, err = s.ApproveLesson(got.ID, ApproveOptions{Scope: "team"})
	assert.True(t, errors.Is(err, ErrInvalid))

	c2 := lessonCandidate("Stop using console.log for errors; use the logger.")
	res2, err := s.ProposeLesson(NewLessonEntry(c2, TemplateDraft(c2)), ProposeOptions{})
	require.NoError(t, err)
	_, err = s.RejectLesson(res2.Entry.ID)
	require.NoError(t, err)
	raw, err = os.ReadFile(RejectedPath(s.WorkflowPath))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "console.log for errors", "normalized words only")
	assert.Contains(t, string(raw), LessonFingerprint(c2.CorrectionText()))
	assert.Equal(t, LessonSuppressed, s.PrecheckLesson(c2.CorrectionText()))
	res3, err := s.ProposeLesson(NewLessonEntry(c2, TemplateDraft(c2)), ProposeOptions{})
	require.NoError(t, err)
	assert.Equal(t, LessonSuppressed, res3.Outcome)
}

// TestParseLessonRecord (#203): a memory file round-trips an entry's lesson
// record, and unknown lesson keys or a bad trigger are rejected with their
// position.
func TestParseLessonRecord(t *testing.T) {
	good := `{"memories":[{"id":"l1","type":"lesson","status":"proposed","text":"When x, do y.","lesson":{"trigger":"steering","evidence":2,"confidence":0.5}}]}`
	es, err := Parse([]byte(good), ScopePrivate)
	require.NoError(t, err)
	require.NotNil(t, es[0].Lesson)
	assert.Equal(t, 2, es[0].Lesson.Evidence)

	_, err = Parse([]byte(strings.Replace(good, `"evidence"`, `"weight"`, 1)), ScopePrivate)
	assert.ErrorContains(t, err, "memories[0].lesson.weight: unknown field")
	_, err = Parse([]byte(strings.Replace(good, `"steering"`, `"vibes"`, 1)), ScopePrivate)
	assert.ErrorContains(t, err, "lesson.trigger")
	_, err = Parse([]byte(strings.Replace(good, `0.5`, `3`, 1)), ScopePrivate)
	assert.ErrorContains(t, err, "lesson.confidence")
}

// TestLessonHealthFlagsUnusedAndFailing (#203): an approved lesson none of
// the last N runs used is "unused"; one whose recent runs mostly failed is
// "failing"; proposals and recently approved lessons are not judged.
func TestLessonHealthFlagsUnusedAndFailing(t *testing.T) {
	approved := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	e := Entry{ID: "l1", Type: TypeLesson, Status: StatusActive, Lesson: &Lesson{Trigger: TriggerSteering, ApprovedAt: &approved}}
	run := func(day int, failed bool, ids ...string) RunUse {
		return RunUse{FinishedAt: approved.AddDate(0, 0, day), Failed: failed, MemoryIDs: ids}
	}
	var runs []RunUse
	for i := range 3 {
		runs = append(runs, run(i+1, false))
	}
	h, _ := HealthOf(e, runs, 3)
	assert.Equal(t, LessonUnused, h)
	h, _ = HealthOf(e, runs, 4)
	assert.Equal(t, LessonHealthy, h, "not enough runs yet")
	runs = append(runs, run(5, false, "L1"))
	h, _ = HealthOf(e, runs, 3)
	assert.Equal(t, LessonHealthy, h, "used recently (ids match case-insensitively)")

	failing := []RunUse{run(1, true, "l1"), run(2, true, "l1"), run(3, false, "l1")}
	h, why := HealthOf(e, failing, 20)
	assert.Equal(t, LessonFailing, h)
	assert.Equal(t, "2 of the last 3 runs that used it failed", why)

	before := []RunUse{{FinishedAt: approved.AddDate(0, 0, -1), Failed: true, MemoryIDs: []string{"l1"}}, {FinishedAt: approved.AddDate(0, 0, -2), Failed: true, MemoryIDs: []string{"l1"}}}
	h, _ = HealthOf(e, before, 20)
	assert.Equal(t, LessonHealthy, h, "runs before approval do not count")

	e.Status = StatusProposed
	h, _ = HealthOf(e, failing, 1)
	assert.Equal(t, LessonHealthy, h)
}
