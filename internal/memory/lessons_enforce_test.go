package memory

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lessonStore is a store over a workflow in a temp dir with the private
// file redirected beside it.
func lessonStore(t *testing.T) Store {
	t.Helper()
	root := t.TempDir()
	t.Setenv(UserMemoryEnv, filepath.Join(root, "home", "memory.json"))
	return Store{WorkflowPath: filepath.Join(root, "repo", "WORKFLOW.md")}
}

func proposeFor(t *testing.T, s Store, c Candidate) ProposeResult {
	t.Helper()
	res, err := s.ProposeLesson(NewLessonEntry(c, TemplateDraft(c)), ProposeOptions{})
	require.NoError(t, err)
	return res
}

// TestEnforcementSuggestionLifecycle (#217): a lesson needs enforcement
// once its evidence reaches the threshold; a suggestion is recorded once
// (a second suggestion is a no-op), decided once (accepted with its issue,
// or dismissed), and survives a parse of the file. An accepted suggestion
// puts an approved lesson in the "enforced" state.
func TestEnforcementSuggestionLifecycle(t *testing.T) {
	s := lessonStore(t)
	c := lessonCandidate("Don't add a CSV dependency; use the csv helper instead.")
	e := proposeFor(t, s, c).Entry
	assert.False(t, NeedsEnforcement(e, 3), "seen once")
	c.RunIDs = []string{"run-2"}
	proposeFor(t, s, c)
	c.RunIDs = []string{"run-3"}
	e = proposeFor(t, s, c).Entry
	require.Equal(t, 3, e.Lesson.Evidence)
	assert.True(t, NeedsEnforcement(e, 3))
	assert.False(t, NeedsEnforcement(e, 0), "threshold 0: never")

	enf := TemplateEnforcement(e)
	assert.Equal(t, EnforceRule, enf.Kind)
	got, ok, err := s.SuggestEnforcement(e.ID, enf)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, EnforcementSuggested, got.Lesson.Enforcement.Status)
	assert.False(t, got.Lesson.Enforcement.SuggestedAt.IsZero())
	assert.False(t, NeedsEnforcement(got, 3), "suggested once")
	_, ok, err = s.SuggestEnforcement(e.ID, enf)
	require.NoError(t, err)
	assert.False(t, ok, "a second suggestion is a no-op")

	accepted, err := s.DecideEnforcement(e.ID, true, "ENG-42", "https://tracker/ENG-42")
	require.NoError(t, err)
	assert.Equal(t, EnforcementAccepted, accepted.Lesson.Enforcement.Status)
	assert.Equal(t, "ENG-42", accepted.Lesson.Enforcement.Issue)
	require.NotNil(t, accepted.Lesson.Enforcement.DecidedAt)
	_, err = s.DecideEnforcement(e.ID, false, "", "")
	require.ErrorIs(t, err, ErrConflict, "decided once")

	for _, f := range s.List() {
		require.NoError(t, f.Err, "the file with the enforcement record still parses")
	}
	health, _ := HealthOf(accepted, nil, 20)
	assert.Equal(t, LessonHealthy, health, "a proposed lesson is not judged")
	active, err := s.ApproveLesson(e.ID, ApproveOptions{})
	require.NoError(t, err)
	health, reason := HealthOf(active, nil, 20)
	assert.Equal(t, LessonEnforced, health)
	assert.Equal(t, "enforced by ENG-42", reason)

	body := EnforcementIssueBody(active)
	assert.Contains(t, body, "## Suggested enforcement (rule)")
	assert.Contains(t, body, "seen 3 times")
	assert.Contains(t, body, "run-1, run-2, run-3")
	assert.True(t, strings.HasPrefix(EnforcementIssueTitle(active), "Enforce: "))
}

// TestSameRunsDoNotBumpEvidence (#217): a sighting whose runs are all
// already part of the lesson's evidence (a re-scan after a restart) does
// not raise its count; a new run does.
func TestSameRunsDoNotBumpEvidence(t *testing.T) {
	s := lessonStore(t)
	c := lessonCandidate("Never log the API token; redact it instead.")
	proposeFor(t, s, c)
	res := proposeFor(t, s, c)
	assert.Equal(t, LessonBumped, res.Outcome)
	assert.Equal(t, 1, res.Entry.Lesson.Evidence, "the same run is not new evidence")
	c.RunIDs = []string{"run-9"}
	assert.Equal(t, 2, proposeFor(t, s, c).Entry.Lesson.Evidence)
}

// TestArchiveLessonAsRule (#217): only an active lesson is archived with a
// rule pointer; the pointer is validated and parses back.
func TestArchiveLessonAsRule(t *testing.T) {
	s := lessonStore(t)
	e := proposeFor(t, s, lessonCandidate("Always run the linter before pushing.")).Entry
	_, err := s.ArchiveLessonAsRule(e.ID, RuleLink{Layer: "project", ID: "lesson-" + e.ID})
	require.ErrorIs(t, err, ErrConflict, "a proposed lesson is not promoted")
	_, err = s.ApproveLesson(e.ID, ApproveOptions{})
	require.NoError(t, err)
	_, err = s.ArchiveLessonAsRule(e.ID, RuleLink{Layer: "project", ID: "bad id!"})
	require.ErrorIs(t, err, ErrInvalid)
	out, err := s.ArchiveLessonAsRule(e.ID, RuleLink{Layer: "project", ID: "lesson-" + e.ID})
	require.NoError(t, err)
	assert.Equal(t, StatusArchived, out.Status)
	got, err := s.Get(e.ID)
	require.NoError(t, err)
	assert.Equal(t, "lesson-"+e.ID, got.Lesson.Rule.ID)
}

// TestParseRejectsUnknownEnforcementFields (#217): the nested records are
// checked like the rest of the file.
func TestParseRejectsUnknownEnforcementFields(t *testing.T) {
	raw := `{"memories": [{"id": "m-1", "type": "lesson", "text": "x", "status": "active", "created": "2026-10-01T00:00:00Z", "updated": "2026-10-01T00:00:00Z",
	  "lesson": {"trigger": "recipe", "enforcement": {"kind": "lint", "status": "suggested", "draft": "d", "suggestedAt": "2026-10-01T00:00:00Z", "extra": 1}}}]}`
	_, err := Parse([]byte(raw), ScopeProject)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "memories[0].lesson.enforcement.extra")
	_, err = Parse([]byte(strings.Replace(raw, `, "extra": 1`, "", 1)), ScopeProject)
	require.NoError(t, err)
}

// TestLessonAnalytics (#217): the runs that used a lesson and how they
// ended, plus how often it was applied.
func TestLessonAnalytics(t *testing.T) {
	e := Entry{ID: "m-1", Type: TypeLesson, UseCount: 4}
	now := time.Now()
	a := AnalyticsOf(e, []RunUse{
		{MemoryIDs: []string{"M-1"}, FinishedAt: now, Succeeded: true},
		{MemoryIDs: []string{"m-1"}, FinishedAt: now, Failed: true},
		{MemoryIDs: []string{"m-1"}, FinishedAt: now}, // cancelled
		{MemoryIDs: []string{"m-2"}, FinishedAt: now, Succeeded: true},
	})
	assert.Equal(t, LessonAnalytics{Applied: 4, Runs: 3, Succeeded: 1, Failed: 1}, a)
}

// TestRecipeTemplateDraft (#217): a recipe is "Recipe: when …, … Verify
// with: …" and carries its verification on the record.
func TestRecipeTemplateDraft(t *testing.T) {
	c := Candidate{Trigger: TriggerRecipe, Title: "Flaky scheduler tests", Corrections: []string{"Use a fake clock"},
		Verification: "go test -count=20 passes"}
	d := TemplateDraft(c)
	assert.Equal(t, "Recipe: when working on issues like “Flaky scheduler tests”, use a fake clock. Verify with: go test -count=20 passes.", d.Text)
	e := NewLessonEntry(c, d)
	assert.Equal(t, "go test -count=20 passes", e.Lesson.Verification)
	assert.Contains(t, e.Tags, "recipe")
}
